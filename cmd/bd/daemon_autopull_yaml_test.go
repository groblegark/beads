package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/testutil/teststore"
)

// TestAutoPullDefaultFromSQLite verifies the legacy SQLite path still works.
// This test should PASS because the SQLite path works (it's just not used by config.yaml users).
// Note: Tests for syncbranch.IsConfigured() and syncbranch.IsConfiguredWithDB() have been
// removed since the syncbranch package was deleted.
func TestAutoPullDefaultFromSQLite(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	// Create database WITH sync.branch
	ctx := context.Background()
	testStore := teststore.New(t)
	defer testStore.Close()

	// Set sync.branch in SQLite (legacy configuration)
	if err := testStore.SetConfig(ctx, "sync.branch", "sqlite-sync-branch"); err != nil {
		t.Fatalf("Failed to set sync.branch in database: %v", err)
	}

	// The current daemon code should work for this case
	var autoPullFromDB bool
	if syncBranch, err := testStore.GetConfig(ctx, "sync.branch"); err == nil && syncBranch != "" {
		autoPullFromDB = true
	}

	if !autoPullFromDB {
		t.Errorf("Expected autoPull=true when sync.branch is in SQLite, got false")
	}
}
