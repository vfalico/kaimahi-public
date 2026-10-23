package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type kagentEvaluationObservation struct {
	Service kagentControllerService
}

type kagentEvaluationOperation struct {
	ExecutionRequirement []byte
	Requests             map[string]kagentEvaluationExecution
	Tasks, Contexts      map[string]bool
	RequireHistory       bool
}

func kagentUnknownEvaluation(ctx context.Context, c agentruntime.EvaluationCase, execution kagentEvaluationExecution, judge agentruntime.EvaluationJudge) agentruntime.EvaluationReceipt {
	return agentruntime.EvaluationReceipt{CaseID: c.ID, Verdict: agentruntime.EvaluationUnknown, Detail: evaluationExecutionUnavailable, Execution: &execution, Assertions: evaluateCaseWithJudge(ctx, c, agentruntime.EvaluationEvidence{ToolSupported: false}, judge)}
}

func (a *App) executeKagentEvaluationCase(ctx context.Context, ref agentruntime.AgentRef, c agentruntime.EvaluationCase, execution kagentEvaluationExecution, op *kagentEvaluationOperation, observe func() (kagentEvaluationObservation, error), judge agentruntime.EvaluationJudge) (agentruntime.EvaluationReceipt, string, error) {
	result := kagentUnknownEvaluation(ctx, c, execution, judge)
	observed, err := observe()
	if err != nil {
		return agentruntime.EvaluationReceipt{}, "", err
	}
	body, err := kagentEvaluationRequestBody(c.Input, execution, op.ExecutionRequirement, a.selectedKagentProfile().A2AV1)
	if err != nil {
		return agentruntime.EvaluationReceipt{}, "", fmt.Errorf("Kagent evaluation request unavailable")
	}
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return agentruntime.EvaluationReceipt{}, "", fmt.Errorf("Kagent evaluation deadline exhausted before send")
	}
	prefix := "/api/a2a/"
	if a.selectedKagentProfile().A2AV1 {
		prefix = "/agents/"
	}
	path := kagentServiceProxyPath(observed.Service, prefix+url.PathEscape(ref.Namespace)+"/"+url.PathEscape(ref.Name))
	raw, sendErr := a.kagentCaptureWithTimeout(ctx, remaining, remaining.String(), body, "create", "--raw", path, "-f", "-")
	answer := ""
	var parseErr error
	if sendErr == nil {
		execution, answer, parseErr = parseKagentEvaluationResponse(raw, execution, c.Input)
		if op.RequireHistory && !suiteKagentResponseHistory(raw) {
			parseErr = fmt.Errorf("Kagent suite requires attributable history")
		}
		if execution.TaskID != "" && (op.Tasks[execution.TaskID] || op.Contexts[execution.ContextID]) {
			execution = op.Requests[c.ID]
			parseErr = fmt.Errorf("Kagent evaluation returned reused native identity")
		}
		if execution.TaskID != "" {
			op.Tasks[execution.TaskID], op.Contexts[execution.ContextID] = true, true
		}
	}
	result.Execution = &execution
	// Attribution takes precedence over both a native failure and an answer.
	if _, err = observe(); err != nil {
		result.Stop = true
		return result, answer, nil
	}
	if sendErr != nil || parseErr != nil {
		result.Stop = true
		return result, answer, nil
	}
	switch execution.State {
	case "failed", "canceled", "rejected":
		result.Verdict = agentruntime.EvaluationFail
		result.Detail = evaluationExecutionFailed
	case "completed":
		result.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(answer)
		result.Detail = ""
		result.Assertions = evaluateCaseWithJudge(ctx, c, agentruntime.EvaluationEvidence{Answer: answer, AnswerAvailable: true, ToolSupported: false}, judge)
		result.Verdict = agentruntime.EvaluationAssertionsVerdict(result.Assertions)
	default:
		result.Stop = true
	}
	return result, answer, nil
}

// Requirement is carried once at the public request level, never under message
// metadata or an Agent template. Padded base64 preserves the exact opaque bytes.
func kagentEvaluationRequestBody(input string, execution kagentEvaluationExecution, requirement []byte, v1 ...bool) ([]byte, error) {
	params := map[string]any{"message": map[string]any{"role": "ROLE_USER", "messageId": execution.RequestMessageID, "parts": []any{map[string]any{"text": input}}}, "configuration": map[string]any{"returnImmediately": false}}
	if len(requirement) != 0 {
		if len(requirement) > 1<<20 || !json.Valid(requirement) {
			return nil, fmt.Errorf("invalid opaque execution requirement")
		}
		params["metadata"] = map[string]any{"kagent.dev/execution-requirement/v1": base64.StdEncoding.EncodeToString(requirement)}
	}
	method := "message/send"
	if len(v1) > 0 && v1[0] {
		method = "SendMessage"
	} else {
		message := params["message"].(map[string]any)
		message["kind"] = "message"
		message["role"] = "user"
		message["parts"] = []any{map[string]any{"kind": "text", "text": input}}
		params["configuration"] = map[string]any{"blocking": true}
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": execution.RPCID, "method": method, "params": params})
}
