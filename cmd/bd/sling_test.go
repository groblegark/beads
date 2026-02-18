package main

import (
	"strings"
	"testing"
)

func TestGenerateSpawnName_Format(t *testing.T) {
	name := generateSpawnName()
	parts := strings.SplitN(name, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("expected adjective-noun format, got %q", name)
	}
	if parts[0] == "" || parts[1] == "" {
		t.Errorf("empty part in name %q", name)
	}
}

func TestGenerateSpawnName_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		name := generateSpawnName()
		if seen[name] {
			t.Errorf("duplicate name %q after %d generations", name, i)
		}
		seen[name] = true
	}
}

func TestFormatSlingMessage_Basic(t *testing.T) {
	msg := formatSlingMessage("bd-abc", "Fix the bug", "bright-lark", "")
	if !strings.Contains(msg, "WORK: Fix the bug") {
		t.Error("missing title line")
	}
	if !strings.Contains(msg, "Bead: bd-abc") {
		t.Error("missing bead ID")
	}
	if !strings.Contains(msg, "Dispatched by: bright-lark") {
		t.Error("missing dispatcher")
	}
	if !strings.Contains(msg, "bd show bd-abc") {
		t.Error("missing show command hint")
	}
	if strings.Contains(msg, "Context:") {
		t.Error("should not include context when extraArgs is empty")
	}
}

func TestFormatSlingMessage_WithArgs(t *testing.T) {
	msg := formatSlingMessage("bd-xyz", "Add feature", "swift-fox", "focus on error handling")
	if !strings.Contains(msg, "Context: focus on error handling") {
		t.Error("missing extra args context")
	}
}

func TestFormatSpawnPrompt_Basic(t *testing.T) {
	prompt := formatSpawnPrompt("bd-abc", "Fix the bug", "")
	if !strings.Contains(prompt, "bd-abc") {
		t.Error("missing bead ID")
	}
	if !strings.Contains(prompt, "Fix the bug") {
		t.Error("missing title")
	}
	if !strings.Contains(prompt, "bd show bd-abc") {
		t.Error("missing show command")
	}
	if !strings.Contains(prompt, "bd update bd-abc --status=in_progress") {
		t.Error("missing claim command")
	}
	if !strings.Contains(prompt, "bd close bd-abc") {
		t.Error("missing close command")
	}
	if strings.Contains(prompt, "Context:") {
		t.Error("should not include context when extraArgs is empty")
	}
}

func TestFormatSpawnPrompt_WithArgs(t *testing.T) {
	prompt := formatSpawnPrompt("bd-xyz", "Add feature", "focus on the API layer")
	if !strings.Contains(prompt, "Context: focus on the API layer") {
		t.Error("missing extra args context")
	}
}

func TestFormatSlingMessage_SpecialChars(t *testing.T) {
	msg := formatSlingMessage("bd-123", "Fix: handle \"quotes\" & <brackets>", "test-agent", "arg with\nnewline")
	if !strings.Contains(msg, "Fix: handle \"quotes\" & <brackets>") {
		t.Error("special chars in title not preserved")
	}
	if !strings.Contains(msg, "arg with\nnewline") {
		t.Error("special chars in args not preserved")
	}
}

func TestFormatSpawnPrompt_SpecialChars(t *testing.T) {
	prompt := formatSpawnPrompt("bd-123", "Title with $pecial chars", "args with 'quotes'")
	if !strings.Contains(prompt, "$pecial") {
		t.Error("special chars in title not preserved")
	}
	if !strings.Contains(prompt, "'quotes'") {
		t.Error("special chars in args not preserved")
	}
}
