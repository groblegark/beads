package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// bd-206: Test updating open issue to closed preserves closed_at
func TestImportOpenToClosedTransition(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-open-to-closed-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	ctx := t.Context()

	// Step 1: Create an open issue in the database
	openIssue := &types.Issue{
		ID:          "bd-transition-1",
		Title:       "Test transition",
		Description: "This will be closed",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		ClosedAt:    nil,
	}

	err = testStore.CreateIssue(ctx, openIssue, "test")
	if err != nil {
		t.Fatalf("Failed to create open issue: %v", err)
	}

	// Step 2: Update via UpdateIssue with closed status (closed_at managed automatically)
	updates := map[string]interface{}{
		"status": types.StatusClosed,
	}

	err = testStore.UpdateIssue(ctx, "bd-transition-1", updates, "test")
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Step 3: Verify the issue is now closed with correct closed_at
	updated, err := testStore.GetIssue(ctx, "bd-transition-1")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if updated.Status != types.StatusClosed {
		t.Errorf("Expected status to be closed, got %s", updated.Status)
	}

	if updated.ClosedAt == nil {
		t.Fatal("Expected closed_at to be set after transition to closed")
	}
}

// bd-206: Test updating closed issue to open clears closed_at
func TestImportClosedToOpenTransition(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-test-closed-to-open-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")

	testStore := newTestStoreWithPrefix(t, dbPath, "bd")

	ctx := t.Context()

	// Step 1: Create a closed issue in the database
	closedTime := time.Now()
	closedIssue := &types.Issue{
		ID:          "bd-transition-2",
		Title:       "Test reopening",
		Description: "This will be reopened",
		Status:      types.StatusClosed,
		Priority:    1,
		IssueType:   types.TypeBug,
		CreatedAt:   time.Now(),
		UpdatedAt:   closedTime,
		ClosedAt:    &closedTime,
	}

	err = testStore.CreateIssue(ctx, closedIssue, "test")
	if err != nil {
		t.Fatalf("Failed to create closed issue: %v", err)
	}

	// Step 2: Update via UpdateIssue with open status (closed_at managed automatically)
	updates := map[string]interface{}{
		"status": types.StatusOpen,
	}

	err = testStore.UpdateIssue(ctx, "bd-transition-2", updates, "test")
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Step 3: Verify the issue is now open with null closed_at
	updated, err := testStore.GetIssue(ctx, "bd-transition-2")
	if err != nil {
		t.Fatalf("Failed to get updated issue: %v", err)
	}

	if updated.Status != types.StatusOpen {
		t.Errorf("Expected status to be open, got %s", updated.Status)
	}

	if updated.ClosedAt != nil {
		t.Errorf("Expected closed_at to be nil after reopening, got %v", updated.ClosedAt)
	}
}
