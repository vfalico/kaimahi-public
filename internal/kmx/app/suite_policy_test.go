package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

const appDenyAllPolicy = `{"network":{"egressAllow":[]},"requiredCapabilities":["network/egress-allow/v1"],"version":"agentsuite.policy/v2"}`

func addWorkspacePolicy(t *testing.T, root string) {
	t.Helper()
	name := filepath.Join(root, "agents/greeter/member.yaml")
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, name, string(raw)+"policy: policy.json\n")
	writeFile(t, filepath.Join(root, "agents/greeter/policy.json"), appDenyAllPolicy)
}

func TestDeclaredPolicyPreparesEvaluationOffline(t *testing.T) {
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	opt := nativeEvaluationOptions(t)
	addWorkspacePolicy(t, opt.Workspace)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	expected, err := resolveSuiteExecutionRequirement(p.suite)
	if err != nil {
		t.Fatal(err)
	}
	var frozen suiteExecutionRequirement
	if json.Unmarshal(p.executionRequirement, &frozen) != nil || !reflect.DeepEqual(frozen, *expected) {
		t.Fatal("offline preparation lost exact Orka envelope")
	}
	if _, err := os.Stat(opt.Out); !os.IsNotExist(err) {
		t.Fatal("offline preparation exported passing output")
	}
}
func TestDeclaredPolicyNativeRequiresInstalledABI(t *testing.T) {
	a, suite, reads := nativeFixture(t)
	suite.Policy = &workspace.NativePolicy{Bytes: []byte(appDenyAllPolicy)}
	// Historical installed v0.1.3 lacks the contractVersion field; refuse, never strip it.
	if _, err := a.prepareSuiteNative(t.Context(), suite, OrkaNamespace, "inference"); err == nil || !strings.Contains(err.Error(), "contractVersion") {
		t.Fatalf("unsupported installed ABI: %v", err)
	}
	if reads.calls == 0 {
		t.Fatal("installed schema was not read")
	}
}
func TestDeclaredPolicyRefusesReceiptAndReleaseLiftBeforeReads(t *testing.T) {
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	root, prepared, suite, placement, observations := passingFixture(t)
	receipt, err := newSuitePassingReceipt(t.Context(), prepared, placement, observations)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonicalSuitePassingReceipt(t.Context(), receipt, suite)
	if err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(privateSuiteDir(t), "old-pass.json")
	if err := os.WriteFile(evidence, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	addWorkspacePolicy(t, root)
	candidate, err := workspace.Prepare(t.Context(), root, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	declared, err := workspace.Load(t.Context(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalSuitePassingReceipt(t.Context(), receipt, declared); err == nil {
		t.Fatalf("receipt accepted policy: %v", err)
	}
	a, _, reads := nativeFixture(t)
	before := reads.calls
	_, err = a.PrepareReleaseLift(t.Context(), ReleaseLiftOptions{Reference: "suite@" + declared.Digest, FromLayout: candidate.Path, ToContext: "kind-test", ToNamespace: OrkaNamespace, Inference: "provider:inference", Evidence: evidence})
	if err == nil {
		t.Fatal("stale policy-free receipt authorized Lift")
	}
	if reads.calls != before {
		t.Fatal("policy Lift reached target observations")
	}
	for _, mode := range []string{"managed", "explicit"} {
		layout := filepath.Join(t.TempDir(), "refused-layout")
		if mode == "managed" {
			_, err = a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: root, Reference: "team:v1", Layout: layout})
		} else {
			_, err = a.PushSuiteWorkspaceEvaluated(t.Context(), root, "team:v1", layout, false, false, SuitePushProvenance{}, evidence)
		}
		if err == nil {
			t.Fatalf("%s stale passing proof accepted", mode)
		}
		if _, err := os.Stat(layout); !os.IsNotExist(err) {
			t.Fatal("refused evaluated push transferred content")
		}
	}
}
func TestDeclaredPolicyUnevaluatedPushPreservesGraph(t *testing.T) {
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	opt := nativeEvaluationOptions(t)
	addWorkspacePolicy(t, opt.Workspace)
	var out, diagnostics bytes.Buffer
	a := &App{Out: &out, Err: &diagnostics}
	layout := filepath.Join(t.TempDir(), "layout")
	result, err := a.PushSuiteWorkspaceManaged(t.Context(), SuiteWorkspacePushOptions{Workspace: opt.Workspace, Reference: "team:v1", Layout: layout, Unevaluated: true})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := suiteoras.ReadArtifactLayout(t.Context(), layout, result.Descriptor.Digest.String())
	if err != nil {
		t.Fatal(err)
	}
	inv, err := workspace.LoadArtifactInventory(t.Context(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Members) != 1 || inv.Members[0].Policy == nil || string(inv.Members[0].Policy.Bytes) != appDenyAllPolicy {
		t.Fatal("unevaluated publication dropped policy")
	}
}
