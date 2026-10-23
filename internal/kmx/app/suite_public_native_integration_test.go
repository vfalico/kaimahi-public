//go:build nativeintegration

package app

import (
	"context"
	"encoding/json"
	"errors"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	sdk "github.com/agent-primitives/sessions/client"
	"github.com/agent-primitives/sessions/harness/toolchatagent"
	substrateadapter "github.com/agent-primitives/sessions/integrations/substrate"
	"github.com/agent-primitives/sessions/placement"
	substrateruntime "github.com/agent-primitives/sessions/runtime/substrate"
	atepb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	suitePublicNativePlacer = func(t *testing.T, nativeRoot string) *placement.Placer {
		router, err := os.ReadFile(filepath.Join(nativeRoot, "router-ready"))
		if err != nil {
			t.Fatal(err)
		}
		control, err := grpc.NewClient("unix://"+filepath.Join(nativeRoot, "control-rpc.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { control.Close() })
		descriptor, err := (toolchatagent.Harness{Model: "fixture"}).Describe(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		actual := substrateruntime.New(substrateadapter.New(control), "native", substrateruntime.ObjectRef{Atespace: "native", Name: "toolchat"}, descriptor, substrateruntime.WithRouter(string(router)))
		return placement.New(actual, nil)
	}
}

// Opt-in public Suite path through the production Sessions Substrate adapter,
// generated Control RPC, PostgreSQL workflow, real atelet/Envoy/atunnel/Kata.
func TestSuiteToolchatPublicNativeIntegration(t *testing.T) {
	root := os.Getenv("EXECUTION_PUBLIC_NATIVE_ROOT")
	if root == "" {
		t.Skip("explicit task resources required")
	}
	a, opt, _ := suiteToolchatEvaluationFixture(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	err = a.EvaluateSuiteWorkspace(t.Context(), p)
	if faultScenario := os.Getenv("EXECUTION_PUBLIC_NATIVE_SCENARIO"); faultScenario == "lost-resume" || faultScenario == "lost-create" {
		if err == nil {
			t.Fatal("lost Resume reply produced successful Suite receipt")
		}
		if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
			t.Fatal("uncertain execution left passing receipt", e)
		}
		raw, e := os.ReadFile(filepath.Join(root, "injected-"+faultScenario+".json"))
		if e != nil {
			t.Fatal("real native fault not reached", e)
		}
		var fault map[string]string
		if e = json.Unmarshal(raw, &fault); e != nil {
			t.Fatal(e)
		}
		uid, original := fault["name"], fault["uid"]
		if uid == "" || original == "" {
			t.Fatal("missing exact original native authority")
		}
		direct, e := sdk.Dial(p.sessions.host.Address)
		if e != nil {
			t.Fatal(e)
		}
		defer direct.Close()
		records, e := direct.Replay(t.Context(), uid, 1, 0)
		if e != nil {
			t.Fatal(e)
		}
		prepared, owned := 0, 0
		for _, record := range records {
			ev := record.GetEvent()
			if ev.GetExecutionStart() != nil {
				t.Fatal("turn dispatched after uncertain native Resume")
			}
			l := ev.GetLifecycle()
			if l == nil {
				continue
			}
			switch l.GetKind() {
			case v1.Lifecycle_LIFECYCLE_COMPUTE_PREPARED:
				prepared++
				if len(l.GetComputeOperation()) == 0 {
					t.Fatal("original operation not journaled")
				}
				if faultScenario == "lost-create" {
					var operation map[string]any
					if e = json.Unmarshal(l.GetComputeOperation(), &operation); e != nil {
						t.Fatal(e)
					}
					if operation["operation_id"] != fault["operation_id"] {
						t.Fatal("committed operation differs from durable pre-RPC journal")
					}
				}
			case v1.Lifecycle_LIFECYCLE_COMPUTE_OWNED:
				owned++
				if l.GetComputeInstance() != original {
					t.Fatal("retained UID not original native Actor")
				}
			}
		}
		expectedOwned := 1
		if faultScenario == "lost-create" {
			expectedOwned = 0
		}
		if prepared != 1 || owned != expectedOwned {
			t.Fatal("wrong durable native ownership prefix", prepared, owned)
		}
		for i := 0; i < 2; i++ {
			if _, e = direct.DeleteSession(t.Context(), uid); e != nil {
				t.Fatal("native uncertain Resume cleanup did not converge", e)
			}
		}
		control, e := grpc.NewClient("unix://"+filepath.Join(root, "control-rpc.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e != nil {
			t.Fatal(e)
		}
		defer control.Close()
		if _, e = atepb.NewControlClient(control).GetActor(t.Context(), &atepb.GetActorRequest{Actor: &atepb.ObjectRef{Atespace: "native", Name: uid}}); status.Code(e) != codes.NotFound {
			t.Fatal("original native Actor survived cleanup", e)
		}
		if _, e = direct.Exec(t.Context(), sdk.ExecOptions{Session: uid, Inputs: []string{"must not dispatch"}}); e == nil {
			t.Fatal("deleted uncertain session dispatched")
		}
		capture, e := json.Marshal(records)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, faultScenario+"-journal.json"), capture, 0600); e != nil {
			t.Fatal(e)
		}
		t.Log("PUBLIC_KAIMAHI_REAL_NATIVE_" + strings.ToUpper(faultScenario) + "_REFUSAL_NO_RECEIPT_PASS")
		return
	}

	if os.Getenv("EXECUTION_NATIVE_NETWORK_INDEPENDENT") == "1" {
		// This probe deliberately finishes with an error, including the useful
		// allow control; neither result may manufacture an inference receipt.
		if err == nil {
			t.Fatal("network-independent probe manufactured inference success")
		}
		if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
			t.Fatal("network-independent probe left inference receipt", e)
		}
		raw, e := os.ReadFile(filepath.Join(root, "guest-output.log"))
		if e != nil {
			t.Fatal(e)
		}
		expected := "NETWORK_INDEPENDENT_NATIVE_PROBE_ALLOWED"
		if os.Getenv("EXECUTION_PUBLIC_NATIVE_SCENARIO") == "empty" {
			expected = "NETWORK_INDEPENDENT_NATIVE_PROBE_DENIED"
		}
		if !strings.Contains(string(raw), "NETWORK_INDEPENDENT_NATIVE_PROBE_ATTEMPT") || !strings.Contains(string(raw), expected) {
			t.Fatal("native probe did not run", string(raw))
		}
		t.Log("PUBLIC_ORDINARY_DAEMON_NETWORK_INDEPENDENT_NO_INFERENCE_RECEIPT", expected)
		return
	}
	if scenario := os.Getenv("EXECUTION_PUBLIC_NATIVE_SCENARIO"); scenario == "deny" || scenario == "empty" {
		if err == nil {
			t.Fatal("forbidden public turn unexpectedly passed")
		}
		if _, statErr := os.Stat(opt.Out); !os.IsNotExist(statErr) {
			t.Fatalf("failed turn left passing receipt: %v", statErr)
		}
		if scenario != "deny" {
			t.Fatal("ordinary empty failure is not useful deny-all evidence; use network-independent probe")
		}
		// A failed model, transport, startup or arbitrary tool error cannot pass.
		// Require a structured native errno from this invocation's exact journal
		// call, session and immutable submitted policy. Receiver absence is checked
		// independently by the owning compute fixture after this returns.
		qualified := verifyOrdinaryNativeDenial(t, root, p.sessions.host.Address)
		data, e := json.MarshalIndent(qualified, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, "ordinary-native-denial.json"), data, 0600); e != nil {
			t.Fatal(e)
		}
		t.Logf("PUBLIC_KAIMAHI_REAL_NATIVE_%s_REFUSAL_NO_RECEIPT_PASS", strings.ToUpper(scenario))
		return
	}
	if err != nil {
		t.Fatalf("%v cause=%+v stdout=%v stderr=%v", err, errors.Unwrap(err), a.Out, a.Err)
	}

	receipt, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Cases) != 1 {
		t.Fatal("wrong case count")
	}
	raw, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "public-kaimahi-passing.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := agentsessions.Dial(agentsessions.Options{Address: p.sessions.host.Address})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	row := receipt.Cases[0]
	verifyCtx, verifyCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer verifyCancel()
	proof, err := client.VerifyCase(verifyCtx, agentsessions.VerifyRequest{SessionUID: row.SessionsExecution.SessionUID, Harness: "toolchat", Model: "fixture", Head: agentsessions.JournalHead{Seq: row.SessionsExecution.HeadSeq, Hash: row.SessionsExecution.HeadHash}, AnswerSHA256: strings.TrimPrefix(row.AnswerDigest, "sha256:")})
	if err != nil || proof.Status != "equivalent" || proof.ModelCalls != 0 {
		t.Fatal(proof, err)
	}

	if os.Getenv("EXECUTION_PUBLIC_NATIVE_SCENARIO") == "sessions-restart" {
		if suiteOrdinaryDaemonRestart == nil {
			t.Fatal("ordinary daemon restart hook absent")
		}
		direct, e := sdk.Dial(p.sessions.host.Address)
		if e != nil {
			t.Fatal(e)
		}
		defer direct.Close()
		uid := row.SessionsExecution.SessionUID
		before, e := direct.GetSession(t.Context(), uid)
		if e != nil {
			t.Fatal(e)
		}
		if e = suiteOrdinaryDaemonRestart(); e != nil {
			t.Fatal(e)
		}
		// New client, new real daemon process, same on-disk journal. No edits.
		reopened, e := sdk.Dial(p.sessions.host.Address)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		current, e := reopened.GetSession(t.Context(), uid)
		if e != nil || current.GetLastSeq() != before.GetLastSeq() {
			t.Fatal("journal/identity drift across daemon restart", current, e)
		}
		last := current.GetLastSeq()
		turn, e := reopened.Exec(t.Context(), sdk.ExecOptions{Session: uid, Inputs: []string{"fetch allowed after ordinary daemon restart"}, Config: []byte(`{"system_prompt":"Use native http_get and answer complete."}`), ExpectedLastSeq: &last})
		if e != nil || turn == nil || turn.Output != "complete" {
			t.Fatal("recovered native turn", turn, e)
		}
		if _, e = reopened.DeleteSession(t.Context(), uid); e != nil {
			t.Fatal(e)
		}
		if _, e = reopened.DeleteSession(t.Context(), uid); e != nil {
			t.Fatal("retry deletion", e)
		}
		final, e := reopened.GetSession(t.Context(), uid)
		if e != nil {
			t.Fatal(e)
		}
		finalLast := final.GetLastSeq()
		if _, e = reopened.Exec(t.Context(), sdk.ExecOptions{Session: uid, Inputs: []string{"must not run after deletion"}, ExpectedLastSeq: &finalLast}); e == nil {
			t.Fatal("deleted session dispatched after daemon restart")
		}
		control, e := grpc.NewClient("unix://"+filepath.Join(root, "control-rpc.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e != nil {
			t.Fatal(e)
		}
		defer control.Close()
		if _, e = atepb.NewControlClient(control).GetActor(t.Context(), &atepb.GetActorRequest{Actor: &atepb.ObjectRef{Atespace: "native", Name: uid}}); status.Code(e) != codes.NotFound {
			t.Fatal("native Actor remains after recovered deletion", e)
		}
		records, e := reopened.Replay(t.Context(), uid, 1, 0)
		if e != nil {
			t.Fatal(e)
		}
		archive, e := json.MarshalIndent(struct {
			Before, Reopened *v1.Session
			Records          []*v1.LogRecord
		}{before, current, records}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, "public-sessions-daemon-restart.json"), archive, 0600); e != nil {
			t.Fatal(e)
		}
		t.Log("PUBLIC_ORDINARY_SESSIONS_DAEMON_SIGKILL_JOURNAL_REOPEN_NATIVE_TURN_DELETE_RETRY_PASS")
	}

	if os.Getenv("EXECUTION_PUBLIC_NATIVE_SCENARIO") == "restart" {
		direct, e := sdk.Dial(p.sessions.host.Address)
		if e != nil {
			t.Fatal(e)
		}
		defer direct.Close()
		uid := receipt.Cases[0].SessionsExecution.SessionUID
		before, e := direct.GetSession(t.Context(), uid)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, "restart-request"), nil, 0600); e != nil {
			t.Fatal(e)
		}

		deadlineDown := time.Now().Add(12 * time.Second)
		for {
			if _, e = os.Stat(filepath.Join(root, "restart-stopped")); e == nil {
				break
			}
			if time.Now().After(deadlineDown) {
				t.Fatal("old daemon death not observed", e)
			}
			time.Sleep(25 * time.Millisecond)
		}
		// No runtime send may follow an unavailable fresh enforcement check.
		// This refusal is not a passing receipt or a silently repaired binding.
		downLast := before.GetLastSeq()
		_, downErr := direct.Exec(t.Context(), sdk.ExecOptions{Session: uid, Inputs: []string{"fetch while enforcer unavailable"}, Config: []byte(`{"system_prompt":"Use native http_get."}`), ExpectedLastSeq: &downLast})
		if downErr == nil {
			t.Fatal("warm execution admitted without current enforcer readback")
		}
		if e = os.WriteFile(filepath.Join(root, "restart-continue"), nil, 0600); e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(12 * time.Second)
		for {
			if _, e = os.Stat(filepath.Join(root, "restart-ready")); e == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("actual daemon restart not ready", e)
			}
			time.Sleep(25 * time.Millisecond)
		}
		config := []byte(`{"system_prompt":"Use native http_get and answer complete."}`)
		current, e := direct.GetSession(t.Context(), uid)
		if e != nil {
			t.Fatal(e)
		}
		last := current.GetLastSeq()

		// The delivered lifecycle elects destructive stop on a failed governed
		// operation. Never reinterpret its cleanup-only authority as restart authority.
		continued, e := direct.Exec(t.Context(), sdk.ExecOptions{Session: uid, Inputs: []string{"fetch allowed"}, Config: config, ExpectedLastSeq: &last})
		if e == nil {
			t.Fatal("retired operation resumed after enforcer return", continued)
		}
		for i := 0; i < 2; i++ {
			if _, e = direct.DeleteSession(t.Context(), uid); e != nil {
				t.Fatal("ordinary deletion did not finish retained cleanup", i, e)
			}
		}
		// Read the actual owning Control service, not Sessions' stale projection.
		control, e := grpc.NewClient("unix://"+filepath.Join(root, "control-rpc.sock"), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e != nil {
			t.Fatal(e)
		}
		defer control.Close()
		owner := atepb.NewControlClient(control)
		if _, e = owner.GetActor(t.Context(), &atepb.GetActorRequest{Actor: &atepb.ObjectRef{Atespace: "native", Name: uid}}); status.Code(e) != codes.NotFound {
			t.Fatal("original Actor retained after exact cleanup", e)
		}
		final, e := direct.GetSession(t.Context(), uid)
		if e != nil {
			t.Fatal(e)
		}
		finalLast := final.GetLastSeq()
		if _, e = direct.Exec(t.Context(), sdk.ExecOptions{Session: uid, Inputs: []string{"must not run after deletion"}, Config: config, ExpectedLastSeq: &finalLast}); e == nil {
			t.Fatal("deleted session dispatched")
		}
		records, e := direct.Replay(t.Context(), uid, 1, 0)
		if e != nil {
			t.Fatal(e)
		}
		archive := struct {
			Before, Final *v1.Session
			Records       []*v1.LogRecord
		}{before, final, records}
		evidence, e := json.MarshalIndent(archive, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(root, "public-session-native-restart.json"), evidence, 0600); e != nil {
			t.Fatal(e)
		}
		t.Log("PUBLIC_SESSIONS_REAL_SIGKILL_OUTAGE_STOP_IRREVERSIBLE_REFUSAL_RETRY_CLEANUP_PASS")
	}
	t.Log("PUBLIC_KAIMAHI_REAL_NATIVE_TOOLCHAT_REPLAY_PASS")
}
