package workspace_test

import (
	suiteoras "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/oras"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"reflect"
	"testing"
)

func TestCurrentKagentCodexOfflineProfile(t *testing.T) {
	for _, mode := range []string{"exact", "missing-policy", "legacy-pin", "legacy-engine", "floating"} {
		t.Run(mode, func(t *testing.T) {
			root, cache := createWorkspace(t)
			version, engine := workspace.KagentCodexVersion, "codex"
			switch mode {
			case "legacy-pin":
				version = "v0.10.3"
			case "legacy-engine":
				engine = "go"
			case "floating":
				version = "main"
			}
			setMemberRuntime(t, root, "runtime: {id: kagent, version: "+version+", engine: "+engine+"}\n")
			if mode != "missing-policy" {
				declarePolicy(t, root, "greeter", networkPolicy(policyEndpointA))
			}
			p, e := workspace.Prepare(t.Context(), root, cache)
			if mode != "exact" {
				if e == nil {
					t.Fatal("incomplete current contract accepted", mode)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			native, e := workspace.Load(t.Context(), p)
			if e != nil {
				t.Fatal(e)
			}
			if native.Runtime.Engine != "codex" || native.ExecutionProfile != workspace.KagentCodexExecutionProfile || native.Policy == nil {
				t.Fatal(native)
			}
			a, e := suiteoras.ReadArtifactLayout(t.Context(), p.Path, p.Descriptor.Digest.String())
			if e != nil {
				t.Fatal(e)
			}
			copy, e := workspace.LoadArtifact(t.Context(), a)
			if e != nil || !reflect.DeepEqual(native, copy) {
				t.Fatal("offline profile drift", e)
			}
		})
	}
}
