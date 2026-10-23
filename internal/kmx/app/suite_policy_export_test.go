package app

import (
	"encoding/json"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture emits through the actual Suite resolver, not invented wire JSON.
func TestSuiteExecutionExportFixture(t *testing.T) {
	out := os.Getenv("KMX_REQUIREMENT_FIXTURE")
	if out == "" {
		t.Skip("explicit evidence output required")
	}
	s := &workspace.NativeSuite{Runtime: workspace.RuntimeProfile{ID: "orka"}, Policy: &workspace.NativePolicy{Bytes: []byte(`{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[{"address":"127.0.0.1","protocol":"TCP","port":28181},{"address":"127.0.0.1","protocol":"TCP","port":28182},{"address":"127.0.0.1","protocol":"TCP","port":28184}]}}`)}}
	r, err := resolveSuiteExecutionRequirement(s)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// Uses the ordinary authored workspace and actual RunAgent submission method.
// The existing kubectl fixture is an explicitly captured API boundary, not a
// real Kubernetes execution claim. Its exact Task bytes feed Orka tests.
func TestSuiteExecutionCapturesActualSubmission(t *testing.T) {
	out := os.Getenv("KMX_SUBMISSION_FIXTURE")
	if out == "" {
		t.Skip("explicit evidence output required")
	}
	f := runFixture(t)
	native := nativeEvaluationOptions(t)
	root := native.Workspace
	addWorkspacePolicy(t, root)
	policyFile := filepath.Join(root, "agents/greeter/policy.json")
	policy := `{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[{"address":"127.0.0.1","protocol":"TCP","port":28181},{"address":"127.0.0.1","protocol":"TCP","port":28182},{"address":"127.0.0.1","protocol":"TCP","port":28184}]}}`
	if err := os.WriteFile(policyFile, []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeFile := filepath.Join(root, "runtime.json")
	_ = runtimeFile
	// Native fixture's authored runtime is Orka; no requirement fixture is
	// supplied to RunAgent. It resolves and snapshots from workspace itself.
	agentPath := filepath.Join(f.dir, "agent.json")
	raw, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	var agent map[string]any
	if err = json.Unmarshal(raw, &agent); err != nil {
		t.Fatal(err)
	}
	agent["spec"] = map[string]any{"runtime": map[string]any{"type": "codex", "contractVersion": "orka.harness.v2"}}
	raw, _ = json.Marshal(agent)
	if err = os.WriteFile(agentPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.answers["private prompt"] = "NETWORK_NATIVE_OK"
	opt := runOption(f)
	opt.PolicyWorkspace = root
	if err = f.app.RunAgent(opt); err != nil {
		t.Fatal(err)
	}
	var captured map[string]any
	for _, call := range orkaCalls(t, f.dir) {
		if call.Document != nil && call.Document["kind"] == "Task" {
			captured = call.Document
		}
	}
	if captured == nil {
		t.Fatal("actual RunAgent never submitted Task")
	}
	spec := captured["spec"].(map[string]any)
	if spec["type"] != "agent" {
		t.Fatal("legacy type submitted")
	}
	requirement := spec["execution"].(map[string]any)["requirement"]
	raw, err = json.MarshalIndent(captured, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = json.MarshalIndent(requirement, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out+".requirement.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Prove accepted bytes survive source edit: retain the API capture, then
	// mutate the authored source without re-running the accepted submission.
	if err = os.WriteFile(policyFile, []byte(strings.ReplaceAll(policy, "28182", "28183")), 0600); err != nil {
		t.Fatal(err)
	}
	now, err := loadSuiteExecutionRequirement(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	before := requirement.(map[string]any)["identity"].(string)
	if now.Identity == before {
		t.Fatal("source mutation did not create a new policy identity")
	}
	t.Log("actual Kaimahi RunAgent submitted type:agent with frozen Suite descriptor; later source edit produced a distinct new descriptor")
}
