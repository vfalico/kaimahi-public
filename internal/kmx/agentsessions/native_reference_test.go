package agentsessions

import (
	"context"
	"encoding/json"
	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/harness/chatagent"
	"github.com/agent-primitives/sessions/wire"
	"testing"
)

type nativeFixtureSink struct {
	api.EventSink
	calls *int
}

func (s nativeFixtureSink) Model(ctx context.Context, req api.ModelRequest) (api.ModelResponse, error) {
	req.Native = true
	req.InvokeNative = func(context.Context) (api.ModelResponse, error) {
		*s.calls++
		return api.ModelResponse{Message: *api.TextMessage("assistant", replayAnswer)}, nil
	}
	return s.EventSink.Model(ctx, req)
}

type nativeFixtureHarness struct {
	chatagent.Harness
	calls *int
}

func (h nativeFixtureHarness) Run(ctx context.Context, start *api.Start, sink api.EventSink) error {
	return h.Harness.Run(ctx, start, nativeFixtureSink{sink, h.calls})
}
func TestNativeReferenceChatRetainsPlacementAndNeverCallsProvider(t *testing.T) {
	r := suiteRequest()
	config, err := json.Marshal(struct {
		SystemPrompt string `json:"system_prompt"`
	}{r.Instructions})
	if err != nil {
		t.Fatal(err)
	}
	log := eventlog.New()
	c, err := controller.New(eventlog.AsStore(log), nil, controller.WithStart(config, 0))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err = c.Exec(t.Context(), nativeFixtureHarness{chatagent.Harness{Model: r.Model}, &calls}, []api.Message{*api.TextMessage("user", r.Input)}, 0); err != nil {
		t.Fatal(err)
	}
	records := log.Snapshot()
	if !records[2].Event.ModelCall.Native || calls != 1 {
		t.Fatal("native placement was not committed")
	}
	// Canonical wire round-trip must preserve placement, not reinterpret a hash.
	for i := range records {
		records[i] = eventlog.RecordFromProto(eventlog.RecordToProto(records[i]))
	}
	if !records[2].Event.ModelCall.Native || !wire.EventFromProto(wire.EventToProto(records[2].Event)).ModelCall.Native {
		t.Fatal("native placement lost on wire")
	}
	proof, err := referenceChatProof(t.Context(), records, r.Model, verifySHA256([]byte(replayAnswer)))
	if err != nil || proof.Status != "equivalent" || proof.ModelCalls != 0 || calls != 1 {
		t.Fatal(proof, calls, err)
	}
	records[2].Event.ModelCall.Native = false
	proof, err = referenceChatProof(t.Context(), records, r.Model, verifySHA256([]byte(replayAnswer)))
	if err != nil || proof.Status != "mismatch" || calls != 1 {
		t.Fatal("placement change accepted", proof, err)
	}
}
