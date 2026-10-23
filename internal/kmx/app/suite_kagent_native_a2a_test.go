package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSuiteKagentRealNativeA2ACarrier(t *testing.T) {
	socket := os.Getenv("KMX_KAGENT_NATIVE_SOCKET")
	if socket == "" {
		t.Skip("explicit native gateway")
	}
	a, _, _, _ := suiteKagentFixture(t, "")
	profile := a.selectedKagentProfile()
	profile.A2AV1 = true
	a.kagentProfile = &profile
	opt := suiteKagentEvaluationOptions(t)
	raw, err := os.ReadFile(filepath.Join(os.Getenv("EXECUTION_KAGENT_NATIVE_ROOT"), "requirement.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Bytes string `json:"bytes"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	policy, err := base64.StdEncoding.DecodeString(envelope.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(opt.Workspace, "agents/sample/policy.json"), string(policy))
	member := filepath.Join(opt.Workspace, "agents/sample/member.yaml")
	b, err := os.ReadFile(member)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, member, string(b)+"policy: policy.json\n")
	if err = os.Remove(filepath.Join(opt.Workspace, "eval/second.yml")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(opt.Workspace, "eval/example.yaml"), "id: native\ninput: Execute the native network probe then answer complete.\nexpectContains: [complete]\n")
	// Translate the fake kubectl service-proxy carrier at its executable boundary;
	// the entire payload and response travel unchanged through the real gateway.
	capture := a.captureCommand
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 85 * time.Second}
	sends := 0
	a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if strings.Contains(arg, "/proxy/agents/") && slices.Contains(cmd.Args, "create") {
				sends++
				_, suffix, _ := strings.Cut(arg, "/proxy")
				requestRaw, e := io.ReadAll(cmd.Stdin)
				if e != nil {
					return e
				}
				_ = os.WriteFile(filepath.Join(os.Getenv("EXECUTION_KAGENT_NATIVE_ROOT"), "gateway-request.json"), requestRaw, 0600)
				req, e := http.NewRequestWithContext(ctx, "POST", "http://native"+suffix, bytes.NewReader(requestRaw))
				if e != nil {
					return e
				}
				req.Header.Set("Content-Type", "application/json")
				response, e := client.Do(req)
				if e != nil {
					return e
				}
				defer response.Body.Close()
				responseRaw, e := io.ReadAll(response.Body)
				if e != nil {
					return e
				}
				_ = os.WriteFile(filepath.Join(os.Getenv("EXECUTION_KAGENT_NATIVE_ROOT"), "gateway-response.json"), responseRaw, 0600)
				t.Logf("real gateway status=%d body=%s", response.StatusCode, responseRaw)
				_, e = cmd.Stdout.Write(responseRaw)
				return e
			}
		}
		return capture(ctx, cmd)
	}
	p, err := PrepareSuiteWorkspaceEvaluation(t.Context(), opt)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	err = a.EvaluateSuiteWorkspace(t.Context(), p)
	if os.Getenv("EXECUTION_KAGENT_NATIVE_SCENARIO") == "deny" {
		if err == nil {
			t.Fatal("forbidden native tool execution credited as passing")
		}
		if _, e := os.Stat(opt.Out); !os.IsNotExist(e) {
			t.Fatal("failed native execution left passing receipt", e)
		}
		if sends != 1 {
			t.Fatal("native send count", sends)
		}
		t.Log("KAIMAHI_NATIVE_KAGENT_DENIED_TOOL_NO_PASSING_RECEIPT_PASS")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatalf("send count %d", sends)
	}
	t.Log("KAIMAHI_REAL_NATIVE_KAGENT_CARRIER_PASS exact ordinary policy, actual gateway and native result; discovery/placement fixture")
}
