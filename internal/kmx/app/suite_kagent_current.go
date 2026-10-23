package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/kagentcurrent"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// Existing-agent native lane, separate from legacy Deployment/Go placement.
// This is literal-loopback development, not remote identity attestation.
type suiteCurrentHost struct {
	SchemaVersion    int    `json:"schemaVersion"`
	Address          string `json:"address"`
	Namespace        string `json:"namespace"`
	Agent            string `json:"agent"`
	Harness          string `json:"harness"`
	WorkerPool       string `json:"workerPool"`
	SnapshotLocation string `json:"snapshotLocation"`
	ColdStartOnly    bool   `json:"coldStartOnly,omitempty"`
	BaseURL          string `json:"baseURL"`
	SecretName       string `json:"secretName"`
	SecretKey        string `json:"secretKey"`
}
type suiteCurrentSnapshot struct {
	path         string
	raw          []byte
	parent, file os.FileInfo
	host         suiteCurrentHost
}

func snapshotSuiteCurrentHost(path string, s *workspace.NativeSuite) (*suiteCurrentSnapshot, error) {
	fail := errors.New("current kagent requires explicit private loopback host and exact native Codex profile")
	if s == nil || s.Runtime != (workspace.RuntimeProfile{ID: "kagent", Version: workspace.KagentCodexVersion, Engine: "codex"}) || s.ExecutionProfile != workspace.KagentCodexExecutionProfile {
		return nil, errors.New("current kagent packaged runtime/profile mismatch")
	}
	raw, parent, file, err := readSuiteKagentBindings(path)
	if err != nil || parent.Mode().Perm()&0077 != 0 || file.Mode().Perm()&0077 != 0 || len(raw) > 16384 {
		return nil, errors.New("current kagent host must be bounded regular private file in private directory")
	}
	var host suiteCurrentHost
	if agentsuite.Unmarshal(raw, &host) != nil || host.SchemaVersion != 1 || !suiteSessionsLoopback(host.Address) {
		return nil, errors.New("current kagent host requires closed schema and literal loopback address")
	}
	if scaffold.ValidateNamespace(host.Namespace) != nil || scaffold.ValidateObjectName(host.Agent) != nil {
		return nil, fail
	}
	if _, err = currentExpectedBundle(s, host); err != nil {
		return nil, fmt.Errorf("current kagent host compiler validation: %w", err)
	}
	return &suiteCurrentSnapshot{path, raw, parent, file, host}, nil
}
func (s *suiteCurrentSnapshot) recheck(suite *workspace.NativeSuite) error {
	current, err := snapshotSuiteCurrentHost(s.path, suite)
	if err != nil {
		return err
	}
	if !os.SameFile(s.parent, current.parent) || !os.SameFile(s.file, current.file) || !bytes.Equal(s.raw, current.raw) {
		return errors.New("current kagent host changed")
	}
	return nil
}
func currentExpectedBundle(s *workspace.NativeSuite, h suiteCurrentHost) (*scaffold.KagentCodexBundle, error) {
	return scaffold.GenerateSuiteKagentCodex(scaffold.KagentCodexSpec{KagentSpec: scaffold.KagentSpec{Name: h.Agent, Namespace: h.Namespace, Description: s.Description, Runtime: "codex", Instructions: string(s.Instructions), ProviderType: "openai", Model: s.Model, BaseURL: h.BaseURL, SecretName: h.SecretName, SecretKey: h.SecretKey}, HarnessName: h.Harness, WorkerPool: h.WorkerPool, SnapshotLocation: h.SnapshotLocation, ColdStartOnly: h.ColdStartOnly})
}
func currentMap(value any) map[string]any { m, _ := value.(map[string]any); return m }
func currentVerify(ctx context.Context, client *kagentcurrent.Client, s *workspace.NativeSuite, h suiteCurrentHost) (string, error) {
	expected, err := currentExpectedBundle(s, h)
	if err != nil {
		return "", err
	}
	// The canonical v1alpha3 CRD defaults stream=true. Explicitly require
	// that default rather than ignore extra model settings.
	currentMap(expected.ModelConfig["spec"])["stream"] = true
	// Kubernetes typed Agent omits an empty description. This is the only
	// renderer-to-wire omission normalized; unknown spec fields still refuse.
	inline := currentMap(currentMap(currentMap(expected.Agent["spec"])["template"])["inline"])
	if inline["description"] == "" {
		delete(inline, "description")
	}

	response, err := client.Call(ctx, "AgentService", "GetAgent", map[string]any{"ref": map[string]any{"namespace": h.Namespace, "name": h.Agent}})
	if err != nil {
		return "", err
	}
	agent := currentMap(currentMap(response["agent"])["resource"])["value"]
	live := currentMap(agent)
	status, meta := currentMap(live["status"]), currentMap(live["metadata"])
	uid, _ := meta["uid"].(string)
	revision, _ := status["latestSuccessfulRevision"].(string)
	// Compare canonical JSON because Kubernetes/protobuf number representations differ.
	same := func(a, b any) bool {
		x, e := json.Marshal(a)
		y, f := json.Marshal(b)
		return e == nil && f == nil && bytes.Equal(x, y)
	}
	ready := false
	conditions, _ := status["conditions"].([]any)
	for _, value := range conditions {
		condition := currentMap(value)
		if condition["type"] == "Ready" && condition["status"] == "True" && same(condition["observedGeneration"], meta["generation"]) {
			ready = true
		}
	}
	if uid == "" || revision == "" || !ready || !same(status["observedGeneration"], meta["generation"]) || status["desiredRevision"] != revision || meta["deletionTimestamp"] != nil || !same(live["spec"], expected.Agent["spec"]) {
		return "", errors.New("current Agent not prepared with packaged behavior")
	}
	identity := uid + "/" + revision
	for _, item := range []struct {
		service, method, key, name string
		expected                   map[string]any
	}{{"ModelService", "ListModelConfigs", "modelConfigs", h.Agent + "-model", expected.ModelConfig}, {"HarnessService", "ListHarnesses", "harnesses", h.Harness, expected.Harness}} {
		input := map[string]any{"namespace": h.Namespace}
		if item.service == "ModelService" {
			input = map[string]any{}
		}
		list, e := client.Call(ctx, item.service, item.method, input)
		if e != nil {
			return "", e
		}
		found := false
		values, _ := list[item.key].([]any)
		for _, row := range values {
			r := currentMap(currentMap(currentMap(row)["resource"])["value"])
			m := currentMap(r["metadata"])
			if m["name"] == item.name && m["namespace"] == h.Namespace {
				if found || m["uid"] == nil || m["deletionTimestamp"] != nil || !same(r["spec"], item.expected["spec"]) {
					return "", errors.New("current model/harness drift")
				}
				resourceUID, ok := m["uid"].(string)
				if !ok || resourceUID == "" {
					return "", errors.New("missing current resource identity")
				}
				identity += "/" + resourceUID
				found = true
			}
		}
		if !found {
			return "", errors.New("current model/harness missing")
		}
	}
	return identity, nil
}

func (a *App) evaluateSuiteCurrentWorkspace(ctx context.Context, p *PreparedSuiteWorkspaceEvaluation) (err error) {
	if p.started {
		return errors.New("prepared invocation already used")
	}
	p.started = true
	recheck := func(c context.Context) error {
		if e := workspace.CheckCurrent(c, p.root, p.prepared); e != nil {
			return e
		}
		return p.current.recheck(p.suite)
	}
	if err = recheck(ctx); err != nil {
		return err
	}
	client, err := kagentcurrent.Dial(p.current.host.Address)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	identity, err := currentVerify(ctx, client, p.suite, p.current.host)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(a.Err, "Development: existing current kagent on literal loopback; unauthenticated operator-selected endpoint. Packaged Agent/Model/Harness specs checked via public API; no deployment attestation. Native Sessions and Tasks retained upstream; Sessions deleted after each run.")
	if err != nil {
		return err
	}
	type caseOutput struct {
		CaseID, TaskID, ContextID, AnswerDigest, Verdict string
		Assertions                                       []agentruntime.EvaluationAssertionResult
	}
	rows := []caseOutput{}
	seen := map[string]bool{}
	_, err = evaluateSuiteRuns(ctx, p.inventory, p.options.CaseTimeout, a.Out, func(caseCtx context.Context, frame suitePassingInventoryCase) (observed suitePassingObservation, runErr error) {
		observed = suitePassingObservation{CaseDigest: frame.Metadata.CaseDigest, InputDigest: frame.Metadata.InputDigest, EvaluationReceipt: agentruntime.EvaluationReceipt{CaseID: frame.Case.ID, Verdict: agentruntime.EvaluationUnknown}}
		runErr = func() (err error) {
			if e := recheck(caseCtx); e != nil {
				return e
			}
			now, e := currentVerify(caseCtx, client, p.suite, p.current.host)
			if e != nil || now != identity {
				return errors.New("current runtime source changed")
			}
			rpc, e := randomHex(16)
			if e != nil {
				return e
			}
			message, e := randomHex(16)
			if e != nil {
				return e
			}
			request := kagentEvaluationExecution{RPCID: "rpc-" + rpc, RequestMessageID: "message-" + message}
			// Public explicit Session creation supplies retry-stable operation ownership
			// and a cleanup identity before any A2A Task is dispatched.
			response, e := client.Create(caseCtx, map[string]any{"agent": map[string]any{"namespace": p.current.host.Namespace, "name": p.current.host.Agent}, "requestId": request.RequestMessageID, "executionRequirement": base64.StdEncoding.EncodeToString(p.executionRequirement)})
			if e != nil {
				return e
			}
			session := currentMap(response["session"])
			id, _ := session["id"].(string)
			contextID, _ := session["contextId"].(string)
			if id == "" || contextID == "" {
				return errors.New("current Session identity unavailable")
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer stop()
				_, e := client.Call(cleanup, "SessionService", "DeleteSession", map[string]any{"sessionId": id})
				if e != nil {
					_, e = client.Call(cleanup, "SessionService", "DeleteSession", map[string]any{"sessionId": id})
				}
				err = errors.Join(err, e)
			}()
			if e = caseCtx.Err(); e != nil {
				return e
			}
			ref := currentMap(session["agent"])
			if session["executionRequirement"] != base64.StdEncoding.EncodeToString(p.executionRequirement) && len(p.executionRequirement) > 0 {
				return errors.New("Session requirement not preserved")
			}
			if ref["namespace"] != p.current.host.Namespace || ref["name"] != p.current.host.Agent || session["preparedRevision"] != strings.Split(identity, "/")[1] {
				return errors.New("Session source does not match prepared Agent")
			}
			if session["state"] != "RUNTIME_STATE_READY" {
				return errors.New("current Session not ready")
			}
			if e = recheck(caseCtx); e != nil {
				return e
			}
			raw, e := client.Send(caseCtx, p.current.host.Namespace, p.current.host.Agent, map[string]any{"jsonrpc": "2.0", "id": request.RPCID, "method": "SendMessage", "params": map[string]any{"message": map[string]any{"role": "ROLE_USER", "messageId": request.RequestMessageID, "contextId": contextID, "parts": []any{map[string]any{"text": frame.Case.Input}}}, "configuration": map[string]any{"returnImmediately": false}, "metadata": map[string]any{"kagent.dev/execution-requirement/v1": base64.StdEncoding.EncodeToString(p.executionRequirement)}}})
			if e != nil {
				return e
			}
			execution, answer, e := parseKagentEvaluationResponse(raw, request, frame.Case.Input)
			if e != nil {
				return e
			}
			if execution.State != "completed" || execution.ContextID != contextID || !suiteKagentResponseHistory(raw) || seen[execution.TaskID] || seen[contextID] {
				return errors.New("current Task attribution unavailable")
			}
			seen[execution.TaskID], seen[contextID] = true, true
			evidence := agentruntime.EvaluationEvidence{AnswerAvailable: true, Answer: answer}
			assertions := agentruntime.EvaluateAssertions(frame.Case, evidence)
			if p.judge != nil {
				assertions = p.judge(caseCtx, frame.Case, evidence)
			}
			verdict := suiteRunVerdict(assertions)
			observed.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(answer)
			observed.Assertions = assertions
			observed.Verdict = verdict
			now, e = currentVerify(caseCtx, client, p.suite, p.current.host)
			if e != nil || now != identity {
				return errors.New("current source changed during execution")
			}
			if e = recheck(caseCtx); e != nil {
				return e
			}
			rows = append(rows, caseOutput{frame.Case.ID, execution.TaskID, contextID, agentruntime.EvaluationAnswerDigest(answer), string(verdict), assertions})
			if verdict == agentruntime.EvaluationFail {
				return errSuiteNativeCaseFailed
			}
			if verdict != agentruntime.EvaluationPass {
				return errSuiteNativeEvaluation
			}
			return nil
		}()
		return observed, runErr
	})
	if err != nil {
		return err
	}
	if err = recheck(ctx); err != nil {
		return err
	}
	report := struct {
		SchemaVersion                       int    `json:"schemaVersion"`
		Domain                              string `json:"domain"`
		Suite, PolicyDigest, SourceIdentity string
		Cases                               []caseOutput
		Scope                               string
	}{1, "kmx.current-kagent.development-evaluation/v1", p.suite.Digest, "", identity, rows, "existing-agent literal-loopback public API observations, not managed promotion or remote attestation"}
	if len(p.executionRequirement) > 0 {
		var requirement struct {
			Digest string `json:"digest"`
		}
		if json.Unmarshal(p.executionRequirement, &requirement) != nil {
			return errors.New("invalid frozen requirement")
		}
		report.PolicyDigest = requirement.Digest
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = p.output.publishBytes(ctx, append(raw, '\n')); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Out, "Current kagent native evaluation: %d passing runs; Sessions deleted; result %q (development observations, not managed proof)\n", len(rows), p.options.Out)
	return err
}
