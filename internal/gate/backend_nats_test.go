package gate

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

func startTestNATS(t testing.TB) (nats.KeyValue, func()) {
	t.Helper()
	dir := t.TempDir()
	opts := &natsserver.Options{
		Port:               -1,
		JetStream:          true,
		JetStreamMaxMemory: 1024 << 20,
		JetStreamMaxStore:  1024 << 20,
		StoreDir:           dir,
		NoLog:              true,
		NoSigs:             true,
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("create test NATS server: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("test NATS server failed to start")
	}

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		t.Fatalf("connect to test NATS: %v", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatalf("get JetStream context: %v", err)
	}

	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket:  "GATE_STATE_TEST",
		History: 5,
		Storage: nats.MemoryStorage,
	})
	if err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatalf("create KV bucket: %v", err)
	}

	cleanup := func() {
		nc.Close()
		ns.Shutdown()
	}
	return kv, cleanup
}

func TestNATSBackend_MarkAndGet(t *testing.T) {
	kv, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewNATSBackend(kv)

	// Not satisfied initially.
	if b.IsSatisfied("sharp-seal", "decision") {
		t.Fatal("expected gate NOT satisfied initially")
	}
	if entry := b.Get("sharp-seal", "decision"); entry != nil {
		t.Fatal("expected nil entry initially")
	}

	// Mark it.
	err := b.Mark("sharp-seal", "decision", MarkOpts{
		Mechanism: "decision_create",
		Actor:     "sharp-seal-1",
		SessionID: "abc-123",
	})
	if err != nil {
		t.Fatalf("Mark: %v", err)
	}

	// Now satisfied.
	if !b.IsSatisfied("sharp-seal", "decision") {
		t.Fatal("expected gate satisfied after Mark")
	}

	entry := b.Get("sharp-seal", "decision")
	if entry == nil {
		t.Fatal("expected non-nil entry after Mark")
	}
	if entry.Mechanism != "decision_create" {
		t.Errorf("mechanism = %q, want %q", entry.Mechanism, "decision_create")
	}
	if entry.Actor != "sharp-seal-1" {
		t.Errorf("actor = %q, want %q", entry.Actor, "sharp-seal-1")
	}
}

func TestNATSBackend_Clear(t *testing.T) {
	kv, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewNATSBackend(kv)

	_ = b.Mark("sharp-seal", "decision", MarkOpts{Mechanism: "test"})
	if !b.IsSatisfied("sharp-seal", "decision") {
		t.Fatal("expected satisfied after Mark")
	}

	err := b.Clear("sharp-seal", "decision")
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if b.IsSatisfied("sharp-seal", "decision") {
		t.Fatal("expected NOT satisfied after Clear")
	}
}

func TestNATSBackend_ClearAll(t *testing.T) {
	kv, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewNATSBackend(kv)

	_ = b.Mark("sharp-seal", "decision", MarkOpts{Mechanism: "test"})
	_ = b.Mark("sharp-seal", "commit-push", MarkOpts{Mechanism: "test"})
	_ = b.Mark("stout-fish", "decision", MarkOpts{Mechanism: "test"})

	err := b.ClearAll("sharp-seal")
	if err != nil {
		t.Fatalf("ClearAll: %v", err)
	}

	if b.IsSatisfied("sharp-seal", "decision") {
		t.Error("sharp-seal.decision should be cleared")
	}
	if b.IsSatisfied("sharp-seal", "commit-push") {
		t.Error("sharp-seal.commit-push should be cleared")
	}
	// Other agent's gates should be unaffected.
	if !b.IsSatisfied("stout-fish", "decision") {
		t.Error("stout-fish.decision should still be satisfied")
	}
}

func TestNATSBackend_ClearGateAllAgents(t *testing.T) {
	kv, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewNATSBackend(kv)

	_ = b.Mark("sharp-seal", "decision", MarkOpts{Mechanism: "test"})
	_ = b.Mark("stout-fish", "decision", MarkOpts{Mechanism: "test"})
	_ = b.Mark("sharp-seal", "commit-push", MarkOpts{Mechanism: "test"})

	err := b.ClearGateAllAgents("decision")
	if err != nil {
		t.Fatalf("ClearGateAllAgents: %v", err)
	}

	if b.IsSatisfied("sharp-seal", "decision") {
		t.Error("sharp-seal.decision should be cleared")
	}
	if b.IsSatisfied("stout-fish", "decision") {
		t.Error("stout-fish.decision should be cleared")
	}
	// Other gates should be unaffected.
	if !b.IsSatisfied("sharp-seal", "commit-push") {
		t.Error("sharp-seal.commit-push should still be satisfied")
	}
}

func TestNATSBackend_TTLExpiration(t *testing.T) {
	kv, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewNATSBackend(kv)

	// Mark with a 1-second TTL.
	err := b.Mark("sharp-seal", "decision", MarkOpts{
		Mechanism: "test",
		TTL:       1 * time.Second,
	})
	if err != nil {
		t.Fatalf("Mark: %v", err)
	}

	// Should be satisfied immediately.
	if !b.IsSatisfied("sharp-seal", "decision") {
		t.Error("expected gate satisfied before TTL expiration")
	}

	// Sleep past the TTL.
	time.Sleep(1100 * time.Millisecond)

	// Should be expired.
	if b.IsSatisfied("sharp-seal", "decision") {
		t.Error("expected gate NOT satisfied after TTL expiration")
	}
	if entry := b.Get("sharp-seal", "decision"); entry != nil {
		t.Error("expected nil entry after TTL expiration")
	}
}

func TestNATSBackend_NoTTL(t *testing.T) {
	kv, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewNATSBackend(kv)

	// Mark without TTL — should persist indefinitely.
	err := b.Mark("sharp-seal", "commit-push", MarkOpts{Mechanism: "test"})
	if err != nil {
		t.Fatalf("Mark: %v", err)
	}

	if !b.IsSatisfied("sharp-seal", "commit-push") {
		t.Error("expected gate satisfied (no TTL)")
	}

	entry := b.Get("sharp-seal", "commit-push")
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.TTL != 0 {
		t.Errorf("TTL = %d, want 0", entry.TTL)
	}
}

func TestNATSBackend_AgentIsolation(t *testing.T) {
	kv, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewNATSBackend(kv)

	_ = b.Mark("sharp-seal", "decision", MarkOpts{Mechanism: "test"})

	// Different agent should not see it.
	if b.IsSatisfied("stout-fish", "decision") {
		t.Error("stout-fish should not see sharp-seal's gate")
	}
}

func TestNATSBackend_ClearNonexistent(t *testing.T) {
	kv, cleanup := startTestNATS(t)
	defer cleanup()

	b := NewNATSBackend(kv)

	// Should not error.
	if err := b.Clear("nobody", "decision"); err != nil {
		t.Errorf("Clear nonexistent: %v", err)
	}
	if err := b.ClearAll("nobody"); err != nil {
		t.Errorf("ClearAll nonexistent: %v", err)
	}
	if err := b.ClearGateAllAgents("nonexistent"); err != nil {
		t.Errorf("ClearGateAllAgents nonexistent: %v", err)
	}
}
