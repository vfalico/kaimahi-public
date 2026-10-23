package agentsessions

import (
	"bytes"
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"testing"
	"time"
)

// Real gRPC adapter; fake remote host, not an enforcement qualification.
func TestSuiteRequirementExactCarriageAndEchoGate(t *testing.T) {
	for _, scenario := range []string{"exact", "omitted-create", "changed-create", "changed-stream", "omitted-input"} {
		t.Run(scenario, func(t *testing.T) {
			req := suiteRequest()
			req.ExecutionRequirement = []byte(`{"opaque":{"integer":9007199254740993}}`)
			s := suiteServerForRequest(t, req)
			setSessionRequirement(s.created, req.ExecutionRequirement)
			setSessionRequirement(s.updates[0].GetSession(), req.ExecutionRequirement)
			switch scenario {
			case "omitted-create":
				s.created.ProtoReflect().SetUnknown(nil)
				setSessionRequirement(s.created, nil)
			case "changed-create":
				s.created.ProtoReflect().SetUnknown(nil)
				setSessionRequirement(s.created, []byte(`{}`))
			case "changed-stream":
				s.updates[0].GetSession().ProtoReflect().SetUnknown(nil)
				setSessionRequirement(s.updates[0].GetSession(), []byte(`{}`))
			case "omitted-input":
				req.ExecutionRequirement = nil
			}
			c, err := Dial(Options{Address: serve(t, s, nil)})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			_, err = c.RunSuiteCase(ctx, req)
			if !bytes.Equal(req.ExecutionRequirement, sessionRequirement(s.createRequest.GetSession())) {
				t.Fatal("adapter altered exact requirement")
			}
			_, execs := s.counts()
			if scenario == "exact" {
				if err != nil || execs != 1 {
					t.Fatal(err, execs)
				}
			} else {
				if err == nil {
					t.Fatal("requirement mismatch was accepted")
				}
				if scenario != "changed-stream" && execs != 0 {
					t.Fatal("creation mismatch dispatched Exec")
				}
				if scenario == "changed-stream" && status.Code(err) != codes.DataLoss {
					t.Fatal(err)
				}
			}
		})
	}
}

// Malformed wire must not compare equal to either an omitted requirement or
// attacker-chosen error-looking payload. Invalidity is out-of-band, not bytes.
func TestRequirementWireRefusesAmbiguity(t *testing.T) {
	for _, raw := range [][]byte{{0x92, 0x01, 0xff}, protowire.AppendTag(nil, 18, protowire.VarintType), append(protowire.AppendBytes(protowire.AppendTag(nil, 18, protowire.BytesType), []byte("a")), protowire.AppendBytes(protowire.AppendTag(nil, 18, protowire.BytesType), []byte("b"))...)} {
		s := suiteServer(t).created
		s.ProtoReflect().SetUnknown(raw)
		// Only the historical descriptor represents field 18 as unknown. Current
		// generated descriptors decode duplicate fields with ordinary proto rules.
		if s.ProtoReflect().Descriptor().Fields().ByNumber(18) != nil {
			continue
		}
		for _, expected := range [][]byte{nil, []byte("invalid requirement wire"), []byte("ambiguous requirement wire")} {
			if matchesSessionRequirement(s, expected) {
				t.Fatal("malformed authority matched", raw)
			}
		}
	}
}

func TestGovernedSuiteRejectsHostModelEvenWithExactEcho(t *testing.T) {
	req := suiteRequest()
	// Build an authentic host-mediated journal first; echo authority afterward.
	remote := suiteServerForRequest(t, req)
	req.ExecutionRequirement = []byte(`{"opaque":true}`)
	setSessionRequirement(remote.created, req.ExecutionRequirement)
	setSessionRequirement(remote.updates[0].GetSession(), req.ExecutionRequirement)
	_, err := runSuite(t, remote, req)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatal("host-mediated execution accepted under governed echo", err)
	}
}
