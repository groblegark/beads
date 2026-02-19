package main

import (
	"testing"
)

// TestResolveBaseName_WithRegistry verifies that resolveBaseName uses the
// session registry when available, correctly handling agents with naturally
// numbered names (e.g., "obsidian-3" should NOT be treated as obsidian's
// continuation). (bd-s0x1m)
func TestResolveBaseName_WithRegistry(t *testing.T) {
	// Simulate a session registry
	sessionBaseNamesLoaded = true
	sessionBaseNames = map[string]string{
		"sharp-seal":   "sharp-seal",
		"sharp-seal-1": "sharp-seal",
		"obsidian-3":   "obsidian-3",   // This is the base name — not a suffix!
		"refinery-3":   "refinery-3",   // Same — naturally numbered agent
		"refinery-34":  "refinery-3",   // This IS a continuation of refinery-3
		"witness-2":    "witness-2",    // Naturally numbered
		"mayor-113":    "mayor-113",    // Naturally numbered
		"mayor-114":    "mayor-113",    // Continuation of mayor-113
	}
	defer func() {
		sessionBaseNamesLoaded = false
		sessionBaseNames = nil
	}()

	tests := []struct {
		agent      string
		requestedBy string
		wantMatch  bool
	}{
		// Exact matches
		{"sharp-seal", "sharp-seal", true},
		{"obsidian-3", "obsidian-3", true},

		// Valid continuation matches (same base per registry)
		{"sharp-seal", "sharp-seal-1", true},
		{"sharp-seal-1", "sharp-seal", true},

		// Naturally numbered agents should NOT match their "stripped" form
		{"obsidian-3", "obsidian", false},  // obsidian-3 is its own agent
		{"refinery-3", "refinery", false},  // refinery-3 is its own agent
		{"witness-2", "witness", false},    // witness-2 is its own agent

		// refinery-34 IS a continuation of refinery-3
		{"refinery-3", "refinery-34", true},
		{"refinery-34", "refinery-3", true},

		// mayor-114 IS a continuation of mayor-113
		{"mayor-113", "mayor-114", true},
		{"mayor-114", "mayor-113", true},

		// Different agents entirely
		{"sharp-seal", "obsidian-3", false},
		{"refinery-3", "witness-2", false},
	}

	for _, tt := range tests {
		got := matchesAgentVariant(tt.agent, tt.requestedBy)
		if got != tt.wantMatch {
			t.Errorf("matchesAgentVariant(%q, %q) = %v, want %v",
				tt.agent, tt.requestedBy, got, tt.wantMatch)
		}
	}
}

// TestResolveBaseName_FallbackToRegex verifies that when the session registry
// is unavailable, resolveBaseName falls back to the regex-based AgentBaseName.
// This is the legacy behavior and remains useful for offline/local usage. (bd-s0x1m)
func TestResolveBaseName_FallbackToRegex(t *testing.T) {
	// Ensure registry is empty (simulating daemon unavailable)
	sessionBaseNamesLoaded = true
	sessionBaseNames = nil
	defer func() {
		sessionBaseNamesLoaded = false
		sessionBaseNames = nil
	}()

	// With no registry, falls back to regex stripping
	tests := []struct {
		agent      string
		requestedBy string
		wantMatch  bool
	}{
		{"sharp-seal", "sharp-seal-1", true},   // regex strips -1
		{"sharp-seal-1", "sharp-seal", true},   // regex strips -1
		{"obsidian-3", "obsidian", true},        // BUG: regex incorrectly strips -3 (known limitation)
	}

	for _, tt := range tests {
		got := matchesAgentVariant(tt.agent, tt.requestedBy)
		if got != tt.wantMatch {
			t.Errorf("matchesAgentVariant(%q, %q) = %v, want %v (fallback mode)",
				tt.agent, tt.requestedBy, got, tt.wantMatch)
		}
	}
}
