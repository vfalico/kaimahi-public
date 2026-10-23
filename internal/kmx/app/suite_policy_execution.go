package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	agentsuite "github.com/agent-primitives/suite"
	"github.com/agent-primitives/suite/policy"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// suiteExecutionRequirement is the bounded opaque Orka envelope. The payload
// and its framed digest come from Suite, without reserialization or substitution.
// This is task input, never an Agent or RuntimePool/profile default.
type suiteExecutionRequirement struct {
	Format               string   `json:"format"`
	PolicyBytes          []byte   `json:"policyBytes"`
	RequiredCapabilities []string `json:"requiredCapabilities"`
	PayloadDigest        string   `json:"payloadDigest"`
	Identity             string   `json:"identity"`
}

func resolveSuiteExecutionRequirement(s *workspace.NativeSuite) (*suiteExecutionRequirement, error) {
	if s == nil || s.Runtime.ID != "orka" {
		return nil, errors.New("governed execution requires an Orka Suite")
	}
	if err := s.RequireUnprofiled(); err != nil {
		return nil, err
	}
	if s.Policy == nil {
		return nil, errors.New("governed execution requires an authored Suite network-v2 policy")
	}
	payload, err := resolveNativeSuitePolicyPayload(s)
	if err != nil {
		return nil, err
	}
	r := &suiteExecutionRequirement{Format: payload.Version, PolicyBytes: append([]byte(nil), payload.PolicyBytes...), RequiredCapabilities: append([]string(nil), payload.RequiredCapabilities...), PayloadDigest: payload.PolicyDigest}
	h := sha256.New()
	h.Write([]byte("orka.execution-requirement\x00v1\x00"))
	fields := [][]byte{[]byte(r.Format), r.PolicyBytes, []byte(r.PayloadDigest)}
	for _, c := range r.RequiredCapabilities {
		fields = append(fields, []byte(c))
	}
	for _, f := range fields {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(f)))
		h.Write(size[:])
		h.Write(f)
	}
	r.Identity = "sha256:" + hex.EncodeToString(h.Sum(nil))
	return r, nil
}

func loadSuiteExecutionRequirement(ctx context.Context, root string) (*suiteExecutionRequirement, error) {
	resolved, err := resolveSuiteWorkspace(root)
	if err != nil {
		return nil, err
	}
	state, err := config.StateDir()
	if err != nil {
		return nil, err
	}
	prepared, err := workspace.Prepare(ctx, resolved, filepath.Join(state, "suites"))
	if err != nil {
		return nil, err
	}
	s, err := workspace.Load(ctx, prepared)
	if err != nil {
		return nil, err
	}
	return resolveSuiteExecutionRequirement(s)
}

// qualifySuiteACPAgent refuses the legacy AI renderer, external runtimes,
// mixed runtime/provider identities and unqualified runtime ABIs. Backend
// availability and environment qualification remain Orka's admission duties.
func qualifySuiteACPAgent(raw []byte) error {
	var doc struct {
		Spec struct {
			Runtime *struct {
				Type            string          `json:"type"`
				ContractVersion string          `json:"contractVersion"`
				RuntimeRef      json.RawMessage `json:"runtimeRef"`
			} `json:"runtime"`
			ProviderRef json.RawMessage `json:"providerRef"`
		} `json:"spec"`
	}
	if agentsuite.Unmarshal(raw, &doc) != nil {
		// Live Kubernetes objects contain server fields: ordinary json decoding is
		// intentional here, while exact required fields below remain mandatory.
		if json.Unmarshal(raw, &doc) != nil {
			return errors.New("cannot qualify live Agent")
		}
	}
	r := doc.Spec.Runtime
	if r == nil || r.Type != "codex" || r.ContractVersion != "orka.harness.v2" || (len(r.RuntimeRef) > 0 && string(r.RuntimeRef) != "null") || (len(doc.Spec.ProviderRef) > 0 && string(doc.Spec.ProviderRef) != "null") {
		return errors.New("Suite policy requires a built-in Codex orka.harness.v2 Agent without runtimeRef or providerRef; legacy AI execution remains refused")
	}
	return nil
}

// Native payload adapters share Suite resolution, never an administrative
// exception or inferred endpoint. Different runtime envelopes remain distinct.
func resolveNativeSuitePolicyPayload(s *workspace.NativeSuite) (*policy.ProviderPayload, error) {
	if s == nil || s.Policy == nil {
		return nil, errors.New("authored Suite network-v2 policy required")
	}
	if err := s.RequireUnprofiled(); err != nil {
		return nil, err
	}
	authored, err := policy.DecodeSuitePolicy(s.Policy.Bytes)
	if err != nil || authored.Version != policy.SchemaVersionV2 {
		return nil, errors.New("ordinary Suite network-v2 policy required")
	}
	effective, err := policy.Resolve(authored, policy.AdministrativePolicy{})
	if err != nil {
		return nil, fmt.Errorf("resolve Suite policy: %w", err)
	}
	payload, err := policy.Payload(effective)
	if err != nil {
		return nil, fmt.Errorf("freeze Suite policy: %w", err)
	}
	if len(payload.PolicyBytes) > 256<<10 {
		return nil, errors.New("effective Suite policy exceeds native requirement capacity")
	}
	return &payload, nil
}

// resolveSuiteComputeRequirement is the neutral owning-compute envelope used
// by the two Actor runtimes. The payload and digest come directly from Suite.
// Guardd remains responsible for accepting the complete effective policy.
func resolveSuiteComputeRequirement(s *workspace.NativeSuite) ([]byte, error) {
	if s == nil || (s.Runtime.ID != "agentsessions" && s.Runtime.ID != "kagent") {
		return nil, workspace.ErrPolicyExecutionUnsupported
	}
	if s.Policy == nil {
		return nil, nil
	}
	payload, err := resolveNativeSuitePolicyPayload(s)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Format       string   `json:"format"`
		Bytes        []byte   `json:"bytes"`
		Digest       string   `json:"digest"`
		Capabilities []string `json:"capabilities"`
	}{payload.Version, append([]byte(nil), payload.PolicyBytes...), payload.PolicyDigest, append([]string(nil), payload.RequiredCapabilities...)})
}

// prepareGovernedSuiteOrka projects only an authored policy to the built-in
// native ABI; policy bytes are Task input, never Agent or Provider defaults.
func prepareGovernedSuiteOrka(s *workspace.NativeSuite, bundle *scaffold.OrkaBundle) (*suiteExecutionRequirement, error) {
	if s == nil || s.Policy == nil {
		return nil, nil
	}
	requirement, err := resolveSuiteExecutionRequirement(s)
	if err != nil {
		return nil, err
	}
	spec, ok := bundle.Agent["spec"].(map[string]any)
	if !ok {
		return nil, errors.New("missing native Agent")
	}
	// Validate the ordinary value-free bundle before the ABI-specific projection.
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	delete(spec, "providerRef")
	spec["runtime"] = map[string]any{"type": "codex", "contractVersion": "orka.harness.v2"}
	spec["model"] = map[string]any{"name": s.Model}
	raw, err := json.Marshal(bundle.Agent)
	if err != nil {
		return nil, err
	}
	if err := qualifySuiteACPAgent(raw); err != nil {
		return nil, err
	}
	return requirement, nil
}

func resolveSuiteRuntimeRequirement(s *workspace.NativeSuite) ([]byte, error) {
	if s == nil {
		return nil, workspace.ErrPolicyExecutionUnsupported
	}
	if s.Policy == nil {
		return nil, nil
	}
	if s.Runtime.ID == "orka" {
		r, err := resolveSuiteExecutionRequirement(s)
		if err != nil {
			return nil, err
		}
		return json.Marshal(r)
	}
	return resolveSuiteComputeRequirement(s)
}
