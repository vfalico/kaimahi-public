package policy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixture() (Policy, Binding) {
	p := Policy{APIVersion: APIVersion, Kind: KindPolicy, Metadata: DocumentMeta{Name: "guard", UID: "policy-uid"}, Spec: PolicySpec{DenyWriteObjects: []WriteObject{{Path: "/workspace/locked"}}, ViolationResponse: ViolationResponse{Action: "deny", Threshold: 1}, RequiredCapabilities: []string{"ig.agent-policy/file-permission/v1"}}}
	d, _ := PolicyDigest(p)
	b := Binding{APIVersion: APIVersion, Kind: KindBinding, Metadata: DocumentMeta{Name: "guard-binding", UID: "binding-uid"}, Spec: BindingSpec{Subject: BindingSubject{SuiteDigest: "sha256:" + strings.Repeat("1", 64), AgentID: "agent", CompositionDigest: "sha256:" + strings.Repeat("2", 64), SandboxBindingDigest: "sha256:" + strings.Repeat("3", 64)}, Sources: []PolicySource{{Name: p.Metadata.Name, UID: p.Metadata.UID, Digest: d}}}}
	return p, b
}
func TestDigestStableAcrossRuleOrder(t *testing.T) {
	p, _ := fixture()
	p.Spec.DenyWriteObjects = append(p.Spec.DenyWriteObjects, WriteObject{Path: "/workspace/a"})
	a, _ := PolicyDigest(p)
	p.Spec.DenyWriteObjects[0], p.Spec.DenyWriteObjects[1] = p.Spec.DenyWriteObjects[1], p.Spec.DenyWriteObjects[0]
	b, _ := PolicyDigest(p)
	if a != b {
		t.Fatalf("digest changed with order: %s != %s", a, b)
	}
}
func TestValidationRejectsUnsupportedOrDuplicate(t *testing.T) {
	p, _ := fixture()
	p.Spec.DenyWriteObjects = append(p.Spec.DenyWriteObjects, p.Spec.DenyWriteObjects[0])
	if err := p.Validate(); err == nil {
		t.Fatal("duplicate accepted")
	}
	p, _ = fixture()
	p.Spec.RequiredCapabilities = []string{"invented"}
	if err := p.Validate(); err == nil {
		t.Fatal("unknown capability accepted")
	}
	p, _ = fixture()
	p.Spec.RequiredCapabilities = []string{"ig.agent-policy/cgroup-connect4/v1"}
	if err := p.Validate(); err == nil {
		t.Fatal("unimplemented cgroup-skb capability accepted")
	}
}
func TestStrictDecode(t *testing.T) {
	p, _ := fixture()
	raw, _ := json.Marshal(p)
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	var got Policy
	if err := StrictDecode(raw, &got); err == nil {
		t.Fatal("unknown field accepted")
	}
}
func TestBindingWindowAndRender(t *testing.T) {
	p, b := fixture()
	now := time.Now()
	b.Spec.NotBefore = &now
	b.Spec.NotAfter = &now
	if err := b.Validate(); err == nil {
		t.Fatal("invalid window accepted")
	}
	b.Spec.NotAfter = nil
	out, err := RenderOrka("agents", p, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "kind: AgentPolicyBinding") || !strings.Contains(string(out), "uid: policy-uid") || !strings.Contains(string(out), "name: guard") {
		t.Fatalf("missing portable source identity: %s", out)
	}
	if !strings.Contains(string(out), "notBefore:") || strings.Contains(string(out), "notAfter:") || strings.Contains(string(out), "null") {
		t.Fatalf("optional validity fields were not rendered structurally: %s", out)
	}
}
func TestRenderOmitsAbsentValidityWindow(t *testing.T) {
	p, b := fixture()
	out, err := RenderOrka("agents", p, b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "notBefore:") || strings.Contains(string(out), "notAfter:") || strings.Contains(string(out), "null") {
		t.Fatalf("absent validity fields must be omitted: %s", out)
	}
}
func TestOrkaContractDigestVectors(t *testing.T) {
	p, b := fixture()
	pd, err := PolicyDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	bd, err := BindingDigest(b)
	if err != nil {
		t.Fatal(err)
	}
	// These vectors are also asserted in Orka. A change on either side must be
	// coordinated rather than silently producing permanently pending bindings.
	if pd != "sha256:c6cc7347b678b819e529237356492d8222467f68de0dc62a369f3660d428be72" || bd != "sha256:342eeb5999e887a536d8d3d260b522bc31227d950d92862947aa543bd8857e72" {
		t.Fatalf("update cross-project vectors after intentional contract change: policy=%s binding=%s", pd, bd)
	}
}
