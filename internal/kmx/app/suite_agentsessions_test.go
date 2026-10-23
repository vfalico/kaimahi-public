package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/harness/chatagent"
	"github.com/agent-primitives/sessions/harness/toolchatagent"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/runtime/local"
	"github.com/agent-primitives/sessions/session"
	"github.com/agent-primitives/sessions/sqlitelog"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// A real pinned Sessions service and chat harness exercise config/journal semantics;
// the deterministic host-owned model is an instruction-sensitive test fixture.
func suiteSessionsEvaluationFixture(t *testing.T, native ...*suiteNativeComputeFixture) (*App, SuiteWorkspaceEvaluationOptions, *atomic.Int64) {
	t.Helper()
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	dir := privateSuiteDir(t)
	store, err := sqlitelog.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	calls := new(atomic.Int64)
	backend := local.New(chatagent.Harness{Model: "fixture-model"})
	t.Cleanup(func() { backend.Close() })
	provider := func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		answer := "5"
		if len(req.Messages) == 2 && req.Messages[0].Text() == "Answer only four." {
			answer = "4"
		}
		return api.ModelResponse{Message: *api.TextMessage("assistant", answer)}, nil
	}
	placer := placement.New(backend, provider)
	if len(native) != 0 {
		owned := native[0]
		owned.Backend = backend
		listener := bufconn.Listen(1 << 20)
		server := grpc.NewServer()
		v1.RegisterHarnessServer(server, harnesswire.NewNativeServer(chatagent.Harness{Model: "fixture-model"}, provider))
		go server.Serve(listener)
		t.Cleanup(server.Stop)
		conn, err := grpc.NewClient("passthrough:///native", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		placer = placement.New(owned, nil, placement.WithDialer(func(api.Incarnation) (api.Harness, func() error, error) {
			return harnesswire.NewClientHarness(v1.NewHarnessClient(conn)), func() error { return nil }, nil
		}))
	}

	routes, err := placement.NewRegistry("chat", map[string]*placement.Placer{"chat": placer})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	v1.RegisterSessionsServer(server, session.NewService(store, routes))
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "Answer only four.")
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "fixture-model", InstructionsFile: input, Runtime: workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsVersion, Engine: "chat"}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: first\ninput: Say four\nexpectContains: ['4']\n")
	writeFile(t, filepath.Join(root, "eval/second.yml"), "id: second\ninput: Say four again\nexpectContains: ['4']\n")
	host := map[string]any{"schemaVersion": 1, "address": listener.Addr().String(), "observation": map[string]any{
		"mode": "operator-observed-development", "hostID": "owned-host", "journalID": "owned-journal", "runtimeVersion": workspace.AgentSessionsVersion,
		"runtimeBinaryDigest": "sha256:" + strings.Repeat("a", 64), "runtimeConfigDigest": "sha256:" + strings.Repeat("b", 64), "inferenceID": "owned-inference", "inferenceRevision": "sha256:" + strings.Repeat("c", 64),
		"model": "fixture-model", "harness": "chat", "harnessVersion": "1", "retention": "operator-managed",
	}}
	data, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	hostPath := filepath.Join(dir, "host.json")
	if err := os.WriteFile(hostPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	opt := SuiteWorkspaceEvaluationOptions{Workspace: root, DevelopmentHost: hostPath, Out: filepath.Join(privateSuiteDir(t), "pass.json"), CaseTimeout: 45 * time.Second}
	return &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, opt, calls
}

func TestSuiteSessionsEvaluationFullPassAndExactPush(t *testing.T) {
	a, opt, calls := suiteSessionsEvaluationFixture(t)
	t.Setenv("PATH", t.TempDir())
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("full inventory did not execute once", calls.Load())
	}
	r, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil || r.Sessions == nil || len(r.Cases) != 2 {
		t.Fatal("missing complete runtime proof", err)
	}
	if r.Cases[0].SessionsExecution.SessionUID == r.Cases[1].SessionsExecution.SessionUID {
		t.Fatal("reused Session")
	}
	if !strings.Contains(a.Err.(*bytes.Buffer).String(), "Development: unauthenticated operator-observed host; not authenticated remote promotion.") {
		t.Fatal("missing trust warning")
	}
	result, err := a.pushSuiteWorkspaceEvaluated(t.Context(), opt.Workspace, "suite:v1", filepath.Join(t.TempDir(), "layout"), false, false, SuitePushProvenance{}, opt.Out)
	if err != nil || result.Descriptor.Digest.String() != p.suite.Digest {
		t.Fatal("evaluated push changed S", err)
	}
	client, err := agentsessions.Dial(agentsessions.Options{Address: p.sessions.host.Address})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e := r.Cases[0].SessionsExecution
	proof, err := client.VerifyCase(ctx, agentsessions.VerifyRequest{SessionUID: e.SessionUID, Harness: e.Harness, Model: p.suite.Model, Head: agentsessions.JournalHead{Seq: e.HeadSeq, Hash: e.HeadHash}, AnswerSHA256: r.Cases[0].AnswerDigest})
	if err != nil || proof.Status != "equivalent" || proof.ModelCalls != 0 || calls.Load() != 2 {
		t.Fatal("replay reached model or changed proof", proof, err, calls.Load())
	}
}

func TestSuiteSessionsEvaluationStopsWithoutExport(t *testing.T) {
	for _, failure := range []string{"instruction", "host drift", "source drift", "warning", "case output", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			a, opt, calls := suiteSessionsEvaluationFixture(t)
			if failure == "instruction" {
				writeFile(t, filepath.Join(opt.Workspace, "agents/greeter/instructions.md"), "Answer only five.")
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if failure == "warning" {
				a.Err = evaluationDiffFailWriter{}
			}
			if failure == "case output" {
				a.Out = suiteCaseSummaryFailWriter{needle: "first: pass"}
			}
			if failure == "host drift" || failure == "source drift" {
				a.Out = suiteSummaryObserver(func(raw []byte) (int, error) {
					if bytes.Contains(raw, []byte("first: pass")) {
						if failure == "host drift" {
							writeFile(t, opt.DevelopmentHost, "{}")
						} else {
							writeFile(t, filepath.Join(opt.Workspace, "agents/greeter/instructions.md"), "changed")
						}
					}
					return len(raw), nil
				})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if failure == "cancel" {
				cancel()
			}
			if err := a.EvaluateSuiteWorkspace(ctx, p); err == nil {
				t.Fatal("failure exported a pass")
			}
			if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
				t.Fatal("failure left passing output", err)
			}
			expected := int64(1)
			if failure == "warning" || failure == "cancel" {
				expected = 0
			}
			if calls.Load() != expected {
				t.Fatal("later case ran after failure", calls.Load())
			}
		})
	}
}

func TestSuiteSessionsLatePublicationFailureReportsExistingCompleteProof(t *testing.T) {
	a, opt, calls := suiteSessionsEvaluationFixture(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Exercise the shared publisher's documented post-link directory-sync error.
	if err := p.output.directory.Close(); err != nil {
		t.Fatal(err)
	}
	err = a.EvaluateSuiteWorkspace(t.Context(), p)
	if !errors.Is(err, errSuitePassingExport) || !strings.Contains(err.Error(), "output may exist") {
		t.Fatalf("publication ambiguity was hidden: %v", err)
	}
	r, readErr := readSuitePassingReceipt(t.Context(), opt.Out)
	if readErr != nil || r.Sessions == nil || len(r.Cases) != 2 || calls.Load() != 2 {
		t.Fatalf("post-link failure changed complete-pass semantics: %v", readErr)
	}
}

type suiteSessionsCauseWriter struct{ cause error }

func (w suiteSessionsCauseWriter) Write([]byte) (int, error) { return 0, w.cause }

func TestSuiteSessionsFailurePreservesCauseWithoutPrintingPayload(t *testing.T) {
	a, opt, calls := suiteSessionsEvaluationFixture(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	sentinel := errors.New("PRIVATE_FAILURE_CANARY")
	cause := errors.Join(sentinel, status.Error(codes.DeadlineExceeded, "PRIVATE_FAILURE_CANARY"))
	a.Err = suiteSessionsCauseWriter{cause}
	err = a.EvaluateSuiteWorkspace(t.Context(), p)
	if !errors.Is(err, sentinel) || status.Code(err) != codes.DeadlineExceeded || strings.Contains(err.Error(), "PRIVATE_FAILURE_CANARY") {
		t.Fatalf("failure lost cause/status or exposed payload: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("warning failure reached model")
	}
	if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
		t.Fatal("warning failure exported proof")
	}
}

func TestSuiteSessionsJudgedRepeats(t *testing.T) {
	for _, outcome := range []string{"pass", "threshold", "late split", "model mismatch", "unavailable", "deadline", "host drift", "source drift"} {
		t.Run(outcome, func(t *testing.T) {
			a, opt, executions := suiteSessionsEvaluationFixture(t)
			caseYAML := suiteJudgeYAML + "runs: 3\nminPasses: 2\n"
			if outcome == "model mismatch" {
				caseYAML = strings.Replace(caseYAML, "judgeModel: judge-model", "judgeModel: other-model", 1)
			}
			writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), caseYAML)
			endpoint, samples := suiteJudgeFixtureServer(t, func(r *http.Request, n int) int {
				switch outcome {
				case "threshold":
					return 20
				case "pass":
					if n <= 2 {
						return 20
					}
				case "late split":
					if n == 5 {
						return 20
					}
				case "unavailable":
					return -1
				case "deadline":
					<-r.Context().Done()
				case "host drift":
					writeFile(t, opt.DevelopmentHost, "{}")
				case "source drift":
					writeFile(t, filepath.Join(opt.Workspace, "agents/greeter/instructions.md"), "changed")
				}
				return 90
			})
			opt.JudgeBaseURL, opt.ManagedPass = endpoint, true
			if outcome == "deadline" {
				opt.CaseTimeout = 2 * time.Second
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			err = a.EvaluateSuiteWorkspace(t.Context(), p)
			wantExecutions, wantSamples := int64(1), int32(2)
			if outcome == "pass" {
				wantExecutions, wantSamples = 4, 6
			}
			if outcome == "threshold" || outcome == "late split" {
				wantExecutions, wantSamples = 3, 6
			}
			if outcome == "deadline" {
				wantSamples = 1
			}
			if executions.Load() != wantExecutions || samples.Load() != wantSamples {
				t.Fatalf("unexpected work: executions=%d samples=%d error=%v", executions.Load(), samples.Load(), err)
			}
			if outcome != "pass" {
				if err == nil || len(p.completed) != 0 || p.record != "" {
					t.Fatal("non-pass registered evidence", err)
				}
				if outcome == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("lost judge deadline", err)
				}
				if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
					t.Fatal("non-pass exported evidence", e)
				}
				if strings.Contains(a.Out.(*bytes.Buffer).String(), "first run 1/3: pass") && strings.HasSuffix(outcome, "drift") {
					t.Fatal("drift credited pass")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r, err := readSuitePassingReceipt(t.Context(), opt.Out)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Cases) != 4 || r.Cases[0].Verdict != "fail" || r.Cases[0].Run != 1 || r.Cases[2].Run != 3 || r.Cases[3].Run != 0 {
				t.Fatal("lost actual outcomes or ordinals")
			}
			seenSessions, seenExecutions := map[string]bool{}, map[string]bool{}
			for _, row := range r.Cases {
				e := row.SessionsExecution
				if seenSessions[e.SessionUID] || seenExecutions[e.ExecutionID] || e.HeadSeq != 5 {
					t.Fatal("reused identity or changed journal contract")
				}
				seenSessions[e.SessionUID], seenExecutions[e.ExecutionID] = true, true
			}
			if !strings.Contains(a.Err.(*bytes.Buffer).String(), "4 fresh Sessions") || !strings.Contains(a.Err.(*bytes.Buffer).String(), "6 independent requests") {
				t.Fatal("missing work disclosure")
			}
			if strings.Contains(fmt.Sprint(a.Out, a.Err), suiteJudgeCanary) {
				t.Fatal("judge payload leaked")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			layout := filepath.Join(t.TempDir(), "layout")
			result, err := a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: opt.Workspace, Reference: "suite:v1", Layout: layout})
			if err != nil || result.Descriptor.Digest.String() != p.suite.Digest {
				t.Fatal("managed exact-S push", err)
			}
			if _, err := a.PushSuiteWorkspaceEvaluated(t.Context(), opt.Workspace, "suite:v2", filepath.Join(t.TempDir(), "explicit"), false, false, SuitePushProvenance{}, opt.Out); err != nil {
				t.Fatal("explicit push", err)
			}
			if err := os.Rename(opt.Workspace, opt.Workspace+"-withheld"); err != nil {
				t.Fatal(err)
			}
			artifact, err := suiteoras.ReadArtifactLayout(t.Context(), layout, p.suite.Digest)
			if err != nil {
				t.Fatal(err)
			}
			suite, err := workspace.LoadArtifact(t.Context(), artifact)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonicalSuitePassingReceipt(t.Context(), r, suite)
			if err != nil {
				t.Fatal("source-free coherence", err)
			}
			for _, mutation := range []string{"missing", "ordinal", "threshold", "unknown", "session", "execution", "style", "model", "scores"} {
				t.Run(mutation, func(t *testing.T) {
					bad, e := parseSuitePassingReceipt(raw)
					if e != nil {
						t.Fatal(e)
					}
					switch mutation {
					case "missing":
						bad.Cases = bad.Cases[1:]
					case "ordinal":
						bad.Cases[1].Run = 1
					case "threshold":
						for i := 0; i < 3; i++ {
							bad.Cases[i].MinPasses = 3
						}
					case "unknown":
						bad.Cases[2].Verdict = "unknown"
					case "session":
						bad.Cases[1].SessionsExecution.SessionUID = bad.Cases[0].SessionsExecution.SessionUID
					case "execution":
						bad.Cases[1].SessionsExecution.ExecutionID = bad.Cases[0].SessionsExecution.ExecutionID
					case "style":
						bad.Cases[0].Assertions[0].Judge.RequestStyle = ""
					case "model":
						bad.Cases[0].Assertions[0].Judge.Model = "other-model"
					case "scores":
						bad.Cases[0].Assertions[0].Judge.Scores = []int{90, 90}
					}
					if _, e := canonicalSuitePassingReceipt(t.Context(), bad, suite); !errors.Is(e, errSuitePassingReceipt) {
						t.Fatal("tampered source-free proof accepted", e)
					}
				})
			}
			if executions.Load() != 4 || samples.Load() != 6 {
				t.Fatal("offline consumers executed work")
			}
		})
	}
}

func TestSuiteSessionsDevelopmentSelectionRefusesBeforeEffects(t *testing.T) {
	for _, failure := range []string{"missing selection", "cluster", "inference", "bindings", "port", "namespace", "remote", "hostname", "model", "pin", "unknown", "symlink"} {
		t.Run(failure, func(t *testing.T) {
			_, opt, calls := suiteSessionsEvaluationFixture(t)
			raw, err := os.ReadFile(opt.DevelopmentHost)
			if err != nil {
				t.Fatal(err)
			}
			var host map[string]any
			if err := json.Unmarshal(raw, &host); err != nil {
				t.Fatal(err)
			}
			observation := host["observation"].(map[string]any)
			switch failure {
			case "missing selection":
				opt.DevelopmentHost = ""
			case "cluster":
				opt.ToContext = "kind-other"
			case "inference":
				opt.Inference = "provider:other"
			case "bindings":
				opt.Bindings = "missing"
			case "port":
				opt.ResultPort = "19180"
			case "namespace":
				opt.ToNamespace = "orka-system"
			case "remote":
				host["address"] = "192.0.2.1:8080"
			case "hostname":
				host["address"] = "localhost:8080"
			case "model":
				observation["model"] = "other-model"
			case "pin":
				observation["runtimeVersion"] = "main"
			case "unknown":
				host["private"] = "unrecognized"
			case "symlink":
				alias := filepath.Join(filepath.Dir(opt.DevelopmentHost), "alias.json")
				if err := os.Symlink(opt.DevelopmentHost, alias); err != nil {
					t.Fatal(err)
				}
				opt.DevelopmentHost = alias
			}
			if failure == "remote" || failure == "hostname" || failure == "model" || failure == "pin" || failure == "unknown" {
				raw, err = json.Marshal(host)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(opt.DevelopmentHost, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if p != nil {
				p.Close()
				t.Fatal("unsupported selector prepared execution")
			}
			if err == nil || calls.Load() != 0 {
				t.Fatal("unsupported mode reached host", err)
			}
			if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
				t.Fatal("unsupported mode wrote evidence")
			}
		})
	}
}

func TestSuitePolicyPublicEvalRefusesHostCallbacksBeforeModel(t *testing.T) {
	a, opt, calls := suiteSessionsEvaluationFixture(t)
	agent := filepath.Join(opt.Workspace, "agents/greeter/member.yaml")
	raw, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, agent, string(raw)+"policy: policy.json\n")
	writeFile(t, filepath.Join(opt.Workspace, "agents/greeter/policy.json"), `{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[]}}`)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal("public authored policy preflight refused", err)
	}
	defer p.Close()
	if len(p.executionRequirement) == 0 {
		t.Fatal("whole-policy input was not frozen")
	}
	var frozen map[string]json.RawMessage
	if err = json.Unmarshal(p.executionRequirement, &frozen); err != nil || len(frozen) != 4 {
		t.Fatal("wrong neutral envelope", err)
	}
	if err = a.EvaluateSuiteWorkspace(t.Context(), p); err == nil {
		t.Fatal("host-mediated model was admitted under strict policy")
	}
	if calls.Load() != 0 {
		t.Fatal("model executed before containment refusal", calls.Load())
	}
	if _, err = os.Stat(opt.Out); !os.IsNotExist(err) {
		t.Fatal("refusal exported passing evidence")
	}
}

// This deliberately fake compute backend is only a public wire/admission
// fixture. It does not install policy, create isolation or attest enforcement.
type suiteNativeComputeFixture struct {
	*local.Backend
	requirements [][]byte
}

func (*suiteNativeComputeFixture) ExecutionRequirementRuntime() {}
func (b *suiteNativeComputeFixture) Create(ctx context.Context, spec *api.SessionSpec) (api.Incarnation, error) {
	if spec == nil || len(spec.ExecutionRequirement) == 0 {
		return api.Incarnation{}, errors.New("fixture requires opaque policy")
	}
	b.requirements = append(b.requirements, append([]byte(nil), spec.ExecutionRequirement...))
	inc, err := b.Backend.Create(ctx, spec)
	inc.ComputeInstance = "fixture-native-" + spec.SessionUID
	return inc, err
}
func TestSuitePolicyPublicEvalNativeTransportAndReplay(t *testing.T) {
	owned := &suiteNativeComputeFixture{}
	a, opt, calls := suiteSessionsEvaluationFixture(t, owned)
	agent := filepath.Join(opt.Workspace, "agents/greeter/member.yaml")
	raw, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, agent, string(raw)+"policy: policy.json\n")
	writeFile(t, filepath.Join(opt.Workspace, "agents/greeter/policy.json"), `{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[]}}`)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	expected := append([]byte(nil), p.executionRequirement...)
	if err = a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(owned.requirements) != 2 {
		t.Fatal("inventory bypassed native model", calls.Load(), len(owned.requirements))
	}
	for _, actual := range owned.requirements {
		if !bytes.Equal(expected, actual) {
			t.Fatal("opaque authority changed before backend")
		}
	}
	receipt, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil || receipt.Sessions == nil || len(receipt.Cases) != 2 {
		t.Fatal("public output unavailable", err)
	}
	for _, c := range receipt.Cases {
		if c.SessionsExecution == nil {
			t.Fatal("ordinary journal identity missing")
		}
		client, err := agentsessions.Dial(agentsessions.Options{Address: p.sessions.host.Address})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		proof, err := client.VerifyCase(ctx, agentsessions.VerifyRequest{SessionUID: c.SessionsExecution.SessionUID, Harness: "chat", Model: "fixture-model", Head: agentsessions.JournalHead{Seq: c.SessionsExecution.HeadSeq, Hash: c.SessionsExecution.HeadHash}, AnswerSHA256: strings.TrimPrefix(c.AnswerDigest, "sha256:")})
		cancel()
		client.Close()
		if err != nil || proof.Status != "equivalent" || proof.ModelCalls != 0 {
			t.Fatal("ordinary reference replay missing", proof, err)
		}
	}
}

// Test-only hook owns precisely the ordinary daemon child and its unchanged journal.
var suiteOrdinaryDaemonRestart func() error

func suiteToolchatEvaluationFixture(t *testing.T, native ...*suiteNativeComputeFixture) (*App, SuiteWorkspaceEvaluationOptions, *atomic.Int64) {
	t.Helper()
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	dir := privateSuiteDir(t)
	store, err := sqlitelog.Open(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	calls := new(atomic.Int64)
	backend := local.New(toolchatagent.Harness{Model: "fixture-model"})
	t.Cleanup(func() { backend.Close() })
	tools := new(atomic.Int64)
	provider := func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
		calls.Add(1)
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			return api.ModelResponse{Message: *api.TextMessage("assistant", "4")}, nil
		}
		return api.ModelResponse{Message: api.Message{Role: "assistant", Parts: []api.Part{{ToolCall: &api.ToolCall{ID: "fetch-1", Tool: "http_get", Args: map[string]any{"url": "http://example.invalid/"}}}}}}, nil
	}
	tool := func(_ context.Context, call api.ToolCall) (api.ToolResult, error) {
		tools.Add(1)
		return api.ToolResult{ID: call.ID, Content: []api.Part{{Text: &api.TextPart{Text: "fixture-read"}}}}, nil
	}
	t.Cleanup(func() {
		if os.Getenv("EXECUTION_PUBLIC_NATIVE_ROOT") == "" && tools.Load() != 2 {
			t.Error("expected two real fixture callbacks", tools.Load())
		}
	})

	placer := placement.New(backend, provider)
	if len(native) != 0 {
		owned := native[0]
		owned.Backend = backend
		listener := bufconn.Listen(1 << 20)
		server := grpc.NewServer(grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			err := handler(srv, stream)
			if err != nil {
				t.Logf("native Sessions stream %s: %v", info.FullMethod, err)
			}
			return err
		}))
		v1.RegisterHarnessServer(server, harnesswire.NewNativeToolServer(toolchatagent.Harness{Model: "fixture-model"}, provider, tool))
		go server.Serve(listener)
		t.Cleanup(server.Stop)
		conn, err := grpc.NewClient("passthrough:///native", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		placer = placement.New(owned, nil, placement.WithDialer(func(api.Incarnation) (api.Harness, func() error, error) {
			return harnesswire.NewClientHarness(v1.NewHarnessClient(conn)), func() error { return nil }, nil
		}))
	}

	if nativeRoot := os.Getenv("EXECUTION_PUBLIC_NATIVE_ROOT"); nativeRoot != "" {
		if suitePublicNativePlacer == nil {
			t.Fatal("build with nativeintegration tag")
		}
		placer = suitePublicNativePlacer(t, nativeRoot)
	}

	routes, err := placement.NewRegistry("toolchat", map[string]*placement.Placer{"toolchat": placer})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		err := handler(srv, stream)
		if err != nil {
			t.Logf("native Sessions stream %s: %v", info.FullMethod, err)
		}
		return err
	}))

	if executable := os.Getenv("EXECUTION_ORDINARY_SESSIONS_BINARY"); executable != "" && os.Getenv("EXECUTION_PUBLIC_NATIVE_ROOT") != "" {
		// Exercise the delivered executable and normal daemon registry/listener,
		// rather than a test-created Sessions service. Control and native compute
		// remain explicit task-owned real fixture infrastructure.
		address := listener.Addr().String()
		if err = listener.Close(); err != nil {
			t.Fatal(err)
		}
		nativeRoot := os.Getenv("EXECUTION_PUBLIC_NATIVE_ROOT")
		raw, e := os.ReadFile(filepath.Join(nativeRoot, "control-tls-ready.json"))
		if e != nil {
			t.Fatal(e)
		}
		var control struct {
			Address    string `json:"address"`
			ServerName string `json:"serverName"`
			CAFile     string `json:"caFile"`
			TokenFile  string `json:"tokenFile"`
		}
		if e = json.Unmarshal(raw, &control); e != nil {
			t.Fatal(e)
		}
		router, e := os.ReadFile(filepath.Join(nativeRoot, "router-ready"))
		if e != nil {
			t.Fatal(e)
		}
		descriptor, e := (toolchatagent.Harness{Model: "fixture"}).Describe(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		config := map[string]any{"name": "toolchat", "atespace": "native", "templateAtespace": "native", "template": "toolchat", "router": strings.TrimSpace(string(router)), "controlAddress": control.Address, "controlServerName": control.ServerName, "controlCAFile": control.CAFile, "controlTokenFile": control.TokenFile, "descriptor": descriptor}
		encoded, e := json.Marshal(config)
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(nativeRoot, "ordinary-sessions-runtime.json")
		if e = os.WriteFile(path, encoded, 0600); e != nil {
			t.Fatal(e)
		}
		journal := filepath.Join(nativeRoot, "ordinary-sessions-journal.db")
		log, e := os.OpenFile(filepath.Join(nativeRoot, "ordinary-sessions-daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if e != nil {
			t.Fatal(e)
		}
		cmd := exec.Command(executable, "-runtime-config", path, "--", "-addr", address, "-journal", journal)
		cmd.Stdout = log
		cmd.Stderr = log
		if e = cmd.Start(); e != nil {
			log.Close()
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			if e := cmd.Process.Signal(os.Interrupt); e != nil {
				t.Error("stop ordinary daemon", e)
			}
			select {
			case e := <-done:
				if e != nil {
					t.Error("ordinary daemon exit", e)
				}
			case <-time.After(10 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Error("ordinary daemon shutdown timeout")
			}
			log.Close()
		})
		deadline := time.Now().Add(15 * time.Second)
		for {
			conn, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if e == nil {
				conn.Close()
				break
			}
			select {
			case e := <-done:
				t.Fatal("ordinary daemon exited", e)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("ordinary daemon listener deadline")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if os.Getenv("EXECUTION_PUBLIC_NATIVE_SCENARIO") == "sessions-restart" {
			suiteOrdinaryDaemonRestart = func() error {
				old := cmd.Process.Pid
				// Exact exec.Cmd-owned child; no process-name lookup or shared unit.
				if e := cmd.Process.Kill(); e != nil {
					return e
				}
				if e := <-done; e == nil {
					return errors.New("injected daemon SIGKILL not observed")
				}
				cmd = exec.Command(executable, "-runtime-config", path, "--", "-addr", address, "-journal", journal)
				cmd.Stdout, cmd.Stderr = log, log
				if e := cmd.Start(); e != nil {
					return e
				}
				done = make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				until := time.Now().Add(15 * time.Second)
				for {
					conn, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
					if e == nil {
						conn.Close()
						break
					}
					select {
					case e := <-done:
						return fmt.Errorf("replacement daemon exited: %w", e)
					default:
					}
					if time.Now().After(until) {
						return errors.New("replacement daemon listener deadline")
					}
					time.Sleep(20 * time.Millisecond)
				}
				raw, e := json.Marshal(map[string]any{"old_pid": old, "new_pid": cmd.Process.Pid, "journal_reused": true, "injected": "SIGKILL after successful public turn"})
				if e != nil {
					return e
				}
				return os.WriteFile(filepath.Join(nativeRoot, "sessions-daemon-restart.json"), raw, 0600)
			}
			t.Cleanup(func() { suiteOrdinaryDaemonRestart = nil })
		}
		t.Log("ORDINARY_SESSIONS_SUBSTRATE_DAEMON_READY", address)
	} else {
		v1.RegisterSessionsServer(server, session.NewService(store, routes))
		go server.Serve(listener)
		t.Cleanup(server.Stop)
	}
	root := filepath.Join(t.TempDir(), "workspace")
	input := filepath.Join(t.TempDir(), "instructions.md")
	writeFile(t, input, "Answer only four.")
	if err := workspace.Create(t.Context(), root, workspace.CreateOptions{Name: "greeter", Model: "fixture-model", InstructionsFile: input, Runtime: workspace.RuntimeProfile{ID: "agentsessions", Version: workspace.AgentSessionsToolchatVersion, Engine: "toolchat"}}); err != nil {
		t.Fatal(err)
	}
	member := filepath.Join(root, "agents/greeter/member.yaml")
	raw, e := os.ReadFile(member)
	if e != nil {
		t.Fatal(e)
	}
	writeFile(t, member, string(raw)+"policy: policy.json\n")
	writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), `{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[]}}`)
	writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: first\ninput: Say four\nassertions:\n- {id: answer, type: contains, value: '4'}\n- {id: tool, type: toolCalled, tool: http_get}\n")
	writeFile(t, filepath.Join(root, "eval/second.yml"), "id: second\ninput: Say four again\nassertions:\n- {id: answer, type: contains, value: '4'}\n- {id: tool, type: toolCalled, tool: http_get}\n")
	if nativeRoot := os.Getenv("EXECUTION_PUBLIC_NATIVE_ROOT"); nativeRoot != "" {
		// One successful bounded turn; separate native deny coverage is retained in
		// owning-compute tests. No fake owning-compute backend enters this branch.
		if err = os.Remove(filepath.Join(root, "eval/second.yml")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: native\ninput: allow\nassertions:\n- {id: answer, type: contains, value: complete}\n- {id: tool, type: toolCalled, tool: http_get}\n")
		writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), `{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[{"address":"169.254.17.1","port":28292,"protocol":"TCP"}]}}`)
		// Optional exact shared acceptance input: never re-author a per-runtime
		// allow set. The public policy loader remains authoritative validation.
		if file := os.Getenv("EXECUTION_SHARED_POLICY_ENVELOPE"); file != "" {
			raw, e := os.ReadFile(file)
			if e != nil {
				t.Fatal(e)
			}
			var envelope struct {
				Bytes []byte `json:"bytes"`
			}
			if e = json.Unmarshal(raw, &envelope); e != nil || len(envelope.Bytes) == 0 {
				t.Fatal("invalid shared policy input", e)
			}
			writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), string(envelope.Bytes))
		}
		switch os.Getenv("EXECUTION_PUBLIC_NATIVE_SCENARIO") {
		case "deny":
			writeFile(t, filepath.Join(root, "eval/example.yaml"), "id: native\ninput: deny\nassertions:\n- {id: answer, type: contains, value: complete}\n")
		case "empty":
			writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), `{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[]}}`)
		}

		memberRaw, err := os.ReadFile(member)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, member, strings.ReplaceAll(string(memberRaw), "fixture-model", "fixture"))
	}

	host := map[string]any{"schemaVersion": 1, "address": listener.Addr().String(), "observation": map[string]any{
		"mode": "operator-observed-development", "hostID": "owned-host", "journalID": "owned-journal", "runtimeVersion": workspace.AgentSessionsToolchatVersion,
		"runtimeBinaryDigest": "sha256:" + strings.Repeat("a", 64), "runtimeConfigDigest": "sha256:" + strings.Repeat("b", 64), "inferenceID": "owned-inference", "inferenceRevision": "sha256:" + strings.Repeat("c", 64),
		"model": "fixture-model", "harness": "toolchat", "harnessVersion": "1", "retention": "operator-managed",
	}}
	data, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	hostPath := filepath.Join(dir, "host.json")
	if err := os.WriteFile(hostPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if nativeRoot := os.Getenv("EXECUTION_PUBLIC_NATIVE_ROOT"); nativeRoot != "" {
		observation := host["observation"].(map[string]any)
		observation["model"] = "fixture"
		binary, err := os.ReadFile(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		observation["runtimeBinaryDigest"] = fmt.Sprintf("sha256:%x", sha256.Sum256(binary))
		config, err := os.ReadFile(filepath.Join(nativeRoot, "fixture-native-config.json"))
		if err != nil {
			t.Fatal(err)
		}
		observation["runtimeConfigDigest"] = fmt.Sprintf("sha256:%x", sha256.Sum256(config))
		receiver, err := os.ReadFile(os.Getenv("EXECUTION_NATIVE_COMPUTE_SOURCE"))
		if err != nil {
			t.Fatal(err)
		}
		observation["inferenceRevision"] = fmt.Sprintf("sha256:%x", sha256.Sum256(receiver))
		data, err = json.Marshal(host)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(hostPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	opt := SuiteWorkspaceEvaluationOptions{Workspace: root, DevelopmentHost: hostPath, Out: filepath.Join(privateSuiteDir(t), "pass.json"), CaseTimeout: 30 * time.Second}
	return &App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, opt, calls
}

// This fixture covers public Kaimahi + Sessions plumbing with a declared fake
// owning-compute backend and in-memory native callbacks, NOT real containment.
func TestSuiteToolchatPublicEvaluationAndReplay(t *testing.T) {
	owned := &suiteNativeComputeFixture{}
	a, opt, calls := suiteToolchatEvaluationFixture(t, owned)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatalf("%v; stdout=%v stderr=%v", err, a.Out, a.Err)
	}
	defer p.Close()
	if err = a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatalf("%v; stdout=%v stderr=%v", err, a.Out, a.Err)
	}
	if calls.Load() != 4 || len(owned.requirements) != 2 {
		t.Fatal("missing native effects", calls.Load(), len(owned.requirements))
	}
	receipt, err := readSuitePassingReceipt(t.Context(), opt.Out)
	if err != nil {
		t.Fatalf("%v; stdout=%v stderr=%v", err, a.Out, a.Err)
	}
	for _, row := range receipt.Cases {
		c, err := agentsessions.Dial(agentsessions.Options{Address: p.sessions.host.Address})
		if err != nil {
			t.Fatalf("%v; stdout=%v stderr=%v", err, a.Out, a.Err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		proof, err := c.VerifyCase(ctx, agentsessions.VerifyRequest{SessionUID: row.SessionsExecution.SessionUID, Harness: "toolchat", Model: "fixture-model", Head: agentsessions.JournalHead{Seq: row.SessionsExecution.HeadSeq, Hash: row.SessionsExecution.HeadHash}, AnswerSHA256: strings.TrimPrefix(row.AnswerDigest, "sha256:")})
		cancel()
		c.Close()
		if err != nil || proof.Status != "equivalent" || proof.ModelCalls != 0 {
			t.Fatal(proof, err)
		}
	}
	if calls.Load() != 4 {
		t.Fatal("replay issued model effect", calls.Load())
	}
}

var suitePublicNativePlacer func(*testing.T, string) *placement.Placer
