package app

import (
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentCodexRefusesBeforeLegacyPlacement(t *testing.T) {
	opt := suiteKagentEvaluationOptions(t)
	file := filepath.Join(opt.Workspace, "agents/sample/member.yaml")
	raw, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "v0.10.3", workspace.KagentCodexVersion), "engine: go", "engine: codex")
	text += "policy: policy.json\n"
	writeFile(t, file, text)
	writeFile(t, filepath.Join(opt.Workspace, "agents/sample/policy.json"), `{"version":"agentsuite.policy/v2","network":{"egressAllow":[]},"requiredCapabilities":["network/egress-allow/v1"]}`)
	p, e := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if e == nil {
		p.Close()
		t.Fatal("unqualified current-fork profile admitted through legacy placement")
	}
	if !strings.Contains(e.Error(), "offline packaging only") {
		t.Fatal("unexpected refusal", e)
	}
	if _, e = os.Stat(opt.Out); !os.IsNotExist(e) {
		t.Fatal("refusal produced receipt", e)
	}
}
