package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var capabilitySet = map[string]bool{"ig.agent-policy/file-permission/v1": true}

func StrictDecode(data []byte, out any) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) == nil {
		return errors.New("multiple JSON values")
	}
	return nil
}
func validateMeta(m DocumentMeta) error {
	if len(m.Name) > 63 || !nameRE.MatchString(m.Name) {
		return fmt.Errorf("metadata.name %q is not a DNS label", m.Name)
	}
	if strings.TrimSpace(m.UID) == "" || len(m.UID) > 128 {
		return errors.New("metadata.uid must contain 1..128 characters")
	}
	return nil
}
func (p Policy) Validate() error {
	if p.APIVersion != APIVersion || p.Kind != KindPolicy {
		return errors.New("unsupported policy type")
	}
	if err := validateMeta(p.Metadata); err != nil {
		return err
	}
	if len(p.Spec.DenyWriteObjects) == 0 {
		return errors.New("policy must contain at least one enforcement rule")
	}
	if len(p.Spec.DenyWriteObjects) > MaxWriteObjects {
		return errors.New("policy rule limit exceeded")
	}
	seen := map[string]bool{}
	for _, o := range p.Spec.DenyWriteObjects {
		if !filepath.IsAbs(o.Path) || filepath.Clean(o.Path) != o.Path {
			return fmt.Errorf("write object path %q must be absolute and clean", o.Path)
		}
		if seen[o.Path] {
			return fmt.Errorf("duplicate write object %q", o.Path)
		}
		seen[o.Path] = true
	}
	if p.Spec.ViolationResponse.Action != "deny" {
		return errors.New("violationResponse.action must be deny")
	}
	if p.Spec.ViolationResponse.Threshold == 0 {
		return errors.New("violationResponse.threshold must be positive")
	}
	if len(p.Spec.RequiredCapabilities) == 0 {
		return errors.New("requiredCapabilities is required")
	}
	seen = map[string]bool{}
	for _, c := range p.Spec.RequiredCapabilities {
		if !capabilitySet[c] {
			return fmt.Errorf("unsupported capability %q", c)
		}
		if seen[c] {
			return fmt.Errorf("duplicate capability %q", c)
		}
		seen[c] = true
	}
	return nil
}
func (b Binding) Validate() error {
	if b.APIVersion != APIVersion || b.Kind != KindBinding {
		return errors.New("unsupported binding type")
	}
	if err := validateMeta(b.Metadata); err != nil {
		return err
	}
	s := b.Spec.Subject
	if !validDigest(s.SuiteDigest) || !validDigest(s.CompositionDigest) || !validDigest(s.SandboxBindingDigest) || strings.TrimSpace(s.AgentID) == "" {
		return errors.New("binding subject digests and agentID are required")
	}
	if len(b.Spec.Sources) == 0 || len(b.Spec.Sources) > 32 {
		return errors.New("binding requires 1..32 policy sources")
	}
	seen := map[string]bool{}
	for _, s := range b.Spec.Sources {
		if len(s.Name) > 63 || !nameRE.MatchString(s.Name) || s.UID == "" || !validDigest(s.Digest) {
			return errors.New("policy source name, UID, and sha256 digest are required")
		}
		if seen[s.UID] {
			return fmt.Errorf("duplicate policy source UID %q", s.UID)
		}
		seen[s.UID] = true
	}
	if b.Spec.NotBefore != nil && b.Spec.NotAfter != nil && !b.Spec.NotBefore.Before(*b.Spec.NotAfter) {
		return errors.New("notBefore must precede notAfter")
	}
	return nil
}
func validDigest(s string) bool {
	return len(s) == 71 && strings.HasPrefix(s, "sha256:") && func() bool {
		for _, c := range s[7:] {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return false
			}
		}
		return true
	}()
}
func normalizedPolicy(p Policy) Policy {
	p.Spec.DenyWriteObjects = append([]WriteObject(nil), p.Spec.DenyWriteObjects...)
	p.Spec.RequiredCapabilities = append([]string(nil), p.Spec.RequiredCapabilities...)
	sort.Slice(p.Spec.DenyWriteObjects, func(i, j int) bool { return p.Spec.DenyWriteObjects[i].Path < p.Spec.DenyWriteObjects[j].Path })
	sort.Strings(p.Spec.RequiredCapabilities)
	return p
}
func normalizedBinding(b Binding) Binding {
	b.Spec.Sources = append([]PolicySource(nil), b.Spec.Sources...)
	sort.Slice(b.Spec.Sources, func(i, j int) bool {
		if b.Spec.Sources[i].UID == b.Spec.Sources[j].UID {
			if b.Spec.Sources[i].Name == b.Spec.Sources[j].Name {
				return b.Spec.Sources[i].Digest < b.Spec.Sources[j].Digest
			}
			return b.Spec.Sources[i].Name < b.Spec.Sources[j].Name
		}
		return b.Spec.Sources[i].UID < b.Spec.Sources[j].UID
	})
	return b
}
