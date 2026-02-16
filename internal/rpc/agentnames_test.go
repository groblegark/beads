package rpc

import (
	"strings"
	"testing"
)

func TestGenerateAgentName_Deterministic(t *testing.T) {
	name1 := GenerateAgentName("test-session-key")
	name2 := GenerateAgentName("test-session-key")
	if name1 != name2 {
		t.Errorf("same key should produce same name: got %q and %q", name1, name2)
	}
}

func TestGenerateAgentName_Format(t *testing.T) {
	name := GenerateAgentName("some-key")
	parts := strings.SplitN(name, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("expected adjective-noun format, got %q", name)
	}
	if parts[0] == "" || parts[1] == "" {
		t.Errorf("empty part in name %q", name)
	}
}

func TestGenerateAgentName_DifferentKeys(t *testing.T) {
	name1 := GenerateAgentName("key-alpha")
	name2 := GenerateAgentName("key-beta")
	if name1 == name2 {
		t.Errorf("different keys should usually produce different names: both got %q", name1)
	}
}

func TestGenerateAgentName_Distribution(t *testing.T) {
	// Generate many names and verify we get reasonable variety
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name := GenerateAgentName(string(rune('a' + i)))
		seen[name] = true
	}
	// With 50 adjectives * 70 nouns = 3500 possible names,
	// 100 random picks should yield at least 50 unique names
	if len(seen) < 50 {
		t.Errorf("poor distribution: only %d unique names from 100 keys", len(seen))
	}
}
