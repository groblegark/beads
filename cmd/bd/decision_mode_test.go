package main

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// resolveDecisionEnabled tests
// ---------------------------------------------------------------------------

func TestResolveDecisionEnabled_DefaultEnabled(t *testing.T) {
	// Empty metadata → default enabled
	metadata := map[string]interface{}{}
	if !resolveDecisionEnabled(metadata, "") {
		t.Error("expected default enabled=true with empty metadata")
	}
}

func TestResolveDecisionEnabled_GlobalDisabled(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": false,
		},
	}
	if resolveDecisionEnabled(metadata, "") {
		t.Error("expected disabled when global enabled=false")
	}
}

func TestResolveDecisionEnabled_GlobalEnabled(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": true,
		},
	}
	if !resolveDecisionEnabled(metadata, "") {
		t.Error("expected enabled when global enabled=true")
	}
}

func TestResolveDecisionEnabled_AgentOverrideDisabled(t *testing.T) {
	// Global enabled, but agent override disables it
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": true,
			"agent_overrides": map[string]interface{}{
				"bright-lark": false,
			},
		},
	}
	if resolveDecisionEnabled(metadata, "bright-lark") {
		t.Error("expected disabled for bright-lark (agent override)")
	}
}

func TestResolveDecisionEnabled_AgentOverrideEnabled(t *testing.T) {
	// Global disabled, but agent override enables it
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": false,
			"agent_overrides": map[string]interface{}{
				"fair-mare": true,
			},
		},
	}
	if !resolveDecisionEnabled(metadata, "fair-mare") {
		t.Error("expected enabled for fair-mare (agent override)")
	}
}

func TestResolveDecisionEnabled_AgentNoOverrideFallsToGlobal(t *testing.T) {
	// Agent has no override → falls back to global
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": false,
			"agent_overrides": map[string]interface{}{
				"other-agent": true,
			},
		},
	}
	if resolveDecisionEnabled(metadata, "bright-lark") {
		t.Error("expected disabled for bright-lark (no override, global disabled)")
	}
}

func TestResolveDecisionEnabled_EmptyAgentNameUsesGlobal(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": false,
			"agent_overrides": map[string]interface{}{
				"bright-lark": true,
			},
		},
	}
	if resolveDecisionEnabled(metadata, "") {
		t.Error("empty agent name should use global (disabled)")
	}
}

// ---------------------------------------------------------------------------
// setAgentOverride tests
// ---------------------------------------------------------------------------

func TestSetAgentOverride_CreatesMap(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": true,
		},
	}
	setAgentOverride(metadata, "bright-lark", false)

	overrides := getAgentOverrides(metadata)
	if enabled, ok := overrides["bright-lark"]; !ok || enabled {
		t.Errorf("expected bright-lark=false, got ok=%v enabled=%v", ok, enabled)
	}
}

func TestSetAgentOverride_AddsToExisting(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": true,
			"agent_overrides": map[string]interface{}{
				"fair-mare": true,
			},
		},
	}
	setAgentOverride(metadata, "bright-lark", false)

	overrides := getAgentOverrides(metadata)
	if len(overrides) != 2 {
		t.Errorf("expected 2 overrides, got %d", len(overrides))
	}
	if overrides["fair-mare"] != true {
		t.Error("existing override should be preserved")
	}
	if overrides["bright-lark"] != false {
		t.Error("new override should be set")
	}
}

func TestSetAgentOverride_OverwritesExisting(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"agent_overrides": map[string]interface{}{
				"bright-lark": false,
			},
		},
	}
	setAgentOverride(metadata, "bright-lark", true)

	overrides := getAgentOverrides(metadata)
	if !overrides["bright-lark"] {
		t.Error("override should be updated to true")
	}
}

func TestSetAgentOverride_NoStopDecisionSection(t *testing.T) {
	metadata := map[string]interface{}{}
	setAgentOverride(metadata, "bright-lark", false)

	overrides := getAgentOverrides(metadata)
	if _, ok := overrides["bright-lark"]; !ok {
		t.Error("should create stop_decision and agent_overrides")
	}
}

// ---------------------------------------------------------------------------
// clearAgentOverride tests
// ---------------------------------------------------------------------------

func TestClearAgentOverride_RemovesOverride(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"agent_overrides": map[string]interface{}{
				"bright-lark": false,
				"fair-mare":   true,
			},
		},
	}
	clearAgentOverride(metadata, "bright-lark")

	overrides := getAgentOverrides(metadata)
	if _, ok := overrides["bright-lark"]; ok {
		t.Error("bright-lark should be removed")
	}
	if _, ok := overrides["fair-mare"]; !ok {
		t.Error("fair-mare should remain")
	}
}

func TestClearAgentOverride_RemovesMapWhenEmpty(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"agent_overrides": map[string]interface{}{
				"bright-lark": false,
			},
		},
	}
	clearAgentOverride(metadata, "bright-lark")

	sd := metadata["stop_decision"].(map[string]interface{})
	if _, ok := sd["agent_overrides"]; ok {
		t.Error("agent_overrides map should be removed when empty")
	}
}

func TestClearAgentOverride_NoopWhenMissing(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": true,
		},
	}
	// Should not panic
	clearAgentOverride(metadata, "nonexistent")
}

func TestClearAgentOverride_NoopEmptyMetadata(t *testing.T) {
	metadata := map[string]interface{}{}
	// Should not panic
	clearAgentOverride(metadata, "bright-lark")
}

// ---------------------------------------------------------------------------
// getAgentOverrides tests
// ---------------------------------------------------------------------------

func TestGetAgentOverrides_Empty(t *testing.T) {
	metadata := map[string]interface{}{}
	overrides := getAgentOverrides(metadata)
	if len(overrides) != 0 {
		t.Errorf("expected empty overrides, got %d", len(overrides))
	}
}

func TestGetAgentOverrides_IgnoresNonBool(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"agent_overrides": map[string]interface{}{
				"good":    true,
				"bad":     "not a bool",
				"also-ok": false,
			},
		},
	}
	overrides := getAgentOverrides(metadata)
	if len(overrides) != 2 {
		t.Errorf("expected 2 valid overrides, got %d", len(overrides))
	}
}

// ---------------------------------------------------------------------------
// getStopDecisionEnabled tests
// ---------------------------------------------------------------------------

func TestGetStopDecisionEnabled_Default(t *testing.T) {
	if !getStopDecisionEnabled(map[string]interface{}{}) {
		t.Error("default should be enabled")
	}
}

func TestGetStopDecisionEnabled_Explicit(t *testing.T) {
	meta := map[string]interface{}{
		"stop_decision": map[string]interface{}{"enabled": false},
	}
	if getStopDecisionEnabled(meta) {
		t.Error("should be disabled when explicitly set to false")
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip test — ensures metadata survives marshal/unmarshal
// ---------------------------------------------------------------------------

func TestDecisionModeMetadata_JSONRoundTrip(t *testing.T) {
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled":              true,
			"agent_decision_prompt": "test prompt",
		},
	}

	// Set an override
	setAgentOverride(metadata, "bright-lark", false)

	// Marshal to JSON (simulates writing to config bead)
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}

	// Unmarshal back (simulates reading from config bead)
	var restored map[string]interface{}
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}

	// Verify the override survived
	if resolveDecisionEnabled(restored, "bright-lark") {
		t.Error("override should survive JSON round-trip")
	}
	if !resolveDecisionEnabled(restored, "other-agent") {
		t.Error("non-overridden agent should use global (enabled)")
	}

	// Verify prompt survived
	sd := restored["stop_decision"].(map[string]interface{})
	if sd["agent_decision_prompt"] != "test prompt" {
		t.Error("prompt should survive round-trip")
	}
}

// ---------------------------------------------------------------------------
// modeLabel test
// ---------------------------------------------------------------------------

func TestModeLabel(t *testing.T) {
	if modeLabel(true) != "enabled" {
		t.Error("expected 'enabled'")
	}
	if modeLabel(false) != "disabled" {
		t.Error("expected 'disabled'")
	}
}
