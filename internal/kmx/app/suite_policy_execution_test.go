package app

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/agent-primitives/suite/policy"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func TestSuiteExecutionRequirementExactSuiteBytes(t *testing.T) {
	raw := []byte(`{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[{"address":"127.0.0.1","protocol":"TCP","port":18082},{"address":"127.0.0.1","protocol":"TCP","port":18081}]}}`)
	s := &workspace.NativeSuite{Runtime: workspace.RuntimeProfile{ID: "orka"}, Policy: &workspace.NativePolicy{Bytes: raw}}
	r, err := resolveSuiteExecutionRequirement(s)
	if err != nil {
		t.Fatal(err)
	}
	authored, err := policy.DecodeSuitePolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := policy.Resolve(authored, policy.AdministrativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := policy.Payload(effective)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.PolicyBytes, payload.PolicyBytes) || r.PayloadDigest != payload.PolicyDigest || r.Format != payload.Version {
		t.Fatal("Suite payload changed")
	}
	accepted := append([]byte(nil), r.PolicyBytes...)
	for i := range raw {
		raw[i] = 'x'
	}
	if !bytes.Equal(accepted, r.PolicyBytes) {
		t.Fatal("accepted bytes alias source")
	}
	serialized, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var received suiteExecutionRequirement
	if err = json.Unmarshal(serialized, &received); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received.PolicyBytes, accepted) || received.Identity != r.Identity {
		t.Fatal("submission changed bytes")
	}
}
func TestSuiteExecutionRefusesOtherPolicyAndRuntime(t *testing.T) {
	for _, s := range []*workspace.NativeSuite{nil, {Runtime: workspace.RuntimeProfile{ID: "orka"}}, {Runtime: workspace.RuntimeProfile{ID: "agentsessions"}, Policy: &workspace.NativePolicy{Bytes: []byte(`{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[]}}`)}}, {Runtime: workspace.RuntimeProfile{ID: "orka"}, Policy: &workspace.NativePolicy{Bytes: []byte(`{"version":"agentsuite.policy/v1","denyWritePaths":["/tmp/x"]}`)}}} {
		if _, err := resolveSuiteExecutionRequirement(s); err == nil {
			t.Fatal("unsupported policy accepted")
		}
	}
}
func TestSuiteExecutionAgentQualification(t *testing.T) {
	valid := []byte(`{"metadata":{"uid":"u"},"spec":{"runtime":{"type":"codex","contractVersion":"orka.harness.v2"}}}`)
	if err := qualifySuiteACPAgent(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"spec":{"providerRef":{"name":"legacy"}}}`, `{"spec":{"runtime":{"type":"codex"}}}`, `{"spec":{"runtime":{"type":"claude","contractVersion":"orka.harness.v2"}}}`, `{"spec":{"runtime":{"type":"codex","contractVersion":"orka.harness.v2","runtimeRef":{"name":"external"}}}}`} {
		if qualifySuiteACPAgent([]byte(raw)) == nil {
			t.Fatal("unqualified Agent accepted")
		}
	}
}

func TestSuiteComputeRequirementUsesExactSuitePayload(t *testing.T) {
	for _, runtime := range []string{"kagent", "agentsessions"} {
		t.Run(runtime, func(t *testing.T) {
			raw := []byte(`{"version":"agentsuite.policy/v2","requiredCapabilities":["network/egress-allow/v1"],"network":{"egressAllow":[{"address":"127.0.0.1","protocol":"TCP","port":18082},{"address":"127.0.0.1","protocol":"TCP","port":18081}]}}`)
			native := &workspace.NativeSuite{Runtime: workspace.RuntimeProfile{ID: runtime}, Policy: &workspace.NativePolicy{Bytes: raw}}
			wire, err := resolveSuiteComputeRequirement(native)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Format       string   `json:"format"`
				Bytes        []byte   `json:"bytes"`
				Digest       string   `json:"digest"`
				Capabilities []string `json:"capabilities"`
			}
			if err := json.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			authored, err := policy.DecodeSuitePolicy(raw)
			if err != nil {
				t.Fatal(err)
			}
			effective, err := policy.Resolve(authored, policy.AdministrativePolicy{})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := policy.Payload(effective)
			if err != nil {
				t.Fatal(err)
			}
			if got.Format != payload.Version || !bytes.Equal(got.Bytes, payload.PolicyBytes) || got.Digest != payload.PolicyDigest || len(got.Capabilities) != 1 || got.Capabilities[0] != "network/egress-allow/v1" {
				t.Fatal("effective policy was changed")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(wire, &fields); err != nil || len(fields) != 4 {
				t.Fatal("runtime-specific extra fields", err)
			}
			if !bytes.Equal(native.Policy.Bytes, raw) {
				t.Fatal("authored document mutated")
			}
			native.Policy = nil
			wire, err = resolveSuiteComputeRequirement(native)
			if err != nil || len(wire) != 0 {
				t.Fatal("policy-free default added", err)
			}
		})
	}
	if _, err := resolveSuiteComputeRequirement(&workspace.NativeSuite{Runtime: workspace.RuntimeProfile{ID: "orka"}}); err == nil {
		t.Fatal("Orka envelope confused with compute envelope")
	}
}
