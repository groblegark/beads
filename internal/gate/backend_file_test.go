package gate

import (
	"testing"
)

func TestFileBackend_MarkAndGet(t *testing.T) {
	workDir := t.TempDir()
	b := NewFileBackend(workDir)

	if b.IsSatisfied("session-abc", "decision") {
		t.Fatal("expected NOT satisfied initially")
	}

	err := b.Mark("session-abc", "decision", MarkOpts{Mechanism: "test"})
	if err != nil {
		t.Fatalf("Mark: %v", err)
	}

	if !b.IsSatisfied("session-abc", "decision") {
		t.Fatal("expected satisfied after Mark")
	}

	entry := b.Get("session-abc", "decision")
	if entry == nil {
		t.Fatal("expected non-nil entry")
	}
	if entry.Mechanism != "file_marker" {
		t.Errorf("mechanism = %q, want %q", entry.Mechanism, "file_marker")
	}
}

func TestFileBackend_Clear(t *testing.T) {
	workDir := t.TempDir()
	b := NewFileBackend(workDir)

	_ = b.Mark("session-abc", "decision", MarkOpts{})
	_ = b.Clear("session-abc", "decision")

	if b.IsSatisfied("session-abc", "decision") {
		t.Fatal("expected NOT satisfied after Clear")
	}
}

func TestFileBackend_ClearAll(t *testing.T) {
	workDir := t.TempDir()
	b := NewFileBackend(workDir)

	_ = b.Mark("session-abc", "decision", MarkOpts{})
	_ = b.Mark("session-abc", "commit-push", MarkOpts{})
	_ = b.ClearAll("session-abc")

	if b.IsSatisfied("session-abc", "decision") {
		t.Error("decision should be cleared")
	}
	if b.IsSatisfied("session-abc", "commit-push") {
		t.Error("commit-push should be cleared")
	}
}

func TestFileBackend_ClearGateAllAgents(t *testing.T) {
	workDir := t.TempDir()
	b := NewFileBackend(workDir)

	_ = b.Mark("session-1", "decision", MarkOpts{})
	_ = b.Mark("session-2", "decision", MarkOpts{})
	_ = b.Mark("session-1", "commit-push", MarkOpts{})

	_ = b.ClearGateAllAgents("decision")

	if b.IsSatisfied("session-1", "decision") {
		t.Error("session-1.decision should be cleared")
	}
	if b.IsSatisfied("session-2", "decision") {
		t.Error("session-2.decision should be cleared")
	}
	if !b.IsSatisfied("session-1", "commit-push") {
		t.Error("session-1.commit-push should still be satisfied")
	}
}

func TestFileBackend_Implements_GateBackend(t *testing.T) {
	var _ GateBackend = (*FileBackend)(nil)
}
