package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"strings"
	"testing"
)

func evaluationTaskJSON(rpc, message, state, artifact string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"result":{"kind":"task","id":"task-1","contextId":"context-1","status":{"state":%q},"artifacts":%s,"history":[{"kind":"message","role":"user","messageId":%q,"parts":[{"kind":"text","text":"input"}]}]}}`, rpc, state, artifact, message))
}

func TestKagentEvaluationParserExactText(t *testing.T) {
	raw := evaluationTaskJSON("rpc-1", "request-1", "completed", `[{"artifactId":"answer-1","parts":[{"kind":"text","text":"  first \n"},{"kind":"text","text":"second\t "}]}]`)
	execution, answer, err := parseKagentEvaluationResponse(raw, kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, "input")
	if err != nil || answer != "  first \n\nsecond\t " || execution.TaskID != "task-1" || execution.ContextID != "context-1" || execution.State != "completed" {
		t.Fatalf("exact answer/native identity lost: %+v %q %v", execution, answer, err)
	}
}

func TestKagentEvaluationParserRepeatedRequestHistoryIsOneTurn(t *testing.T) {
	base := string(evaluationTaskJSON("rpc-1", "request-1", "completed", `[{"artifactId":"answer-1","parts":[{"kind":"text","text":"answer"}]}]`))
	user := `{"kind":"message","role":"user","messageId":"request-1","parts":[{"kind":"text","text":"input"}]}`
	raw := []byte(strings.Replace(base, `"history":[`, `"history":[`+user+`,`, 1))
	_, answer, err := parseKagentEvaluationResponse(raw, kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, "input")
	if err != nil || answer != "answer" {
		t.Fatalf("same submitted message was counted as prior history: %v", err)
	}
}

func TestKagentEvaluationParserRefusesAmbiguity(t *testing.T) {
	base := string(evaluationTaskJSON("rpc-1", "request-1", "completed", `[{"artifactId":"answer-1","parts":[{"kind":"text","text":"answer"}]}]`))
	for _, tc := range []struct{ name, old, replacement string }{
		{"rpc", `"id":"rpc-1"`, `"id":"other"`},
		{"exact-EOF", base, base + " {}"},
		{"artifact-parts-type", `"parts":[{"kind":"text","text":"answer"}]`, `"parts":{}`},
		{"artifact-empty-parts", `"parts":[{"kind":"text","text":"answer"}]`, `"parts":[]`},
		{"rpc-duplicate", `"id":"rpc-1"`, `"id":"rpc-1","ID":"rpc-1"`},
		{"rpc-error", `"result":`, `"error":{"message":"private diagnostic"},"result":`},
		{"message", `"kind":"task"`, `"kind":"message"`},
		{"task-id", `"id":"task-1"`, `"id":"bad/id"`},
		{"context", `"contextId":"context-1"`, `"contextId":null`},
		{"status", `"state":"completed"`, `"state":42`},
		{"blank", `"text":"answer"`, `"text":"  "`},
		{"control", `"text":"answer"`, `"text":"answer\u001b"`},
		{"text-type", `"text":"answer"`, `"text":null`},
		{"part-kind", `"kind":"text","text":"answer"`, `"kind":"data","text":"answer"`},
		{"artifact-id", `"artifactId":"answer-1"`, `"artifactId":null`},
		{"multiple-answers", `"artifacts":[`, `"artifacts":[{"artifactId":"other","parts":[{"kind":"text","text":"other"}]},`},
		{"history-user", `"messageId":"request-1"`, `"messageId":"foreign"`},
		{"history-input", `"text":"input"`, `"text":"foreign input"`},
		{"history-task", `"messageId":"request-1"`, `"messageId":"request-1","taskId":"other"`},
		{"extra-user", `"history":[`, `"history":[{"kind":"message","role":"user","messageId":"previous","parts":[{"kind":"text","text":"input"}]},`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(strings.Replace(base, tc.old, tc.replacement, 1))
			_, _, err := parseKagentEvaluationResponse(raw, kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, "input")
			if err == nil || strings.Contains(err.Error(), "private diagnostic") || strings.Contains(err.Error(), "answer") {
				t.Fatalf("ambiguous/payload diagnostic: %v", err)
			}
		})
	}
}

func TestKagentEvaluationParserNativeStates(t *testing.T) {
	for _, state := range []string{"failed", "canceled", "rejected", "working", "input-required", "auth-required", "submitted", "unknown"} {
		execution, answer, err := parseKagentEvaluationResponse(evaluationTaskJSON("rpc-1", "request-1", state, `[]`), kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, "input")
		if err != nil || execution.State != state || answer != "" {
			t.Fatalf("native state %s lost: %+v %q %v", state, execution, answer, err)
		}
	}
}

func evaluationSends(t *testing.T, dir string) []kagentCall {
	t.Helper()
	var sends []kagentCall
	for _, call := range kagentCalls(t, dir) {
		if call.Body["method"] == "message/send" {
			sends = append(sends, call)
		}
	}
	return sends
}

func TestKagentEvaluationOpaquePublicCarrier(t *testing.T) {
	original := []byte(` {"large":9007199254740993,"opaque":["a","b"]} `)
	for _, requirement := range [][]byte{nil, original, []byte("bad"), bytes.Repeat([]byte(" "), (1<<20)+1)} {
		raw, err := kagentEvaluationRequestBody("input", kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "message-1"}, requirement)
		valid := len(requirement) == 0 || bytes.Equal(requirement, original)
		if (err == nil) != valid {
			t.Fatal("invalid carrier accepted", err)
		}
		if !valid {
			continue
		}
		var request struct {
			Params struct {
				Metadata map[string]string `json:"metadata"`
				Message  struct {
					Metadata map[string]any `json:"metadata"`
				} `json:"message"`
			} `json:"params"`
		}
		if err = json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		if request.Params.Message.Metadata != nil {
			t.Fatal("carrier nested under caller message")
		}
		if len(requirement) == 0 {
			if request.Params.Metadata != nil {
				t.Fatal("default authority added")
			}
			continue
		}
		got, err := base64.StdEncoding.DecodeString(request.Params.Metadata["kagent.dev/execution-requirement/v1"])
		if err != nil || !bytes.Equal(got, requirement) {
			t.Fatal("opaque bytes changed", err)
		}
	}
}
