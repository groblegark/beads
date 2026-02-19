// Package gate implements session-level gates for Claude Code hook events.
//
// This is Layer 2 in the three-layer gate architecture:
//   - Layer 1: Formula gates (internal/formula/) — per-step in a molecule
//   - Layer 2: Session gates (this package) — per-hook-event in a session
//   - Layer 3: Config bead policies — per-scope policy
//
// Session gates use a file-marker system in .runtime/gates/<session-id>/
// to track which gates have been satisfied during a Claude Code session.
package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HookType represents a Claude Code hook event type.
type HookType string

const (
	HookStop             HookType = "Stop"
	HookPreToolUse       HookType = "PreToolUse"
	HookUserPromptSubmit HookType = "UserPromptSubmit"
	HookPreCompact       HookType = "PreCompact"
)

// ValidHookTypes returns all valid hook types.
func ValidHookTypes() []HookType {
	return []HookType{HookStop, HookPreToolUse, HookUserPromptSubmit, HookPreCompact}
}

// ParseHookType parses a string into a HookType, case-insensitive.
func ParseHookType(s string) (HookType, error) {
	lower := strings.ToLower(s)
	for _, h := range ValidHookTypes() {
		if strings.ToLower(string(h)) == lower {
			return h, nil
		}
	}
	return "", fmt.Errorf("unknown hook type %q (valid: Stop, PreToolUse, UserPromptSubmit, PreCompact)", s)
}

// GateMode determines whether a gate blocks or warns.
type GateMode string

const (
	GateModeStrict GateMode = "strict" // Block the hook event
	GateModeSoft   GateMode = "soft"   // Warn but allow
)

// GateContext provides runtime context for gate auto-check functions.
type GateContext struct {
	SessionID          string
	TerminalSessionID  string // TERM_SESSION_ID — stable across session rotations
	HookType           HookType
	WorkDir            string
	HookBead           string // current hooked bead if any
	Role               string // GT_ROLE
	ToolInput          string // for PreToolUse: the command being executed
}

// Gate defines a session-level gate that can block or warn on a hook event.
type Gate struct {
	ID          string                    // e.g., "decision", "commit-push", "destructive-op"
	Hook        HookType                  // which hook this gate applies to
	Description string                    // human-readable purpose
	Mode        GateMode                  // strict (block) or soft (warn)
	AutoCheck   func(ctx GateContext) bool // optional: returns true if gate is auto-satisfied
	Hint        string                    // how to satisfy this gate
}

// GateResult holds the outcome of checking a single gate.
type GateResult struct {
	GateID    string   `json:"gate_id"`
	Hook      HookType `json:"hook"`
	Satisfied bool     `json:"satisfied"`
	Mode      GateMode `json:"mode"`
	Message   string   `json:"message,omitempty"`
	Hint      string   `json:"hint,omitempty"`
}

// CheckResponse is the JSON response for a hook gate check.
type CheckResponse struct {
	Decision string       `json:"decision"`          // "allow" or "block"
	Reason   string       `json:"reason,omitempty"`   // why blocked
	Results  []GateResult `json:"results,omitempty"`  // per-gate details
	Warnings []string     `json:"warnings,omitempty"` // soft gate warnings
}

// runtimeGatesDir returns the path to the gates runtime directory.
// Layout: <workdir>/.runtime/gates/<session-id>/
func runtimeGatesDir(workDir, sessionID string) string {
	return filepath.Join(workDir, ".runtime", "gates", sessionID)
}

// markerPath returns the path to a gate marker file.
func markerPath(workDir, sessionID, gateID string) string {
	return filepath.Join(runtimeGatesDir(workDir, sessionID), gateID)
}

// MarkGate marks a gate as satisfied for the given session.
func MarkGate(workDir, sessionID, gateID string) error {
	dir := runtimeGatesDir(workDir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating gates dir: %w", err)
	}
	f, err := os.Create(markerPath(workDir, sessionID, gateID))
	if err != nil {
		return fmt.Errorf("marking gate %s: %w", gateID, err)
	}
	return f.Close()
}

// ClearGate removes a gate marker for the given session.
func ClearGate(workDir, sessionID, gateID string) {
	_ = os.Remove(markerPath(workDir, sessionID, gateID))
}

// ClearGatesForHook clears all gate markers for a specific hook type.
// It only removes markers for gates that are registered for that hook.
func ClearGatesForHook(workDir, sessionID string, hookType HookType, reg *Registry) {
	gates := reg.GatesForHook(hookType)
	for _, g := range gates {
		ClearGate(workDir, sessionID, g.ID)
	}
}

// ClearAllGates removes all gate markers for the given session.
func ClearAllGates(workDir, sessionID string) {
	dir := runtimeGatesDir(workDir, sessionID)
	_ = os.RemoveAll(dir)
}

// IsGateSatisfied checks whether a gate marker exists for the given session.
func IsGateSatisfied(workDir, sessionID, gateID string) bool {
	_, err := os.Stat(markerPath(workDir, sessionID, gateID))
	return err == nil
}

// ClearGateAllSessions removes a gate marker from ALL session directories.
// Used to clear stale markers that would otherwise satisfy the gate via
// the cross-session fallback in CheckGatesForHook.
func ClearGateAllSessions(workDir, gateID string) {
	gatesDir := filepath.Join(workDir, ".runtime", "gates")
	entries, err := os.ReadDir(gatesDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(gatesDir, entry.Name(), gateID))
	}
}

// IsGateSatisfiedAnySession checks whether a gate marker exists in ANY session
// directory under .runtime/gates/. This handles Claude Code session_id rotation
// (which occurs on compaction, continuation, or context recovery) — the marker
// may have been written with a previous session_id that's no longer current.
//
// Only considers markers younger than 12 hours to avoid stale markers from
// old sessions incorrectly satisfying gates in new ones. (bd-02qeb)
//
// WARNING: In multi-agent setups, this can leak across agents — one agent's
// marker satisfies another agent's gate. Prefer IsGateSatisfiedSameTerminal
// when a TerminalSessionID is available. (bd-gh0ik)
func IsGateSatisfiedAnySession(workDir, gateID string) bool {
	gatesDir := filepath.Join(workDir, ".runtime", "gates")
	entries, err := os.ReadDir(gatesDir)
	if err != nil {
		return false
	}
	cutoff := time.Now().Add(-12 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		marker := filepath.Join(gatesDir, entry.Name(), gateID)
		info, err := os.Stat(marker)
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			return true
		}
	}
	return false
}

// IsGateSatisfiedSameTerminal checks whether a gate marker exists in any session
// directory that belongs to the same terminal (TERM_SESSION_ID). This scopes the
// cross-session fallback to only the calling agent's own session lineage, preventing
// one agent's markers from satisfying another agent's gates. (bd-gh0ik)
//
// Falls back to IsGateSatisfiedAnySession if termSessionID is empty (backward compat).
func IsGateSatisfiedSameTerminal(workDir, gateID, termSessionID string) bool {
	if termSessionID == "" {
		return IsGateSatisfiedAnySession(workDir, gateID)
	}

	// Build the set of CLAUDE_SESSION_IDs that belong to this terminal.
	// Reverse mapping: session-terminal/<CLAUDE_SESSION_ID> contains TERM_SESSION_ID.
	termDir := filepath.Join(workDir, ".runtime", "session-terminal")
	entries, err := os.ReadDir(termDir)
	if err != nil {
		return false
	}

	ownSessions := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(termDir, entry.Name())) //nolint:gosec // G304: path from controlled .runtime/ directory
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == termSessionID {
			ownSessions[entry.Name()] = true
		}
	}

	if len(ownSessions) == 0 {
		return false
	}

	cutoff := time.Now().Add(-12 * time.Hour)
	gatesDir := filepath.Join(workDir, ".runtime", "gates")
	for sessionID := range ownSessions {
		marker := filepath.Join(gatesDir, sessionID, gateID)
		info, err := os.Stat(marker)
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			return true
		}
	}
	return false
}

// WriteSessionTerminal writes a reverse mapping from CLAUDE_SESSION_ID to
// TERM_SESSION_ID so that cross-session gate checks can be scoped to the
// same terminal/agent. (bd-gh0ik)
func WriteSessionTerminal(workDir, claudeSessionID, termSessionID string) error {
	dir := filepath.Join(workDir, ".runtime", "session-terminal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, claudeSessionID), []byte(termSessionID), 0o600)
}

// CheckGatesForHook evaluates all registered gates for the specified hook type.
// A gate is satisfied if:
//  1. Its marker file exists, OR
//  2. Its AutoCheck function returns true (and the marker is set automatically)
//
// The optional terminalSessionID scopes cross-session fallback checks to the
// same terminal/agent, preventing cross-agent gate leakage. (bd-gh0ik)
func CheckGatesForHook(workDir, sessionID string, hookType HookType, reg *Registry, terminalSessionID ...string) ([]GateResult, error) {
	gates := reg.GatesForHook(hookType)
	results := make([]GateResult, 0, len(gates))

	var termSID string
	if len(terminalSessionID) > 0 {
		termSID = terminalSessionID[0]
	}

	ctx := GateContext{
		SessionID:         sessionID,
		TerminalSessionID: termSID,
		HookType:          hookType,
		WorkDir:           workDir,
	}

	for _, g := range gates {
		result := GateResult{
			GateID: g.ID,
			Hook:   g.Hook,
			Mode:   g.Mode,
			Hint:   g.Hint,
		}

		// Check marker first — exact session match
		if IsGateSatisfied(workDir, sessionID, g.ID) {
			result.Satisfied = true
			result.Message = "marked satisfied"
			results = append(results, result)
			continue
		}

		// Fallback: check if the marker exists in a session directory belonging
		// to the same terminal/agent. Claude Code rotates session_id on
		// compaction/continuation, so the marker may have been written under a
		// previous session_id. Scoped to same terminal to prevent cross-agent
		// leakage. (bd-02qeb, bd-gh0ik)
		if IsGateSatisfiedSameTerminal(workDir, g.ID, ctx.TerminalSessionID) {
			result.Satisfied = true
			result.Message = "marked satisfied (cross-session)"
			results = append(results, result)
			continue
		}

		// Try auto-check
		if g.AutoCheck != nil && g.AutoCheck(ctx) {
			// Auto-satisfied — set the marker for future checks
			if err := MarkGate(workDir, sessionID, g.ID); err != nil {
				return nil, fmt.Errorf("auto-marking gate %s: %w", g.ID, err)
			}
			result.Satisfied = true
			result.Message = "auto-satisfied"
			results = append(results, result)
			continue
		}

		// Not satisfied
		result.Satisfied = false
		result.Message = g.Description
		results = append(results, result)
	}

	return results, nil
}

// EvaluateHook checks all gates for a hook type and returns a CheckResponse.
// If any strict gate is unsatisfied, the decision is "block".
// Soft unsatisfied gates produce warnings but allow the hook.
func EvaluateHook(workDir, sessionID string, hookType HookType, reg *Registry, terminalSessionID ...string) (*CheckResponse, error) {
	results, err := CheckGatesForHook(workDir, sessionID, hookType, reg, terminalSessionID...)
	if err != nil {
		return nil, err
	}

	resp := &CheckResponse{
		Decision: "allow",
		Results:  results,
	}

	var blockReasons []string
	for _, r := range results {
		if r.Satisfied {
			continue
		}
		switch r.Mode {
		case GateModeStrict:
			blockReasons = append(blockReasons, fmt.Sprintf("%s: %s", r.GateID, r.Message))
		case GateModeSoft:
			warning := fmt.Sprintf("%s: %s", r.GateID, r.Message)
			if r.Hint != "" {
				warning += fmt.Sprintf(" (hint: %s)", r.Hint)
			}
			resp.Warnings = append(resp.Warnings, warning)
		}
	}

	if len(blockReasons) > 0 {
		resp.Decision = "block"
		resp.Reason = strings.Join(blockReasons, "; ")
	}

	return resp, nil
}
