// Package gate — backend.go defines the GateBackend interface for pluggable
// gate satisfaction storage. The default implementation uses filesystem markers;
// the NATS KV implementation provides daemon-authoritative state with TTLs. (bd-vecxd)
package gate

import "time"

// GateEntry represents the satisfaction state of a single gate.
type GateEntry struct {
	Satisfied bool   `json:"satisfied"`
	Mechanism string `json:"mechanism"` // "decision_create", "auto_check", "manual", etc.
	Actor     string `json:"actor"`     // agent that satisfied the gate
	SessionID string `json:"session_id"`
	Timestamp int64  `json:"ts"`
	TTL       int    `json:"ttl_seconds,omitempty"` // 0 = no expiration
}

// MarkOpts configures gate marking.
type MarkOpts struct {
	Mechanism string        // what caused the gate to be satisfied
	Actor     string        // agent name
	SessionID string        // Claude session ID
	TTL       time.Duration // 0 = no TTL (default for file backend)
}

// GateBackend abstracts gate state storage. Implementations include:
//   - FileBackend: current filesystem marker approach (.runtime/gates/)
//   - NATSBackend: NATS KV bucket with TTLs and audit trail
type GateBackend interface {
	// Mark marks a gate as satisfied for the given agent.
	Mark(agent, gateID string, opts MarkOpts) error

	// Clear clears a gate for the given agent.
	Clear(agent, gateID string) error

	// ClearAll clears all gates for the given agent.
	ClearAll(agent string) error

	// ClearGateAllAgents clears a specific gate across ALL agents.
	ClearGateAllAgents(gateID string) error

	// IsSatisfied checks whether a gate is satisfied for the given agent.
	IsSatisfied(agent, gateID string) bool

	// Get returns the full gate entry, or nil if not satisfied.
	Get(agent, gateID string) *GateEntry
}

// DefaultTTLs maps gate IDs to their recommended TTL durations.
// Used by the NATS backend to set per-key expiration. (bd-vecxd)
var DefaultTTLs = map[string]time.Duration{
	"decision":         5 * time.Minute,  // must be refreshed by active decision cycle
	"decision-waiting": 5 * time.Minute,  // active decision blocking wait
	"commit-push":      1 * time.Hour,    // stays satisfied after git push
	"bead-update":      1 * time.Hour,    // stays satisfied after bead update
	"destructive-op":   1 * time.Minute,  // per-command approval
	"sandbox-boundary": 1 * time.Minute,  // per-command approval
	"state-checkpoint": 5 * time.Minute,  // valid for current compaction cycle
	"dirty-work":       1 * time.Hour,    // stays satisfied after git commit
	"mol-gate-pending": 30 * time.Minute, // stays valid while mol is active
	"context-injection": 5 * time.Minute,
	"stale-context":     5 * time.Minute,
}
