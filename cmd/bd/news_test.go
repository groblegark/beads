package main

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestFilterOutActor(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-1", Title: "My work", Assignee: "Matthew.Baker", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-2", Title: "Someone else's work", Assignee: "gastown/polecats/furiosa", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-3", Title: "Unassigned work", Assignee: "", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-4", Title: "Team lead work", Assignee: "gastown/Toast", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-5", Title: "Case mismatch", Assignee: "matthew.baker", CreatedAt: now, UpdatedAt: now},
	}

	tests := []struct {
		name    string
		actor   string
		wantIDs []string
	}{
		{
			name:    "filter exact match",
			actor:   "Matthew.Baker",
			wantIDs: []string{"bd-2", "bd-3", "bd-4"},
		},
		{
			name:    "filter case-insensitive",
			actor:   "matthew.baker",
			wantIDs: []string{"bd-2", "bd-3", "bd-4"},
		},
		{
			name:    "filter suffix match for role paths",
			actor:   "furiosa",
			wantIDs: []string{"bd-1", "bd-3", "bd-4", "bd-5"},
		},
		{
			name:    "filter suffix match for team name",
			actor:   "Toast",
			wantIDs: []string{"bd-1", "bd-2", "bd-3", "bd-5"},
		},
		{
			name:    "empty actor filters nothing",
			actor:   "",
			wantIDs: []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5"},
		},
		{
			name:    "unknown actor filters nothing",
			actor:   "unknown",
			wantIDs: []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5"},
		},
		{
			name:    "no matches keeps all",
			actor:   "nonexistent-user",
			wantIDs: []string{"bd-1", "bd-2", "bd-3", "bd-4", "bd-5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterOutActor(issues, tt.actor)
			if len(result) != len(tt.wantIDs) {
				t.Errorf("filterOutActor(%q) returned %d issues, want %d", tt.actor, len(result), len(tt.wantIDs))
				for _, issue := range result {
					t.Logf("  got: %s (%s)", issue.ID, issue.Assignee)
				}
				return
			}
			for i, issue := range result {
				if issue.ID != tt.wantIDs[i] {
					t.Errorf("filterOutActor(%q)[%d].ID = %q, want %q", tt.actor, i, issue.ID, tt.wantIDs[i])
				}
			}
		})
	}
}

func TestFilterOutActor_NilSlice(t *testing.T) {
	result := filterOutActor(nil, "anyone")
	if result != nil {
		t.Errorf("filterOutActor(nil) = %v, want nil", result)
	}
}

func TestFilterOutActor_EmptySlice(t *testing.T) {
	result := filterOutActor([]*types.Issue{}, "anyone")
	if len(result) != 0 {
		t.Errorf("filterOutActor([]) = %v, want empty", result)
	}
}

func TestFilterOutIDs(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-1", Title: "First", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-2", Title: "Second", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-3", Title: "Third", CreatedAt: now, UpdatedAt: now},
	}

	tests := []struct {
		name    string
		exclude map[string]bool
		wantIDs []string
	}{
		{
			name:    "exclude one",
			exclude: map[string]bool{"bd-2": true},
			wantIDs: []string{"bd-1", "bd-3"},
		},
		{
			name:    "exclude multiple",
			exclude: map[string]bool{"bd-1": true, "bd-3": true},
			wantIDs: []string{"bd-2"},
		},
		{
			name:    "exclude none",
			exclude: map[string]bool{},
			wantIDs: []string{"bd-1", "bd-2", "bd-3"},
		},
		{
			name:    "exclude all",
			exclude: map[string]bool{"bd-1": true, "bd-2": true, "bd-3": true},
			wantIDs: nil,
		},
		{
			name:    "exclude nonexistent",
			exclude: map[string]bool{"bd-999": true},
			wantIDs: []string{"bd-1", "bd-2", "bd-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterOutIDs(issues, tt.exclude)
			var gotIDs []string
			for _, issue := range result {
				gotIDs = append(gotIDs, issue.ID)
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Errorf("filterOutIDs() returned %v, want %v", gotIDs, tt.wantIDs)
				return
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Errorf("filterOutIDs()[%d] = %q, want %q", i, gotIDs[i], tt.wantIDs[i])
				}
			}
		})
	}
}

func TestFilterOutIDs_NilSlice(t *testing.T) {
	result := filterOutIDs(nil, map[string]bool{"bd-1": true})
	if result != nil {
		t.Errorf("filterOutIDs(nil) = %v, want nil", result)
	}
}

func TestFilterOutDecisions(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-1", Title: "Normal task", IssueType: "task", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-2", Title: "[DECISION] Deploy to prod?", IssueType: "task", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-3", Title: "A gate issue", IssueType: "gate", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-4", Title: "A decision type", IssueType: "decision", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-5", Title: "Another task", IssueType: "bug", CreatedAt: now, UpdatedAt: now},
	}

	result := filterOutDecisions(issues)
	wantIDs := []string{"bd-1", "bd-5"}
	if len(result) != len(wantIDs) {
		t.Errorf("filterOutDecisions() returned %d issues, want %d", len(result), len(wantIDs))
		for _, issue := range result {
			t.Logf("  got: %s (%s) %q", issue.ID, issue.IssueType, issue.Title)
		}
		return
	}
	for i, issue := range result {
		if issue.ID != wantIDs[i] {
			t.Errorf("filterOutDecisions()[%d].ID = %q, want %q", i, issue.ID, wantIDs[i])
		}
	}
}

func TestFilterOutDecisions_NilSlice(t *testing.T) {
	result := filterOutDecisions(nil)
	if result != nil {
		t.Errorf("filterOutDecisions(nil) = %v, want nil", result)
	}
}

func TestFilterOutDecisions_NoDecisions(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-1", Title: "Task", IssueType: "task", CreatedAt: now, UpdatedAt: now},
		{ID: "bd-2", Title: "Bug", IssueType: "bug", CreatedAt: now, UpdatedAt: now},
	}
	result := filterOutDecisions(issues)
	if len(result) != 2 {
		t.Errorf("filterOutDecisions() filtered when it shouldn't have: got %d, want 2", len(result))
	}
}

func TestCoalesce(t *testing.T) {
	t.Run("nil returns empty slice", func(t *testing.T) {
		result := coalesce(nil)
		if result == nil {
			t.Error("coalesce(nil) returned nil, want empty slice")
		}
		if len(result) != 0 {
			t.Errorf("coalesce(nil) returned %d items, want 0", len(result))
		}
	})

	t.Run("non-nil passes through", func(t *testing.T) {
		issues := []*types.Issue{{ID: "bd-1"}}
		result := coalesce(issues)
		if len(result) != 1 || result[0].ID != "bd-1" {
			t.Errorf("coalesce() changed the slice")
		}
	})

	t.Run("empty slice passes through", func(t *testing.T) {
		issues := []*types.Issue{}
		result := coalesce(issues)
		if result == nil {
			t.Error("coalesce([]) returned nil")
		}
		if len(result) != 0 {
			t.Errorf("coalesce([]) returned %d items", len(result))
		}
	})
}

func TestFormatDurationShort(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Minute, "30m"},
		{1 * time.Hour, "1h"},
		{2 * time.Hour, "2h"},
		{4 * time.Hour, "4h"},
		{90 * time.Minute, "1.5h"},
		{150 * time.Minute, "2.5h"},
		{24 * time.Hour, "24h"},
		{5 * time.Minute, "5m"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatDurationShort(tt.d)
			if got != tt.want {
				t.Errorf("formatDurationShort(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestWindowSuffix(t *testing.T) {
	got := windowSuffix(2 * time.Hour)
	want := " (last 2h)"
	if got != want {
		t.Errorf("windowSuffix(2h) = %q, want %q", got, want)
	}

	got = windowSuffix(30 * time.Minute)
	want = " (last 30m)"
	if got != want {
		t.Errorf("windowSuffix(30m) = %q, want %q", got, want)
	}
}

func TestSortByUpdatedDesc(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	issues := []*types.Issue{
		{ID: "bd-1", Title: "Oldest", UpdatedAt: t1},
		{ID: "bd-2", Title: "Middle", UpdatedAt: t2},
		{ID: "bd-3", Title: "Newest", UpdatedAt: t3},
	}

	sortByUpdatedDesc(issues)

	wantOrder := []string{"bd-3", "bd-2", "bd-1"}
	for i, issue := range issues {
		if issue.ID != wantOrder[i] {
			t.Errorf("sortByUpdatedDesc()[%d].ID = %q, want %q", i, issue.ID, wantOrder[i])
		}
	}
}

func TestSortByUpdatedDesc_AlreadySorted(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)

	issues := []*types.Issue{
		{ID: "bd-1", Title: "Newest", UpdatedAt: t1},
		{ID: "bd-2", Title: "Oldest", UpdatedAt: t2},
	}

	sortByUpdatedDesc(issues)

	if issues[0].ID != "bd-1" || issues[1].ID != "bd-2" {
		t.Errorf("sortByUpdatedDesc changed already-sorted order")
	}
}

func TestSortByUpdatedDesc_Empty(t *testing.T) {
	sortByUpdatedDesc(nil)
	sortByUpdatedDesc([]*types.Issue{})
	// No panic = pass
}

func TestSortByClosedDesc(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	issues := []*types.Issue{
		{ID: "bd-1", Title: "Closed first", ClosedAt: &t1, UpdatedAt: t1},
		{ID: "bd-2", Title: "Closed second", ClosedAt: &t2, UpdatedAt: t2},
		{ID: "bd-3", Title: "Closed third", ClosedAt: &t3, UpdatedAt: t3},
	}

	sortByClosedDesc(issues)

	wantOrder := []string{"bd-3", "bd-2", "bd-1"}
	for i, issue := range issues {
		if issue.ID != wantOrder[i] {
			t.Errorf("sortByClosedDesc()[%d].ID = %q, want %q", i, issue.ID, wantOrder[i])
		}
	}
}

func TestSortByClosedDesc_FallbackToUpdatedAt(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	closedEarly := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

	issues := []*types.Issue{
		{ID: "bd-1", Title: "Has ClosedAt", ClosedAt: &closedEarly, UpdatedAt: t1},
		{ID: "bd-2", Title: "No ClosedAt", ClosedAt: nil, UpdatedAt: t2},
	}

	sortByClosedDesc(issues)

	// bd-2 has UpdatedAt=12:00, bd-1 has ClosedAt=09:00 → bd-2 first
	if issues[0].ID != "bd-2" {
		t.Errorf("sortByClosedDesc() should fallback to UpdatedAt for nil ClosedAt, got %q first", issues[0].ID)
	}
}

func TestSortByClosedDesc_Empty(t *testing.T) {
	sortByClosedDesc(nil)
	sortByClosedDesc([]*types.Issue{})
	// No panic = pass
}

func TestMostRecentUpdate(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)

	issues := []*types.Issue{
		{ID: "bd-1", UpdatedAt: t1},
		{ID: "bd-2", UpdatedAt: t2},
		{ID: "bd-3", UpdatedAt: t3},
	}

	got := mostRecentUpdate(issues)
	if !got.Equal(t2) {
		t.Errorf("mostRecentUpdate() = %v, want %v", got, t2)
	}
}

func TestMostRecentUpdate_Single(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	issues := []*types.Issue{{ID: "bd-1", UpdatedAt: t1}}

	got := mostRecentUpdate(issues)
	if !got.Equal(t1) {
		t.Errorf("mostRecentUpdate() = %v, want %v", got, t1)
	}
}

func TestMostRecentUpdate_Empty(t *testing.T) {
	got := mostRecentUpdate(nil)
	if !got.IsZero() {
		t.Errorf("mostRecentUpdate(nil) = %v, want zero time", got)
	}
}

func TestPrepareNewsLines_Expand(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-epic", Title: "Epic", IssueType: types.TypeEpic, UpdatedAt: now},
		{ID: "bd-epic.1", Title: "Child 1", IssueType: "task", UpdatedAt: now},
		{ID: "bd-epic.2", Title: "Child 2", IssueType: "task", UpdatedAt: now},
		{ID: "bd-epic.3", Title: "Child 3", IssueType: "task", UpdatedAt: now},
	}

	// With expand=true, no collapsing should happen
	lines := prepareNewsLines(issues, true)
	if len(lines) != 4 {
		t.Errorf("prepareNewsLines(expand=true) returned %d lines, want 4", len(lines))
	}
	for _, line := range lines {
		if line.collapsed {
			t.Error("prepareNewsLines(expand=true) returned a collapsed line")
		}
	}
}

func TestPrepareNewsLines_CollapseEpic(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-epic", Title: "My Epic", IssueType: types.TypeEpic, UpdatedAt: now},
		{ID: "bd-epic.1", Title: "Child 1", IssueType: "task", UpdatedAt: now},
		{ID: "bd-epic.2", Title: "Child 2", IssueType: "task", UpdatedAt: now},
		{ID: "bd-epic.3", Title: "Child 3", IssueType: "task", UpdatedAt: now},
	}

	lines := prepareNewsLines(issues, false)
	if len(lines) != 1 {
		t.Errorf("prepareNewsLines() returned %d lines, want 1 (collapsed)", len(lines))
		for _, l := range lines {
			t.Logf("  line: %s collapsed=%v", l.issue.ID, l.collapsed)
		}
		return
	}
	if !lines[0].collapsed {
		t.Error("expected collapsed line")
	}
	if lines[0].issue.ID != "bd-epic" {
		t.Errorf("collapsed line ID = %q, want %q", lines[0].issue.ID, "bd-epic")
	}
	if lines[0].childCount != 3 {
		t.Errorf("collapsed line childCount = %d, want 3", lines[0].childCount)
	}
}

func TestPrepareNewsLines_NoCollapseUnderThreshold(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-epic", Title: "Small Epic", IssueType: types.TypeEpic, UpdatedAt: now},
		{ID: "bd-epic.1", Title: "Child 1", IssueType: "task", UpdatedAt: now},
		{ID: "bd-epic.2", Title: "Child 2", IssueType: "task", UpdatedAt: now},
	}

	lines := prepareNewsLines(issues, false)
	// 2 children < threshold of 3, so epic + 2 children shown individually
	// But the epic itself is shown as a standalone line when < threshold
	collapsedCount := 0
	for _, l := range lines {
		if l.collapsed {
			collapsedCount++
		}
	}
	if collapsedCount > 0 {
		t.Errorf("expected no collapsed lines for < threshold children, got %d", collapsedCount)
	}
}

func TestPrepareNewsLines_StandaloneIssues(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-1", Title: "Standalone task", IssueType: "task", UpdatedAt: now},
		{ID: "bd-2", Title: "Standalone bug", IssueType: "bug", UpdatedAt: now},
	}

	lines := prepareNewsLines(issues, false)
	if len(lines) != 2 {
		t.Errorf("prepareNewsLines() returned %d lines, want 2", len(lines))
	}
	for _, line := range lines {
		if line.collapsed {
			t.Error("standalone issues should not be collapsed")
		}
	}
}

func TestPrepareNewsLines_MixedEpicAndStandalone(t *testing.T) {
	now := time.Now()
	issues := []*types.Issue{
		{ID: "bd-1", Title: "Standalone", IssueType: "task", UpdatedAt: now},
		{ID: "bd-epic", Title: "Big Epic", IssueType: types.TypeEpic, UpdatedAt: now},
		{ID: "bd-epic.1", Title: "Child 1", IssueType: "task", UpdatedAt: now},
		{ID: "bd-epic.2", Title: "Child 2", IssueType: "task", UpdatedAt: now},
		{ID: "bd-epic.3", Title: "Child 3", IssueType: "task", UpdatedAt: now},
		{ID: "bd-2", Title: "Another standalone", IssueType: "bug", UpdatedAt: now},
	}

	lines := prepareNewsLines(issues, false)

	// Should have: standalone bd-1, collapsed epic, standalone bd-2
	if len(lines) != 3 {
		t.Errorf("prepareNewsLines() returned %d lines, want 3", len(lines))
		for _, l := range lines {
			t.Logf("  line: %s collapsed=%v", l.issue.ID, l.collapsed)
		}
		return
	}
	if lines[0].issue.ID != "bd-1" || lines[0].collapsed {
		t.Errorf("line[0] = %q collapsed=%v, want bd-1 not collapsed", lines[0].issue.ID, lines[0].collapsed)
	}
	if lines[1].issue.ID != "bd-epic" || !lines[1].collapsed {
		t.Errorf("line[1] = %q collapsed=%v, want bd-epic collapsed", lines[1].issue.ID, lines[1].collapsed)
	}
	if lines[2].issue.ID != "bd-2" || lines[2].collapsed {
		t.Errorf("line[2] = %q collapsed=%v, want bd-2 not collapsed", lines[2].issue.ID, lines[2].collapsed)
	}
}

func TestPrepareNewsLines_ChildrenWithoutEpicInList(t *testing.T) {
	now := time.Now()
	// Children of an epic that isn't in this list (e.g., only children recently opened)
	issues := []*types.Issue{
		{ID: "bd-epic.1", Title: "Child 1", IssueType: "task", CreatedBy: "alice", UpdatedAt: now, Status: "open"},
		{ID: "bd-epic.2", Title: "Child 2", IssueType: "task", CreatedBy: "alice", UpdatedAt: now, Status: "open"},
		{ID: "bd-epic.3", Title: "Child 3", IssueType: "task", CreatedBy: "alice", UpdatedAt: now, Status: "open"},
	}

	lines := prepareNewsLines(issues, false)
	if len(lines) != 1 {
		t.Errorf("prepareNewsLines() returned %d lines, want 1 (synthesized epic)", len(lines))
		for _, l := range lines {
			t.Logf("  line: %s collapsed=%v title=%q", l.issue.ID, l.collapsed, l.issue.Title)
		}
		return
	}
	if !lines[0].collapsed {
		t.Error("expected collapsed line for synthesized epic")
	}
	if lines[0].issue.ID != "bd-epic" {
		t.Errorf("synthesized epic ID = %q, want %q", lines[0].issue.ID, "bd-epic")
	}
	if lines[0].childCount != 3 {
		t.Errorf("synthesized epic childCount = %d, want 3", lines[0].childCount)
	}
}

func TestPrepareNewsLines_Empty(t *testing.T) {
	lines := prepareNewsLines(nil, false)
	if len(lines) != 0 {
		t.Errorf("prepareNewsLines(nil) returned %d lines, want 0", len(lines))
	}

	lines = prepareNewsLines([]*types.Issue{}, false)
	if len(lines) != 0 {
		t.Errorf("prepareNewsLines([]) returned %d lines, want 0", len(lines))
	}
}

func TestNormalizeAgentName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"alice", "alice"},
		{"gastown/polecats/furiosa", "furiosa"},
		{"team/lead", "lead"},
		{"", ""},
		{"simple", "simple"},
		{"a/b/c/d", "d"},
	}
	for _, tt := range tests {
		got := normalizeAgentName(tt.input)
		if got != tt.want {
			t.Errorf("normalizeAgentName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestInferAgentStatus(t *testing.T) {
	shortAge := 2 * time.Minute
	mediumAge := 15 * time.Minute
	longAge := 45 * time.Minute

	tests := []struct {
		name string
		in   *agentSummary
		want agentStatus
	}{
		{
			name: "pending decision < 5m → active",
			in:   &agentSummary{InProgressCount: 1, PendingDecision: true, LastDecisionAge: &shortAge},
			want: agentStatusActive,
		},
		{
			name: "pending decision < 30m → working",
			in:   &agentSummary{InProgressCount: 1, PendingDecision: true, LastDecisionAge: &mediumAge},
			want: agentStatusWorking,
		},
		{
			name: "pending decision >= 30m → stuck",
			in:   &agentSummary{InProgressCount: 1, PendingDecision: true, LastDecisionAge: &longAge},
			want: agentStatusStuck,
		},
		{
			name: "in-progress work, no decision → working",
			in:   &agentSummary{InProgressCount: 3, PendingDecision: false},
			want: agentStatusWorking,
		},
		{
			name: "no work, no decision → idle",
			in:   &agentSummary{InProgressCount: 0, PendingDecision: false},
			want: agentStatusIdle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferAgentStatus(tt.in)
			if got != tt.want {
				t.Errorf("inferAgentStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusPriority(t *testing.T) {
	// Active should sort before working, working before stuck, stuck before idle
	if statusPriority(agentStatusActive) >= statusPriority(agentStatusWorking) {
		t.Error("active should sort before working")
	}
	if statusPriority(agentStatusWorking) >= statusPriority(agentStatusStuck) {
		t.Error("working should sort before stuck")
	}
	if statusPriority(agentStatusStuck) >= statusPriority(agentStatusIdle) {
		t.Error("stuck should sort before idle")
	}
}

func TestPrepareNewsLines_PreservesOrder(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Issues already sorted desc — prepareNewsLines should preserve order
	issues := []*types.Issue{
		{ID: "bd-3", Title: "Newest", IssueType: "task", UpdatedAt: t1},
		{ID: "bd-2", Title: "Middle", IssueType: "task", UpdatedAt: t2},
		{ID: "bd-1", Title: "Oldest", IssueType: "task", UpdatedAt: t3},
	}

	lines := prepareNewsLines(issues, false)
	wantOrder := []string{"bd-3", "bd-2", "bd-1"}
	for i, line := range lines {
		if line.issue.ID != wantOrder[i] {
			t.Errorf("prepareNewsLines()[%d].ID = %q, want %q", i, line.issue.ID, wantOrder[i])
		}
	}
}
