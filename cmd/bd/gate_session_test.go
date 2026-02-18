package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/gate"
)

func TestGetSessionID(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "test-session-xyz")
	if got := getSessionID(); got != "test-session-xyz" {
		t.Errorf("getSessionID() = %q, want %q", got, "test-session-xyz")
	}

	t.Setenv("CLAUDE_SESSION_ID", "")
	if got := getSessionID(); got != "" {
		t.Errorf("getSessionID() with empty env = %q, want empty", got)
	}
}

func TestSoftCopyRegistry(t *testing.T) {
	reg := gate.NewRegistry()
	_ = reg.Register(&gate.Gate{ID: "strict-stop", Hook: gate.HookStop, Mode: gate.GateModeStrict})
	_ = reg.Register(&gate.Gate{ID: "strict-tool", Hook: gate.HookPreToolUse, Mode: gate.GateModeStrict})

	softReg := softCopyRegistry(reg, gate.HookStop)

	// Stop gate should be soft in the copy
	stopGates := softReg.GatesForHook(gate.HookStop)
	if len(stopGates) != 1 {
		t.Fatalf("expected 1 stop gate, got %d", len(stopGates))
	}
	if stopGates[0].Mode != gate.GateModeSoft {
		t.Errorf("expected soft mode for Stop gate, got %s", stopGates[0].Mode)
	}

	// PreToolUse gate should still be strict
	toolGates := softReg.GatesForHook(gate.HookPreToolUse)
	if len(toolGates) != 1 {
		t.Fatalf("expected 1 tool gate, got %d", len(toolGates))
	}
	if toolGates[0].Mode != gate.GateModeStrict {
		t.Errorf("expected strict mode for PreToolUse gate, got %s", toolGates[0].Mode)
	}

	// Original should be unchanged
	origStopGates := reg.GatesForHook(gate.HookStop)
	if origStopGates[0].Mode != gate.GateModeStrict {
		t.Error("original registry should not be modified")
	}
}

func TestFormatGateResults(t *testing.T) {
	results := []gate.GateResult{
		{GateID: "decision", Satisfied: true},
		{GateID: "commit-push", Satisfied: false},
	}

	got := formatGateResults(results)
	if got != "● decision, ○ commit-push" {
		t.Errorf("formatGateResults = %q, want %q", got, "● decision, ○ commit-push")
	}
}

func TestGateMarkAndStatusIntegration(t *testing.T) {
	workDir := t.TempDir()
	sessionID := "integration-test-1"

	// Mark a gate
	if err := gate.MarkGate(workDir, sessionID, "test-gate"); err != nil {
		t.Fatalf("MarkGate failed: %v", err)
	}

	// Verify marker file exists
	markerFile := filepath.Join(workDir, ".runtime", "gates", sessionID, "test-gate")
	if _, err := os.Stat(markerFile); err != nil {
		t.Errorf("marker file should exist: %v", err)
	}

	// Verify satisfaction
	if !gate.IsGateSatisfied(workDir, sessionID, "test-gate") {
		t.Error("gate should be satisfied after marking")
	}

	// Clear it
	gate.ClearGate(workDir, sessionID, "test-gate")

	if gate.IsGateSatisfied(workDir, sessionID, "test-gate") {
		t.Error("gate should not be satisfied after clearing")
	}
}

func TestSessionGateCheckIntegration(t *testing.T) {
	workDir := t.TempDir()
	sessionID := "integration-test-2"

	reg := gate.NewRegistry()
	_ = reg.Register(&gate.Gate{
		ID:          "test-strict",
		Hook:        gate.HookStop,
		Description: "strict gate",
		Mode:        gate.GateModeStrict,
		Hint:        "satisfy this gate",
	})
	_ = reg.Register(&gate.Gate{
		ID:          "test-soft",
		Hook:        gate.HookStop,
		Description: "soft gate",
		Mode:        gate.GateModeSoft,
	})

	// Check before any marking — should block
	resp, err := gate.EvaluateHook(workDir, sessionID, gate.HookStop, reg)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != "block" {
		t.Errorf("expected block with unsatisfied strict gate, got %q", resp.Decision)
	}

	// Mark the strict gate
	if err := gate.MarkGate(workDir, sessionID, "test-strict"); err != nil {
		t.Fatal(err)
	}

	// Check again — should allow (soft gate just warns)
	resp, err = gate.EvaluateHook(workDir, sessionID, gate.HookStop, reg)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Decision != "allow" {
		t.Errorf("expected allow with strict gate satisfied, got %q", resp.Decision)
	}
	if len(resp.Warnings) != 1 {
		t.Errorf("expected 1 warning for unsatisfied soft gate, got %d", len(resp.Warnings))
	}
}

// ---------------------------------------------------------------------------
// --session flag tests (bd-2q8d8)
// ---------------------------------------------------------------------------

func TestSessionFlagOverridesEnvVar(t *testing.T) {
	// When both --session flag and CLAUDE_SESSION_ID are set,
	// the flag should take priority.
	t.Setenv("CLAUDE_SESSION_ID", "env-session")

	// getSessionID reads from env
	envID := getSessionID()
	if envID != "env-session" {
		t.Errorf("getSessionID() = %q, want 'env-session'", envID)
	}

	// Simulate flag taking priority (this is the logic in the command handler)
	flagSession := "flag-session"
	sessionID := flagSession
	if sessionID == "" {
		sessionID = getSessionID()
	}
	if sessionID != "flag-session" {
		t.Errorf("flag should override env: got %q, want 'flag-session'", sessionID)
	}
}

func TestSessionFlagFallsBackToEnv(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "env-session")

	// Simulate empty flag — should fall back to env
	flagSession := ""
	sessionID := flagSession
	if sessionID == "" {
		sessionID = getSessionID()
	}
	if sessionID != "env-session" {
		t.Errorf("should fall back to env: got %q, want 'env-session'", sessionID)
	}
}

func TestSessionFlagAndEnvBothEmpty(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "")

	flagSession := ""
	sessionID := flagSession
	if sessionID == "" {
		sessionID = getSessionID()
	}
	if sessionID != "" {
		t.Errorf("both empty should yield empty: got %q", sessionID)
	}
}

// ---------------------------------------------------------------------------
// loadGatePromptFromConfig tests (bd-4onc1)
// ---------------------------------------------------------------------------

func TestLoadGatePromptFromConfig_NoDaemon(t *testing.T) {
	// When daemonClient is nil, should return empty string (fail-open)
	oldClient := daemonClient
	daemonClient = nil
	defer func() { daemonClient = oldClient }()

	prompt := loadGatePromptFromConfig()
	if prompt != "" {
		t.Errorf("expected empty prompt with nil daemon, got %q", prompt)
	}
}

func TestLoadGatePromptFromConfig_DaemonRPCFails(t *testing.T) {
	// When daemonClient is non-nil but List fails (no daemon running),
	// loadGatePromptFromConfig should return empty (fail-open).
	// We can't easily unit-test this without a real daemon, so this
	// test documents the expected behavior. The E2E test (bd-cqbc0)
	// covers the full flow on the cluster.
	//
	// The key contract: loadGatePromptFromConfig never panics and
	// always returns "" on any error.
}

func TestLoadGatePromptFromConfig_ParsesPromptFromMetadata(t *testing.T) {
	// Test the metadata parsing logic directly
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"agent_decision_prompt": "Test wrap-up prompt",
			"enabled":              true,
		},
	}
	metaJSON, _ := json.Marshal(metadata)

	// Simulate the parsing logic from loadGatePromptFromConfig
	var data map[string]interface{}
	if err := json.Unmarshal(metaJSON, &data); err != nil {
		t.Fatal(err)
	}

	var prompt string
	if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
		if p, ok := sd["agent_decision_prompt"].(string); ok && p != "" {
			prompt = p
		}
	}

	if prompt != "Test wrap-up prompt" {
		t.Errorf("expected 'Test wrap-up prompt', got %q", prompt)
	}
}

func TestLoadGatePrompt_GlobalDisabledReturnsEmpty(t *testing.T) {
	// When enabled=false, the gate should return empty (allow through)
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"agent_decision_prompt": "Should not see this",
			"enabled":              false,
		},
	}
	metaJSON, _ := json.Marshal(metadata)

	var data map[string]interface{}
	json.Unmarshal(metaJSON, &data)

	var enabled *bool
	if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
		if e, ok := sd["enabled"].(bool); ok {
			enabled = &e
		}
	}

	if enabled == nil || *enabled {
		t.Error("enabled should be false")
	}
}

func TestLoadGatePrompt_AgentOverrideDisablesWhenGlobalEnabled(t *testing.T) {
	// Global enabled, agent override disables — should return empty
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"agent_decision_prompt": "Checkpoint prompt",
			"enabled":              true,
			"agent_overrides": map[string]interface{}{
				"bright-lark": false,
			},
		},
	}
	metaJSON, _ := json.Marshal(metadata)

	var data map[string]interface{}
	json.Unmarshal(metaJSON, &data)

	// Simulate the gate check logic
	globalEnabled := true
	if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
		if e, ok := sd["enabled"].(bool); ok {
			globalEnabled = e
		}
	}

	effectiveEnabled := globalEnabled
	if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
		if overrides, ok := sd["agent_overrides"].(map[string]interface{}); ok {
			if agentEnabled, ok := overrides["bright-lark"].(bool); ok {
				effectiveEnabled = agentEnabled
			}
		}
	}

	if effectiveEnabled {
		t.Error("agent override should disable checkpoints for bright-lark")
	}
}

func TestLoadGatePrompt_AgentOverrideEnablesWhenGlobalDisabled(t *testing.T) {
	// Global disabled, agent override enables — should return prompt
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"agent_decision_prompt": "Checkpoint prompt",
			"enabled":              false,
			"agent_overrides": map[string]interface{}{
				"fair-mare": true,
			},
		},
	}
	metaJSON, _ := json.Marshal(metadata)

	var data map[string]interface{}
	json.Unmarshal(metaJSON, &data)

	globalEnabled := true
	if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
		if e, ok := sd["enabled"].(bool); ok {
			globalEnabled = e
		}
	}

	effectiveEnabled := globalEnabled
	if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
		if overrides, ok := sd["agent_overrides"].(map[string]interface{}); ok {
			if agentEnabled, ok := overrides["fair-mare"].(bool); ok {
				effectiveEnabled = agentEnabled
			}
		}
	}

	if !effectiveEnabled {
		t.Error("agent override should enable checkpoints for fair-mare")
	}
}

func TestLoadGatePromptFromConfig_MissingPromptField(t *testing.T) {
	// Metadata without agent_decision_prompt should return empty
	metadata := map[string]interface{}{
		"stop_decision": map[string]interface{}{
			"enabled": true,
		},
	}
	metaJSON, _ := json.Marshal(metadata)

	var data map[string]interface{}
	json.Unmarshal(metaJSON, &data)

	var prompt string
	if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
		if p, ok := sd["agent_decision_prompt"].(string); ok && p != "" {
			prompt = p
		}
	}

	if prompt != "" {
		t.Errorf("expected empty prompt when field missing, got %q", prompt)
	}
}

func TestLoadGatePromptFromConfig_NoStopDecisionSection(t *testing.T) {
	// Metadata without stop_decision section should return empty
	metadata := map[string]interface{}{
		"hooks": map[string]interface{}{
			"some_other_config": true,
		},
	}
	metaJSON, _ := json.Marshal(metadata)

	var data map[string]interface{}
	json.Unmarshal(metaJSON, &data)

	var prompt string
	if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
		if p, ok := sd["agent_decision_prompt"].(string); ok && p != "" {
			prompt = p
		}
	}

	if prompt != "" {
		t.Errorf("expected empty prompt without stop_decision section, got %q", prompt)
	}
}

func TestLoadGatePromptFromConfig_LastBeadWins(t *testing.T) {
	// When multiple config beads exist, the last one's prompt should win
	// (simulating the merge-by-specificity behavior)
	prompts := []string{"first prompt", "second prompt", "final prompt"}

	var winner string
	for _, p := range prompts {
		if p != "" {
			winner = p
		}
	}

	if winner != "final prompt" {
		t.Errorf("expected last prompt to win, got %q", winner)
	}
}
