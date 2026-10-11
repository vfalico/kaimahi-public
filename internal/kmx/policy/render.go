package policy

import (
	"bytes"
	"fmt"
	"go.yaml.in/yaml/v3"
	"time"
)

func RenderOrka(namespace string, p Policy, b Binding) ([]byte, error) {
	if namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	pd, err := PolicyDigest(p)
	if err != nil {
		return nil, err
	}
	if len(b.Spec.Sources) != 1 || b.Spec.Sources[0].Name != p.Metadata.Name || b.Spec.Sources[0].UID != p.Metadata.UID || b.Spec.Sources[0].Digest != pd {
		return nil, fmt.Errorf("binding source does not match policy UID and digest")
	}
	if _, err = BindingDigest(b); err != nil {
		return nil, err
	}
	bindingSpec := map[string]any{"subject": b.Spec.Subject, "sources": b.Spec.Sources}
	if b.Spec.NotBefore != nil {
		bindingSpec["notBefore"] = timeValue(b.Spec.NotBefore)
	}
	if b.Spec.NotAfter != nil {
		bindingSpec["notAfter"] = timeValue(b.Spec.NotAfter)
	}
	docs := []any{map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "AgentPolicy", "metadata": map[string]any{"name": p.Metadata.Name, "namespace": namespace}, "spec": map[string]any{"uid": p.Metadata.UID, "denyWriteObjects": p.Spec.DenyWriteObjects, "violationResponse": p.Spec.ViolationResponse, "requiredCapabilities": p.Spec.RequiredCapabilities}}, map[string]any{"apiVersion": "core.orka.ai/v1alpha1", "kind": "AgentPolicyBinding", "metadata": map[string]any{"name": b.Metadata.Name, "namespace": namespace}, "spec": bindingSpec}}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), enc.Close()
}

func timeValue(t *time.Time) string {
	return t.UTC().Format(time.RFC3339)
}
