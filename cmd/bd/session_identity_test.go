package main

import (
	"testing"
)

func TestDeriveSessionKey_ClaudeSessionID(t *testing.T) {
	// CLAUDE_SESSION_ID should produce stable keys when no actor identity is set
	t.Setenv("CLAUDE_SESSION_ID", "test-session-abc")
	t.Setenv("TERM_SESSION_ID", "")
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
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

func TestDeriveSessionKey_ActorOverridesClaudeSession(t *testing.T) {
	// Actor identity should take priority over CLAUDE_SESSION_ID so that
	// parent sessions and Claude Code subagents (which get different
	// CLAUDE_SESSION_IDs) resolve to the same session key. (bd-vmuoj)
	t.Setenv("CLAUDE_SESSION_ID", "session-123")
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("GT_ROLE", "mayor")

	keyWithBoth := deriveSessionKey("/test")

	t.Setenv("CLAUDE_SESSION_ID", "")
	keyWithRoleOnly := deriveSessionKey("/test")

	if keyWithBoth != keyWithRoleOnly {
		t.Errorf("actor identity should take priority over CLAUDE_SESSION_ID, got %q and %q", keyWithBoth, keyWithRoleOnly)
	}
}

func TestDeriveSessionKey_SubagentSameSession(t *testing.T) {
	// A parent Claude Code session and its subagent (different CLAUDE_SESSION_ID)
	// should get the same session key when BD_ACTOR is set. This prevents
	// the mayor-1, mayor-2 suffix explosion. (bd-vmuoj)
	t.Setenv("BD_ACTOR", "mayor")
	t.Setenv("GT_ROLE", "mayor")

	t.Setenv("CLAUDE_SESSION_ID", "parent-session-abc")
	keyParent := deriveSessionKey("/home/agent/gt")

	t.Setenv("CLAUDE_SESSION_ID", "subagent-session-xyz")
	keySubagent := deriveSessionKey("/home/agent/gt")

	if keyParent != keySubagent {
		t.Errorf("parent and subagent should share session key when BD_ACTOR is set, got %q and %q", keyParent, keySubagent)
	}
}

func TestDeriveSessionKey_NoBDActorUsesClaudeSession(t *testing.T) {
	// Without BD_ACTOR, CLAUDE_SESSION_ID should be used (local developer case).
	t.Setenv("BD_ACTOR", "")
	t.Setenv("BEADS_ACTOR", "")
	t.Setenv("GT_ROLE", "")
	t.Setenv("CLAUDE_SESSION_ID", "session-123")

	key1 := deriveSessionKey("/test")

	t.Setenv("CLAUDE_SESSION_ID", "session-456")
	key2 := deriveSessionKey("/test")

	if key1 == key2 {
		t.Errorf("different CLAUDE_SESSION_IDs should produce different keys when no actor identity is set")
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
