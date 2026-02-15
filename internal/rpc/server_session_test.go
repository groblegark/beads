package rpc

import (
	"testing"
	"time"
)

func TestSessionRegistry_BasicRegister(t *testing.T) {
	reg := newSessionRegistry()

	// First registration gets bare name
	name, isNew := reg.register("key1", "alice")
	if name != "alice" || !isNew {
		t.Errorf("first register: got (%q, %v), want (alice, true)", name, isNew)
	}

	// Same key returns same name (not new)
	name, isNew = reg.register("key1", "alice")
	if name != "alice" || isNew {
		t.Errorf("re-register: got (%q, %v), want (alice, false)", name, isNew)
	}

	// Different key with same baseName gets suffix
	name, isNew = reg.register("key2", "alice")
	if name != "alice-1" || !isNew {
		t.Errorf("second alice: got (%q, %v), want (alice-1, true)", name, isNew)
	}

	// Third gets alice-2
	name, isNew = reg.register("key3", "alice")
	if name != "alice-2" || !isNew {
		t.Errorf("third alice: got (%q, %v), want (alice-2, true)", name, isNew)
	}

	// Different baseName gets bare name
	name, isNew = reg.register("key4", "bob")
	if name != "bob" || !isNew {
		t.Errorf("first bob: got (%q, %v), want (bob, true)", name, isNew)
	}
}

func TestSessionRegistry_PruneStale(t *testing.T) {
	reg := newSessionRegistry()

	// Register two sessions
	reg.register("key1", "alice")
	reg.register("key2", "bob")

	// Manually backdate key1
	reg.mu.Lock()
	reg.sessions["key1"].LastSeen = time.Now().Add(-25 * time.Hour)
	reg.mu.Unlock()

	// Prune with 24h cutoff
	pruned := reg.pruneStale(24 * time.Hour)
	if pruned != 1 {
		t.Errorf("pruneStale: got %d pruned, want 1", pruned)
	}

	// key1 should be gone, key2 should remain
	entries := reg.list()
	if len(entries) != 1 {
		t.Fatalf("after prune: got %d entries, want 1", len(entries))
	}
	if entries[0].SessionKey != "key2" {
		t.Errorf("remaining entry: got %q, want key2", entries[0].SessionKey)
	}
}

func TestSessionRegistry_PruneFreesName(t *testing.T) {
	reg := newSessionRegistry()

	// Register alice (gets bare name)
	name1, _ := reg.register("key1", "alice")
	if name1 != "alice" {
		t.Fatalf("first alice: got %q, want alice", name1)
	}

	// Register second alice (gets alice-1)
	name2, _ := reg.register("key2", "alice")
	if name2 != "alice-1" {
		t.Fatalf("second alice: got %q, want alice-1", name2)
	}

	// Prune key1 (backdate it)
	reg.mu.Lock()
	reg.sessions["key1"].LastSeen = time.Now().Add(-25 * time.Hour)
	reg.mu.Unlock()
	reg.pruneStale(24 * time.Hour)

	// New registration with same baseName should get alice-2 (not alice,
	// because alice-1 is still active with that baseName)
	name3, _ := reg.register("key3", "alice")
	if name3 != "alice-2" {
		t.Errorf("after prune new alice: got %q, want alice-2", name3)
	}
}

func TestSessionRegistry_List(t *testing.T) {
	reg := newSessionRegistry()

	reg.register("key1", "alice")
	reg.register("key2", "bob")

	entries := reg.list()
	if len(entries) != 2 {
		t.Fatalf("list: got %d entries, want 2", len(entries))
	}

	// Check both are present (order is non-deterministic from map)
	found := map[string]bool{}
	for _, e := range entries {
		found[e.AssignedName] = true
	}
	if !found["alice"] || !found["bob"] {
		t.Errorf("list: expected alice and bob, got %v", found)
	}
}

func TestSessionRegistry_ReRegisterUpdatesLastSeen(t *testing.T) {
	reg := newSessionRegistry()

	reg.register("key1", "alice")

	// Backdate
	reg.mu.Lock()
	reg.sessions["key1"].LastSeen = time.Now().Add(-1 * time.Hour)
	old := reg.sessions["key1"].LastSeen
	reg.mu.Unlock()

	// Re-register updates LastSeen
	reg.register("key1", "alice")

	reg.mu.RLock()
	updated := reg.sessions["key1"].LastSeen
	reg.mu.RUnlock()

	if !updated.After(old) {
		t.Errorf("LastSeen not updated: old=%v, new=%v", old, updated)
	}
}

func TestSessionRegistry_LoadFromDB_NilDB(t *testing.T) {
	reg := newSessionRegistry()
	// Should be a no-op, not panic
	err := reg.loadFromDB(nil, nil, 24*time.Hour)
	if err != nil {
		t.Errorf("loadFromDB(nil): unexpected error: %v", err)
	}
}

func TestSessionRegistry_PersistEntry_NilDB(t *testing.T) {
	reg := newSessionRegistry()
	// Should be a no-op, not panic
	reg.persistEntry(&sessionEntry{
		SessionKey:   "key1",
		AssignedName: "alice",
		BaseName:     "alice",
		LastSeen:     time.Now(),
	})
}
