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
		name     string
		actor    string
		wantIDs  []string
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
