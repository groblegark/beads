package gate

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// skipIfNoDolt skips the test if Dolt is not installed.
func skipIfNoDoltGate(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt not installed, skipping test")
	}
}

// setupTestDB creates a temporary Dolt database for testing and returns
// the sql.DB connection and cleanup function.
func setupTestDB(t testing.TB) (*sql.DB, func()) {
	t.Helper()
	skipIfNoDoltGate(t)

	tmpDir, err := os.MkdirTemp("", "gate-db-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}

	// Initialize dolt repo
	cmd := exec.Command("dolt", "init", "--name", "test", "--email", "test@test.com")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("dolt init: %v\n%s", err, out)
	}

	// Start dolt sql-server
	ctx, cancel := context.WithCancel(context.Background())
	serverCmd := exec.CommandContext(ctx, "dolt", "sql-server",
		"--host", "127.0.0.1",
		"--port", "0", // random port
		"--socket", tmpDir+"/dolt.sock",
	)
	serverCmd.Dir = tmpDir
	serverCmd.Stdout = nil
	serverCmd.Stderr = nil
	if err := serverCmd.Start(); err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		t.Fatalf("start dolt sql-server: %v", err)
	}

	// Connect via Unix socket
	dsn := "root@unix(" + tmpDir + "/dolt.sock)/dolt"
	var db *sql.DB
	for range 50 {
		db, err = sql.Open("mysql", dsn)
		if err == nil {
			if err = db.Ping(); err == nil {
				break
			}
			db.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		cancel()
		os.RemoveAll(tmpDir)
		t.Fatalf("connect to dolt: %v", err)
	}

	// Create the session_gates table
	if err := EnsureSessionGatesTable(db); err != nil {
		db.Close()
		cancel()
		os.RemoveAll(tmpDir)
		t.Fatalf("ensure table: %v", err)
	}

	cleanup := func() {
		db.Close()
		cancel()
		_ = serverCmd.Wait()
		os.RemoveAll(tmpDir)
	}

	return db, cleanup
}

func TestDBBackend_MarkAndGet(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

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

func TestDBBackend_Clear(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

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

func TestDBBackend_ClearAll(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

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

func TestDBBackend_ClearGateAllAgents(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

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

func TestDBBackend_TTLExpiration(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

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

func TestDBBackend_NoTTL(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

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

func TestDBBackend_AgentIsolation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

	_ = b.Mark("sharp-seal", "decision", MarkOpts{Mechanism: "test"})

	// Different agent should not see it.
	if b.IsSatisfied("stout-fish", "decision") {
		t.Error("stout-fish should not see sharp-seal's gate")
	}
}

func TestDBBackend_ClearNonexistent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

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

func TestDBBackend_MarkOverwrite(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

	// Mark with one mechanism.
	_ = b.Mark("sharp-seal", "decision", MarkOpts{Mechanism: "auto_check"})
	entry := b.Get("sharp-seal", "decision")
	if entry == nil || entry.Mechanism != "auto_check" {
		t.Fatal("expected auto_check mechanism")
	}

	// Overwrite with different mechanism (upsert).
	_ = b.Mark("sharp-seal", "decision", MarkOpts{Mechanism: "decision_create"})
	entry = b.Get("sharp-seal", "decision")
	if entry == nil || entry.Mechanism != "decision_create" {
		t.Fatal("expected decision_create mechanism after overwrite")
	}
}

func TestDBBackend_ListForAgent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	b := NewDBBackend(db)

	_ = b.Mark("sharp-seal", "decision", MarkOpts{Mechanism: "test1"})
	_ = b.Mark("sharp-seal", "commit-push", MarkOpts{Mechanism: "test2"})
	_ = b.Mark("stout-fish", "decision", MarkOpts{Mechanism: "other"})

	entries := b.ListForAgent("sharp-seal")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for sharp-seal, got %d", len(entries))
	}
}
