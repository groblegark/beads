package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestAutoResolveDecision_Continue(t *testing.T) {
	options := []types.DecisionOption{
		{ID: "go", Label: "Continue working"},
		{ID: "stop", Label: "Done for now"},
	}

	got := autoResolveDecision(options, "continue")
	if got != "go" {
		t.Errorf("continue action: got %q, want %q", got, "go")
	}
}

func TestAutoResolveDecision_ContinueAllStop(t *testing.T) {
	// When all options are stop-like, continue picks the first one.
	options := []types.DecisionOption{
		{ID: "stop", Label: "Stop"},
	}

	got := autoResolveDecision(options, "continue")
	if got != "stop" {
		t.Errorf("continue with only stop: got %q, want %q", got, "stop")
	}
}

func TestAutoResolveDecision_Stop(t *testing.T) {
	options := []types.DecisionOption{
		{ID: "go", Label: "Continue working"},
		{ID: "stop", Label: "Done for now"},
	}

	got := autoResolveDecision(options, "stop")
	if got != "stop" {
		t.Errorf("stop action: got %q, want %q", got, "stop")
	}
}

func TestAutoResolveDecision_StopFallbackToLast(t *testing.T) {
	// No "stop" option — falls back to last option.
	options := []types.DecisionOption{
		{ID: "a", Label: "Option A"},
		{ID: "b", Label: "Option B"},
	}

	got := autoResolveDecision(options, "stop")
	if got != "b" {
		t.Errorf("stop fallback: got %q, want %q", got, "b")
	}
}

func TestAutoResolveDecision_Ask(t *testing.T) {
	options := []types.DecisionOption{
		{ID: "go", Label: "Continue working"},
		{ID: "stop", Label: "Done for now"},
	}

	got := autoResolveDecision(options, "ask")
	if got != "" {
		t.Errorf("ask action: got %q, want empty string", got)
	}
}

func TestAutoResolveDecision_SpecificOptionFound(t *testing.T) {
	options := []types.DecisionOption{
		{ID: "deploy", Label: "Deploy to prod"},
		{ID: "rollback", Label: "Rollback"},
		{ID: "stop", Label: "Done"},
	}

	got := autoResolveDecision(options, "deploy")
	if got != "deploy" {
		t.Errorf("specific option: got %q, want %q", got, "deploy")
	}
}

func TestAutoResolveDecision_SpecificOptionNotFound(t *testing.T) {
	// Falls back to "continue" behavior when specific option isn't found.
	options := []types.DecisionOption{
		{ID: "go", Label: "Continue"},
		{ID: "stop", Label: "Done"},
	}

	got := autoResolveDecision(options, "nonexistent")
	if got != "go" {
		t.Errorf("nonexistent option fallback: got %q, want %q", got, "go")
	}
}

func TestAutoResolveDecision_EmptyOptions(t *testing.T) {
	var options []types.DecisionOption

	tests := []struct {
		action string
		want   string
	}{
		{"continue", ""},
		{"stop", ""},
		{"ask", ""},
		{"specific", ""},
	}

	for _, tt := range tests {
		got := autoResolveDecision(options, tt.action)
		if got != tt.want {
			t.Errorf("empty options with %q: got %q, want %q", tt.action, got, tt.want)
		}
	}
}

func TestAutoResolveDecision_ContinueSkipsStop(t *testing.T) {
	// Stop is first, continue should skip it.
	options := []types.DecisionOption{
		{ID: "stop", Label: "Done for now"},
		{ID: "fix", Label: "Fix the bug"},
		{ID: "refactor", Label: "Refactor"},
	}

	got := autoResolveDecision(options, "continue")
	if got != "fix" {
		t.Errorf("continue skips stop: got %q, want %q", got, "fix")
	}
}

func TestAutoResolveDecision_StopExactMatch(t *testing.T) {
	// Multiple options, stop is in the middle.
	options := []types.DecisionOption{
		{ID: "go", Label: "Continue"},
		{ID: "stop", Label: "Stop now"},
		{ID: "later", Label: "Later"},
	}

	got := autoResolveDecision(options, "stop")
	if got != "stop" {
		t.Errorf("stop exact match: got %q, want %q", got, "stop")
	}
}
