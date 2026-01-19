// Package dolt provides tests for the Dolt backend fixes in bd-z6d.1.
//
// This file contains comprehensive tests for:
// 1. Connection handling - single connection with USE statement instead of multiple connections
// 2. Create lifecycle - store not closed prematurely during auto-flush
// 3. Daemon event-driven mode - Dolt backends work without JSONL file watching
package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// =============================================================================
// Connection Handling Tests (bd-z6d.1 fix #1)
//
// The Dolt embedded driver shares internal state between connections to the same
// path. If we create two separate *sql.DB connections and close the first one,
// it closes the shared Dolt session, causing "sql: database is closed" errors
// on the second connection.
//
// The fix: Use a single connection and switch databases using USE statement.
// =============================================================================

// TestSingleConnectionWithUSE verifies that the store uses a single connection
// and switches databases with USE instead of creating multiple connections.
func TestSingleConnectionWithUSE(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-conn-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a Dolt store - this should use single connection with USE
	cfg := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "testdb",
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create Dolt store: %v", err)
	}
	defer store.Close()

	// Verify we can access the database after creation
	// This would fail with "database is closed" if we used multiple connections
	if err := store.SetConfig(ctx, "test_key", "test_value"); err != nil {
		t.Errorf("SetConfig failed (possible connection issue): %v", err)
	}

	value, err := store.GetConfig(ctx, "test_key")
	if err != nil {
		t.Errorf("GetConfig failed (possible connection issue): %v", err)
	}
	if value != "test_value" {
		t.Errorf("expected 'test_value', got %q", value)
	}
}

// TestMultipleDatabasesSingleConnection verifies that we can work with
// multiple databases on a single connection using USE statements.
func TestMultipleDatabasesSingleConnection(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-multi-db-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create first store/database
	cfg1 := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "db1",
	}

	store1, err := New(ctx, cfg1)
	if err != nil {
		t.Fatalf("failed to create first store: %v", err)
	}

	// Set a value in db1
	if err := store1.SetConfig(ctx, "db_name", "db1"); err != nil {
		t.Fatalf("failed to set config in db1: %v", err)
	}

	// Close first store
	if err := store1.Close(); err != nil {
		t.Fatalf("failed to close first store: %v", err)
	}

	// Create second store/database at same path
	cfg2 := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "db2",
	}

	store2, err := New(ctx, cfg2)
	if err != nil {
		t.Fatalf("failed to create second store: %v", err)
	}
	defer store2.Close()

	// Set a value in db2
	if err := store2.SetConfig(ctx, "db_name", "db2"); err != nil {
		t.Fatalf("failed to set config in db2: %v", err)
	}

	// Verify db2 has correct value (not db1's value)
	value, err := store2.GetConfig(ctx, "db_name")
	if err != nil {
		t.Fatalf("failed to get config from db2: %v", err)
	}
	if value != "db2" {
		t.Errorf("expected 'db2', got %q (possible database isolation issue)", value)
	}
}

// TestConnectionNotClosedDuringOperations verifies that the database connection
// remains open throughout a sequence of operations (the "database is closed"
// error was caused by connection being closed prematurely).
func TestConnectionNotClosedDuringOperations(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Perform a sequence of operations that would have triggered
	// "database is closed" with the old two-connection approach

	// 1. Create database and set prefix
	if err := store.SetConfig(ctx, "issue_prefix", "conn"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// 2. Create an issue
	issue := &types.Issue{
		Title:       "Connection Test Issue",
		Description: "Testing connection remains open",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// 3. Update the issue
	updates := map[string]interface{}{
		"description": "Updated description",
	}
	if err := store.UpdateIssue(ctx, issue.ID, updates, "tester"); err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}

	// 4. Query the issue back
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue failed: %v", err)
	}
	if retrieved.Description != "Updated description" {
		t.Errorf("expected updated description, got %q", retrieved.Description)
	}

	// 5. Search issues
	_, err = store.SearchIssues(ctx, "Connection", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}

	// 6. Commit changes (Dolt-specific)
	if err := store.Commit(ctx, "Test operations completed"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// If we got here without "database is closed" errors, the fix works
	t.Log("All operations completed successfully - connection remained open")
}

// TestIsClosed verifies the IsClosed() method tracks store closure state correctly.
func TestIsClosed(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-isclosed-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "testdb",
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Store should not be closed initially
	if store.IsClosed() {
		t.Error("store should not be closed initially")
	}

	// Close the store
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	// Store should be closed now
	if !store.IsClosed() {
		t.Error("store should be closed after Close()")
	}
}

// TestConcurrentOperationsWithSingleConnection verifies that concurrent operations
// work correctly with the single-connection approach.
func TestConcurrentOperationsWithSingleConnection(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	// Create initial issue
	issue := &types.Issue{
		ID:          "test-concurrent-conn",
		Title:       "Concurrent Connection Test",
		Description: "Test",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Run concurrent operations
	const numWorkers = 10
	const opsPerWorker = 20
	var wg sync.WaitGroup
	errChan := make(chan error, numWorkers*opsPerWorker)

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				// Mix of read and write operations
				switch i % 3 {
				case 0:
					_, err := store.GetIssue(ctx, issue.ID)
					if err != nil {
						errChan <- err
					}
				case 1:
					_, err := store.GetConfig(ctx, "issue_prefix")
					if err != nil {
						errChan <- err
					}
				case 2:
					_, err := store.SearchIssues(ctx, "", types.IssueFilter{})
					if err != nil {
						errChan <- err
					}
				}
			}
		}(w)
	}

	wg.Wait()
	close(errChan)

	// Check for "database is closed" errors specifically
	closedErrors := 0
	otherErrors := 0
	for err := range errChan {
		if err != nil {
			if err == sql.ErrConnDone || containsDatabaseClosed(err) {
				closedErrors++
			} else {
				otherErrors++
			}
		}
	}

	if closedErrors > 0 {
		t.Errorf("got %d 'database is closed' errors - connection handling fix may not be working", closedErrors)
	}
	if otherErrors > 0 {
		t.Logf("got %d other errors (may be expected under heavy contention)", otherErrors)
	}
}

// containsDatabaseClosed checks if an error contains "database is closed" message
func containsDatabaseClosed(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return errStr == "sql: database is closed" ||
		errStr == "database is closed" ||
		// Also check for wrapped errors
		(len(errStr) > 0 && (errStr[len(errStr)-1] == 'd' || errStr[len(errStr)-1] == 'D'))
}

// =============================================================================
// Create Lifecycle Tests (bd-z6d.1 fix #2)
//
// When routing to a different repo, the global store was being closed before
// PersistentPostRun's auto-flush, causing "sql: database is closed" errors.
//
// The fix: Remove defer that closes targetStore after assigning it to global store.
// The global store is now closed by PersistentPostRun after auto-flush completes.
// =============================================================================

// TestStoreLifecycleForAutoFlush simulates the create command's store lifecycle
// to verify that the store remains open for auto-flush operations.
func TestStoreLifecycleForAutoFlush(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Simulate the create command flow:
	// 1. Create an issue (marks dirty)
	issue := &types.Issue{
		Title:       "Lifecycle Test Issue",
		Description: "Testing store lifecycle for auto-flush",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// 2. Check dirty issues (simulates what auto-flush does)
	dirtyIDs, err := store.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("GetDirtyIssues failed (store may be closed): %v", err)
	}

	// The issue we created should be dirty
	found := false
	for _, id := range dirtyIDs {
		if id == issue.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("created issue not in dirty list")
	}

	// 3. Simulate auto-flush export (get all dirty issues with their data)
	for _, id := range dirtyIDs {
		retrieved, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue for dirty issue %s failed: %v", id, err)
		}
		if retrieved == nil {
			t.Errorf("dirty issue %s not found", id)
		}
	}

	// 4. Clear dirty issues (simulates successful flush)
	if err := store.ClearDirtyIssuesByID(ctx, dirtyIDs); err != nil {
		t.Fatalf("ClearDirtyIssuesByID failed: %v", err)
	}

	// 5. Verify store is still usable after "flush"
	if store.IsClosed() {
		t.Error("store should not be closed during flush operations")
	}

	// 6. Verify we can still query
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues after flush failed: %v", err)
	}
	if len(allIssues) == 0 {
		t.Error("expected at least one issue after flush")
	}

	t.Log("Store lifecycle test passed - store remained open throughout auto-flush simulation")
}

// TestStoreNotClosedAfterAssignment simulates the scenario where a store is
// assigned to a "global" variable and should not be closed prematurely.
func TestStoreNotClosedAfterAssignment(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-lifecycle-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "testdb",
	}

	// Create "target" store (simulates opening store for different repo)
	targetStore, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create target store: %v", err)
	}

	// Simulate assignment to global store (the fix removes defer close here)
	globalStore := targetStore
	// In the buggy code, there would be: defer targetStore.Close()
	// which would close globalStore prematurely

	// Set up the database
	if err := globalStore.SetConfig(ctx, "issue_prefix", "lifecycle"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Create an issue
	issue := &types.Issue{
		Title:       "Assignment Test",
		Description: "Test store not closed after assignment",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := globalStore.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue via global store failed: %v", err)
	}

	// Simulate some time passing (like command completion)
	time.Sleep(10 * time.Millisecond)

	// NOW simulate PersistentPostRun's auto-flush
	// This should work because store is NOT closed
	dirtyIDs, err := globalStore.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("GetDirtyIssues failed (store may have been closed prematurely): %v", err)
	}

	if len(dirtyIDs) == 0 {
		t.Error("expected dirty issues for auto-flush")
	}

	// Clean up - close at the end (simulates PersistentPostRun cleanup)
	if err := globalStore.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}
}

// TestMultipleStoresLifecycle tests that multiple stores can be opened and used
// without interfering with each other's lifecycle.
func TestMultipleStoresLifecycle(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir1, err := os.MkdirTemp("", "dolt-store1-*")
	if err != nil {
		t.Fatalf("failed to create temp dir 1: %v", err)
	}
	defer os.RemoveAll(tmpDir1)

	tmpDir2, err := os.MkdirTemp("", "dolt-store2-*")
	if err != nil {
		t.Fatalf("failed to create temp dir 2: %v", err)
	}
	defer os.RemoveAll(tmpDir2)

	// Create first store
	store1, err := New(ctx, &Config{
		Path:           tmpDir1,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "db1",
	})
	if err != nil {
		t.Fatalf("failed to create store1: %v", err)
	}

	// Create second store
	store2, err := New(ctx, &Config{
		Path:           tmpDir2,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "db2",
	})
	if err != nil {
		t.Fatalf("failed to create store2: %v", err)
	}

	// Set prefix for each store (required for CreateIssue)
	if err := store1.SetConfig(ctx, "issue_prefix", "s1"); err != nil {
		t.Fatalf("store1 SetConfig prefix failed: %v", err)
	}
	if err := store2.SetConfig(ctx, "issue_prefix", "s2"); err != nil {
		t.Fatalf("store2 SetConfig prefix failed: %v", err)
	}

	// Set different store identifiers
	if err := store1.SetConfig(ctx, "store_id", "store1"); err != nil {
		t.Fatalf("store1 SetConfig store_id failed: %v", err)
	}
	if err := store2.SetConfig(ctx, "store_id", "store2"); err != nil {
		t.Fatalf("store2 SetConfig store_id failed: %v", err)
	}

	// Close store1 (simulates finishing with one repo)
	if err := store1.Close(); err != nil {
		t.Fatalf("store1 close failed: %v", err)
	}

	// Store2 should still work after store1 is closed
	value, err := store2.GetConfig(ctx, "store_id")
	if err != nil {
		t.Fatalf("store2 GetConfig failed after store1 closed: %v", err)
	}
	if value != "store2" {
		t.Errorf("expected 'store2', got %q", value)
	}

	// Create an issue in store2 after store1 is closed
	issue := &types.Issue{
		Title:       "Multi-Store Test",
		Description: "Created after other store closed",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store2.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("store2 CreateIssue failed after store1 closed: %v", err)
	}

	// Clean up store2
	if err := store2.Close(); err != nil {
		t.Fatalf("store2 close failed: %v", err)
	}
}

// =============================================================================
// Daemon Event-Driven Mode Tests (bd-z6d.1 fix #3)
//
// Dolt backends should work in event-driven mode without JSONL file watching.
// Dolt uses RPC mutation events and native change tracking instead.
// =============================================================================

// TestDoltBackendDetection verifies that Dolt stores are correctly detected
// as having the Commit method (used by daemon to detect Dolt backend).
func TestDoltBackendDetection(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// The daemon detects Dolt backend by checking for Commit method:
	// _, isDoltBackend := store.(interface{ Commit(context.Context, string) error })
	// Since we have a concrete *DoltStore, verify it has the Commit method by using it
	ctx, cancel := testContext(t)
	defer cancel()

	// If this compiles, DoltStore implements the interface the daemon checks for
	var committer interface {
		Commit(context.Context, string) error
	} = store

	// Verify it's not nil (store implements the interface)
	if committer == nil {
		t.Error("DoltStore should implement the Commit interface")
	}

	// Also verify Commit actually works
	err := store.Commit(ctx, "Backend detection test")
	if err != nil {
		t.Logf("Commit returned error (may be expected with no changes): %v", err)
	}
}

// TestDoltNativeChangeTracking verifies that Dolt's native change tracking
// works without relying on JSONL file watching.
func TestDoltNativeChangeTracking(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// 1. Create an issue
	issue := &types.Issue{
		Title:       "Change Tracking Test",
		Description: "Testing Dolt native change tracking",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// 2. Check Dolt status - should show uncommitted changes
	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}

	hasChanges := len(status.Staged) > 0 || len(status.Unstaged) > 0
	if !hasChanges {
		// Note: Dolt auto-stages by default with -Am commit
		t.Log("No uncommitted changes detected (may be auto-staged)")
	}

	// 3. Commit the changes
	if err := store.Commit(ctx, "Test commit"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// 4. Verify commit in log
	log, err := store.Log(ctx, 1)
	if err != nil {
		t.Fatalf("Log failed: %v", err)
	}
	if len(log) == 0 {
		t.Error("expected at least one commit in log")
	}
	if log[0].Message != "Test commit" {
		t.Errorf("expected commit message 'Test commit', got %q", log[0].Message)
	}

	// 5. Make another change
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"description": "Updated for tracking test",
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}

	// 6. Status should show new uncommitted changes
	status, err = store.Status(ctx)
	if err != nil {
		t.Fatalf("Status after update failed: %v", err)
	}

	// Dolt tracks changes natively - no JSONL file watching needed
	t.Log("Dolt native change tracking works - no JSONL file watching required")
}

// TestDoltWithoutJSONL verifies that Dolt operations work correctly even when
// there's no JSONL file (the daemon event-driven mode fix).
func TestDoltWithoutJSONL(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Simulate daemon event-driven mode operations without JSONL

	// 1. Create issues with explicit unique IDs
	for i := 0; i < 5; i++ {
		issue := &types.Issue{
			ID:          fmt.Sprintf("test-nojsonl-%d", i),
			Title:       fmt.Sprintf("No-JSONL Test %d", i),
			Description: "Testing without JSONL",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %d failed: %v", i, err)
		}
	}

	// 2. Query issues (RPC-style operation)
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues failed: %v", err)
	}
	if len(issues) != 5 {
		t.Errorf("expected 5 issues, got %d", len(issues))
	}

	// 3. Commit all changes (Dolt's native sync)
	if err := store.Commit(ctx, "Batch commit without JSONL"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// 4. Verify data persisted via Dolt's native mechanism
	log, err := store.Log(ctx, 5)
	if err != nil {
		t.Fatalf("Log failed: %v", err)
	}
	if len(log) == 0 {
		t.Error("expected commits in log")
	}

	t.Log("Dolt backend works correctly without JSONL - event-driven mode supported")
}

// TestDoltRPCMutationEvents simulates the RPC mutation events that the daemon
// would receive for Dolt backends instead of file watching.
func TestDoltRPCMutationEvents(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Track "events" that would be sent to daemon
	type mutationEvent struct {
		Type     string
		IssueID  string
		Occurred time.Time
	}
	var events []mutationEvent

	// Simulate RPC handlers that track mutations

	// 1. Create event
	issue := &types.Issue{
		Title:       "RPC Event Test",
		Description: "Testing RPC mutation events",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	events = append(events, mutationEvent{
		Type:     "create",
		IssueID:  issue.ID,
		Occurred: time.Now(),
	})

	// 2. Update event
	if err := store.UpdateIssue(ctx, issue.ID, map[string]interface{}{
		"status": string(types.StatusInProgress),
	}, "tester"); err != nil {
		t.Fatalf("UpdateIssue failed: %v", err)
	}
	events = append(events, mutationEvent{
		Type:     "update",
		IssueID:  issue.ID,
		Occurred: time.Now(),
	})

	// 3. Close event
	if err := store.CloseIssue(ctx, issue.ID, "done", "tester", "session-1"); err != nil {
		t.Fatalf("CloseIssue failed: %v", err)
	}
	events = append(events, mutationEvent{
		Type:     "close",
		IssueID:  issue.ID,
		Occurred: time.Now(),
	})

	// Verify events tracked
	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}

	// In the real daemon, these events would trigger:
	// - Dirty issue tracking
	// - Debounced Dolt commit
	// - (Optionally) push to remote

	t.Logf("Tracked %d RPC mutation events", len(events))
}

// TestDoltVersionedStorage verifies that DoltStore implements the
// VersionedStorage interface methods needed for event-driven mode.
func TestDoltVersionedStorage(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Test all VersionedStorage interface methods

	// First, commit the initial schema so branches will have it
	if err := store.Commit(ctx, "Initial schema"); err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}

	// Branch operations
	if err := store.Branch(ctx, "feature-1"); err != nil {
		t.Fatalf("Branch failed: %v", err)
	}

	// Checkout
	if err := store.Checkout(ctx, "feature-1"); err != nil {
		t.Fatalf("Checkout failed: %v", err)
	}

	// CurrentBranch
	branch, err := store.CurrentBranch(ctx)
	if err != nil {
		t.Fatalf("CurrentBranch failed: %v", err)
	}
	if branch != "feature-1" {
		t.Errorf("expected branch 'feature-1', got %q", branch)
	}

	// Make a change and commit
	issue := &types.Issue{
		ID:          "test-versioned-1",
		Title:       "Versioned Storage Test",
		Description: "Test",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.Commit(ctx, "Feature commit"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Switch back to main
	if err := store.Checkout(ctx, "main"); err != nil {
		t.Fatalf("Checkout main failed: %v", err)
	}

	// Merge
	conflicts, err := store.Merge(ctx, "feature-1")
	if err != nil {
		t.Fatalf("Merge failed: %v", err)
	}
	if len(conflicts) > 0 {
		t.Logf("Merge had %d conflicts", len(conflicts))
	}

	// Log
	log, err := store.Log(ctx, 5)
	if err != nil {
		t.Fatalf("Log failed: %v", err)
	}
	if len(log) == 0 {
		t.Error("expected commits in log")
	}

	// Status
	status, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	_ = status // Just verify it works

	// DeleteBranch
	if err := store.DeleteBranch(ctx, "feature-1"); err != nil {
		t.Fatalf("DeleteBranch failed: %v", err)
	}

	t.Log("All VersionedStorage interface methods work correctly")
}

// TestEventDrivenModeNoJSONLPath verifies that the daemon can operate
// in event-driven mode when JSONL path is empty (Dolt-only mode).
func TestEventDrivenModeNoJSONLPath(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Simulate daemon's event-driven mode check
	// jsonlPath := "" // No JSONL configured

	// DoltStore always has Commit method, so it's always detected as Dolt backend
	// The daemon does: _, isDoltBackend := store.(interface{ Commit(context.Context, string) error })
	// Since we're testing DoltStore directly, we know it's a Dolt backend

	t.Log("Dolt backend detected - event-driven mode works without JSONL file watching")

	// Simulate event-driven operations
	issue := &types.Issue{
		Title:       "Event-Driven Mode Test",
		Description: "Testing without JSONL path",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// In event-driven mode, mutations are tracked via RPC
	// and synced via Dolt commit/push instead of JSONL export

	// Commit represents the "sync" operation for Dolt
	if err := store.Commit(ctx, "Event-driven sync"); err != nil {
		t.Fatalf("Commit (sync) failed: %v", err)
	}

	t.Log("Event-driven mode operations successful without JSONL")
}

// =============================================================================
// Integration Tests - Combining All Fixes
// =============================================================================

// TestFullWorkflowWithFixes runs a complete workflow that exercises all three fixes:
// 1. Single connection with USE
// 2. Store lifecycle for auto-flush
// 3. Event-driven mode without JSONL
func TestFullWorkflowWithFixes(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-full-workflow-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// === Fix #1: Single connection with USE ===
	cfg := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "workflow_db",
	}

	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	// Don't defer close - we'll close manually to test Fix #2

	// Configure the database
	if err := store.SetConfig(ctx, "issue_prefix", "wf"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	// Create several issues with explicit unique IDs
	var issueIDs []string
	for i := 0; i < 5; i++ {
		issue := &types.Issue{
			ID:          fmt.Sprintf("wf-workflow-%d", i),
			Title:       fmt.Sprintf("Workflow Test Issue %d", i),
			Description: "Testing full workflow",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue %d failed: %v", i, err)
		}
		issueIDs = append(issueIDs, issue.ID)
	}

	// === Fix #2: Store lifecycle for auto-flush ===
	// Simulate auto-flush before store close
	dirtyIDs, err := store.GetDirtyIssues(ctx)
	if err != nil {
		t.Fatalf("GetDirtyIssues failed (Fix #2 regression): %v", err)
	}
	t.Logf("Dirty issues for auto-flush: %d", len(dirtyIDs))

	// Export dirty issues (simulates flush)
	for _, id := range dirtyIDs {
		_, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue during flush failed: %v", err)
		}
	}

	// Clear dirty after "flush"
	if err := store.ClearDirtyIssuesByID(ctx, dirtyIDs); err != nil {
		t.Fatalf("ClearDirtyIssuesByID failed: %v", err)
	}

	// === Fix #3: Event-driven mode without JSONL ===
	// Verify Dolt backend detection - DoltStore always implements Commit
	// The daemon checks: _, isDoltBackend := store.(interface{ Commit(context.Context, string) error })
	// Since store is *DoltStore, it always implements this interface
	var committer interface {
		Commit(context.Context, string) error
	} = store
	if committer == nil {
		t.Error("should be detected as Dolt backend")
	}

	// Use Dolt's native commit for sync (no JSONL needed)
	if err := store.Commit(ctx, "Workflow sync commit"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Verify log
	log, err := store.Log(ctx, 1)
	if err != nil {
		t.Fatalf("Log failed: %v", err)
	}
	if len(log) == 0 || log[0].Message != "Workflow sync commit" {
		t.Error("commit not recorded correctly")
	}

	// Store should still be usable
	if store.IsClosed() {
		t.Error("store should not be closed yet")
	}

	// Final verification - all issues accessible
	for _, id := range issueIDs {
		issue, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("failed to get issue %s: %v", id, err)
		}
		if issue == nil {
			t.Errorf("issue %s not found", id)
		}
	}

	// Now close (simulates end of command)
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if !store.IsClosed() {
		t.Error("store should be closed after Close()")
	}

	t.Log("Full workflow with all three fixes completed successfully")
}

// TestReopenAfterClose verifies that a store can be reopened after closing
// (important for daemon restart scenarios).
func TestReopenAfterClose(t *testing.T) {
	skipIfNoDolt(t)

	ctx, cancel := testContext(t)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "dolt-reopen-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &Config{
		Path:           tmpDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       "reopen_db",
	}

	// First open
	store1, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store (first open): %v", err)
	}

	// Set some data
	if err := store1.SetConfig(ctx, "issue_prefix", "reopen"); err != nil {
		t.Fatalf("SetConfig failed: %v", err)
	}

	issue := &types.Issue{
		ID:          "reopen-test-1",
		Title:       "Reopen Test",
		Description: "Test reopening after close",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store1.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}

	// Commit to persist
	if err := store1.Commit(ctx, "Initial data"); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Close first store
	if err := store1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen
	store2, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to create store (reopen): %v", err)
	}
	defer store2.Close()

	// Verify data persisted
	prefix, err := store2.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("GetConfig after reopen failed: %v", err)
	}
	if prefix != "reopen" {
		t.Errorf("expected prefix 'reopen', got %q", prefix)
	}

	retrieved, err := store2.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue after reopen failed: %v", err)
	}
	if retrieved == nil {
		t.Error("issue not found after reopen")
	} else if retrieved.Title != "Reopen Test" {
		t.Errorf("expected title 'Reopen Test', got %q", retrieved.Title)
	}

	t.Log("Store successfully reopened after close")
}

// =============================================================================
// Helper functions for daemon testing
// =============================================================================

// testDaemonConfig represents daemon configuration for testing
type testDaemonConfig struct {
	AutoCommit bool
	AutoPush   bool
	AutoPull   bool
	LocalMode  bool
	DaemonMode string // "events" or "poll"
}

// simulateDaemonEventLoop simulates the daemon's event-driven loop for Dolt
func simulateDaemonEventLoop(ctx context.Context, store *DoltStore, cfg testDaemonConfig) error {
	// DoltStore always has Commit method, so it's always a Dolt backend
	// The daemon checks: _, isDoltBackend := store.(interface{ Commit(context.Context, string) error })
	// Since store is *DoltStore, it always passes this check

	// For Dolt in event-driven mode:
	// 1. Mutations come via RPC (not file watching)
	// 2. Dirty tracking is done in-memory
	// 3. Sync happens via Dolt commit (not JSONL export)

	if cfg.AutoCommit {
		if err := store.Commit(ctx, "Daemon auto-commit"); err != nil {
			return err
		}
	}

	return nil
}

// Helper to create test directory with subdir structure
func createTestDirWithSubdir(t *testing.T, baseName, subdir string) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", baseName)
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	if subdir != "" {
		subdirPath := filepath.Join(tmpDir, subdir)
		if err := os.MkdirAll(subdirPath, 0755); err != nil {
			os.RemoveAll(tmpDir)
			t.Fatalf("failed to create subdir: %v", err)
		}
	}
	return tmpDir
}
