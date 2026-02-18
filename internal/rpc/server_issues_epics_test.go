package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestUpdatesFromArgs_DueAt verifies that DueAt is extracted from UpdateArgs
// and included in the updates map for the storage layer.
//
// This test is a TRACER BULLET for GH#952 Issue 1: Daemon ignoring --due flag.
// Gap 1: updatesFromArgs() handles 19 fields but DueAt/DeferUntil are MISSING.
//
// Expected behavior: When UpdateArgs.DueAt contains an RFC3339 date string,
// it should be parsed and added to the updates map as a time.Time value.
func TestUpdatesFromArgs_DueAt(t *testing.T) {
	tests := map[string]struct {
		input    string // ISO date or RFC3339 format
		wantKey  string
		wantTime bool // if true, expect time.Time value; if false, expect nil
	}{
		"RFC3339 with timezone": {
			input:    "2026-01-15T10:00:00Z",
			wantKey:  "due_at",
			wantTime: true,
		},
		"ISO date only": {
			input:    "2026-01-15",
			wantKey:  "due_at",
			wantTime: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			args := UpdateArgs{
				ID:    "test-issue",
				DueAt: &tt.input,
			}

			updates, err := updatesFromArgs(args)
			if err != nil {
				t.Fatalf("updatesFromArgs returned error: %v", err)
			}

			val, exists := updates[tt.wantKey]
			if !exists {
				t.Fatalf("updatesFromArgs did not include %q key; got keys: %v", tt.wantKey, mapKeys(updates))
			}

			if tt.wantTime {
				if _, ok := val.(time.Time); !ok {
					t.Errorf("expected time.Time value for %q, got %T: %v", tt.wantKey, val, val)
				}
			}
		})
	}
}

// TestUpdatesFromArgs_DeferUntil verifies that DeferUntil is extracted from UpdateArgs
// and included in the updates map for the storage layer.
//
// This test is a TRACER BULLET for GH#952 Issue 1: Daemon ignoring --defer flag.
// Gap 1: updatesFromArgs() handles 19 fields but DueAt/DeferUntil are MISSING.
//
// Expected behavior: When UpdateArgs.DeferUntil contains an RFC3339 date string,
// it should be parsed and added to the updates map as a time.Time value.
func TestUpdatesFromArgs_DeferUntil(t *testing.T) {
	tests := map[string]struct {
		input    string // ISO date or RFC3339 format
		wantKey  string
		wantTime bool
	}{
		"RFC3339 with timezone": {
			input:    "2026-01-20T14:30:00Z",
			wantKey:  "defer_until",
			wantTime: true,
		},
		"ISO date only": {
			input:    "2026-01-20",
			wantKey:  "defer_until",
			wantTime: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			args := UpdateArgs{
				ID:         "test-issue",
				DeferUntil: &tt.input,
			}

			updates, err := updatesFromArgs(args)
			if err != nil {
				t.Fatalf("updatesFromArgs returned error: %v", err)
			}

			val, exists := updates[tt.wantKey]
			if !exists {
				t.Fatalf("updatesFromArgs did not include %q key; got keys: %v", tt.wantKey, mapKeys(updates))
			}

			if tt.wantTime {
				if _, ok := val.(time.Time); !ok {
					t.Errorf("expected time.Time value for %q, got %T: %v", tt.wantKey, val, val)
				}
			}
		})
	}
}

// TestUpdatesFromArgs_ClearFields verifies that empty strings clear date fields.
//
// This test is a TRACER BULLET for GH#952: verifying that undefer works.
// When an empty string is passed for DueAt or DeferUntil, it should result in
// a nil value in the updates map, which will clear the field in the database.
//
// Expected behavior: Empty string input should set the field to nil in updates map.
func TestUpdatesFromArgs_ClearFields(t *testing.T) {
	tests := map[string]struct {
		setupArgs func() UpdateArgs
		wantKey   string
	}{
		"clear due_at with empty string": {
			setupArgs: func() UpdateArgs {
				empty := ""
				return UpdateArgs{
					ID:    "test-issue",
					DueAt: &empty,
				}
			},
			wantKey: "due_at",
		},
		"clear defer_until with empty string": {
			setupArgs: func() UpdateArgs {
				empty := ""
				return UpdateArgs{
					ID:         "test-issue",
					DeferUntil: &empty,
				}
			},
			wantKey: "defer_until",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			args := tt.setupArgs()

			updates, err := updatesFromArgs(args)
			if err != nil {
				t.Fatalf("updatesFromArgs returned error: %v", err)
			}

			val, exists := updates[tt.wantKey]
			if !exists {
				t.Fatalf("updatesFromArgs did not include %q key for clearing; got keys: %v", tt.wantKey, mapKeys(updates))
			}

			// When clearing, value should be nil (not an empty string)
			if val != nil {
				t.Errorf("expected nil value for clearing %q, got %T: %v", tt.wantKey, val, val)
			}
		})
	}
}

// TestHandleCreate_DeferUntil verifies that DeferUntil is parsed and set in handleCreate.
//
// This test is a TRACER BULLET for GH#952 Issue 1: Daemon ignoring --defer in create.
// Gap 2: handleCreate() parses DueAt (lines 224-239) but NOT DeferUntil.
//
// Expected behavior: When CreateArgs.DeferUntil contains an ISO date or RFC3339 string,
// it should be parsed and set on the created issue's DeferUntil field.
func TestHandleCreate_DeferUntil(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	tests := map[string]struct {
		deferUntil string
		wantSet    bool // true if DeferUntil should be set on the issue
	}{
		"RFC3339 format": {
			deferUntil: "2026-01-20T14:30:00Z",
			wantSet:    true,
		},
		"ISO date format": {
			deferUntil: "2026-01-20",
			wantSet:    true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			createArgs := &CreateArgs{
				Title:      "Test issue with defer - " + name,
				IssueType:  "task",
				Priority:   1,
				DeferUntil: tt.deferUntil,
			}

			resp, err := client.Create(createArgs)
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}
			if !resp.Success {
				t.Fatalf("Create returned error: %s", resp.Error)
			}

			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err != nil {
				t.Fatalf("Failed to unmarshal issue: %v", err)
			}

			if tt.wantSet {
				if issue.DeferUntil == nil {
					t.Error("expected DeferUntil to be set, got nil")
				}
			}
		})
	}
}

// TestUpdateViaDaemon_DueAt tests end-to-end update of DueAt through the daemon RPC.
//
// This test verifies that `bd update --due` works via daemon mode.
// It creates an issue, updates it with a due date via RPC, and verifies
// the due date was actually persisted.
func TestUpdateViaDaemon_DueAt(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue without due date
	createArgs := &CreateArgs{
		Title:     "Issue for due date update test",
		IssueType: "task",
		Priority:  1,
	}

	createResp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	if err := json.Unmarshal(createResp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal issue: %v", err)
	}

	// Update with due date via daemon RPC
	dueDate := "2026-01-25"
	updateArgs := &UpdateArgs{
		ID:    issue.ID,
		DueAt: &dueDate,
	}

	updateResp, err := client.Update(updateArgs)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if !updateResp.Success {
		t.Fatalf("Update returned error: %s", updateResp.Error)
	}

	// Verify directly from storage
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}

	if retrieved.DueAt == nil {
		t.Fatal("expected DueAt to be set after update, got nil")
	}

	// Verify the date is correct (just check the date part)
	expectedDate := time.Date(2026, 1, 25, 0, 0, 0, 0, time.Local)
	if retrieved.DueAt.Year() != expectedDate.Year() ||
		retrieved.DueAt.Month() != expectedDate.Month() ||
		retrieved.DueAt.Day() != expectedDate.Day() {
		t.Errorf("DueAt date mismatch: got %v, want date 2026-01-25", retrieved.DueAt)
	}
}

// TestUpdateViaDaemon_DeferUntil tests end-to-end update of DeferUntil through the daemon RPC.
//
// This test verifies that `bd update --defer` and `bd defer --until` work via daemon mode.
func TestUpdateViaDaemon_DeferUntil(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue without defer_until
	createArgs := &CreateArgs{
		Title:     "Issue for defer update test",
		IssueType: "task",
		Priority:  1,
	}

	createResp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	if err := json.Unmarshal(createResp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal issue: %v", err)
	}

	// Update with defer_until via daemon RPC
	deferDate := "2026-01-30"
	updateArgs := &UpdateArgs{
		ID:         issue.ID,
		DeferUntil: &deferDate,
	}

	updateResp, err := client.Update(updateArgs)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if !updateResp.Success {
		t.Fatalf("Update returned error: %s", updateResp.Error)
	}

	// Verify directly from storage
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}

	if retrieved.DeferUntil == nil {
		t.Fatal("expected DeferUntil to be set after update, got nil")
	}

	// Verify the date is correct
	expectedDate := time.Date(2026, 1, 30, 0, 0, 0, 0, time.Local)
	if retrieved.DeferUntil.Year() != expectedDate.Year() ||
		retrieved.DeferUntil.Month() != expectedDate.Month() ||
		retrieved.DeferUntil.Day() != expectedDate.Day() {
		t.Errorf("DeferUntil date mismatch: got %v, want date 2026-01-30", retrieved.DeferUntil)
	}
}

// TestUndefer_ClearsDeferUntil tests that undefer clears the defer_until field via daemon.
//
// This verifies SC-005: `bd undefer` clears defer_until via daemon.
func TestUndefer_ClearsDeferUntil(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create an issue with defer_until set
	createArgs := &CreateArgs{
		Title:      "Issue to undefer",
		IssueType:  "task",
		Priority:   1,
		DeferUntil: "2026-02-15",
	}

	createResp, err := client.Create(createArgs)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var issue types.Issue
	if err := json.Unmarshal(createResp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal issue: %v", err)
	}

	// Verify defer_until was set on create
	retrieved, err := store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue: %v", err)
	}
	if retrieved.DeferUntil == nil {
		t.Log("WARNING: DeferUntil not set on create - Gap 2 not yet fixed")
		// Set it directly for this test
		deferTime := time.Date(2026, 2, 15, 0, 0, 0, 0, time.Local)
		updates := map[string]interface{}{"defer_until": deferTime}
		if err := store.UpdateIssue(ctx, issue.ID, updates, "test"); err != nil {
			t.Fatalf("Failed to set defer_until directly: %v", err)
		}
	}

	// Now clear defer_until via RPC update with empty string
	empty := ""
	updateArgs := &UpdateArgs{
		ID:         issue.ID,
		DeferUntil: &empty,
	}

	updateResp, err := client.Update(updateArgs)
	if err != nil {
		t.Fatalf("Update (undefer) failed: %v", err)
	}
	if !updateResp.Success {
		t.Fatalf("Update (undefer) returned error: %s", updateResp.Error)
	}

	// Verify defer_until was cleared
	retrieved, err = store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("Failed to get issue after undefer: %v", err)
	}

	if retrieved.DeferUntil != nil {
		t.Errorf("expected DeferUntil to be nil after undefer, got %v", retrieved.DeferUntil)
	}
}

// TestCreateWithRelativeDate tests that relative date formats like "+1d" work via daemon create.
//
// This test validates GH#952 Issue 3 fix: CLI formats relative dates as RFC3339.
// Gap 3 fix: create.go now converts "+1d", "tomorrow" etc. to RFC3339 before sending.
//
// This test simulates the fixed CLI behavior by pre-formatting relative dates.
// The daemon receives RFC3339 strings and parses them correctly.
func TestCreateWithRelativeDate(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	tests := map[string]struct {
		dueOffset   time.Duration // Duration from now for DueAt
		deferOffset time.Duration // Duration from now for DeferUntil
		wantDue     bool
		wantDefer   bool
	}{
		"relative +1d for due": {
			dueOffset: 24 * time.Hour,
			wantDue:   true,
		},
		"relative tomorrow for defer": {
			deferOffset: 24 * time.Hour,
			wantDefer:   true,
		},
		"both relative dates": {
			dueOffset:   48 * time.Hour,
			deferOffset: 24 * time.Hour,
			wantDue:     true,
			wantDefer:   true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// Simulate what create.go now does: format times as RFC3339
			var dueStr, deferStr string
			if tt.dueOffset > 0 {
				dueStr = now.Add(tt.dueOffset).Format(time.RFC3339)
			}
			if tt.deferOffset > 0 {
				deferStr = now.Add(tt.deferOffset).Format(time.RFC3339)
			}

			createArgs := &CreateArgs{
				Title:      "Issue with relative date - " + name,
				IssueType:  "task",
				Priority:   1,
				DueAt:      dueStr,
				DeferUntil: deferStr,
			}

			resp, err := client.Create(createArgs)
			if err != nil {
				t.Fatalf("Create failed: %v", err)
			}
			if !resp.Success {
				// This is expected to fail currently because the daemon doesn't parse relative dates
				t.Logf("Create returned error (expected with current bug): %s", resp.Error)
				t.Fatalf("Create failed with relative date: %s", resp.Error)
			}

			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err != nil {
				t.Fatalf("Failed to unmarshal issue: %v", err)
			}

			// Verify from storage to ensure persistence
			retrieved, err := store.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatalf("Failed to get issue: %v", err)
			}

			if tt.wantDue {
				if retrieved.DueAt == nil {
					t.Error("expected DueAt to be set from relative date, got nil")
				} else {
					// Verify it's in the future
					if retrieved.DueAt.Before(time.Now()) {
						t.Errorf("expected DueAt to be in the future, got %v", retrieved.DueAt)
					}
				}
			}

			if tt.wantDefer {
				if retrieved.DeferUntil == nil {
					t.Error("expected DeferUntil to be set from relative date, got nil")
				} else {
					// Verify it's in the future
					if retrieved.DeferUntil.Before(time.Now()) {
						t.Errorf("expected DeferUntil to be in the future, got %v", retrieved.DeferUntil)
					}
				}
			}
		})
	}
}

// mapKeys returns the keys of a map for debugging
func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestDualPathParity validates that daemon mode produces identical results to direct mode.
// This test prevents regressions like GH#952 where new fields work in direct mode but
// fail in daemon mode due to missing extraction in updatesFromArgs() or handleCreate().
//
// ADD NEW FIELDS HERE when extending the Issue type to prevent future gaps.
func TestDualPathParity(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()
	ctx := context.Background()

	now := time.Now()
	dueAt := now.Add(24 * time.Hour)
	deferUntil := now.Add(48 * time.Hour)

	t.Run("Create_DueAt", func(t *testing.T) {
		// Direct mode: set field directly on Issue struct
		directIssue := &types.Issue{
			Title:     "Direct DueAt",
			IssueType: "task",
			Priority:  1,
			Status:    types.StatusOpen,
			DueAt:     &dueAt,
			CreatedAt: now,
		}
		if err := store.CreateIssue(ctx, directIssue, "bd"); err != nil {
			t.Fatalf("Direct create failed: %v", err)
		}

		// Daemon mode: send via RPC
		resp, err := client.Create(&CreateArgs{
			Title:     "Daemon DueAt",
			IssueType: "task",
			Priority:  1,
			DueAt:     dueAt.Format(time.RFC3339),
		})
		if err != nil || !resp.Success {
			t.Fatalf("Daemon create failed: %v / %s", err, resp.Error)
		}
		var daemonIssue types.Issue
		if err := json.Unmarshal(resp.Data, &daemonIssue); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Compare persisted values
		directRetrieved, _ := store.GetIssue(ctx, directIssue.ID)
		daemonRetrieved, _ := store.GetIssue(ctx, daemonIssue.ID)

		if !compareTimePtr(t, "DueAt", directRetrieved.DueAt, daemonRetrieved.DueAt) {
			t.Error("PARITY FAILURE: DueAt differs between direct and daemon mode")
		}
	})

	t.Run("Create_DeferUntil", func(t *testing.T) {
		// Direct mode
		directIssue := &types.Issue{
			Title:      "Direct DeferUntil",
			IssueType:  "task",
			Priority:   1,
			Status:     types.StatusOpen,
			DeferUntil: &deferUntil,
			CreatedAt:  now,
		}
		if err := store.CreateIssue(ctx, directIssue, "bd"); err != nil {
			t.Fatalf("Direct create failed: %v", err)
		}

		// Daemon mode
		resp, err := client.Create(&CreateArgs{
			Title:      "Daemon DeferUntil",
			IssueType:  "task",
			Priority:   1,
			DeferUntil: deferUntil.Format(time.RFC3339),
		})
		if err != nil || !resp.Success {
			t.Fatalf("Daemon create failed: %v / %s", err, resp.Error)
		}
		var daemonIssue types.Issue
		if err := json.Unmarshal(resp.Data, &daemonIssue); err != nil {
			t.Fatalf("Failed to unmarshal: %v", err)
		}

		// Compare persisted values
		directRetrieved, _ := store.GetIssue(ctx, directIssue.ID)
		daemonRetrieved, _ := store.GetIssue(ctx, daemonIssue.ID)

		if !compareTimePtr(t, "DeferUntil", directRetrieved.DeferUntil, daemonRetrieved.DeferUntil) {
			t.Error("PARITY FAILURE: DeferUntil differs between direct and daemon mode")
		}
	})

	t.Run("Update_DueAt", func(t *testing.T) {
		// Create base issue
		issue := &types.Issue{
			Title:     "Update DueAt Test",
			IssueType: "task",
			Priority:  1,
			Status:    types.StatusOpen,
			CreatedAt: now,
		}
		if err := store.CreateIssue(ctx, issue, "bd"); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// Update via daemon — use UTC to avoid Dolt DATETIME timezone stripping.
		// Dolt DATETIME stores wall-clock digits without timezone conversion,
		// so we must send UTC to get consistent round-trip results.
		dueUTC := dueAt.UTC()
		dueStr := dueUTC.Format(time.RFC3339)
		resp, err := client.Update(&UpdateArgs{
			ID:    issue.ID,
			DueAt: &dueStr,
		})
		if err != nil || !resp.Success {
			t.Fatalf("Daemon update failed: %v / %s", err, resp.Error)
		}

		// Verify persisted
		retrieved, _ := store.GetIssue(ctx, issue.ID)
		if retrieved.DueAt == nil {
			t.Error("PARITY FAILURE: DueAt not set after daemon update")
		} else {
			gotUTC := retrieved.DueAt.UTC().Truncate(time.Second)
			wantUTC := dueUTC.Truncate(time.Second)
			if gotUTC.Sub(wantUTC).Abs() > time.Second {
				t.Errorf("PARITY FAILURE: DueAt mismatch: got %v, want %v", gotUTC, wantUTC)
			}
		}
	})

	t.Run("Update_DeferUntil", func(t *testing.T) {
		// Create base issue
		issue := &types.Issue{
			Title:     "Update DeferUntil Test",
			IssueType: "task",
			Priority:  1,
			Status:    types.StatusOpen,
			CreatedAt: now,
		}
		if err := store.CreateIssue(ctx, issue, "bd"); err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		// Update via daemon — use UTC to avoid Dolt DATETIME timezone stripping.
		// Dolt DATETIME stores wall-clock digits without timezone conversion,
		// so we must send UTC to get consistent round-trip results.
		deferUTC := deferUntil.UTC()
		deferStr := deferUTC.Format(time.RFC3339)
		resp, err := client.Update(&UpdateArgs{
			ID:         issue.ID,
			DeferUntil: &deferStr,
		})
		if err != nil || !resp.Success {
			t.Fatalf("Daemon update failed: %v / %s", err, resp.Error)
		}

		// Verify persisted
		retrieved, _ := store.GetIssue(ctx, issue.ID)
		if retrieved.DeferUntil == nil {
			t.Error("PARITY FAILURE: DeferUntil not set after daemon update")
		} else {
			gotUTC := retrieved.DeferUntil.UTC().Truncate(time.Second)
			wantUTC := deferUTC.Truncate(time.Second)
			if gotUTC.Sub(wantUTC).Abs() > time.Second {
				t.Errorf("PARITY FAILURE: DeferUntil mismatch: got %v, want %v", gotUTC, wantUTC)
			}
		}
	})

	// ADD NEW FIELD PARITY TESTS HERE when extending Issue type
}

// compareTimePtr compares two time pointers with 1-second tolerance.
// Both times are normalized to UTC and truncated to seconds before comparison,
// because Dolt DATETIME strips timezone info and subsecond precision.
func compareTimePtr(t *testing.T, name string, direct, daemon *time.Time) bool {
	if (direct == nil) != (daemon == nil) {
		t.Errorf("%s nil mismatch: direct=%v, daemon=%v", name, direct, daemon)
		return false
	}
	if direct != nil && daemon != nil {
		// Normalize both to UTC and truncate to second for comparison,
		// since Dolt DATETIME strips timezone and subsecond precision.
		dUTC := direct.UTC().Truncate(time.Second)
		daemonUTC := daemon.UTC().Truncate(time.Second)
		if dUTC.Sub(daemonUTC).Abs() > time.Second {
			t.Errorf("%s value mismatch: direct=%v (UTC: %v), daemon=%v (UTC: %v)", name, *direct, dUTC, *daemon, daemonUTC)
			return false
		}
	}
	return true
}

// TestHandleListWatch verifies the list_watch RPC handler (bd-la75).
// It tests:
// - Initial fetch returns issues immediately when Since=0
// - LastMutationMs is set to a valid timestamp
// - Filter parameters are properly applied
func TestHandleListWatch(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	ctx := context.Background()

	// Create some test issues
	for i := 0; i < 3; i++ {
		createArgs := &CreateArgs{
			Title:     "Test issue " + string(rune('A'+i)),
			IssueType: "task",
			Priority:  2,
		}
		_, err := client.Create(createArgs)
		if err != nil {
			t.Fatalf("failed to create test issue %d: %v", i, err)
		}
	}

	t.Run("initial fetch returns issues immediately", func(t *testing.T) {
		args := &ListWatchArgs{
			Since:     0, // Initial fetch
			TimeoutMs: 100,
		}
		result, err := client.ListWatch(args)
		if err != nil {
			t.Fatalf("ListWatch failed: %v", err)
		}

		if len(result.Issues) < 3 {
			t.Errorf("expected at least 3 issues, got %d", len(result.Issues))
		}

		if result.LastMutationMs == 0 {
			t.Error("expected LastMutationMs to be set")
		}
	})

	t.Run("filter by status", func(t *testing.T) {
		args := &ListWatchArgs{
			Since:     0,
			TimeoutMs: 100,
			Status:    "open",
		}
		result, err := client.ListWatch(args)
		if err != nil {
			t.Fatalf("ListWatch with status filter failed: %v", err)
		}

		for _, issue := range result.Issues {
			if issue.Status != types.StatusOpen {
				t.Errorf("expected all issues to have status open, got %s for %s", issue.Status, issue.ID)
			}
		}
	})

	t.Run("returns quickly with long timeout when Since=0", func(t *testing.T) {
		start := time.Now()
		args := &ListWatchArgs{
			Since:     0,           // Initial fetch returns immediately
			TimeoutMs: 30000,       // 30 second timeout should not be reached
		}
		_, err := client.ListWatch(args)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("ListWatch failed: %v", err)
		}

		// Should return quickly (within 1 second) since Since=0
		if elapsed > time.Second {
			t.Errorf("initial fetch took too long: %v", elapsed)
		}
	})

	_ = ctx // silence unused warning
}

// TestEpicOverview_WithChildren verifies that EpicOverview returns open epics
// with their children, assignees, and blocker information via daemon RPC.
func TestEpicOverview_WithChildren(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()
	ctx := context.Background()

	// Create an epic
	epicResp, err := client.Create(&CreateArgs{
		Title:     "Test Epic Overview",
		IssueType: "epic",
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("Create epic failed: %v", err)
	}
	var epic types.Issue
	if err := json.Unmarshal(epicResp.Data, &epic); err != nil {
		t.Fatalf("Unmarshal epic: %v", err)
	}

	// Create children with varying statuses
	child1Resp, _ := client.Create(&CreateArgs{Title: "Open child", IssueType: "task", Priority: 2})
	var child1 types.Issue
	json.Unmarshal(child1Resp.Data, &child1)

	child2Resp, _ := client.Create(&CreateArgs{Title: "In-progress child", IssueType: "task", Priority: 2})
	var child2 types.Issue
	json.Unmarshal(child2Resp.Data, &child2)

	child3Resp, _ := client.Create(&CreateArgs{Title: "Closed child", IssueType: "task", Priority: 2})
	var child3 types.Issue
	json.Unmarshal(child3Resp.Data, &child3)

	// Set child2 to in_progress
	ipStatus := "in_progress"
	client.Update(&UpdateArgs{ID: child2.ID, Status: &ipStatus})

	// Set child2's assignee
	assignee := "alice"
	client.Update(&UpdateArgs{ID: child2.ID, Assignee: &assignee})

	// Close child3
	client.CloseIssue(&CloseArgs{ID: child3.ID, Reason: "done"})

	// Link children to epic via parent-child dependencies
	for _, childID := range []string{child1.ID, child2.ID, child3.ID} {
		dep := &types.Dependency{
			IssueID:     childID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("AddDependency failed for %s: %v", childID, err)
		}
	}

	// Call EpicOverview
	resp, err := client.EpicOverview(&EpicOverviewArgs{})
	if err != nil {
		t.Fatalf("EpicOverview failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("EpicOverview returned error: %s", resp.Error)
	}

	var overviews []*types.EpicOverview
	if err := json.Unmarshal(resp.Data, &overviews); err != nil {
		t.Fatalf("Unmarshal overviews: %v", err)
	}

	// Find our epic in the results
	var found *types.EpicOverview
	for _, ov := range overviews {
		if ov.Epic.ID == epic.ID {
			found = ov
			break
		}
	}
	if found == nil {
		t.Fatalf("Epic %s not found in overview results", epic.ID)
	}

	// Verify counts
	if found.TotalChildren != 3 {
		t.Errorf("Expected 3 total children, got %d", found.TotalChildren)
	}
	if found.ClosedChildren != 1 {
		t.Errorf("Expected 1 closed child, got %d", found.ClosedChildren)
	}

	// Verify children are present
	if len(found.Children) != 3 {
		t.Errorf("Expected 3 children, got %d", len(found.Children))
	}

	// Find in-progress child and verify assignee
	for _, child := range found.Children {
		if child.Issue.ID == child2.ID {
			if child.Issue.Assignee != "alice" {
				t.Errorf("Expected assignee 'alice' on in-progress child, got %q", child.Issue.Assignee)
			}
			if child.Issue.Status != types.StatusInProgress {
				t.Errorf("Expected in_progress status, got %s", child.Issue.Status)
			}
		}
	}
}

// TestEpicOverview_Empty verifies that EpicOverview returns nil/empty when there are no open epics.
func TestEpicOverview_Empty(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	resp, err := client.EpicOverview(&EpicOverviewArgs{})
	if err != nil {
		t.Fatalf("EpicOverview failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("EpicOverview returned error: %s", resp.Error)
	}

	var overviews []*types.EpicOverview
	if err := json.Unmarshal(resp.Data, &overviews); err != nil {
		t.Fatalf("Unmarshal overviews: %v", err)
	}

	if len(overviews) != 0 {
		t.Errorf("Expected 0 overviews for empty database, got %d", len(overviews))
	}
}

// TestEpicOverview_ExcludesClosedEpics verifies that closed epics are not included.
func TestEpicOverview_ExcludesClosedEpics(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()
	ctx := context.Background()

	// Create and close an epic
	epicResp, _ := client.Create(&CreateArgs{Title: "Closed Epic", IssueType: "epic", Priority: 2})
	var epic types.Issue
	json.Unmarshal(epicResp.Data, &epic)

	// Add a child so it has children (otherwise it won't show up even if open)
	childResp, _ := client.Create(&CreateArgs{Title: "Child", IssueType: "task", Priority: 2})
	var child types.Issue
	json.Unmarshal(childResp.Data, &child)

	store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: epic.ID, Type: types.DepParentChild,
	}, "test")

	// Close the epic
	client.CloseIssue(&CloseArgs{ID: epic.ID, Reason: "all done"})

	// Verify overview does not include the closed epic
	resp, err := client.EpicOverview(&EpicOverviewArgs{})
	if err != nil {
		t.Fatalf("EpicOverview failed: %v", err)
	}

	var overviews []*types.EpicOverview
	json.Unmarshal(resp.Data, &overviews)

	for _, ov := range overviews {
		if ov.Epic.ID == epic.ID {
			t.Errorf("Closed epic %s should not appear in overview", epic.ID)
		}
	}
}

// TestEpicOverview_WithBlockers verifies that blocker information is included for children.
func TestEpicOverview_WithBlockers(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()
	ctx := context.Background()

	// Create epic
	epicResp, _ := client.Create(&CreateArgs{Title: "Blocker Epic", IssueType: "epic", Priority: 2})
	var epic types.Issue
	json.Unmarshal(epicResp.Data, &epic)

	// Create blocker and blocked child
	blockerResp, _ := client.Create(&CreateArgs{Title: "Blocker task", IssueType: "task", Priority: 1})
	var blocker types.Issue
	json.Unmarshal(blockerResp.Data, &blocker)

	blockedResp, _ := client.Create(&CreateArgs{Title: "Blocked child", IssueType: "task", Priority: 2})
	var blocked types.Issue
	json.Unmarshal(blockedResp.Data, &blocked)

	// Link child to epic
	store.AddDependency(ctx, &types.Dependency{
		IssueID: blocked.ID, DependsOnID: epic.ID, Type: types.DepParentChild,
	}, "test")

	// Add blocking dependency: blocker blocks blocked
	store.AddDependency(ctx, &types.Dependency{
		IssueID: blocked.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "test")

	resp, err := client.EpicOverview(&EpicOverviewArgs{})
	if err != nil {
		t.Fatalf("EpicOverview failed: %v", err)
	}

	var overviews []*types.EpicOverview
	json.Unmarshal(resp.Data, &overviews)

	var found *types.EpicOverview
	for _, ov := range overviews {
		if ov.Epic.ID == epic.ID {
			found = ov
			break
		}
	}
	if found == nil {
		t.Fatalf("Epic %s not found", epic.ID)
	}

	// Verify the blocked child has blocker info
	for _, child := range found.Children {
		if child.Issue.ID == blocked.ID {
			if len(child.BlockedBy) == 0 {
				t.Errorf("Expected blocked child to have blockers, got none")
			} else if child.BlockedBy[0] != blocker.ID {
				t.Errorf("Expected blocker %s, got %v", blocker.ID, child.BlockedBy)
			}
			return
		}
	}
	t.Errorf("Blocked child %s not found in overview children", blocked.ID)
}

// TestEpicOverview_ClosedBlockerNotShown verifies that closed blockers are NOT reported.
func TestEpicOverview_ClosedBlockerNotShown(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()
	ctx := context.Background()

	epicResp, _ := client.Create(&CreateArgs{Title: "Epic", IssueType: "epic", Priority: 2})
	var epic types.Issue
	json.Unmarshal(epicResp.Data, &epic)

	blockerResp, _ := client.Create(&CreateArgs{Title: "Resolved blocker", IssueType: "task", Priority: 1})
	var blocker types.Issue
	json.Unmarshal(blockerResp.Data, &blocker)

	childResp, _ := client.Create(&CreateArgs{Title: "Was blocked child", IssueType: "task", Priority: 2})
	var child types.Issue
	json.Unmarshal(childResp.Data, &child)

	store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: epic.ID, Type: types.DepParentChild,
	}, "test")
	store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: blocker.ID, Type: types.DepBlocks,
	}, "test")

	// Close the blocker — it should no longer appear in BlockedBy
	client.CloseIssue(&CloseArgs{ID: blocker.ID, Reason: "resolved"})

	resp, _ := client.EpicOverview(&EpicOverviewArgs{})
	var overviews []*types.EpicOverview
	json.Unmarshal(resp.Data, &overviews)

	for _, ov := range overviews {
		if ov.Epic.ID == epic.ID {
			for _, c := range ov.Children {
				if c.Issue.ID == child.ID {
					if len(c.BlockedBy) > 0 {
						t.Errorf("Expected no blockers after blocker was closed, got %v", c.BlockedBy)
					}
					return
				}
			}
		}
	}
}

// TestOrphanedChildren_ParentClosed finds children whose parent epic has been closed.
func TestOrphanedChildren_ParentClosed(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()
	ctx := context.Background()

	// Create epic and child
	epicResp, _ := client.Create(&CreateArgs{Title: "Epic to close", IssueType: "epic", Priority: 2})
	var epic types.Issue
	json.Unmarshal(epicResp.Data, &epic)

	childResp, _ := client.Create(&CreateArgs{Title: "Orphaned child", IssueType: "task", Priority: 2})
	var child types.Issue
	json.Unmarshal(childResp.Data, &child)

	store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: epic.ID, Type: types.DepParentChild,
	}, "test")

	// Close the parent epic (leaving child open → orphan)
	client.CloseIssue(&CloseArgs{ID: epic.ID, Reason: "done"})

	resp, err := client.EpicOrphanedChildren(&EpicOrphanedChildrenArgs{})
	if err != nil {
		t.Fatalf("EpicOrphanedChildren failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("EpicOrphanedChildren returned error: %s", resp.Error)
	}

	var orphans []*types.OrphanedChild
	if err := json.Unmarshal(resp.Data, &orphans); err != nil {
		t.Fatalf("Unmarshal orphans: %v", err)
	}

	// Find our orphan
	var found *types.OrphanedChild
	for _, o := range orphans {
		if o.ID == child.ID {
			found = o
			break
		}
	}
	if found == nil {
		t.Fatalf("Orphaned child %s not found in results", child.ID)
	}

	if found.Reason != "parent_closed" {
		t.Errorf("Expected reason 'parent_closed', got %q", found.Reason)
	}
	if found.ParentID != epic.ID {
		t.Errorf("Expected parent ID %s, got %s", epic.ID, found.ParentID)
	}
	if found.Title != "Orphaned child" {
		t.Errorf("Expected title 'Orphaned child', got %q", found.Title)
	}
	if found.Status != types.StatusOpen {
		t.Errorf("Expected status 'open', got %s", found.Status)
	}
}

// TestOrphanedChildren_NoOrphans verifies empty result when all parents are open.
func TestOrphanedChildren_NoOrphans(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()
	ctx := context.Background()

	// Create open epic with open child
	epicResp, _ := client.Create(&CreateArgs{Title: "Open Epic", IssueType: "epic", Priority: 2})
	var epic types.Issue
	json.Unmarshal(epicResp.Data, &epic)

	childResp, _ := client.Create(&CreateArgs{Title: "Normal child", IssueType: "task", Priority: 2})
	var child types.Issue
	json.Unmarshal(childResp.Data, &child)

	store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: epic.ID, Type: types.DepParentChild,
	}, "test")

	resp, err := client.EpicOrphanedChildren(&EpicOrphanedChildrenArgs{})
	if err != nil {
		t.Fatalf("EpicOrphanedChildren failed: %v", err)
	}

	var orphans []*types.OrphanedChild
	json.Unmarshal(resp.Data, &orphans)

	// The child should NOT be orphaned since parent is still open
	for _, o := range orphans {
		if o.ID == child.ID {
			t.Errorf("Child %s should not be orphaned — parent epic is still open", child.ID)
		}
	}
}

// TestOrphanedChildren_ClosedChildNotOrphan verifies that closed children
// are not reported as orphans even if their parent is closed.
func TestOrphanedChildren_ClosedChildNotOrphan(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()
	ctx := context.Background()

	epicResp, _ := client.Create(&CreateArgs{Title: "Closed Epic", IssueType: "epic", Priority: 2})
	var epic types.Issue
	json.Unmarshal(epicResp.Data, &epic)

	childResp, _ := client.Create(&CreateArgs{Title: "Also closed child", IssueType: "task", Priority: 2})
	var child types.Issue
	json.Unmarshal(childResp.Data, &child)

	store.AddDependency(ctx, &types.Dependency{
		IssueID: child.ID, DependsOnID: epic.ID, Type: types.DepParentChild,
	}, "test")

	// Close both epic and child
	client.CloseIssue(&CloseArgs{ID: child.ID, Reason: "done"})
	client.CloseIssue(&CloseArgs{ID: epic.ID, Reason: "done"})

	resp, _ := client.EpicOrphanedChildren(&EpicOrphanedChildrenArgs{})
	var orphans []*types.OrphanedChild
	json.Unmarshal(resp.Data, &orphans)

	for _, o := range orphans {
		if o.ID == child.ID {
			t.Errorf("Closed child %s should not be reported as orphan", child.ID)
		}
	}
}
