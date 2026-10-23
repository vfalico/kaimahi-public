package agentsessions

import (
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/eventlog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

// Real public gRPC adapter with fake remote host; reference records come from
// the actual native effect controller fixture, not an enforcement acceptance.
func TestToolchatSuiteExplicitProfileAndMultiEffectProof(t *testing.T) {
	for _, mode := range []string{"exact", "chat-profile", "no-requirement", "host-tool", "changed-args", "changed-input", "omitted-result"} {
		t.Run(mode, func(t *testing.T) {
			req := suiteRequest()
			req.Harness = "toolchat"
			req.ExecutionRequirement = []byte(`{"opaque":true}`)
			req.Instructions = "fixture instructions"
			req.Model = "fixture"
			req.Input = "fetch"
			created := newSession()
			created.Harness = "toolchat"
			setSessionRequirement(created, req.ExecutionRequirement)
			s := &sessionsServer{created: created, updates: []*v1.ExecUpdate{sessionFrame(created)}}
			for _, r := range toolchatRecords(t) {
				s.updates = append(s.updates, &v1.ExecUpdate{Update: &v1.ExecUpdate_Record{Record: eventlog.RecordToProto(r)}})
			}
			switch mode {
			case "chat-profile":
				req.Harness = ""
			case "no-requirement":
				req.ExecutionRequirement = nil
			case "host-tool":
				s.updates[5].GetRecord().Event.GetTool().Native = false
				rehashSuite(t, s)
			case "changed-args":
				s.updates[5].GetRecord().Event.GetTool().Args = nil
				rehashSuite(t, s)
			case "changed-input":
				req.Input = "changed"
			case "omitted-result":
				s.updates = append(s.updates[:6], s.updates[7:]...)
			}
			got, err := runSuite(t, s, req)
			if mode == "exact" {
				if err != nil || got.Output != "complete" || got.Harness != "toolchat" || got.ExecutionID == "" || got.ConfigDigest == "" || got.ToolCalls["http_get"] != 1 {
					t.Fatal(got, err)
				}
				if s.execRequest.Harness != "toolchat" || s.createRequest.Session.Harness != "toolchat" {
					t.Fatal("profile lost")
				}
			} else if err == nil {
				t.Fatal("invalid toolchat profile/evidence accepted", mode, got)
			}
			if mode == "no-requirement" && status.Code(err) != codes.InvalidArgument {
				t.Fatal(err)
			}
		})
	}
}
