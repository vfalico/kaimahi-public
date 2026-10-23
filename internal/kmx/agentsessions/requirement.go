package agentsessions

import (
	"bytes"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Session field 18 is the additive execution_requirement bytes field in the
// current owning Sessions API. Preserve the existing generated release pin:
// unknown protobuf fields carry the same wire contract to a current server.
// An old server that drops it cannot pass the exact echo gate. No new policy
// default, server capability or successful enforcement is inferred here.
const requirementField protoreflect.FieldNumber = 18

func setSessionRequirement(s *v1.Session, raw []byte) {
	m := s.ProtoReflect()
	if f := m.Descriptor().Fields().ByNumber(requirementField); f != nil {
		m.Set(f, protoreflect.ValueOfBytes(append([]byte(nil), raw...)))
		return
	}
	if len(raw) == 0 {
		return
	}
	unknown := append([]byte(nil), m.GetUnknown()...)
	unknown = protowire.AppendTag(unknown, protowire.Number(requirementField), protowire.BytesType)
	unknown = protowire.AppendBytes(unknown, raw)
	m.SetUnknown(unknown)
}
func sessionRequirement(s *v1.Session) []byte {
	value, _ := parseSessionRequirement(s)
	return value
}
func matchesSessionRequirement(s *v1.Session, expected []byte) bool {
	value, valid := parseSessionRequirement(s)
	return valid && bytes.Equal(value, expected)
}
func parseSessionRequirement(s *v1.Session) ([]byte, bool) {
	if s == nil {
		return nil, true
	}
	m := s.ProtoReflect()
	if f := m.Descriptor().Fields().ByNumber(requirementField); f != nil {
		return m.Get(f).Bytes(), true
	}
	var value []byte
	found := false
	for raw := m.GetUnknown(); len(raw) > 0; {
		n, kind, count := protowire.ConsumeTag(raw)
		if count < 0 {
			return nil, false
		}
		raw = raw[count:]
		if n == protowire.Number(requirementField) {
			if kind != protowire.BytesType || found {
				return nil, false
			}
			b, size := protowire.ConsumeBytes(raw)
			if size < 0 {
				return nil, false
			}
			value = b
			found = true
			raw = raw[size:]
		} else {
			size := protowire.ConsumeFieldValue(n, kind, raw)
			if size < 0 {
				return nil, false
			}
			raw = raw[size:]
		}
	}
	return value, true
}
