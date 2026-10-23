package agentsessions

import (
	"bytes"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/eventlog"
)

// Host cleanup metadata is not an invocation, activation proof or permission to
// release compute. Accept only this bounded known shape; never a general
// lifecycle/empty-execution exemption. Full wire round-trip and chain checks
// remain in validEvent and the stream reader.
func ownedEvent(e *v1.Event) bool {
	if e == nil || e.Kind != v1.EventKind_EVENT_LIFECYCLE || e.ExecutionId != "" || e.SchemaVersion != 1 {
		return false
	}
	l := e.GetLifecycle()
	return l != nil && l.Kind == v1.Lifecycle_LIFECYCLE_COMPUTE_OWNED &&
		safeIdentity(l.ComputeId) && safeIdentity(l.ComputeRuntime) && safeIdentity(l.ComputeHarness) && safeIdentity(l.ComputeInstance) &&
		len(l.ComputeOperation) == 0 && len(l.ComputeRequirement) > 0 && len(l.ComputeRequirement) <= 1<<20 && l.Detail == "" && l.SnapshotLocal == "" && l.SnapshotExternalUri == "" && !l.SnapshotMemory && !l.SnapshotSealed
}

// Prepared operation bytes remain opaque: this reader validates only the
// host metadata envelope and exact policy association, not backend authority.
func preparedEvent(e *v1.Event) bool {
	if e == nil || e.Kind != v1.EventKind_EVENT_LIFECYCLE || e.ExecutionId != "" || e.SchemaVersion != 1 {
		return false
	}
	l := e.GetLifecycle()
	return l != nil && l.Kind == v1.Lifecycle_LIFECYCLE_COMPUTE_PREPARED && safeIdentity(l.ComputeId) && safeIdentity(l.ComputeHarness) && l.ComputeInstance == "" && l.ComputeRuntime == "" && len(l.ComputeOperation) > 0 && len(l.ComputeOperation) <= 2<<20 && len(l.ComputeRequirement) > 0 && len(l.ComputeRequirement) <= 1<<20 && l.Detail == "" && l.SnapshotLocal == "" && l.SnapshotExternalUri == "" && !l.SnapshotMemory && !l.SnapshotSealed
}

// Return a shape-only invocation view. Original sequences/hashes and the full
// immutable journal are retained for reference replay. No rebasing/rehashing,
// unverified prefix stripping, or cleanup-metadata execution occurs.
func invocationRecords(records []eventlog.Record) ([]eventlog.Record, bool) {
	if len(records) == 0 {
		return records, false
	}
	if records[0].Event.Kind != api.EventLifecycle {
		return records, true
	}

	first := eventlog.RecordToProto(records[0])
	offset := 0
	var prepared *v1.Lifecycle
	if preparedEvent(first.Event) {
		if !validEvent(first.Event) || first.Seq != 1 || first.PrevHash != "" || len(records) < 3 {
			return nil, false
		}
		prepared = first.Event.GetLifecycle()
		offset = 1
	}
	owned := eventlog.RecordToProto(records[offset])
	if !ownedEvent(owned.Event) || !validEvent(owned.Event) || owned.Seq != int64(offset+1) || len(records) < offset+2 {
		return nil, false
	}
	if offset == 0 && owned.PrevHash != "" {
		return nil, false
	}
	l := owned.Event.GetLifecycle()
	if prepared != nil && (l.ComputeId != prepared.ComputeId || l.ComputeHarness != prepared.ComputeHarness || !bytes.Equal(l.ComputeRequirement, prepared.ComputeRequirement)) {
		return nil, false
	}
	start := records[offset+1].Event.ExecutionStart
	if records[offset+1].Event.Kind != api.EventExecutionStart || records[offset+1].Seq != int64(offset+2) || start == nil || start.Harness != l.ComputeHarness || start.ComputeInstance != l.ComputeInstance {
		return nil, false
	}
	return records[offset+1:], true
}
