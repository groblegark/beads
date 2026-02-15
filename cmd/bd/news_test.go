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
