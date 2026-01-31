package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestAdviceFilterLogic(t *testing.T) {
	// Test the filtering logic without database - using in-memory issues

	// Create test advice issues
	advice1 := &types.Issue{
		ID:        "test-1",
		Title:     "Always check your hook first",
		IssueType: types.TypeAdvice,
		Status:    types.StatusOpen,
		// Global - no targeting
	}
	advice2 := &types.Issue{
		ID:               "test-2",
		Title:            "Run tests before committing",
		IssueType:        types.TypeAdvice,
		Status:           types.StatusOpen,
		AdviceTargetRole: "polecat",
	}
	advice3 := &types.Issue{
		ID:              "test-3",
		Title:           "Use bd commands for issue tracking",
		IssueType:       types.TypeAdvice,
		Status:          types.StatusOpen,
		AdviceTargetRig: "beads",
	}
	advice4 := &types.Issue{
		ID:                "test-4",
		Title:             "Focus on CLI development",
		IssueType:         types.TypeAdvice,
		Status:            types.StatusOpen,
		AdviceTargetAgent: "beads/polecats/obsidian",
	}

	issues := []*types.Issue{advice1, advice2, advice3, advice4}

	// Test filtering for beads polecat obsidian
	filtered := filterAdviceForAgent(issues, "beads/polecats/obsidian", "beads", "polecat")

	// Should get all 4: agent-specific, role-specific, rig-specific, global
	if len(filtered) != 4 {
		t.Errorf("Expected 4 applicable advice for beads polecat, got %d", len(filtered))
	}

	// First should be agent-specific (highest priority = 1)
	if filtered[0].AdviceTargetAgent != "beads/polecats/obsidian" {
		t.Errorf("Expected first advice to be agent-specific, got target_agent=%s", filtered[0].AdviceTargetAgent)
	}

	// Second should be role-specific (priority = 2)
	if filtered[1].AdviceTargetRole != "polecat" {
		t.Errorf("Expected second advice to be role-specific, got target_role=%s", filtered[1].AdviceTargetRole)
	}

	// Third should be rig-specific (priority = 3)
	if filtered[2].AdviceTargetRig != "beads" {
		t.Errorf("Expected third advice to be rig-specific, got target_rig=%s", filtered[2].AdviceTargetRig)
	}

	// Fourth should be global (priority = 4)
	if filtered[3].AdviceTargetAgent != "" || filtered[3].AdviceTargetRole != "" || filtered[3].AdviceTargetRig != "" {
		t.Errorf("Expected fourth advice to be global")
	}

	// Test filtering for different agent in same role but different rig
	filtered2 := filterAdviceForAgent(issues, "gastown/polecats/quartz", "gastown", "polecat")

	// Should get 2: role-specific, global (not rig-specific "beads" or agent-specific)
	if len(filtered2) != 2 {
		t.Errorf("Expected 2 applicable advice for gastown polecat, got %d", len(filtered2))
		for _, a := range filtered2 {
			t.Logf("  - %s: agent=%s role=%s rig=%s", a.ID, a.AdviceTargetAgent, a.AdviceTargetRole, a.AdviceTargetRig)
		}
	}

	// Test filtering for witness role in beads rig
	filtered3 := filterAdviceForAgent(issues, "beads/witness/primary", "beads", "witness")

	// Should get 2: rig-specific (beads), global
	if len(filtered3) != 2 {
		t.Errorf("Expected 2 applicable advice for beads witness, got %d", len(filtered3))
	}
}

func TestAdviceMatchesAgent(t *testing.T) {
	tests := []struct {
		name      string
		advice    *types.Issue
		agentID   string
		agentRig  string
		agentRole string
		matches   bool
		priority  int
	}{
		{
			name:      "global advice matches all",
			advice:    &types.Issue{},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   true,
			priority:  4,
		},
		{
			name:      "rig advice matches same rig",
			advice:    &types.Issue{AdviceTargetRig: "beads"},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   true,
			priority:  3,
		},
		{
			name:      "rig advice does not match different rig",
			advice:    &types.Issue{AdviceTargetRig: "gastown"},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   false,
			priority:  0,
		},
		{
			name:      "role advice matches same role",
			advice:    &types.Issue{AdviceTargetRole: "polecat"},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   true,
			priority:  2,
		},
		{
			name:      "role advice does not match different role",
			advice:    &types.Issue{AdviceTargetRole: "witness"},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   false,
			priority:  0,
		},
		{
			name:      "role+rig advice matches both",
			advice:    &types.Issue{AdviceTargetRole: "polecat", AdviceTargetRig: "beads"},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   true,
			priority:  2,
		},
		{
			name:      "role+rig advice does not match wrong rig",
			advice:    &types.Issue{AdviceTargetRole: "polecat", AdviceTargetRig: "gastown"},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   false,
			priority:  0,
		},
		{
			name:      "agent advice matches exact agent",
			advice:    &types.Issue{AdviceTargetAgent: "beads/polecats/obsidian"},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   true,
			priority:  1,
		},
		{
			name:      "agent advice does not match different agent",
			advice:    &types.Issue{AdviceTargetAgent: "beads/polecats/quartz"},
			agentID:   "beads/polecats/obsidian",
			agentRig:  "beads",
			agentRole: "polecat",
			matches:   false,
			priority:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches, priority := adviceMatchesAgent(tt.advice, tt.agentID, tt.agentRig, tt.agentRole)
			if matches != tt.matches {
				t.Errorf("matches: got %v, want %v", matches, tt.matches)
			}
			if priority != tt.priority {
				t.Errorf("priority: got %d, want %d", priority, tt.priority)
			}
		})
	}
}
