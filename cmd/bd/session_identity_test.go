package main

import (
	"testing"
)

func TestDeriveSessionKey_ClaudeSessionID(t *testing.T) {
	// CLAUDE_SESSION_ID should take priority and produce stable keys
	t.Setenv("CLAUDE_SESSION_ID", "test-session-abc")
	t.Setenv("TERM_SESSION_ID", "")
	t.Setenv("BD_ACTOR", "")
	t.Setenv("GT_ROLE", "")

	key1 := deriveSessionKey("/home/agent/gt")
	key2 := deriveSessionKey("/home/agent/gt")
	if key1 != key2 {
		t.Errorf("same CLAUDE_SESSION_ID should produce same key, got %q and %q", key1, key2)
	}

	// Different session ID → different key
	t.Setenv("CLAUDE_SESSION_ID", "test-session-xyz")
	key3 := deriveSessionKey("/home/agent/gt")
	if key1 == key3 {
		t.Errorf("different CLAUDE_SESSION_ID should produce different key")
	}
}

func TestDeriveSessionKey_ActorIdentity(t *testing.T) {
	// When no CLAUDE_SESSION_ID or TERM_SESSION_ID, but GT_ROLE is set,
	// the key should be stable (not based on PPID).
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("TERM_SESSION_ID", "")
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("GT_ROLE", "mayor")

	key1 := deriveSessionKey("/home/agent/gt")
	key2 := deriveSessionKey("/home/agent/gt")
	if key1 != key2 {
		t.Errorf("same GT_ROLE should produce same key, got %q and %q", key1, key2)
	}

	// Different project root → different key
	key3 := deriveSessionKey("/home/agent/gt/beads")
	if key1 == key3 {
		t.Errorf("different project root should produce different key")
	}
}

func TestDeriveSessionKey_BDActorPriority(t *testing.T) {
	// BD_ACTOR should take priority over GT_ROLE for actor identity
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("TERM_SESSION_ID", "")
	t.Setenv("BD_ACTOR", "custom-agent")
	t.Setenv("GT_ROLE", "mayor")

	keyWithBDActor := deriveSessionKey("/test")

	t.Setenv("BD_ACTOR", "")
	keyWithGTRole := deriveSessionKey("/test")

	if keyWithBDActor == keyWithGTRole {
		t.Errorf("BD_ACTOR and GT_ROLE should produce different keys")
	}
}

func TestDeriveSessionKey_ClaudeSessionOverridesActor(t *testing.T) {
	// CLAUDE_SESSION_ID should take priority over actor identity
	t.Setenv("CLAUDE_SESSION_ID", "session-123")
	t.Setenv("GT_ROLE", "mayor")

	keyWithSession := deriveSessionKey("/test")

	t.Setenv("CLAUDE_SESSION_ID", "")
	keyWithRole := deriveSessionKey("/test")

	if keyWithSession == keyWithRole {
		t.Errorf("CLAUDE_SESSION_ID should take priority over GT_ROLE")
	}
}

func TestGetStableActorIdentity(t *testing.T) {
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("GT_ROLE", "")

	if got := getStableActorIdentity(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	t.Setenv("GT_ROLE", "mayor")
	if got := getStableActorIdentity(); got != "mayor" {
		t.Errorf("expected 'mayor', got %q", got)
	}

	t.Setenv("BEADS_ACTOR", "mcp-agent")
	if got := getStableActorIdentity(); got != "mcp-agent" {
		t.Errorf("expected 'mcp-agent' (higher priority), got %q", got)
	}

	t.Setenv("BD_ACTOR", "explicit-agent")
	if got := getStableActorIdentity(); got != "explicit-agent" {
		t.Errorf("expected 'explicit-agent' (highest priority), got %q", got)
	}
}
