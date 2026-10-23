package agentsessions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/canon"
	"github.com/agent-primitives/sessions/eventlog"
	"github.com/agent-primitives/sessions/wire"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Explicit fake remote records. Hash chaining and reference harness replay are
// actual implementations; no native enforcement/activation is asserted here.
func withOwnershipPrefix(t *testing.T, s *sessionsServer, r SuiteCaseRequest) {
	t.Helper()
	setSessionRequirement(s.created, r.ExecutionRequirement)
	setSessionRequirement(s.updates[0].GetSession(), r.ExecutionRequirement)
	l := &api.Lifecycle{Kind: api.LifecycleComputeOwned, ComputeID: "session-1", ComputeRuntime: "fixture", ComputeHarness: s.created.GetHarness(), ComputeInstance: "original-uid", ComputeRequirement: append([]byte(nil), r.ExecutionRequirement...)}
	ev := wire.EventToProto(api.Event{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: l})
	prefix := &v1.ExecUpdate{Update: &v1.ExecUpdate_Record{Record: &v1.LogRecord{Seq: 1, Event: ev}}}
	s.updates = append(s.updates[:1], append([]*v1.ExecUpdate{prefix}, s.updates[1:]...)...)
	for i, u := range s.updates[1:] {
		rec := u.GetRecord()
		rec.Seq = int64(i + 1)
		if start := rec.GetEvent().GetExecutionStart(); start != nil {
			start.ComputeInstance = "original-uid"
		}
	}
	rehashSuite(t, s)
}

func TestSuiteCleanupOwnershipPrefixIsNotInvocationOrActivation(t *testing.T) {
	for _, scenario := range []string{"exact", "changed-policy", "changed-uid", "other-session", "other-harness", "empty-runtime", "snapshot", "extra-detail", "duplicate", "policy-free", "nonempty-execution", "unknown-wire", "tampered-hash"} {
		t.Run(scenario, func(t *testing.T) {
			req := suiteRequest()
			req.ExecutionRequirement = []byte(`{"opaque":true}`)
			s := suiteServerForRequest(t, req)
			withOwnershipPrefix(t, s, req)
			e := s.updates[1].GetRecord().Event
			l := e.GetLifecycle()
			switch scenario {
			case "changed-policy":
				l.ComputeRequirement = []byte(`{"opaque":false}`)
			case "changed-uid":
				l.ComputeInstance = "replacement-uid"
			case "other-session":
				l.ComputeId = "other-session"
			case "other-harness":
				l.ComputeHarness = "toolchat"
			case "empty-runtime":
				l.ComputeRuntime = ""
			case "snapshot":
				l.SnapshotLocal = "snapshot"
			case "extra-detail":
				l.Detail = "activation-proof"
			case "duplicate":
				dup := proto.Clone(s.updates[1]).(*v1.ExecUpdate)
				s.updates = append(s.updates[:2], append([]*v1.ExecUpdate{dup}, s.updates[2:]...)...)
				for i, u := range s.updates[1:] {
					u.GetRecord().Seq = int64(i + 1)
				}
			case "policy-free":
				req.ExecutionRequirement = nil
				setSessionRequirement(s.created, nil)
				setSessionRequirement(s.updates[0].GetSession(), nil)
			case "nonempty-execution":
				e.ExecutionId = "unexpected-turn"
			case "unknown-wire":
				l.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			}
			rehashSuite(t, s)
			if scenario == "tampered-hash" {
				s.updates[1].GetRecord().ContentHash = strings.Repeat("0", 64)
			}
			got, err := runSuite(t, s, req)
			if scenario == "exact" {
				if err != nil || got.Head.Seq != 6 || got.Output != replayAnswer {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("invalid cleanup metadata became invocation evidence", scenario, got)
			}
		})
	}
}

func TestToolchatOwnershipPrefixPreservesFullReferenceChain(t *testing.T) {
	records := toolchatRecords(t)
	l := &api.Lifecycle{Kind: api.LifecycleComputeOwned, ComputeID: "session-1", ComputeRuntime: "fixture", ComputeHarness: "toolchat", ComputeInstance: "original-uid", ComputeRequirement: []byte(`{"opaque":true}`)}
	records = append([]eventlog.Record{{Event: api.Event{Kind: api.EventLifecycle, SchemaVersion: 1, Lifecycle: l}}}, records...)
	records[1].Event.ExecutionStart.ComputeInstance = "original-uid"
	prev := ""
	for i := range records {
		records[i].Seq = int64(i + 1)
		records[i].PrevHash = prev
		hash, err := canon.HashRecord(prev, records[i].Seq, records[i].Event)
		if err != nil {
			t.Fatal(err)
		}
		records[i].Hash = hash
		prev = hash
	}
	if !toolchatInvocation(records) {
		t.Fatal("valid cleanup-only prefix not recognized")
	}
	proof, err := referenceToolchatProof(t.Context(), records, "fixture", verifySHA256([]byte("complete")))
	if err != nil || proof.Status != "equivalent" || proof.ModelCalls != 0 {
		t.Fatal(proof, err)
	}
	// Reader verifies the entire retained chain, including ownership, without any
	// live runtime or model. Request session must still match original owner.
	wireRecords := make([]*v1.LogRecord, len(records))
	for i, r := range records {
		wireRecords[i] = eventlog.RecordToProto(r)
	}
	c := replayClient(t, &replayServer{records: wireRecords})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req := VerifyRequest{SessionUID: "session-1", Harness: "toolchat", Model: "fixture", Head: JournalHead{Seq: records[len(records)-1].Seq, Hash: records[len(records)-1].Hash}, AnswerSHA256: verifySHA256([]byte("complete"))}
	proof, err = c.VerifyCase(ctx, req)
	if err != nil || proof.Status != "equivalent" {
		t.Fatal(proof, err)
	}
	req.SessionUID = "unrelated"
	proof, err = c.VerifyCase(ctx, req)
	if err != nil || proof.Status != "mismatch" {
		t.Fatal(proof, err)
	}
	if status.Code(err) != codes.OK {
		t.Fatal(err)
	}
}
