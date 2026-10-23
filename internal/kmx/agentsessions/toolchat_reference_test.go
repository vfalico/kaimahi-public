package agentsessions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harness/toolchatagent"
)

type toolchatFixture struct {
	toolchatagent.Harness
	models, tools *int
}
type toolchatFixtureSink struct {
	api.EventSink
	models, tools *int
}

func (s toolchatFixtureSink) Model(ctx context.Context, r api.ModelRequest) (api.ModelResponse, error) {
	r.Native = true
	r.InvokeNative = func(context.Context) (api.ModelResponse, error) {
		*s.models++
		if *s.models == 1 {
			return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "fetch-1", Tool: toolchatagent.ToolName, Args: map[string]any{"url": "http://example.invalid/"}}}}}}, nil
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", "complete")}, nil
	}
	return s.EventSink.Model(ctx, r)
}
func (s toolchatFixtureSink) ToolCall(ctx context.Context, c api.ToolCall) (api.ToolResult, error) {
	c.Native = true
	c.InvokeNative = func(context.Context) (api.ToolResult, error) {
		*s.tools++
		return api.ToolResult{ID: c.ID, Content: []api.Part{{Text: &api.TextPart{Text: "fixture"}}}}, nil
	}
	return s.EventSink.ToolCall(ctx, c)
}
func (h toolchatFixture) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	return h.Harness.Run(ctx, start, toolchatFixtureSink{sink, h.models, h.tools})
}
func toolchatRecords(t *testing.T) []eventlog.Record {
	t.Helper()
	log := eventlog.New()
	config, _ := json.Marshal(map[string]string{"system_prompt": "fixture instructions"})
	c, err := controller.New(eventlog.AsStore(log), nil, controller.WithStart(config, 0))
	if err != nil {
		t.Fatal(err)
	}
	models, tools := 0, 0
	if err = c.Exec(t.Context(), toolchatFixture{toolchatagent.Harness{Model: "fixture"}, &models, &tools}, []api.Message{*api.TextMessage("user", "fetch")}, 0); err != nil {
		t.Fatal(err)
	}
	if models != 2 || tools != 1 {
		t.Fatal(models, tools)
	}
	return log.Snapshot()
}
func TestToolchatReferenceReplaysNativeEffectsWithoutIO(t *testing.T) {
	records := toolchatRecords(t)
	for i := range records {
		records[i] = eventlog.RecordFromProto(eventlog.RecordToProto(records[i]))
	}
	if !toolchatInvocation(records) {
		t.Fatal("native conversation shape refused")
	}
	proof, err := referenceToolchatProof(t.Context(), records, "fixture", verifySHA256([]byte("complete")))
	if err != nil || proof.Status != "equivalent" || proof.ModelCalls != 0 {
		t.Fatal(proof, err)
	}
	if referenceChatInvocation(records) {
		t.Fatal("legacy tool-free profile broadened")
	}
}
func TestToolchatReferenceRefusesDriftAndIncompleteEffects(t *testing.T) {
	for _, name := range []string{"model-placement", "tool-placement", "tool-name", "tool-args", "result-id", "unfinished", "no-final", "model-hash", "input", "answer", "config", "approval", "tool-key", "host-harness", "unknown-model"} {
		t.Run(name, func(t *testing.T) {
			records := toolchatRecords(t)
			answer := verifySHA256([]byte("complete"))
			switch name {
			case "model-placement":
				records[2].Event.ModelCall.Native = false
			case "tool-placement":
				records[4].Event.ToolCall.Native = false
			case "tool-name":
				records[4].Event.ToolCall.Tool = "other"
			case "tool-args":
				records[4].Event.ToolCall.Args = map[string]any{"url": "http://changed.invalid/"}
			case "result-id":
				records[5].Event.Result.ID = "other"
			case "unfinished":
				records = records[:len(records)-1]
			case "no-final":
				records = append(records[:6], records[len(records)-1])
			case "model-hash":
				records[2].Event.ModelCall.InputHash = verifySHA256([]byte("other"))
			case "input":
				records[1].Event.Message = api.TextMessage("user", "changed")
			case "answer":
				answer = verifySHA256([]byte("other"))
			case "config":
				records[0].Event.ExecutionStart.Config = []byte(`{"system_prompt":"changed"}`)
			case "approval":
				records[5].Event.Result.ApprovalRequestSeq = 3
			case "tool-key":
				records[4].Event.ToolCall.IdempotencyKey = "other"
			case "host-harness":
				records[0].Event.ExecutionStart.Harness = "chat"
			case "unknown-model":
				records[6].Event.ModelCall.Model = "other"
			}
			proof, err := referenceToolchatProof(t.Context(), records, "fixture", answer)
			if err != nil || proof.Status == "equivalent" || proof.ModelCalls != 0 {
				t.Fatal("drift accepted or IO", name, proof, err)
			}
		})
	}
}
