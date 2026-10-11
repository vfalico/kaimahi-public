// Package policy implements the portable AgentSuite policy contract.
package policy

import "time"

const (
	APIVersion      = "agentsuite.dev/v1alpha1"
	KindPolicy      = "AgentPolicy"
	KindBinding     = "AgentPolicyBinding"
	MaxWriteObjects = 128
)

type DocumentMeta struct {
	Name string `json:"name" yaml:"name"`
	UID  string `json:"uid" yaml:"uid"`
}
type WriteObject struct {
	Path string `json:"path" yaml:"path"`
}
type ViolationResponse struct {
	Action    string `json:"action" yaml:"action"`
	Threshold uint32 `json:"threshold" yaml:"threshold"`
}
type PolicySpec struct {
	DenyWriteObjects     []WriteObject     `json:"denyWriteObjects,omitempty" yaml:"denyWriteObjects,omitempty"`
	ViolationResponse    ViolationResponse `json:"violationResponse" yaml:"violationResponse"`
	RequiredCapabilities []string          `json:"requiredCapabilities" yaml:"requiredCapabilities"`
}
type Policy struct {
	APIVersion string       `json:"apiVersion" yaml:"apiVersion"`
	Kind       string       `json:"kind" yaml:"kind"`
	Metadata   DocumentMeta `json:"metadata" yaml:"metadata"`
	Spec       PolicySpec   `json:"spec" yaml:"spec"`
}
type PolicySource struct {
	Name   string `json:"name" yaml:"name"`
	UID    string `json:"uid" yaml:"uid"`
	Digest string `json:"digest" yaml:"digest"`
}
type BindingSubject struct {
	SuiteDigest          string `json:"suiteDigest" yaml:"suiteDigest"`
	AgentID              string `json:"agentID" yaml:"agentID"`
	CompositionDigest    string `json:"compositionDigest" yaml:"compositionDigest"`
	SandboxBindingDigest string `json:"sandboxBindingDigest" yaml:"sandboxBindingDigest"`
}
type BindingSpec struct {
	Subject   BindingSubject `json:"subject" yaml:"subject"`
	Sources   []PolicySource `json:"sources" yaml:"sources"`
	NotBefore *time.Time     `json:"notBefore,omitempty" yaml:"notBefore,omitempty"`
	NotAfter  *time.Time     `json:"notAfter,omitempty" yaml:"notAfter,omitempty"`
}
type Binding struct {
	APIVersion string       `json:"apiVersion" yaml:"apiVersion"`
	Kind       string       `json:"kind" yaml:"kind"`
	Metadata   DocumentMeta `json:"metadata" yaml:"metadata"`
	Spec       BindingSpec  `json:"spec" yaml:"spec"`
}
