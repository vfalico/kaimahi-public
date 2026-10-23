// Package agentsessions owns the transport and journal evidence for a single chat evaluation.
package agentsessions

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	sdk "github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/wire"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const maxAnswerBytes = 1 << 20

type Options struct{ Address, CAFile string }
type CaseRequest struct {
	PortableDigest, CasesDigest, Instructions, Model, Input string
	Harness                                                 string
}
type JournalHead struct {
	Seq  int64
	Hash string
}
type CaseResult struct {
	SessionUID, Harness, Model string
	// Suite proof metadata is populated only after complete strict verification.
	// Legacy RunCase leaves these fields empty.
	ExecutionID, HarnessVersion, ConfigDigest string
	// ModelMixed is sticky and clears Model when distinct safe models are observed.
	ModelMixed bool
	// ExecutionFailed requires a complete, attributable journal with a recognized
	// terminal END and execution error evidence; invalid or partial streams stay false.
	ExecutionFailed      bool
	Head                 JournalHead
	Output               string
	ToolCalls            map[string]int
	ToolEvidenceComplete bool
}
type Client struct{ sdk *sdk.Client }

func (c *Client) Close() error { return c.sdk.Close() }

// RunCase creates a fresh chat session and executes exactly one turn. The caller must
// supply a deadline and an expected model. Model is observed, never selected by metadata.
// On failure, the result retains safe identity and the last verified journal prefix.
func (c *Client) RunCase(ctx context.Context, request CaseRequest) (CaseResult, error) {
	return c.runCase(ctx, request, map[string]string{
		"kmx.portable-digest": request.PortableDigest,
		"kmx.cases-digest":    request.CasesDigest,
	}, false, nil, nil)
}

func (c *Client) runCase(ctx context.Context, request CaseRequest, labels map[string]string, strict bool, usedSessionUIDs map[string]bool, requirement []byte) (CaseResult, error) {
	var result CaseResult
	harness := request.Harness
	if harness == "" {
		harness = "chat"
	}
	if harness != "chat" && harness != "toolchat" {
		return result, status.Error(codes.InvalidArgument, "agentsessions: unsupported harness")
	}
	if harness == "toolchat" && (!strict || len(requirement) == 0) {
		return result, status.Error(codes.InvalidArgument, "agentsessions: toolchat requires governed suite invocation")
	}
	requirement = append([]byte(nil), requirement...)
	deadline, bounded := ctx.Deadline()
	if !bounded || !safeIdentity(request.Model) || !utf8.ValidString(request.Instructions) || !utf8.ValidString(request.Input) {
		return result, status.Error(codes.InvalidArgument, "agentsessions: deadline and valid case request required")
	}
	if err := ctx.Err(); err != nil {
		return result, safeRPCError(err, "agentsessions: case context ended")
	}
	config, err := chatConfig(request.Instructions)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	createdRequest := &v1.Session{
		Harness: harness,
		Labels:  labels,
	}
	setSessionRequirement(createdRequest, requirement)
	session, err := c.sdk.CreateSession(ctx, createdRequest)
	if err != nil {
		return result, safeRPCError(err, "agentsessions: session creation failed")
	}
	if safeIdentity(session.GetMetadata().GetUid()) {
		result.SessionUID = session.GetMetadata().GetUid()
	}
	if safeIdentity(session.GetHarness()) {
		result.Harness = session.GetHarness()
	}
	if !freshHarnessSession(session, harness) || !matchesSessionRequirement(session, requirement) {
		return result, status.Error(codes.FailedPrecondition, "agentsessions: invalid created session identity")
	}
	if strict && usedSessionUIDs[result.SessionUID] {
		return result, status.Error(codes.FailedPrecondition, "agentsessions: reused suite session identity")
	}

	// SDK ExecOptions supports per-exec config, but omits deadline_unix and
	// buffers unbounded records/output while hiding unknown frames. Its official
	// stub preserves the bounded streaming evidence and explicit host deadline.
	input := wire.MessageToProto(api.TextMessage("user", request.Input))
	zero := int64(0)
	var callOptions []grpc.CallOption
	if strict {
		callOptions = append(callOptions, grpc.MaxCallRecvMsgSize(maxSuiteBytes))
	}
	stream, err := c.sdk.Sessions().Exec(ctx, &v1.ExecRequest{
		Session:         result.SessionUID,
		Harness:         harness,
		Inputs:          []*v1.Message{input},
		Config:          config,
		ExpectedLastSeq: &zero,
		DeadlineUnix:    deadline.Unix(),
	}, callOptions...)
	if err != nil {
		return result, safeRPCError(err, "agentsessions: execution failed")
	}

	var evidenceErr error
	var evidenceInvalid, executionFailed bool
	var tools toolEvidenceCollector
	reportError := func(code codes.Code, diagnostic string) {
		tools.invalidate()
		if evidenceErr == nil {
			evidenceErr = status.Error(code, diagnostic)
		}
	}
	fail := func(code codes.Code, diagnostic string) {
		evidenceInvalid = true
		reportError(code, diagnostic)
	}
	var seenSession, chainValid, ended, seenStart, seenInput bool
	var ownership, prepared *v1.Lifecycle
	var invocationOffset int64
	var executionID, firstModel string
	var output strings.Builder
	var outputOverflow bool
	var captured []eventlog.Record
	var frames, totalBytes int
	for {
		update, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			tools.invalidate()
			return result, safeRPCError(err, "agentsessions: execution stream failed")
		}
		if strict {
			// Count every received frame, including invalid frames and deltas.
			// Cancel rather than drain on exhaustion: an untrusted endless stream
			// cannot turn an earlier refusal into unbounded work.
			size := proto.Size(update)
			frames++
			if frames > maxSuiteFrames || size > maxSuiteBytes-totalBytes {
				return result, status.Error(codes.ResourceExhausted, "agentsessions: suite stream exceeds bounds")
			}
			totalBytes += size
		}
		if update == nil || len(update.ProtoReflect().GetUnknown()) != 0 {
			chainValid = false
			fail(codes.DataLoss, "agentsessions: malformed execution frame")
			continue
		}
		if !seenSession {
			seenSession = true
			s := update.GetSession()
			chainValid = freshHarnessSession(s, harness) && s.GetMetadata().GetUid() == result.SessionUID && matchesSessionRequirement(s, requirement)
			if !chainValid {
				fail(codes.DataLoss, "agentsessions: invalid execution session identity")
			}
			continue
		}
		// Drain even after invalid evidence. Once the chain breaks, later records
		// cannot repair it; model/completion failures alone do not break the chain.
		if !chainValid {
			continue
		}
		switch frame := update.GetUpdate().(type) {
		case *v1.ExecUpdate_Record:
			record := frame.Record
			collectTools := tools.reserve(record)
			event := record.GetEvent()
			if ended || !validEvent(event) || len(record.ProtoReflect().GetUnknown()) != 0 || record.GetSeq() != result.Head.Seq+1 || record.GetPrevHash() != result.Head.Hash || executionID != "" && event.GetExecutionId() != executionID {
				chainValid = false
				fail(codes.DataLoss, "agentsessions: malformed journal record")
				continue
			}
			hash, err := canon.HashRecord(record.GetPrevHash(), record.GetSeq(), wire.EventFromProto(event))
			if err != nil || hash != record.GetContentHash() {
				chainValid = false
				fail(codes.DataLoss, "agentsessions: journal integrity check failed")
				continue
			}
			result.Head = JournalHead{Seq: record.Seq, Hash: hash}
			if preparedEvent(event) {
				l := event.GetLifecycle()
				if prepared != nil || ownership != nil || seenStart || record.Seq != 1 || len(requirement) == 0 || l.GetComputeId() != result.SessionUID || l.GetComputeHarness() != harness || !bytes.Equal(l.GetComputeRequirement(), requirement) {
					fail(codes.DataLoss, "agentsessions: invalid prepared operation prefix")
					continue
				}
				prepared = proto.Clone(l).(*v1.Lifecycle)
				invocationOffset = 1
				if strict {
					captured = append(captured, eventlog.RecordFromProto(proto.Clone(record).(*v1.LogRecord)))
				}
				continue // opaque operation is not target/activation or invocation authority
			}
			if ownedEvent(event) {
				if ownership != nil || seenStart || record.Seq != 1+invocationOffset || len(requirement) == 0 || event.GetLifecycle().GetComputeId() != result.SessionUID || event.GetLifecycle().GetComputeHarness() != harness || !bytes.Equal(event.GetLifecycle().GetComputeRequirement(), requirement) {
					fail(codes.DataLoss, "agentsessions: invalid cleanup ownership prefix")
					continue
				}
				ownership = proto.Clone(event.GetLifecycle()).(*v1.Lifecycle)
				invocationOffset++
				if strict {
					captured = append(captured, eventlog.RecordFromProto(proto.Clone(record).(*v1.LogRecord)))
				}
				continue // cleanup metadata is neither invocation nor activation evidence
			}
			executionID = event.ExecutionId
			if strict {
				if !safeIdentity(executionID) || harness == "chat" && !suiteRecordShapeAt(record, invocationOffset) {
					fail(codes.DataLoss, "agentsessions: unsupported suite invocation record")
				}
				// The bounded copy is isolated from wire buffers before replay.
				captured = append(captured, eventlog.RecordFromProto(proto.Clone(record).(*v1.LogRecord)))
			}
			if collectTools {
				tools.observe(event)
			}
			if event.Kind != v1.EventKind_EVENT_EXECUTION_START && event.Kind != v1.EventKind_EVENT_INPUT && (!seenStart || !seenInput) {
				fail(codes.DataLoss, "agentsessions: incomplete invocation prefix")
			}
			switch event.Kind {
			case v1.EventKind_EVENT_EXECUTION_START:
				start := event.GetExecutionStart()
				if prepared != nil && ownership == nil || seenStart || record.Seq != 1+invocationOffset || ownership != nil && start.GetComputeInstance() != ownership.GetComputeInstance() || !bytes.Equal(start.GetConfig(), config) || start.GetResumeFromSeq() != 0 || start.InputCount == nil || start.GetInputCount() != 1 {
					fail(codes.DataLoss, "agentsessions: invocation start mismatch")
				}
				seenStart = true
			case v1.EventKind_EVENT_INPUT:
				if !seenStart || seenInput || record.Seq != 2+invocationOffset || !proto.Equal(event.GetMessage(), input) {
					fail(codes.DataLoss, "agentsessions: invocation input mismatch")
				}
				seenInput = true
			case v1.EventKind_EVENT_MODEL_CALL:
				if strict && len(requirement) != 0 && !event.GetModel().GetNative() {
					fail(codes.FailedPrecondition, "agentsessions: governed model must execute in owning harness")
				}
				model := event.GetModel().GetModel()
				if !safeIdentity(model) {
					fail(codes.FailedPrecondition, "agentsessions: missing or invalid observed model")
					continue
				}
				if firstModel == "" {
					firstModel = model
					result.Model = model
				} else if model != firstModel {
					result.ModelMixed = true
					result.Model = ""
				}
				if model != request.Model || result.ModelMixed {
					fail(codes.FailedPrecondition, "agentsessions: observed model mismatch")
				}
			case v1.EventKind_EVENT_OUTPUT:
				if outputOverflow {
					continue
				}
				text := wire.MessageFromProto(event.GetMessage()).Text()
				if len(text) > maxAnswerBytes-output.Len() {
					outputOverflow = true
					result.Output = ""
					output.Reset()
					fail(codes.ResourceExhausted, "agentsessions: committed answer exceeds 1 MiB")
					continue
				}
				output.WriteString(text)
				result.Output = output.String()
			case v1.EventKind_EVENT_ERROR:
				executionFailed = true
				reportError(remoteCode(event.GetError().GetCode()), "agentsessions: execution reported an error")
			case v1.EventKind_EVENT_END:
				ended = true
				end := event.GetEnd()
				if end.GetError() != nil {
					executionFailed = true
					reportError(remoteCode(end.GetError().GetCode()), "agentsessions: execution completion reported an error")
				}
				switch api.ExecState(end.GetState()) {
				case api.ExecCompleted:
				case api.ExecFailed, api.ExecCanceled:
					executionFailed = true
					reportError(codes.FailedPrecondition, "agentsessions: execution did not complete")
				default:
					fail(codes.FailedPrecondition, "agentsessions: execution did not complete")
				}
			}
		case *v1.ExecUpdate_Delta:
			d := frame.Delta
			if d == nil || ended || executionID == "" || d.GetExecutionId() != executionID || d.GetPartIndex() < 0 {
				chainValid = false
				fail(codes.DataLoss, "agentsessions: malformed streaming delta")
			}
		default:
			chainValid = false
			fail(codes.DataLoss, "agentsessions: malformed execution frame")
		}
	}
	if !seenSession {
		fail(codes.DataLoss, "agentsessions: missing execution session")
	}
	if !seenStart || !seenInput {
		fail(codes.DataLoss, "agentsessions: missing invocation prefix")
	}
	if firstModel == "" {
		fail(codes.FailedPrecondition, "agentsessions: missing observed model")
	}
	if !ended {
		fail(codes.FailedPrecondition, "agentsessions: missing execution completion")
	}
	// Publish only after EOF: a later integrity or transport failure cannot turn
	// a verified prefix into proof of the whole invocation's execution outcome.
	result.ExecutionFailed = ended && !evidenceInvalid && executionFailed
	if strict && evidenceErr == nil {
		// Prefix checks already bind the observed config/input to the sent request;
		// reconstruction additionally verifies the model-request fingerprint.
		if !(harness == "toolchat" && toolchatInvocation(captured) || harness == "chat" && referenceChatInvocation(captured)) {
			fail(codes.DataLoss, "agentsessions: unsupported suite invocation shape")
		} else {
			proofFn := referenceChatProof
			if harness == "toolchat" {
				proofFn = referenceToolchatProof
			}
			proof, err := proofFn(ctx, captured, request.Model, verifySHA256([]byte(result.Output)))
			if err != nil {
				evidenceErr = safeRPCError(err, "agentsessions: suite reference verification failed")
			} else if proof.Status != "equivalent" || proof.ModelCalls != 0 {
				fail(codes.DataLoss, "agentsessions: suite model request fingerprint mismatch")
			} else {
				result.ExecutionID = executionID
				result.HarnessVersion = suiteHarnessVersion
				result.ConfigDigest, evidenceErr = ConfigDigest(request.Instructions)
			}
		}
	}
	result.ToolCalls, result.ToolEvidenceComplete = tools.finish(evidenceErr == nil)
	return result, evidenceErr
}

func freshChatSession(s *v1.Session) bool { return freshHarnessSession(s, "chat") }
func freshHarnessSession(s *v1.Session, harness string) bool {
	return s != nil && safeIdentity(s.GetMetadata().GetUid()) && s.GetHarness() == harness && s.GetLastSeq() == 0 && s.GetParentUid() == "" && s.GetForkSeq() == 0
}

// Identities may be printed or persisted by the caller; never retain unbounded,
// control-laden, or credential-shaped values supplied by a remote host.
func safeIdentity(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, c := range value {
		if c < '!' || c > '~' {
			return false
		}
	}
	return secretshapes.Match(value) == nil
}

func safeRPCError(err error, diagnostic string) error {
	code := status.Code(err)
	if errors.Is(err, context.Canceled) {
		code = codes.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = codes.DeadlineExceeded
	}
	return status.Error(code, diagnostic)
}

func remoteCode(code int32) codes.Code {
	if code <= int32(codes.OK) || code > int32(codes.Unauthenticated) {
		return codes.Unknown
	}
	return codes.Code(code)
}

func validEvent(e *v1.Event) bool {
	if e == nil || e.GetExecutionId() == "" && !ownedEvent(e) && !preparedEvent(e) || e.GetTs() != nil && e.GetTs().CheckValid() != nil {
		return false
	}
	var body bool
	switch e.Kind {
	case v1.EventKind_EVENT_INPUT, v1.EventKind_EVENT_OUTPUT:
		body = e.GetMessage() != nil
	case v1.EventKind_EVENT_MODEL_CALL:
		body = e.GetModel() != nil
	case v1.EventKind_EVENT_TOOL_CALL:
		body = e.GetTool() != nil
	case v1.EventKind_EVENT_TOOL_RESULT:
		body = e.GetResult() != nil
	case v1.EventKind_EVENT_APPROVAL_REQUEST:
		body = e.GetApproval() != nil
	case v1.EventKind_EVENT_APPROVAL_RESULT:
		body = e.GetApprovalResult() != nil
	case v1.EventKind_EVENT_USAGE:
		body = e.GetUsage() != nil
	case v1.EventKind_EVENT_LIFECYCLE:
		body = e.GetLifecycle() != nil
	case v1.EventKind_EVENT_END:
		body = e.GetEnd() != nil
	case v1.EventKind_EVENT_ERROR:
		body = e.GetError() != nil
	case v1.EventKind_EVENT_EXECUTION_START:
		body = e.GetExecutionStart() != nil
	}
	// Fail closed if the upstream conversion would discard unknown wire content:
	// only the lossless domain representation is covered by HashRecord.
	return body && proto.Equal(e, wire.EventToProto(wire.EventFromProto(e)))
}
