package gate

import (
	"os"
	"time"
)

// FileBackend stores gate state as filesystem marker files in
// .runtime/gates/<agent>/<gate-id>. This is the legacy backend used
// when no NATS daemon is available. (bd-vecxd)
//
// Note: the agent parameter maps to what was previously sessionID — the
// interface uses agent base name as the key, but for backward compatibility
// we pass it directly as the directory name.
type FileBackend struct {
	workDir string
}

// NewFileBackend creates a gate backend backed by the filesystem.
func NewFileBackend(workDir string) *FileBackend {
	return &FileBackend{workDir: workDir}
}

func (b *FileBackend) Mark(agent, gateID string, _ MarkOpts) error {
	return MarkGate(b.workDir, agent, gateID)
}

func (b *FileBackend) Clear(agent, gateID string) error {
	ClearGate(b.workDir, agent, gateID)
	return nil
}

func (b *FileBackend) ClearAll(agent string) error {
	ClearAllGates(b.workDir, agent)
	return nil
}

func (b *FileBackend) ClearGateAllAgents(gateID string) error {
	ClearGateAllSessions(b.workDir, gateID)
	return nil
}

func (b *FileBackend) IsSatisfied(agent, gateID string) bool {
	return IsGateSatisfied(b.workDir, agent, gateID)
}

func (b *FileBackend) Get(agent, gateID string) *GateEntry {
	path := markerPath(b.workDir, agent, gateID)
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return &GateEntry{
		Satisfied: true,
		Mechanism: "file_marker",
		Timestamp: info.ModTime().Unix(),
	}
}

// Ensure FileBackend implements GateBackend.
var _ GateBackend = (*FileBackend)(nil)

// Ensure NATSBackend implements GateBackend (compile-time check).
// This is here rather than in backend_nats.go to keep the check visible.
var _ GateBackend = (*NATSBackend)(nil)

// ActiveBackend is the currently active gate backend. Defaults to nil (use
// filesystem functions directly). When set, CheckGatesForHook and other
// callers should prefer this over the raw filesystem functions.
//
// Set during daemon startup when NATS is available. (bd-vecxd)
var ActiveBackend GateBackend

// DefaultTTLFor returns the recommended TTL for a gate ID, or 0 if none.
func DefaultTTLFor(gateID string) time.Duration {
	return DefaultTTLs[gateID]
}
