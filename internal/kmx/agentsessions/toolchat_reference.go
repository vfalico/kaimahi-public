package agentsessions

import (
	"context"
	"errors"
	"strings"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harness/toolchatagent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// toolchatInvocation admits a fresh, bounded, completed native conversation.
// The controller replay additionally matches request fingerprints, tool request
// identity, receipt correlation and full effect consumption. No failed prefix,
// approval, host placement or reporting-only tool can be called equivalent.
func toolchatInvocation(records []eventlog.Record) bool {
	var ok bool
	records, ok = invocationRecords(records)
	if !ok {
		return false
	}
	if len(records) < 5 || len(records) > maxSuiteFrames {
		return false
	}
	start := records[0].Event.ExecutionStart
	if start == nil || records[0].Event.Kind != api.EventExecutionStart || start.Harness != "toolchat" || start.HarnessVersion != "1" || start.ResumeFromSeq != 0 || start.InputCount == nil || *start.InputCount != 1 {
		return false
	}
	if start.Config == nil {
		return false
	}
	if records[1].Event.Kind != api.EventInput || !referenceTextMessage(records[1].Event.Message, "user") {
		return false
	}
	id := records[0].Event.ExecutionID
	if !safeIdentity(id) {
		return false
	}
	for _, r := range records {
		if r.Event.ExecutionID != id || r.Event.SchemaVersion < 0 || r.Event.SchemaVersion > 1 {
			return false
		}
	}
	modelID := ""
	modelIDs, toolIDs := map[string]bool{}, map[string]bool{}
	i, rounds := 2, 0
	terminal := false
	for i < len(records)-1 {
		rounds++
		if rounds > 8 {
			return false
		}
		call := records[i].Event.ModelCall
		if records[i].Event.Kind != api.EventModelCall || call == nil || !call.Native || !safeIdentity(call.ID) || !safeIdentity(call.Model) || !verifyDigest(call.InputHash) || len(call.Params) != 0 {
			return false
		}
		if modelIDs[call.ID] {
			return false
		}
		modelIDs[call.ID] = true
		if modelID != "" && modelID != call.Model {
			return false
		}
		modelID = call.Model
		i++
		if i >= len(records)-1 || records[i].Event.Kind != api.EventOutput {
			return false
		}
		message := records[i].Event.Message
		if message == nil || message.Role != "assistant" || len(message.Parts) == 0 {
			return false
		}
		i++
		tools := []*api.ToolCall{}
		for _, part := range message.Parts {
			switch {
			case part.ToolCall != nil:
				if part.Text != nil || part.Reasoning != nil || part.ToolResult != nil || part.File != nil || part.Data != nil {
					return false
				}
				tools = append(tools, part.ToolCall)
			case part.Text != nil:
				if part.Reasoning != nil || part.ToolResult != nil || part.File != nil || part.Data != nil {
					return false
				}
			default:
				return false // new part types require an explicit new profile
			}
		}
		for _, modelTool := range tools {
			if i >= len(records)-1 || records[i].Event.Kind != api.EventToolCall {
				return false
			}
			tool := records[i].Event.ToolCall
			if tool == nil || !tool.Native || tool.ID != modelTool.ID || tool.Tool != toolchatagent.ToolName || tool.Mediation != api.MediationControllerMediated || tool.IdempotencyKey != id+"/"+tool.ID {
				return false
			}
			if !safeIdentity(tool.ID) || toolIDs[tool.ID] {
				return false
			}
			toolIDs[tool.ID] = true
			i++
			if i >= len(records)-1 || records[i].Event.Kind != api.EventToolResult {
				return false
			}
			result := records[i].Event.Result
			if result == nil || result.ID != tool.ID || result.IsError || result.Error != "" || result.Approval != nil || result.ApprovalRequestSeq != 0 || result.ApprovalDecisionSeq != 0 {
				return false
			}
			i++
		}
		if len(tools) == 0 {
			// A tool-free final answer is the only terminal conversational output.
			if i != len(records)-1 || !referenceTextMessage(message, "assistant") {
				return false
			}
			terminal = true
			break
		}
	}
	if !terminal || i != len(records)-1 {
		return false
	}
	end := records[i].Event.End
	return records[i].Event.Kind == api.EventEnd && end != nil && end.State == "COMPLETED" && end.Error == nil
}

// referenceToolchatProof never has a live provider, tool executor or runtime.
// Native flags are reconstructed from the selected reference contract, not
// copied from emitted arbitrary events. Placement drift must diverge on replay.
func referenceToolchatProof(ctx context.Context, records []eventlog.Record, model, answerDigest string) (VerifyResult, error) {
	result := VerifyResult{Status: "unsupported", Records: len(records)}
	if !toolchatInvocation(records) {
		result.Detail = "agentsessions: toolchat invocation shape unavailable"
		return result, nil
	}
	invocation, _ := invocationRecords(records)
	result.ConfigSHA256 = verifySHA256(invocation[0].Event.ExecutionStart.Config)
	var committed strings.Builder
	for _, r := range records {
		if r.Event.ModelCall != nil && r.Event.ModelCall.Model != model {
			result.Status = "mismatch"
			result.Detail = "agentsessions: toolchat model mismatch"
			return result, nil
		}
		if r.Event.Kind == api.EventOutput {
			for _, part := range r.Event.Message.Parts {
				if part.Text != nil {
					if len(part.Text.Text) > maxAnswerBytes-committed.Len() {
						return result, status.Error(codes.ResourceExhausted, "agentsessions: toolchat answer exceeds limit")
					}
					committed.WriteString(part.Text.Text)
				}
			}
		}
	}
	calls := 0
	forbidden := func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		calls++
		return api.ModelResponse{}, errors.New("live model forbidden")
	}
	replay, err := controller.New(eventlog.AsStore(eventlog.NewFrom(records)), forbidden)
	if err != nil {
		return result, status.Error(codes.Internal, "agentsessions: toolchat reconstruction unavailable")
	}
	outputs, err := replay.Replay(ctx, nativeReferenceToolchat{Harness: toolchatagent.Harness{Model: model}})
	result.ModelCalls = calls + replay.ModelInvocations()
	if ctx.Err() != nil {
		return result, safeRPCError(ctx.Err(), "agentsessions: toolchat verification context ended")
	}
	if result.ModelCalls != 0 {
		return result, status.Error(codes.Internal, "agentsessions: toolchat replay reached model")
	}
	if err != nil || strings.Join(outputs, "") != committed.String() {
		result.Status = "mismatch"
		result.Detail = "agentsessions: toolchat reference replay diverged"
		return result, nil
	}
	result.AnswerSHA256 = verifySHA256([]byte(committed.String()))
	if result.AnswerSHA256 != answerDigest {
		result.Status = "mismatch"
		result.Detail = "agentsessions: toolchat answer digest mismatch"
		return result, nil
	}
	result.Status = "equivalent"
	return result, nil
}

type nativeReferenceToolchat struct{ api.Harness }
type nativeReferenceToolSink struct{ nativeReferenceSink }

func (s nativeReferenceToolSink) ToolCall(ctx context.Context, call api.ToolCall) (api.ToolResult, error) {
	call.Native = true
	return s.EventSink.ToolCall(ctx, call)
}
func (h nativeReferenceToolchat) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	return h.Harness.Run(ctx, start, nativeReferenceToolSink{nativeReferenceSink{sink}})
}
