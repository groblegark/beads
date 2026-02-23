package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ============================================================================
// Jack Sweeper Tests (beads-uyjs)
//
// Tests for the background jack expiry sweeper (sweepExpiredJacks).
// The sweeper scans in_progress jacks, detects expired TTLs, marks them
// escalated, and emits EventJackExpired events.
// ============================================================================

// createExpiredJackDirect creates a jack via RPC then backdates its expiry directly in storage.
func createExpiredJackDirect(t *testing.T, client *Client, store storage.Storage, target, reason string, expiredAgo time.Duration) string {
	t.Helper()

	args := &JackOnArgs{
		Target:     target,
		Reason:     reason,
		RevertPlan: "Revert changes",
		TTL:        "1h",
		Priority:   2,
	}
	resp, err := client.JackOn(args)
	if err != nil {
		t.Fatalf("JackOn failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn failed: %s", resp.Error)
	}
	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal jack: %v", err)
	}

	// Backdate the expiry
	metadata := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		json.Unmarshal(issue.Metadata, &metadata)
	}
	metadata["jack_expires_at"] = time.Now().UTC().Add(-expiredAgo).Format(time.RFC3339)
	metadataJSON, _ := json.Marshal(metadata)

	ctx := context.Background()
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"metadata": string(metadataJSON),
	}, "test"); err != nil {
		t.Fatalf("Failed to backdate jack expiry: %v", err)
	}

	return issue.ID
}

// --- Sweeper detects expired jacks ---

func TestSweepExpiredJacks_DetectsExpired(t *testing.T) {
	srv, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	// Create a jack that's already expired (30 minutes overdue)
	jackID := createExpiredJackDirect(t, client, store, "pod/bd-daemon-test", "Debug logging", 30*time.Minute)

	// Run the sweeper
	srv.sweepExpiredJacks()

	// Verify the jack was marked as escalated
	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue after sweep: %v", err)
	}

	metadata := make(map[string]interface{})
	json.Unmarshal(issue.Metadata, &metadata)

	if _, ok := metadata["jack_escalated"]; !ok {
		t.Error("expected jack_escalated to be set in metadata after sweep")
	}
	if _, ok := metadata["jack_escalated_at"]; !ok {
		t.Error("expected jack_escalated_at to be set in metadata after sweep")
	}
}

// --- Sweeper skips non-expired jacks ---

func TestSweepExpiredJacks_SkipsActive(t *testing.T) {
	srv, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	// Create a jack with a future expiry (default TTL = 1h, not backdated)
	jackID := createTestJack(t, client, "pod/active-daemon", "Active jack", "Revert config")

	// Run the sweeper
	srv.sweepExpiredJacks()

	// Verify the jack was NOT escalated
	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	metadata := make(map[string]interface{})
	json.Unmarshal(issue.Metadata, &metadata)

	if _, ok := metadata["jack_escalated"]; ok {
		t.Error("active jack should NOT be marked as escalated")
	}
}

// --- Sweeper skips already-escalated jacks (no duplicate alerts) ---

func TestSweepExpiredJacks_SkipsAlreadyEscalated(t *testing.T) {
	srv, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	// Create an expired jack
	jackID := createExpiredJackDirect(t, client, store, "pod/already-escalated", "Old jack", 2*time.Hour)

	// Run sweeper once — should escalate
	srv.sweepExpiredJacks()

	// Get the escalated_at timestamp
	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	metadata := make(map[string]interface{})
	json.Unmarshal(issue.Metadata, &metadata)

	firstEscalatedAt, ok := metadata["jack_escalated_at"].(string)
	if !ok || firstEscalatedAt == "" {
		t.Fatal("expected jack_escalated_at after first sweep")
	}

	// Run sweeper again — should skip (already escalated)
	srv.sweepExpiredJacks()

	// Verify the escalated_at timestamp hasn't changed
	issue, err = store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue after second sweep: %v", err)
	}
	json.Unmarshal(issue.Metadata, &metadata)

	secondEscalatedAt, _ := metadata["jack_escalated_at"].(string)
	if secondEscalatedAt != firstEscalatedAt {
		t.Errorf("expected jack_escalated_at to remain %q, got %q (double escalation)", firstEscalatedAt, secondEscalatedAt)
	}
}

// --- Sweeper skips closed jacks ---

func TestSweepExpiredJacks_SkipsClosed(t *testing.T) {
	srv, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	// Create a jack, backdate its expiry, then close it
	jackID := createExpiredJackDirect(t, client, store, "pod/closed-jack", "Closed before sweep", 1*time.Hour)

	// Close the jack via RPC
	offResp, err := client.JackOff(&JackOffArgs{
		ID:     jackID,
		Reason: "Done",
	})
	if err != nil {
		t.Fatalf("JackOff: %v", err)
	}
	if !offResp.Success {
		t.Fatalf("JackOff failed: %s", offResp.Error)
	}

	// Run the sweeper
	srv.sweepExpiredJacks()

	// Verify the jack was NOT escalated (it's closed, not in_progress)
	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	metadata := make(map[string]interface{})
	json.Unmarshal(issue.Metadata, &metadata)

	if _, ok := metadata["jack_escalated"]; ok {
		t.Error("closed jack should NOT be escalated by sweeper")
	}
}

// --- Sweeper handles nil storage gracefully ---

func TestSweepExpiredJacks_NilStorage(t *testing.T) {
	server := &Server{
		storage:      nil,
		shutdownChan: make(chan struct{}),
	}
	// Should not panic
	server.sweepExpiredJacks()
}

// --- Sweeper handles jacks with missing expiry metadata ---

func TestSweepExpiredJacks_MissingExpiry(t *testing.T) {
	srv, _, store, cleanup := setupJackTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create a jack directly in storage without jack_expires_at
	issue := &types.Issue{
		Title:       "Jack: manual test — no expiry",
		IssueType:   types.TypeJack,
		Status:      types.StatusInProgress,
		Priority:    2,
		Description: "Test jack",
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}

	// Run sweeper — should not panic or escalate
	srv.sweepExpiredJacks()

	// Verify no escalation happened
	updated, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	metadata := make(map[string]interface{})
	if len(updated.Metadata) > 0 {
		json.Unmarshal(updated.Metadata, &metadata)
	}
	if _, ok := metadata["jack_escalated"]; ok {
		t.Error("jack without expiry should NOT be escalated")
	}
}

// --- Sweeper handles multiple jacks: mix of expired and active ---

func TestSweepExpiredJacks_MixedJacks(t *testing.T) {
	srv, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	// Create 2 expired and 2 active jacks
	expired1 := createExpiredJackDirect(t, client, store, "pod/expired-1", "Expired jack 1", 1*time.Hour)
	expired2 := createExpiredJackDirect(t, client, store, "pod/expired-2", "Expired jack 2", 30*time.Minute)
	active1 := createTestJack(t, client, "pod/active-1", "Active jack 1", "Revert 1")
	active2 := createTestJack(t, client, "pod/active-2", "Active jack 2", "Revert 2")

	// Run the sweeper
	srv.sweepExpiredJacks()

	ctx := context.Background()

	// Check expired jacks were escalated
	for _, id := range []string{expired1, expired2} {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue %s: %v", id, err)
		}
		metadata := make(map[string]interface{})
		json.Unmarshal(issue.Metadata, &metadata)
		if _, ok := metadata["jack_escalated"]; !ok {
			t.Errorf("expired jack %s should be escalated", id)
		}
	}

	// Check active jacks were NOT escalated
	for _, id := range []string{active1, active2} {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue %s: %v", id, err)
		}
		metadata := make(map[string]interface{})
		json.Unmarshal(issue.Metadata, &metadata)
		if _, ok := metadata["jack_escalated"]; ok {
			t.Errorf("active jack %s should NOT be escalated", id)
		}
	}
}

// --- Sweeper updates metadata with escalation timestamp ---

func TestSweepExpiredJacks_EscalationTimestamp(t *testing.T) {
	srv, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	before := time.Now().UTC()
	jackID := createExpiredJackDirect(t, client, store, "pod/timestamp-test", "Timestamp test", 15*time.Minute)

	srv.sweepExpiredJacks()

	after := time.Now().UTC()

	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	metadata := make(map[string]interface{})
	json.Unmarshal(issue.Metadata, &metadata)

	escalatedAtStr, ok := metadata["jack_escalated_at"].(string)
	if !ok {
		t.Fatal("expected jack_escalated_at string in metadata")
	}
	escalatedAt, err := time.Parse(time.RFC3339, escalatedAtStr)
	if err != nil {
		t.Fatalf("invalid jack_escalated_at format: %v", err)
	}

	if escalatedAt.Before(before) || escalatedAt.After(after) {
		t.Errorf("jack_escalated_at %v should be between %v and %v", escalatedAt, before, after)
	}
}
