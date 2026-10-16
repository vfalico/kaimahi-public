package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func TestSuitePolicyCommandEmitsCanonicalPayloadAndDigest(t *testing.T) {
	directory := t.TempDir()
	policyPath := filepath.Join(directory, "policy.json")
	adminPath := filepath.Join(directory, "admin.json")
	if err := os.WriteFile(policyPath, []byte(`{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adminPath, []byte(`{"denyWritePaths":["/etc/secrets"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newSuitePolicyCommand()
	command.SetOut(&output)
	command.SetArgs([]string{"--administrative-policy", adminPath, policyPath})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %d, want 2: %q", len(lines), output.String())
	}
	wantPolicy := `{"denyWritePaths":["/etc/secrets","/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"],"version":"agentsuite.policy/v1"}`
	if lines[0] != wantPolicy {
		t.Fatalf("policy = %q, want %q", lines[0], wantPolicy)
	}
	if !strings.HasPrefix(lines[1], "sha256:") || len(lines[1]) != len("sha256:")+64 {
		t.Fatalf("digest = %q", lines[1])
	}
}

func TestSuitePolicyCommandRejectsInvalidCapabilityContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := newSuitePolicyCommand()
	command.SetArgs([]string{path})
	if err := command.Execute(); err == nil {
		t.Fatal("accepted empty mandatory capability set")
	}
}

func TestSuitePolicyCommandUsesSuiteV030CanonicalOrdering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	input := `{"version":"agentsuite.policy/v1","denyWritePaths":["/\ue000","/\ud800\udc00","/\ue000"],"requiredCapabilities":["filesystem/deny-write/v1"]}`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	command := newSuitePolicyCommand()
	command.SetOut(&output)
	command.SetArgs([]string{path})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "{\"denyWritePaths\":[\"/𐀀\",\"/\ue000\"],\"requiredCapabilities\":[\"filesystem/deny-write/v1\"],\"version\":\"agentsuite.policy/v1\"}\n" +
		"sha256:2051c088dcadf8c9df216d7cec3eb1e91630a0d4b265fefcf59b4a3d3f6e9c0f\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestSuitePolicyCommandRejectsNullAdministrativePolicy(t *testing.T) {
	for _, test := range []struct {
		name      string
		input     string
		wantError string
	}{
		{name: "document", input: `null`, wantError: "must be an object"},
		{name: "paths", input: `{"denyWritePaths":null}`, wantError: "must not be null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			policyPath := filepath.Join(directory, "policy.json")
			adminPath := filepath.Join(directory, "admin.json")
			if err := os.WriteFile(policyPath, []byte(`{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"]}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(adminPath, []byte(test.input), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			command := newSuitePolicyCommand()
			command.SilenceUsage = true
			command.SetOut(&output)
			command.SetArgs([]string{"--administrative-policy", adminPath, policyPath})
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want null administrative policy refusal", err)
			}
			if output.Len() != 0 {
				t.Fatalf("refusal emitted stdout: %q", output.String())
			}
		})
	}
}

func TestSuitePolicyCommandSanitizesMalformedInputDiagnostics(t *testing.T) {
	const valid = `{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"]}`
	const malformed = `{} "\u001b[2J\u0007\u009b31m"`
	for _, input := range []string{"suite", "administrative"} {
		t.Run(input, func(t *testing.T) {
			directory := t.TempDir()
			policyPath := filepath.Join(directory, "policy.json")
			adminPath := filepath.Join(directory, "admin.json")
			policyJSON, adminJSON := valid, `{"denyWritePaths":["/etc/secrets"]}`
			if input == "suite" {
				policyJSON = malformed
			} else {
				adminJSON = malformed
			}
			for path, data := range map[string]string{policyPath: policyJSON, adminPath: adminJSON} {
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var output, diagnostics bytes.Buffer
			deps, loads := testDependencies(&output, &diagnostics)
			err := execute([]string{"suite", "policy", policyPath, "--administrative-policy", adminPath}, deps)
			if *loads != 0 {
				t.Fatalf("offline policy command loaded operational configuration %d times", *loads)
			}
			if err == nil || !strings.Contains(err.Error(), "trailing") {
				t.Fatalf("error = %v, want malformed trailing JSON refusal", err)
			}
			for _, r := range err.Error() {
				if unicode.IsControl(r) {
					t.Fatalf("unsafe diagnostic contains terminal control U+%04X", r)
				}
			}
			if strings.ContainsAny(diagnostics.String(), "\x1b\x07\u009b") {
				t.Fatalf("unsafe stderr: %q", diagnostics.String())
			}
			var diagnostic *suiteDiagnosticError
			if !errors.As(err, &diagnostic) || errors.Unwrap(diagnostic) == nil {
				t.Fatalf("error = %T, want suite diagnostic retaining its cause", err)
			}
			if output.Len() != 0 {
				t.Fatalf("refusal emitted stdout: %q", output.String())
			}
		})
	}
}

func TestSuitePolicyCommandRefusesExplicitEmptyAdministrativePolicy(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"version":"agentsuite.policy/v1","denyWritePaths":["/workspace"],"requiredCapabilities":["filesystem/deny-write/v1"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		flags   []string
		wantErr bool
	}{
		{name: "omitted remains optional"},
		{name: "empty equals", flags: []string{"--administrative-policy="}, wantErr: true},
		{name: "empty argument", flags: []string{"--administrative-policy", ""}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			deps, loads := testDependencies(&output, &diagnostics)
			args := append([]string{"suite", "policy", policyPath}, tc.flags...)
			err := execute(args, deps)
			if *loads != 0 {
				t.Fatalf("offline policy command loaded operational configuration %d times", *loads)
			}
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "--administrative-policy cannot be empty") {
					t.Fatalf("error = %v, want explicit empty administrative-policy refusal", err)
				}
				if output.Len() != 0 {
					t.Fatalf("refusal emitted stdout: %q", output.String())
				}
			} else if err != nil || !strings.Contains(output.String(), "sha256:") {
				t.Fatalf("omitted administrative policy: error = %v, output = %q", err, output.String())
			}
		})
	}
}

func TestReadPolicyFileRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, maxPolicyFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPolicyFile(path); err == nil {
		t.Fatal("accepted oversized policy file")
	}
}
