package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func v1EvaluationFixture() string {
	return `{"jsonrpc":"2.0","id":"rpc-1","result":{"task":{"id":"task-1","contextId":"context-1","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"artifactId":"tool-1","parts":[{"data":{"name":"shell","response":{"result":"not the answer"}}}]},{"artifactId":"answer-1","parts":[{"text":"  exact\nanswer\t "}]}],"history":[{"messageId":"request-1","role":"ROLE_USER","taskId":"task-1","contextId":"context-1","parts":[{"text":"input"}]}]}}}`
}
func TestKagentV1StrictTaskContract(t *testing.T) {
	execution := kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}
	got, answer, err := parseKagentEvaluationResponse([]byte(v1EvaluationFixture()), execution, "input")
	if err != nil || answer != "  exact\nanswer\t " || got.State != "completed" || got.TaskID != "task-1" || got.ContextID != "context-1" {
		t.Fatalf("v1 exact result: %+v %q %v", got, answer, err)
	}
	if !suiteKagentResponseHistory([]byte(v1EvaluationFixture())) {
		t.Fatal("v1 history not recognized")
	}
	for name, change := range map[string]func(string) string{
		"wrong-rpc":     func(s string) string { return strings.Replace(s, `"rpc-1"`, `"other"`, 1) },
		"wrong-message": func(s string) string { return strings.Replace(s, `"request-1"`, `"other"`, 1) },
		"wrong-context": func(s string) string {
			return strings.Replace(s, `"contextId":"context-1","parts"`, `"contextId":"other","parts"`, 1)
		},
		"wrong-task":    func(s string) string { return strings.Replace(s, `"taskId":"task-1"`, `"taskId":"other"`, 1) },
		"unknown-union": func(s string) string { return strings.Replace(s, `"result":{`, `"result":{"extra":{},`, 1) },
		"both-unions":   func(s string) string { return strings.Replace(s, `"result":{`, `"result":{"message":{},`, 1) },
		"unknown-task":  func(s string) string { return strings.Replace(s, `"task":{`, `"task":{"extra":{},`, 1) },
		"duplicate-state": func(s string) string {
			return strings.Replace(s, `"state":"TASK_STATE_COMPLETED"`, `"state":"TASK_STATE_FAILED","state":"TASK_STATE_COMPLETED"`, 1)
		},
		"unknown-state":  func(s string) string { return strings.Replace(s, `TASK_STATE_COMPLETED`, `TASK_STATE_SURPRISE`, 1) },
		"v0-state-in-v1": func(s string) string { return strings.Replace(s, `TASK_STATE_COMPLETED`, `completed`, 1) },
		"v0-role-in-v1":  func(s string) string { return strings.Replace(s, `ROLE_USER`, `user`, 1) },
		"ambiguous-part": func(s string) string { return strings.Replace(s, `{"text":"input"}`, `{"text":"input","data":{}}`, 1) },
		"file-part": func(s string) string {
			return strings.Replace(s, `{"text":"input"}`, `{"file":{"uri":"http://invalid"}}`, 1)
		},
		"null-part": func(s string) string { return strings.Replace(s, `{"text":"input"}`, `{"text":null}`, 1) },
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseKagentEvaluationResponse([]byte(change(v1EvaluationFixture())), execution, "input"); err == nil {
				t.Fatal("unsafe v1 result accepted")
			}
		})
	}
}
func TestKagentV1RequestCarrier(t *testing.T) {
	raw, err := kagentEvaluationRequestBody("input", kagentEvaluationExecution{RPCID: "rpc-1", RequestMessageID: "request-1"}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var call struct {
		Method string
		Params struct {
			Message struct {
				Role  string
				Parts []map[string]any
			}
			Configuration map[string]any
		}
	}
	if err = json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	if call.Method != "SendMessage" || call.Params.Message.Role != "ROLE_USER" || len(call.Params.Message.Parts) != 1 || call.Params.Message.Parts[0]["text"] != "input" || len(call.Params.Message.Parts[0]) != 1 {
		t.Fatalf("not current v1 request %s", raw)
	}
	if v, ok := call.Params.Configuration["returnImmediately"]; !ok || v != false || len(call.Params.Configuration) != 1 {
		t.Fatalf("not blocking v1 %s", raw)
	}
}
