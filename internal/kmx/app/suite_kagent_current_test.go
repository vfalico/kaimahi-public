package app

import (
	"encoding/json"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func currentSuiteFixture(t *testing.T) (SuiteWorkspaceEvaluationOptions, string) {
	t.Helper()
	opt := suiteKagentEvaluationOptions(t)
	file := filepath.Join(opt.Workspace, "agents/sample/member.yaml")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "v0.10.3", workspace.KagentCodexVersion), "engine: go", "engine: codex") + "policy: policy.json\n"
	writeFile(t, file, text)
	writeFile(t, filepath.Join(opt.Workspace, "agents/sample/policy.json"), `{"version":"agentsuite.policy/v2","network":{"egressAllow":[]},"requiredCapabilities":["network/egress-allow/v1"]}`)
	host := suiteCurrentHost{SchemaVersion: 1, Address: "127.0.0.1:12345", Namespace: "agents", Agent: "sample", Harness: "native", WorkerPool: "default", SnapshotLocation: "gs://snapshots/prefix", BaseURL: "http://model.native.test:28295/v1", SecretName: "model-auth", SecretKey: "api-key"}
	hostPath := filepath.Join(privateSuiteDir(t), "host.json")
	raw, err = json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(hostPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	opt.CurrentKagentHost = hostPath
	opt.ToContext = ""
	opt.Bindings = ""
	opt.Inference = ""
	opt.ToNamespace = ""
	opt.ResultPort = ""
	opt.ManagedPass = false
	return opt, hostPath
}
func TestCurrentNativePublicPrepareFrozenOpaqueRequirement(t *testing.T) {
	opt, host := currentSuiteFixture(t)
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.current == nil || len(p.executionRequirement) == 0 {
		t.Fatal("missing native frame/requirement")
	}
	frozen := string(p.executionRequirement)
	raw, err := os.ReadFile(host)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(host, append(raw, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if p.current.recheck(p.suite) == nil {
		t.Fatal("host mutation admitted")
	}
	if string(p.executionRequirement) != frozen {
		t.Fatal("frozen requirement mutated")
	}
	if err = os.WriteFile(host, raw, 0600); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(opt.Workspace, "agents/sample/policy.json")
	writeFile(t, policy, `{"version":"agentsuite.policy/v2","network":{"egressAllow":[{"address":"127.0.0.1","port":80,"protocol":"TCP"}]},"requiredCapabilities":["network/egress-allow/v1"]}`)
	if workspace.CheckCurrent(t.Context(), p.root, p.prepared) == nil {
		t.Fatal("source mutation admitted")
	}
	if string(p.executionRequirement) != frozen {
		t.Fatal("source changed frozen envelope")
	}
}
func TestCurrentNativePublicSelectorRefusal(t *testing.T) {
	for _, field := range []string{"managed", "context", "namespace", "inference", "bindings", "result", "development", "json"} {
		t.Run(field, func(t *testing.T) {
			opt, _ := currentSuiteFixture(t)
			switch field {
			case "managed":
				opt.ManagedPass = true
			case "context":
				opt.ToContext = "cluster"
			case "namespace":
				opt.ToNamespace = "agents"
			case "inference":
				opt.Inference = "provider:model"
			case "bindings":
				opt.Bindings = "unused"
			case "result":
				opt.ResultPort = "80"
			case "development":
				opt.DevelopmentHost = "unused"
			case "json":
				opt.JSON = true
			}
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err == nil {
				p.Close()
				t.Fatal("conflicting selector accepted")
			}
			if _, err = os.Stat(opt.Out); !os.IsNotExist(err) {
				t.Fatal("refusal wrote output", err)
			}
		})
	}
}
func TestCurrentNativePublicHostRefusal(t *testing.T) {
	for _, change := range []string{"remote", "hostname", "unknown", "permissions", "replacement"} {
		t.Run(change, func(t *testing.T) {
			opt, host := currentSuiteFixture(t)
			p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			raw, err := os.ReadFile(host)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "remote":
				raw = []byte(strings.Replace(string(raw), "127.0.0.1:12345", "10.0.0.1:12345", 1))
			case "hostname":
				raw = []byte(strings.Replace(string(raw), "127.0.0.1:12345", "localhost:12345", 1))
			case "unknown":
				raw = append([]byte(`{"unknown":true,`), raw[1:]...)
			case "permissions":
				if err = os.Chmod(host, 0644); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				tmp := host + ".replacement"
				if err = os.WriteFile(tmp, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(tmp, host); err != nil {
					t.Fatal(err)
				}
			}
			if change != "permissions" && change != "replacement" {
				if err = os.WriteFile(host, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if p.current.recheck(p.suite) == nil {
				t.Fatal("unsafe host change accepted")
			}
		})
	}
}
