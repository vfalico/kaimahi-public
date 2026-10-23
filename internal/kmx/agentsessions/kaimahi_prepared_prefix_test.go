package agentsessions

import (
	"testing"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/protobuf/proto"
)

func TestSuitePreparedOpaqueOperationPrefixRemainsCleanupOnly(t *testing.T) {
	for _, scenario := range []string{"exact", "changed-policy", "other-session", "other-harness", "target-uid", "runtime", "empty-operation", "duplicate", "missing-owned", "unknown-wire"} {
		t.Run(scenario, func(t *testing.T) {
			req := suiteRequest()
			req.ExecutionRequirement = []byte(`{"opaque":true}`)
			s := suiteServerForRequest(t, req)
			withOwnershipPrefix(t, s, req)
			ev := wire.EventToProto(api.Event{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: &api.Lifecycle{Kind: api.LifecycleComputePrepared, ComputeID: "session-1", ComputeHarness: s.created.GetHarness(), ComputeRequirement: append([]byte(nil), req.ExecutionRequirement...), ComputeOperation: []byte(`{"opaque-owner-op":"original"}`)}})
			l := ev.GetLifecycle()
			switch scenario {
			case "changed-policy":
				l.ComputeRequirement = []byte(`{"opaque":false}`)
			case "other-session":
				l.ComputeId = "other"
			case "other-harness":
				l.ComputeHarness = "other"
			case "target-uid":
				l.ComputeInstance = "not-cleanup-only"
			case "runtime":
				l.ComputeRuntime = "unexpected"
			case "empty-operation":
				l.ComputeOperation = nil
			case "unknown-wire":
				ev.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			}
			prefix := &v1.ExecUpdate{Update: &v1.ExecUpdate_Record{Record: &v1.LogRecord{Event: ev}}}
			s.updates = append(s.updates[:1], append([]*v1.ExecUpdate{prefix}, s.updates[1:]...)...)
			if scenario == "duplicate" {
				s.updates = append(s.updates[:2], append([]*v1.ExecUpdate{proto.Clone(prefix).(*v1.ExecUpdate)}, s.updates[2:]...)...)
			}
			if scenario == "missing-owned" {
				s.updates = append(s.updates[:2], s.updates[3:]...)
			}
			for i, u := range s.updates[1:] {
				u.GetRecord().Seq = int64(i + 1)
			}
			rehashSuite(t, s)
			result, err := runSuite(t, s, req)
			if scenario == "exact" {
				if err != nil || result.Output != replayAnswer {
					t.Fatal("exact prefix refused", result, err)
				}
			} else if err == nil {
				t.Fatal("invalid prepared prefix accepted", result)
			}
		})
	}
}
