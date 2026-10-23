package scaffold

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCurrentCodexV1Alpha3Renderer(t *testing.T) {
	spec := KagentCodexSpec{KagentSpec: KagentSpec{Name: "assistant", Namespace: "test", Runtime: "codex", Instructions: "Use the native network shell probe and answer complete.", ProviderType: "openai", Model: "test-model", BaseURL: "http://model.native.test:28292/v1", SecretName: "model-auth", SecretKey: "api-key"}, HarnessName: "native-codex", WorkerPool: "default", SnapshotLocation: "gs://native-snapshots/prefix"}
	if os.Getenv("KMX_CODEX_RENDER_SEPARATE") == "1" {
		spec.BaseURL = "http://model.native.test:28295/v1"
	}
	b, err := GenerateSuiteKagentCodex(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"harnessRef": map[string]any{"name": "native-codex"}, "template": map[string]any{"inline": map[string]any{"description": "", "systemPrompt": spec.Instructions, "modelConfig": map[string]any{"name": "assistant-model"}}}}
	if !reflect.DeepEqual(b.Agent["spec"], want) {
		t.Fatal(b.Agent)
	}
	if out := os.Getenv("KMX_CODEX_RENDER_CAPTURE"); out != "" {
		raw, e := json.MarshalIndent(b, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Clean(out), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	for _, mode := range []string{"legacy", "provider", "floating-pool", "tools", "missing-url", "ip-endpoint", "missing-snapshot", "bare-snapshot", "snapshot-query", "snapshot-userinfo", "snapshot-scheme"} {
		c := spec
		switch mode {
		case "legacy":
			c.Runtime = "go"
		case "provider":
			c.ProviderType = "anthropic"
		case "floating-pool":
			c.WorkerPool = ""
		case "tools":
			c.Tools = []KagentMCPToolBinding{{}}
		case "bare-snapshot":
			c.SnapshotLocation = "native-snapshots"
		case "snapshot-query":
			c.SnapshotLocation = "gs://bucket/prefix?version=1"
		case "snapshot-userinfo":
			c.SnapshotLocation = "gs://user@bucket/prefix"
		case "snapshot-scheme":
			c.SnapshotLocation = "file://bucket/prefix"
		case "missing-snapshot":
			c.SnapshotLocation = ""
		case "ip-endpoint":
			c.BaseURL = "http://127.0.0.1:28292/v1"
		case "missing-url":
			c.BaseURL = ""
		}
		if _, e := GenerateSuiteKagentCodex(c); e == nil {
			t.Fatal("accepted unsupported", mode)
		}
	}
}

func TestColdStartOnlyIsExplicitAuthoredConfiguration(t *testing.T) {
	spec := KagentCodexSpec{KagentSpec: KagentSpec{Name: "assistant", Namespace: "test", Runtime: "codex", ProviderType: "openai", Model: "fixture", Instructions: "Use the native network shell probe and answer complete.", BaseURL: "http://model.native.test:28295/v1", SecretName: "model-auth", SecretKey: "api-key"}, HarnessName: "native", WorkerPool: "default", SnapshotLocation: "gs://snapshots/prefix"}
	normal, err := GenerateSuiteKagentCodex(spec)
	if err != nil {
		t.Fatal(err)
	}
	policy := normal.Harness["spec"].(map[string]any)["substrate"].(map[string]any)["snapshotPolicy"].(map[string]any)
	if _, ok := policy["coldStartOnly"]; ok {
		t.Fatal("changed default")
	}
	spec.ColdStartOnly = true
	cold, err := GenerateSuiteKagentCodex(spec)
	if err != nil {
		t.Fatal(err)
	}
	policy = cold.Harness["spec"].(map[string]any)["substrate"].(map[string]any)["snapshotPolicy"].(map[string]any)
	if policy["coldStartOnly"] != true {
		t.Fatal("explicit choice not carried")
	}
}
