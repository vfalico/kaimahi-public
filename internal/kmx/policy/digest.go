package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/gowebpki/jcs"
)

func PolicyDigest(p Policy) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	normalized := normalizedPolicy(p)
	return digest("policy", struct {
		UID  string     `json:"uid"`
		Spec PolicySpec `json:"spec"`
	}{UID: normalized.Metadata.UID, Spec: normalized.Spec})
}
func BindingDigest(b Binding) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	return digest("binding", normalizedBinding(b).Spec)
}
func digest(kind string, v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("canonicalize: %w", err)
	}
	pre := append([]byte("agentsuite.policy/"+kind+"/v1alpha1\n"), canonical...)
	sum := sha256.Sum256(pre)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
