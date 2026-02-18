package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestEpicCommand(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	sqliteStore := newTestStore(t, testDB)
	ctx := context.Background()

	// Create an epic with children
	epic := &types.Issue{
		ID:          "test-epic-1",
		Title:       "Test Epic",
		Description: "Epic description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   time.Now(),
	}

	if err := sqliteStore.CreateIssue(ctx, epic, "test"); err != nil {
		t.Fatal(err)
	}

	// Create child tasks
	child1 := &types.Issue{
		Title:     "Child Task 1",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
		ClosedAt:  ptrTime(time.Now()),
	}

	child2 := &types.Issue{
		Title:     "Child Task 2",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	if err := sqliteStore.CreateIssue(ctx, child1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.CreateIssue(ctx, child2, "test"); err != nil {
		t.Fatal(err)
	}

	// Add parent-child dependencies
	dep1 := &types.Dependency{
		IssueID:     child1.ID,
		DependsOnID: epic.ID,
		Type:        types.DepParentChild,
	}
	dep2 := &types.Dependency{
		IssueID:     child2.ID,
		DependsOnID: epic.ID,
		Type:        types.DepParentChild,
	}

	if err := sqliteStore.AddDependency(ctx, dep1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.AddDependency(ctx, dep2, "test"); err != nil {
		t.Fatal(err)
	}

	// Test GetEpicsEligibleForClosure
	store = sqliteStore
	daemonClient = nil

	epics, err := sqliteStore.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		t.Fatalf("GetEpicsEligibleForClosure failed: %v", err)
	}

	if len(epics) != 1 {
		t.Errorf("Expected 1 epic, got %d", len(epics))
	}

	if len(epics) > 0 {
		epicStatus := epics[0]
		if epicStatus.Epic.ID != "test-epic-1" {
			t.Errorf("Expected epic ID test-epic-1, got %s", epicStatus.Epic.ID)
		}
		if epicStatus.TotalChildren != 2 {
			t.Errorf("Expected 2 total children, got %d", epicStatus.TotalChildren)
		}
		if epicStatus.ClosedChildren != 1 {
			t.Errorf("Expected 1 closed child, got %d", epicStatus.ClosedChildren)
		}
		if epicStatus.EligibleForClose {
			t.Error("Epic should not be eligible for close with open children")
		}
	}
}

func TestEpicCommandInit(t *testing.T) {
	if epicCmd == nil {
		t.Fatal("epicCmd should be initialized")
	}

	if epicCmd.Use != "epic" {
		t.Errorf("Expected Use='epic', got %q", epicCmd.Use)
	}

	// Check that subcommands exist
	var hasStatusCmd bool
	for _, cmd := range epicCmd.Commands() {
		if cmd.Use == "status" {
			hasStatusCmd = true
		}
	}

	if !hasStatusCmd {
		t.Error("epic command should have status subcommand")
	}
}

func TestEpicEligibleForClose(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	sqliteStore := newTestStore(t, testDB)
	ctx := context.Background()

	// Create an epic where all children are closed
	epic := &types.Issue{
		ID:          "test-epic-2",
		Title:       "Fully Completed Epic",
		Description: "Epic description",
		Status:      types.StatusOpen,
		Priority:    1,
		IssueType:   types.TypeEpic,
		CreatedAt:   time.Now(),
	}

	if err := sqliteStore.CreateIssue(ctx, epic, "test"); err != nil {
		t.Fatal(err)
	}

	// Create all closed children
	for i := 1; i <= 3; i++ {
		child := &types.Issue{
			Title:     fmt.Sprintf("Child Task %d", i),
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			CreatedAt: time.Now(),
			ClosedAt:  ptrTime(time.Now()),
		}
		if err := sqliteStore.CreateIssue(ctx, child, "test"); err != nil {
			t.Fatal(err)
		}

		// Add parent-child dependency
		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: epic.ID,
			Type:        types.DepParentChild,
		}
		if err := sqliteStore.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatal(err)
		}
	}

	// Test GetEpicsEligibleForClosure
	epics, err := sqliteStore.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		t.Fatalf("GetEpicsEligibleForClosure failed: %v", err)
	}

	// Find our epic
	var epicStatus *types.EpicStatus
	for _, e := range epics {
		if e.Epic.ID == "test-epic-2" {
			epicStatus = e
			break
		}
	}

	if epicStatus == nil {
		t.Fatal("Epic test-epic-2 not found in results")
	}

	if epicStatus.TotalChildren != 3 {
		t.Errorf("Expected 3 total children, got %d", epicStatus.TotalChildren)
	}
	if epicStatus.ClosedChildren != 3 {
		t.Errorf("Expected 3 closed children, got %d", epicStatus.ClosedChildren)
	}
	if !epicStatus.EligibleForClose {
		t.Error("Epic should be eligible for close when all children are closed")
	}
}

func TestChildSortOrder(t *testing.T) {
	tests := []struct {
		status types.Status
		want   int
	}{
		{types.StatusInProgress, 0},
		{types.StatusOpen, 1},
		{types.StatusBlocked, 2},
		{types.StatusClosed, 3},
		{types.Status("unknown"), 1}, // defaults to open's order
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := childSortOrder(tt.status)
			if got != tt.want {
				t.Errorf("childSortOrder(%q) = %d, want %d", tt.status, got, tt.want)
			}
		})
	}

	// Verify ordering is correct: in_progress < open < blocked < closed
	if childSortOrder(types.StatusInProgress) >= childSortOrder(types.StatusOpen) {
		t.Error("in_progress should sort before open")
	}
	if childSortOrder(types.StatusOpen) >= childSortOrder(types.StatusBlocked) {
		t.Error("open should sort before blocked")
	}
	if childSortOrder(types.StatusBlocked) >= childSortOrder(types.StatusClosed) {
		t.Error("blocked should sort before closed")
	}
}

func TestChildStatusIcon(t *testing.T) {
	// Verify each status returns a non-empty icon
	statuses := []types.Status{
		types.StatusClosed,
		types.StatusInProgress,
		types.StatusBlocked,
		types.StatusOpen,
	}

	for _, status := range statuses {
		icon := childStatusIcon(status)
		if icon == "" {
			t.Errorf("childStatusIcon(%q) returned empty string", status)
		}
	}

	// Verify the icons are distinct
	icons := map[string]types.Status{}
	for _, status := range statuses {
		icon := childStatusIcon(status)
		if prev, exists := icons[icon]; exists {
			t.Errorf("childStatusIcon(%q) and childStatusIcon(%q) both return %q", status, prev, icon)
		}
		icons[icon] = status
	}
}

func TestRepeatStr(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want string
	}{
		{"─", 3, "───"},
		{"█", 0, ""},
		{"░", -1, ""},
		{"ab", 2, "abab"},
		{"", 5, ""},
	}

	for _, tt := range tests {
		got := repeatStr(tt.s, tt.n)
		if got != tt.want {
			t.Errorf("repeatStr(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
		}
	}
}

func TestEpicOverviewCommandInit(t *testing.T) {
	// Verify the overview subcommand exists
	var hasOverview bool
	for _, cmd := range epicCmd.Commands() {
		if cmd.Use == "overview" {
			hasOverview = true
			// Verify --hide-closed flag exists
			flag := cmd.Flags().Lookup("hide-closed")
			if flag == nil {
				t.Error("overview command should have --hide-closed flag")
			}
		}
	}
	if !hasOverview {
		t.Error("epic command should have overview subcommand")
	}

	// Verify dashboard subcommand exists
	var hasDashboard bool
	for _, cmd := range epicCmd.Commands() {
		if cmd.Use == "dashboard <epic-id>" {
			hasDashboard = true
		}
	}
	if !hasDashboard {
		t.Error("epic command should have dashboard subcommand")
	}

	// Verify orphaned-children subcommand exists
	var hasOrphaned bool
	for _, cmd := range epicCmd.Commands() {
		if cmd.Use == "orphaned-children" {
			hasOrphaned = true
		}
	}
	if !hasOrphaned {
		t.Error("epic command should have orphaned-children subcommand")
	}
}
