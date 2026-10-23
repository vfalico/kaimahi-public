//go:build nativeintegration

package app

import (
	"bytes"
	"encoding/json"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	sdk "github.com/agent-primitives/sessions/client"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type nativeFailureObservation struct {
	Failure       bool   `json:"native_tool_failure"`
	Session       string `json:"session_uid"`
	Execution     string `json:"execution_id"`
	Call          string `json:"tool_call_id"`
	Errno         int    `json:"native_errno"`
	ContextFailed bool   `json:"context_failed"`
}

func (o nativeFailureObservation) qualifies() bool {
	return o.Failure && o.Session != "" && o.Execution != "" && o.Call != "" && !o.ContextFailed && (o.Errno == int(syscall.EPERM) || o.Errno == int(syscall.EACCES))
}
func verifyOrdinaryNativeDenial(t *testing.T, root, address string) any {
	t.Helper()
	raw, e := os.ReadFile(filepath.Join(root, "guest-output.log"))
	if e != nil {
		t.Fatal(e)
	}
	expected, e := os.ReadFile(filepath.Join(root, "requirement.json"))
	if e != nil {
		t.Fatal(e)
	}
	var want any
	if e = json.Unmarshal(expected, &want); e != nil {
		t.Fatal(e)
	}
	canonical, _ := json.Marshal(want)
	client, e := sdk.Dial(address)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	matches := 0
	var evidence any
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var row struct {
			nativeFailureObservation
			Message string            `json:"message"`
			Labels  map[string]string `json:"labels"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		observation := row.nativeFailureObservation
		if !observation.qualifies() && row.Message != "" {
			_ = json.Unmarshal([]byte(row.Message), &observation)
		}
		if !observation.qualifies() {
			continue
		}
		if row.Labels["ate.actor.name"] != observation.Session || row.Labels["ate.actor.uid"] == "" {
			t.Fatal("native observation actor mismatch")
		}
		session, e := client.GetSession(t.Context(), observation.Session)
		if e != nil {
			t.Fatal(e)
		}
		var actual any
		if e = json.Unmarshal(session.GetExecutionRequirement(), &actual); e != nil {
			t.Fatal(e)
		}
		normalized, _ := json.Marshal(actual)
		if !bytes.Equal(normalized, canonical) {
			t.Fatal("native denial submitted policy mismatch")
		}
		records, e := client.Replay(t.Context(), observation.Session, 1, 0)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, record := range records {
			event := record.GetEvent()
			call := event.GetTool()
			if event.GetKind() == v1.EventKind_EVENT_TOOL_CALL && event.GetExecutionId() == observation.Execution && call.GetId() == observation.Call && call.GetNative() && call.GetTool() == "http_get" && call.GetArgs().AsMap()["url"] == "http://169.254.17.1:28293/tool" {
				found = true
			}
		}
		if !found {
			t.Fatal("native failure not correlated to this ordinary journal invocation")
		}
		matches++
		evidence = struct {
			Observation nativeFailureObservation
			ActorUID    string
			Session     *v1.Session
			Records     []*v1.LogRecord
		}{observation, row.Labels["ate.actor.uid"], session, records}
	}
	if matches != 1 {
		t.Fatalf("ordinary denial unqualified: expected one correlated native EPERM/EACCES with live context, found %d", matches)
	}
	return evidence
}
func TestNativeFailureObservationRejectsUnrelatedErrors(t *testing.T) {
	good := nativeFailureObservation{true, "session", "execution", "call", int(syscall.EPERM), false}
	if !good.qualifies() {
		t.Fatal("native errno refused")
	}
	for _, o := range []nativeFailureObservation{{true, "session", "execution", "call", 0, false}, {true, "session", "execution", "call", int(syscall.ECONNREFUSED), false}, {true, "session", "execution", "call", int(syscall.EPERM), true}, {true, "", "execution", "call", int(syscall.EPERM), false}, {false, "session", "execution", "call", int(syscall.EPERM), false}} {
		if o.qualifies() {
			t.Fatal("arbitrary error admitted", o)
		}
	}
}
