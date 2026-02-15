//go:build integration
// +build integration

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
	"github.com/steveyegge/beads/internal/types"
)

// setupDaemonRPCEnv creates an isolated per-test daemon with its own Dolt
// store, RPC server on an ephemeral port, and connected client.
// Cleanup (server stop, client close, temp dir removal) is automatic.
func setupDaemonRPCEnv(t *testing.T) (context.Context, *rpc.Client, storage.Storage, func()) {
	t.Helper()

	d := testdaemon.Start(t)
	client := d.Client(t)

	return context.Background(), client, d.Store, func() { /* cleanup handled by t.Cleanup */ }
}

// unmarshalIssue is a test helper to unmarshal a single issue from an RPC response.
func unmarshalIssue(t *testing.T, data json.RawMessage) types.Issue {
	t.Helper()
	var issue types.Issue
	if err := json.Unmarshal(data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal issue: %v", err)
	}
	return issue
}

// unmarshalIssues is a test helper to unmarshal a list of issues from an RPC response.
func unmarshalIssues(t *testing.T, data json.RawMessage) []types.Issue {
	t.Helper()
	var issues []types.Issue
	if err := json.Unmarshal(data, &issues); err != nil {
		t.Fatalf("Failed to unmarshal issues: %v", err)
	}
	return issues
}

// =============================================================================
// Create
// =============================================================================

func TestRPC_Create(t *testing.T) {
	_, client, _, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	resp, err := client.Create(&rpc.CreateArgs{
		Title:     "Test issue via RPC",
		IssueType: "task",
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("Create RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Create returned error: %s", resp.Error)
	}

	created := unmarshalIssue(t, resp.Data)
	if created.Title != "Test issue via RPC" {
		t.Errorf("Title = %q, want %q", created.Title, "Test issue via RPC")
	}
	if created.IssueType != "task" {
		t.Errorf("Type = %q, want %q", created.IssueType, "task")
	}
	if created.Priority != 2 {
		t.Errorf("Priority = %d, want 2", created.Priority)
	}
	if created.ID == "" {
		t.Error("Expected non-empty ID")
	}
}

func TestRPC_CreateWithDescription(t *testing.T) {
	_, client, _, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	desc := "This is a detailed description"
	resp, err := client.Create(&rpc.CreateArgs{
		Title:       "Described issue",
		IssueType:   "feature",
		Priority:    1,
		Description: desc,
	})
	if err != nil {
		t.Fatalf("Create RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Create returned error: %s", resp.Error)
	}

	created := unmarshalIssue(t, resp.Data)
	if created.Description != desc {
		t.Errorf("Description = %q, want %q", created.Description, desc)
	}
	if created.IssueType != "feature" {
		t.Errorf("Type = %q, want %q", created.IssueType, "feature")
	}
}

// =============================================================================
// Show
// =============================================================================

func TestRPC_Show(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	// Create issue directly in store
	issue := &types.Issue{
		Title:     "Show test issue",
		Status:    types.StatusOpen,
		Priority:  3,
		IssueType: types.TypeBug,
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create test issue: %v", err)
	}

	// Fetch via RPC
	resp, err := client.Show(&rpc.ShowArgs{ID: issue.ID})
	if err != nil {
		t.Fatalf("Show RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Show returned error: %s", resp.Error)
	}

	shown := unmarshalIssue(t, resp.Data)
	if shown.ID != issue.ID {
		t.Errorf("ID = %q, want %q", shown.ID, issue.ID)
	}
	if shown.Title != "Show test issue" {
		t.Errorf("Title = %q, want %q", shown.Title, "Show test issue")
	}
	if shown.IssueType != types.TypeBug {
		t.Errorf("Type = %q, want %q", shown.IssueType, types.TypeBug)
	}
}

func TestRPC_ShowNotFound(t *testing.T) {
	_, client, _, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	resp, err := client.Show(&rpc.ShowArgs{ID: "nonexistent-id"})
	if err != nil {
		t.Fatalf("Show RPC failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected Show to fail for nonexistent issue")
	}
}

// =============================================================================
// List
// =============================================================================

func TestRPC_List(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	// Create multiple issues
	for _, title := range []string{"First", "Second", "Third"} {
		issue := &types.Issue{
			Title:     title,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create issue %q: %v", title, err)
		}
	}

	resp, err := client.List(&rpc.ListArgs{})
	if err != nil {
		t.Fatalf("List RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("List returned error: %s", resp.Error)
	}

	issues := unmarshalIssues(t, resp.Data)
	if len(issues) != 3 {
		t.Errorf("List returned %d issues, want 3", len(issues))
	}
}

func TestRPC_ListWithStatusFilter(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	// Create open and closed issues
	open := &types.Issue{Title: "Open issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	closed := &types.Issue{Title: "Closed issue", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, open, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}
	if err := store.CreateIssue(ctx, closed, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}
	if err := store.CloseIssue(ctx, closed.ID, "done", "test", ""); err != nil {
		t.Fatalf("Failed to close issue: %v", err)
	}

	resp, err := client.List(&rpc.ListArgs{Status: "open"})
	if err != nil {
		t.Fatalf("List RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("List returned error: %s", resp.Error)
	}

	issues := unmarshalIssues(t, resp.Data)
	if len(issues) != 1 {
		t.Errorf("List returned %d issues, want 1 (open only)", len(issues))
	}
	if len(issues) > 0 && issues[0].Title != "Open issue" {
		t.Errorf("Title = %q, want %q", issues[0].Title, "Open issue")
	}
}

// =============================================================================
// Update
// =============================================================================

func TestRPC_Update(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	issue := &types.Issue{Title: "Before update", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	newTitle := "After update"
	newPriority := 1
	resp, err := client.Update(&rpc.UpdateArgs{
		ID:       issue.ID,
		Title:    &newTitle,
		Priority: &newPriority,
	})
	if err != nil {
		t.Fatalf("Update RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Update returned error: %s", resp.Error)
	}

	// Verify via Show
	showResp, err := client.Show(&rpc.ShowArgs{ID: issue.ID})
	if err != nil {
		t.Fatalf("Show RPC failed: %v", err)
	}
	updated := unmarshalIssue(t, showResp.Data)
	if updated.Title != "After update" {
		t.Errorf("Title = %q, want %q", updated.Title, "After update")
	}
	if updated.Priority != 1 {
		t.Errorf("Priority = %d, want 1", updated.Priority)
	}
}

func TestRPC_UpdateDescription(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	issue := &types.Issue{Title: "Desc update test", Description: "old", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	newDesc := "new description"
	resp, err := client.Update(&rpc.UpdateArgs{
		ID:          issue.ID,
		Description: &newDesc,
	})
	if err != nil {
		t.Fatalf("Update RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Update returned error: %s", resp.Error)
	}

	showResp, _ := client.Show(&rpc.ShowArgs{ID: issue.ID})
	updated := unmarshalIssue(t, showResp.Data)
	if updated.Description != "new description" {
		t.Errorf("Description = %q, want %q", updated.Description, "new description")
	}
}

// =============================================================================
// Close
// =============================================================================

func TestRPC_Close(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	issue := &types.Issue{Title: "Close me", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	resp, err := client.CloseIssue(&rpc.CloseArgs{
		ID:     issue.ID,
		Reason: "completed",
	})
	if err != nil {
		t.Fatalf("Close RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Close returned error: %s", resp.Error)
	}

	// Verify via Show
	showResp, _ := client.Show(&rpc.ShowArgs{ID: issue.ID})
	closed := unmarshalIssue(t, showResp.Data)
	if closed.Status != types.StatusClosed {
		t.Errorf("Status = %q, want %q", closed.Status, types.StatusClosed)
	}
	if closed.CloseReason != "completed" {
		t.Errorf("CloseReason = %q, want %q", closed.CloseReason, "completed")
	}
}

// =============================================================================
// Dependencies
// =============================================================================

func TestRPC_AddDependency(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	blocked := &types.Issue{Title: "Blocked", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, blocker, "test"); err != nil {
		t.Fatalf("Failed to create blocker: %v", err)
	}
	if err := store.CreateIssue(ctx, blocked, "test"); err != nil {
		t.Fatalf("Failed to create blocked: %v", err)
	}

	resp, err := client.AddDependency(&rpc.DepAddArgs{
		FromID:  blocked.ID,
		ToID:    blocker.ID,
		DepType: string(types.DepBlocks),
	})
	if err != nil {
		t.Fatalf("AddDependency RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("AddDependency returned error: %s", resp.Error)
	}

	// Verify via Show — blocked issue should have dependencies
	showResp, _ := client.Show(&rpc.ShowArgs{ID: blocked.ID})
	issue := unmarshalIssue(t, showResp.Data)
	if len(issue.Dependencies) == 0 {
		t.Error("Expected blocked issue to have dependencies")
	}
}

func TestRPC_RemoveDependency(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	blocker := &types.Issue{Title: "Blocker", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask}
	blocked := &types.Issue{Title: "Blocked", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, blocker, "test"); err != nil {
		t.Fatalf("Failed to create blocker: %v", err)
	}
	if err := store.CreateIssue(ctx, blocked, "test"); err != nil {
		t.Fatalf("Failed to create blocked: %v", err)
	}

	dep := &types.Dependency{IssueID: blocked.ID, DependsOnID: blocker.ID, Type: types.DepBlocks}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("Failed to add dependency: %v", err)
	}

	resp, err := client.RemoveDependency(&rpc.DepRemoveArgs{
		FromID: blocked.ID,
		ToID:   blocker.ID,
	})
	if err != nil {
		t.Fatalf("RemoveDependency RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("RemoveDependency returned error: %s", resp.Error)
	}

	// Verify dependency removed
	showResp, _ := client.Show(&rpc.ShowArgs{ID: blocked.ID})
	issue := unmarshalIssue(t, showResp.Data)
	if len(issue.Dependencies) != 0 {
		t.Errorf("Expected no dependencies, got %d", len(issue.Dependencies))
	}
}

// =============================================================================
// Labels
// =============================================================================

func TestRPC_AddLabel(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	issue := &types.Issue{Title: "Label test", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	resp, err := client.AddLabel(&rpc.LabelAddArgs{
		ID:    issue.ID,
		Label: "important",
	})
	if err != nil {
		t.Fatalf("AddLabel RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("AddLabel returned error: %s", resp.Error)
	}

	// Verify via Show
	showResp, _ := client.Show(&rpc.ShowArgs{ID: issue.ID})
	labeled := unmarshalIssue(t, showResp.Data)
	found := false
	for _, l := range labeled.Labels {
		if l == "important" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected label 'important' on issue, got labels: %v", labeled.Labels)
	}
}

func TestRPC_RemoveLabel(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	issue := &types.Issue{Title: "Remove label test", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}
	if err := store.AddLabel(ctx, issue.ID, "removeme", "test"); err != nil {
		t.Fatalf("Failed to add label: %v", err)
	}

	resp, err := client.RemoveLabel(&rpc.LabelRemoveArgs{
		ID:    issue.ID,
		Label: "removeme",
	})
	if err != nil {
		t.Fatalf("RemoveLabel RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("RemoveLabel returned error: %s", resp.Error)
	}

	showResp, _ := client.Show(&rpc.ShowArgs{ID: issue.ID})
	unlabeled := unmarshalIssue(t, showResp.Data)
	for _, l := range unlabeled.Labels {
		if l == "removeme" {
			t.Error("Expected label 'removeme' to be removed")
		}
	}
}

// =============================================================================
// Comments
// =============================================================================

func TestRPC_AddComment(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	issue := &types.Issue{Title: "Comment test", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	resp, err := client.AddComment(&rpc.CommentAddArgs{
		ID:     issue.ID,
		Author: "test",
		Text:   "This is a test comment",
	})
	if err != nil {
		t.Fatalf("AddComment RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("AddComment returned error: %s", resp.Error)
	}

	// Verify via ListComments
	listResp, err := client.ListComments(&rpc.CommentListArgs{ID: issue.ID})
	if err != nil {
		t.Fatalf("ListComments RPC failed: %v", err)
	}
	if !listResp.Success {
		t.Fatalf("ListComments returned error: %s", listResp.Error)
	}

	var comments []types.Comment
	if err := json.Unmarshal(listResp.Data, &comments); err != nil {
		t.Fatalf("Failed to unmarshal comments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("Expected 1 comment, got %d", len(comments))
	}
	if comments[0].Text != "This is a test comment" {
		t.Errorf("Comment text = %q, want %q", comments[0].Text, "This is a test comment")
	}
}

// =============================================================================
// Config
// =============================================================================

func TestRPC_ConfigSetAndGet(t *testing.T) {
	_, client, _, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	// Set a config value
	_, err := client.ConfigSet(&rpc.ConfigSetArgs{
		Key:   "test-key",
		Value: "test-value",
	})
	if err != nil {
		t.Fatalf("ConfigSet RPC failed: %v", err)
	}

	// Get it back
	resp, err := client.GetConfig(&rpc.GetConfigArgs{Key: "test-key"})
	if err != nil {
		t.Fatalf("GetConfig RPC failed: %v", err)
	}

	if resp.Value != "test-value" {
		t.Errorf("Config value = %q, want %q", resp.Value, "test-value")
	}
}

func TestRPC_ConfigUnset(t *testing.T) {
	_, client, _, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	// Set then unset
	_, _ = client.ConfigSet(&rpc.ConfigSetArgs{Key: "ephemeral-key", Value: "temp"})

	_, err := client.ConfigUnset(&rpc.ConfigUnsetArgs{Key: "ephemeral-key"})
	if err != nil {
		t.Fatalf("ConfigUnset RPC failed: %v", err)
	}
}

// =============================================================================
// Delete
// =============================================================================

func TestRPC_Delete(t *testing.T) {
	ctx, client, store, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	issue := &types.Issue{Title: "Delete me", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("Failed to create issue: %v", err)
	}

	resp, err := client.Delete(&rpc.DeleteArgs{
		IDs: []string{issue.ID},
	})
	if err != nil {
		t.Fatalf("Delete RPC failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Delete returned error: %s", resp.Error)
	}

	// Verify it's gone
	showResp, _ := client.Show(&rpc.ShowArgs{ID: issue.ID})
	if showResp.Success {
		t.Error("Expected Show to fail for deleted issue")
	}
}

// =============================================================================
// Health
// =============================================================================

func TestRPC_Health(t *testing.T) {
	_, client, _, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	resp, err := client.Health()
	if err != nil {
		t.Fatalf("Health RPC failed: %v", err)
	}
	if resp.Status != "healthy" {
		t.Errorf("Health status = %q, want %q", resp.Status, "healthy")
	}
}

// =============================================================================
// Create → Show → Update → Close round-trip
// =============================================================================

func TestRPC_FullLifecycle(t *testing.T) {
	_, client, _, cleanup := setupDaemonRPCEnv(t)
	defer cleanup()

	// Create
	createResp, err := client.Create(&rpc.CreateArgs{
		Title:       "Lifecycle test",
		IssueType:   "task",
		Priority:    2,
		Description: "Initial description",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	created := unmarshalIssue(t, createResp.Data)
	id := created.ID

	// Show
	showResp, err := client.Show(&rpc.ShowArgs{ID: id})
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}
	shown := unmarshalIssue(t, showResp.Data)
	if shown.Title != "Lifecycle test" {
		t.Errorf("Title after create = %q", shown.Title)
	}

	// Update status to in_progress
	inProgress := "in_progress"
	_, err = client.Update(&rpc.UpdateArgs{ID: id, Status: &inProgress})
	if err != nil {
		t.Fatalf("Update status failed: %v", err)
	}

	// Verify status change
	showResp, _ = client.Show(&rpc.ShowArgs{ID: id})
	shown = unmarshalIssue(t, showResp.Data)
	if shown.Status != types.StatusInProgress {
		t.Errorf("Status = %q, want %q", shown.Status, types.StatusInProgress)
	}

	// Close
	closeResp, err := client.CloseIssue(&rpc.CloseArgs{ID: id, Reason: "done"})
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !closeResp.Success {
		t.Fatalf("Close returned error: %s", closeResp.Error)
	}

	// Verify closed
	showResp, _ = client.Show(&rpc.ShowArgs{ID: id})
	shown = unmarshalIssue(t, showResp.Data)
	if shown.Status != types.StatusClosed {
		t.Errorf("Final status = %q, want %q", shown.Status, types.StatusClosed)
	}
}
