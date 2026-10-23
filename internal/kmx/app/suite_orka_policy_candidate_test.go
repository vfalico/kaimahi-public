package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"go.yaml.in/yaml/v3"
)

// This is explicitly API-boundary fixture coverage, not real cluster acceptance.
// The installed schemas are an independent captured copy of the Orka fork CRDs.
func TestSuiteOrkaGovernedCandidateCarrier(t *testing.T) {
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	a, _, f, state := publicNativeEvaluationFixture(t)
	t.Setenv("KMX_HOME", privateSuiteDir(t))
	schemas := os.Getenv("KMX_CURRENT_ORKA_CRDS")
	if schemas == "" {
		t.Skip("explicit current-fork CRD capture required")
	}
	capture := a.captureCommand
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		if i := slices.Index(cmd.Args, "get"); i >= 0 && cmd.Args[i+1] == "crd" {
			plural, _, _ := strings.Cut(cmd.Args[i+2], ".")
			raw, err := os.ReadFile(filepath.Join(schemas, "core.orka.ai_"+plural+".yaml"))
			if err != nil {
				return err
			}
			var doc map[string]any
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				return err
			}
			return json.NewEncoder(cmd.Stdout).Encode(doc)
		}
		return capture(ctx, cmd)
	}
	opt := nativeEvaluationOptions(t)
	opt.ResultPort = f.opt.ResultPort
	opt.ManagedPass = true
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
	var gotTasks int
	state.afterCreate = func(key string) {
		doc := state.objects[key]
		spec := doc["spec"].(map[string]any)
		if strings.HasPrefix(key, "Agent/") {
			raw := mustJSON(t, doc)
			if err := qualifySuiteACPAgent(raw); err != nil {
				t.Fatal(err)
			}
			if spec["execution"] != nil {
				t.Fatal("policy default leaked into Agent")
			}
		}
		if strings.HasPrefix(key, "Task/") {
			gotTasks++
			if spec["type"] != "agent" {
				t.Fatal("legacy AI Task")
			}
			execution, ok := spec["execution"].(map[string]any)
			if !ok {
				t.Fatal("requirement omitted")
			}
			var got suiteExecutionRequirement
			if json.Unmarshal(mustJSON(t, execution["requirement"]), &got) != nil || !bytes.Equal(mustJSON(t, got), mustJSON(t, expected)) {
				t.Fatal("Task policy changed")
			}
		}
	}
	for _, c := range p.inventory {
		f.answers[c.Case.Input] = nativeCanary + " answer"
	}
	a.guarded = true
	if err := a.EvaluateSuiteWorkspace(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if gotTasks != len(p.inventory) || len(state.objects) != 0 {
		t.Fatal("incomplete candidate Tasks/cleanup", gotTasks, len(state.objects))
	}
	raw, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	var receipt suitePassingReceipt
	if json.Unmarshal(raw, &receipt) != nil {
		t.Fatal("receipt decode")
	}
	if receipt.Subject.ExecutionIdentity != expected.Identity {
		t.Fatal("receipt lost exact execution identity")
	}
	if _, err := canonicalSuitePassingReceipt(t.Context(), &receipt, p.suite); err != nil {
		t.Fatal(err)
	}
	changed := *p.suite
	changed.Policy = &workspace.NativePolicy{Bytes: []byte(`{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[{"address":"127.0.0.1","port":80,"protocol":"TCP"}]}}`)}
	if _, err := canonicalSuitePassingReceipt(t.Context(), &receipt, &changed); err == nil {
		t.Fatal("changed policy reused passing receipt")
	}
	t.Log("ORKA_GOVERNED_PUBLIC_CANDIDATE_CARRIER_PASS exact Task envelope, receipt identity and candidate cleanup; fixture boundary only")
}
