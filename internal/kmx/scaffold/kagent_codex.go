package scaffold

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// KagentCodexSpec is explicit current-fork authoring. HarnessName and WorkerPool
// are target references, never guessed defaults. No images or secrets are read.
type KagentCodexSpec struct {
	KagentSpec
	HarnessName, WorkerPool, SnapshotLocation string
	ColdStartOnly                             bool
}
type KagentCodexBundle struct{ Secret, ModelConfig, Harness, Agent map[string]any }

// GenerateSuiteKagentCodex emits only the selected a0ad6cb v1alpha3 contract.
// This is an offline renderer, not admission, deployment or execution proof.
func GenerateSuiteKagentCodex(s KagentCodexSpec) (*KagentCodexBundle, error) {
	if s.Runtime != "codex" || s.ProviderType != "openai" || s.BaseURL == "" || len(s.Tools) != 0 {
		return nil, fmt.Errorf("current native Codex requires explicit OpenAI responses endpoint and no external tools")
	}

	endpoint, err := url.Parse(s.BaseURL)
	if err != nil || net.ParseIP(endpoint.Hostname()) != nil || endpoint.Hostname() == "" {
		return nil, fmt.Errorf("current Codex requires a DNS endpoint accepted by the selected compiler; IP literal is unsupported")
	}
	location, e := url.Parse(s.SnapshotLocation)
	if e != nil || location == nil || !strings.Contains(s.SnapshotLocation, "://") || (location.Scheme != "gs" && location.Scheme != "s3") || location.Host == "" || location.Opaque != "" || location.User != nil || location.RawQuery != "" || location.Fragment != "" || strings.ContainsAny(s.SnapshotLocation, " \t\r\n") {
		return nil, fmt.Errorf("current Codex requires explicit gs:// or s3:// snapshot URI with bucket and no query, fragment or userinfo")
	}
	for _, n := range []string{s.HarnessName, s.WorkerPool} {
		if ValidateObjectName(n) != nil {
			return nil, fmt.Errorf("current Codex requires explicit valid Harness and WorkerPool references")
		}
	}
	// Reuse bounded text, namespace, model and secret-reference validation only.
	legacy := s.KagentSpec
	legacy.Runtime = "go"
	validated, err := GenerateSuiteKagent(legacy)
	if err != nil {
		return nil, err
	}
	if s.SecretKey == "" {
		s.SecretKey = DefaultKagentSecretKey
	}
	object := func(kind, name string, spec map[string]any) map[string]any {
		return map[string]any{"apiVersion": "api.kagent.dev/v1alpha3", "kind": kind, "metadata": map[string]any{"name": name, "namespace": s.Namespace}, "spec": spec}
	}
	model := object("ModelConfig", s.Name+"-model", map[string]any{"model": s.Model, "provider": "OpenAI", "apiKeySecret": s.SecretName, "apiKeySecretKey": s.SecretKey, "openAI": map[string]any{"baseUrl": s.BaseURL, "apiFormat": "responses"}})
	harness := object("Harness", s.HarnessName, map[string]any{"codex": map[string]any{}, "substrate": map[string]any{"workerPoolRef": map[string]any{"name": s.WorkerPool}, "snapshotPolicy": map[string]any{"location": s.SnapshotLocation}}})
	if s.ColdStartOnly {
		harness["spec"].(map[string]any)["substrate"].(map[string]any)["snapshotPolicy"].(map[string]any)["coldStartOnly"] = true
	}
	agent := object("Agent", s.Name, map[string]any{"harnessRef": map[string]any{"name": s.HarnessName}, "template": map[string]any{"inline": map[string]any{"description": s.Description, "systemPrompt": s.Instructions, "modelConfig": map[string]any{"name": s.Name + "-model"}}}})
	return &KagentCodexBundle{Secret: validated.Secret, ModelConfig: model, Harness: harness, Agent: agent}, nil
}
