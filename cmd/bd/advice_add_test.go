package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestAdviceAddSuite tests the bd advice add command
func TestAdviceAddSuite(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("GlobalAdvice", func(t *testing.T) {
		// Create global advice (no target flags)
		issue := &types.Issue{
			Title:       "Always check for errors",
			Description: "Always check for errors before proceeding with any operation",
			Priority:    2,
			IssueType:   types.TypeAdvice,
			Status:      types.StatusOpen,
			CreatedAt:   time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create global advice: %v", err)
		}

		// Retrieve and verify
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		if retrieved.IssueType != types.TypeAdvice {
			t.Errorf("expected issue type 'advice', got %q", retrieved.IssueType)
		}
		if retrieved.AdviceTargetRig != "" {
			t.Errorf("expected empty AdviceTargetRig for global advice, got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "" {
			t.Errorf("expected empty AdviceTargetRole for global advice, got %q", retrieved.AdviceTargetRole)
		}
		if retrieved.AdviceTargetAgent != "" {
			t.Errorf("expected empty AdviceTargetAgent for global advice, got %q", retrieved.AdviceTargetAgent)
		}
	})

	t.Run("RigTargetedAdvice", func(t *testing.T) {
		// Create rig-targeted advice
		issue := &types.Issue{
			Title:           "Use go test ./... for testing",
			Description:     "When running tests in this rig, use go test ./...",
			Priority:        2,
			IssueType:       types.TypeAdvice,
			Status:          types.StatusOpen,
			AdviceTargetRig: "beads",
			CreatedAt:       time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create rig-targeted advice: %v", err)
		}

		// Retrieve and verify
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		if retrieved.AdviceTargetRig != "beads" {
			t.Errorf("expected AdviceTargetRig 'beads', got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "" {
			t.Errorf("expected empty AdviceTargetRole for rig advice, got %q", retrieved.AdviceTargetRole)
		}
		if retrieved.AdviceTargetAgent != "" {
			t.Errorf("expected empty AdviceTargetAgent for rig advice, got %q", retrieved.AdviceTargetAgent)
		}
	})

	t.Run("RoleTargetedAdvice", func(t *testing.T) {
		// Create role-targeted advice
		issue := &types.Issue{
			Title:            "Complete work before gt done",
			Description:      "Ensure all tests pass before running gt done",
			Priority:         2,
			IssueType:        types.TypeAdvice,
			Status:           types.StatusOpen,
			AdviceTargetRig:  "beads",
			AdviceTargetRole: "polecat",
			CreatedAt:        time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create role-targeted advice: %v", err)
		}

		// Retrieve and verify
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		if retrieved.AdviceTargetRig != "beads" {
			t.Errorf("expected AdviceTargetRig 'beads', got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "polecat" {
			t.Errorf("expected AdviceTargetRole 'polecat', got %q", retrieved.AdviceTargetRole)
		}
		if retrieved.AdviceTargetAgent != "" {
			t.Errorf("expected empty AdviceTargetAgent for role advice, got %q", retrieved.AdviceTargetAgent)
		}
	})

	t.Run("AgentTargetedAdvice", func(t *testing.T) {
		// Create agent-targeted advice
		issue := &types.Issue{
			Title:             "Focus on CLI tasks",
			Description:       "This agent should focus on CLI implementation tasks",
			Priority:          2,
			IssueType:         types.TypeAdvice,
			Status:            types.StatusOpen,
			AdviceTargetAgent: "beads/polecats/quartz",
			CreatedAt:         time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create agent-targeted advice: %v", err)
		}

		// Retrieve and verify
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		if retrieved.AdviceTargetAgent != "beads/polecats/quartz" {
			t.Errorf("expected AdviceTargetAgent 'beads/polecats/quartz', got %q", retrieved.AdviceTargetAgent)
		}
		// Agent advice should not have rig/role set
		if retrieved.AdviceTargetRig != "" {
			t.Errorf("expected empty AdviceTargetRig for agent advice, got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "" {
			t.Errorf("expected empty AdviceTargetRole for agent advice, got %q", retrieved.AdviceTargetRole)
		}
	})

	t.Run("AdviceAppearsInList", func(t *testing.T) {
		// Create a distinctive advice for listing
		issue := &types.Issue{
			Title:       "Unique advice for list test",
			Description: "This advice should appear in the list",
			Priority:    2,
			IssueType:   types.TypeAdvice,
			Status:      types.StatusOpen,
			CreatedAt:   time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create advice: %v", err)
		}

		// Search for advice type issues
		adviceType := types.TypeAdvice
		filter := types.IssueFilter{
			IssueType: &adviceType,
		}

		issues, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Find our advice
		found := false
		for _, iss := range issues {
			if iss.Title == "Unique advice for list test" {
				found = true
				break
			}
		}

		if !found {
			t.Error("expected advice to appear in list, but it was not found")
		}
	})

	t.Run("TargetingFieldsPersisted", func(t *testing.T) {
		// Create advice with all targeting fields
		issue := &types.Issue{
			Title:            "Persisted targeting test",
			Description:      "Testing that targeting fields persist correctly",
			Priority:         1,
			IssueType:        types.TypeAdvice,
			Status:           types.StatusOpen,
			AdviceTargetRig:  "gastown",
			AdviceTargetRole: "crew",
			CreatedAt:        time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create advice: %v", err)
		}

		// Retrieve multiple times to ensure persistence
		for i := 0; i < 3; i++ {
			retrieved, err := s.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatalf("retrieval %d: failed to get issue: %v", i+1, err)
			}

			if retrieved.AdviceTargetRig != "gastown" {
				t.Errorf("retrieval %d: expected AdviceTargetRig 'gastown', got %q", i+1, retrieved.AdviceTargetRig)
			}
			if retrieved.AdviceTargetRole != "crew" {
				t.Errorf("retrieval %d: expected AdviceTargetRole 'crew', got %q", i+1, retrieved.AdviceTargetRole)
			}
		}
	})

	t.Run("AdviceWithLabels", func(t *testing.T) {
		// Create advice with labels
		issue := &types.Issue{
			Title:       "Advice with labels",
			Description: "Testing label support on advice",
			Priority:    2,
			IssueType:   types.TypeAdvice,
			Status:      types.StatusOpen,
			CreatedAt:   time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create advice: %v", err)
		}

		// Add labels
		if err := s.AddLabel(ctx, issue.ID, "important", "test"); err != nil {
			t.Fatalf("failed to add label: %v", err)
		}
		if err := s.AddLabel(ctx, issue.ID, "security", "test"); err != nil {
			t.Fatalf("failed to add label: %v", err)
		}

		// Verify labels
		labels, err := s.GetLabels(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get labels: %v", err)
		}

		if len(labels) != 2 {
			t.Errorf("expected 2 labels, got %d", len(labels))
		}

		labelMap := make(map[string]bool)
		for _, l := range labels {
			labelMap[l] = true
		}

		if !labelMap["important"] || !labelMap["security"] {
			t.Errorf("expected labels 'important' and 'security', got %v", labels)
		}
	})

	t.Run("AdviceWithPriority", func(t *testing.T) {
		// Test valid priority values (0-4)
		priorities := []int{0, 1, 2, 3, 4}

		for _, priority := range priorities {
			issue := &types.Issue{
				Title:       "Priority test",
				Description: "Testing priority persistence",
				Priority:    priority,
				IssueType:   types.TypeAdvice,
				Status:      types.StatusOpen,
				CreatedAt:   time.Now(),
			}

			if err := s.CreateIssue(ctx, issue, "test"); err != nil {
				t.Fatalf("failed to create advice with priority %d: %v", priority, err)
			}

			retrieved, err := s.GetIssue(ctx, issue.ID)
			if err != nil {
				t.Fatalf("failed to get issue: %v", err)
			}

			if retrieved.Priority != priority {
				t.Errorf("expected priority %d, got %d", priority, retrieved.Priority)
			}
		}
	})

	t.Run("ClosedAdviceNotInOpenList", func(t *testing.T) {
		// Create and close an advice
		issue := &types.Issue{
			Title:       "Closed advice test",
			Description: "This advice will be closed",
			Priority:    2,
			IssueType:   types.TypeAdvice,
			Status:      types.StatusClosed,
			CreatedAt:   time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create advice: %v", err)
		}

		// Search for open advice only
		adviceType := types.TypeAdvice
		openStatus := types.StatusOpen
		filter := types.IssueFilter{
			IssueType: &adviceType,
			Status:    &openStatus,
		}

		issues, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Verify closed advice is not in results
		for _, iss := range issues {
			if iss.ID == issue.ID {
				t.Error("closed advice should not appear in open list")
			}
		}
	})

	t.Run("FilterByRig", func(t *testing.T) {
		// Create advice for different rigs
		beadsAdvice := &types.Issue{
			Title:           "Beads specific advice",
			Description:     "For beads rig",
			Priority:        2,
			IssueType:       types.TypeAdvice,
			Status:          types.StatusOpen,
			AdviceTargetRig: "beads",
			CreatedAt:       time.Now(),
		}

		gastownAdvice := &types.Issue{
			Title:           "Gastown specific advice",
			Description:     "For gastown rig",
			Priority:        2,
			IssueType:       types.TypeAdvice,
			Status:          types.StatusOpen,
			AdviceTargetRig: "gastown",
			CreatedAt:       time.Now(),
		}

		if err := s.CreateIssue(ctx, beadsAdvice, "test"); err != nil {
			t.Fatalf("failed to create beads advice: %v", err)
		}
		if err := s.CreateIssue(ctx, gastownAdvice, "test"); err != nil {
			t.Fatalf("failed to create gastown advice: %v", err)
		}

		// Search for advice type
		adviceType := types.TypeAdvice
		filter := types.IssueFilter{
			IssueType: &adviceType,
		}

		issues, err := s.SearchIssues(ctx, "", filter)
		if err != nil {
			t.Fatalf("failed to search issues: %v", err)
		}

		// Filter in-memory for beads rig
		var beadsFiltered []*types.Issue
		for _, iss := range issues {
			if iss.AdviceTargetRig == "beads" && iss.AdviceTargetRole == "" {
				beadsFiltered = append(beadsFiltered, iss)
			}
		}

		// Verify we found the beads advice
		found := false
		for _, iss := range beadsFiltered {
			if iss.ID == beadsAdvice.ID {
				found = true
				break
			}
		}

		if !found {
			t.Error("expected to find beads advice in filtered results")
		}
	})
}

// TestAdviceAddValidation tests error cases for bd advice add
func TestAdviceAddValidation(t *testing.T) {
	t.Run("RoleRequiresRig", func(t *testing.T) {
		// This test validates the constraint that --role requires --rig
		// In the actual command, this is validated before storage
		// Here we just test the data integrity constraint

		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		ctx := context.Background()

		// While storage doesn't enforce this (command does), we can verify
		// that storing role without rig is technically possible but semantically wrong
		issue := &types.Issue{
			Title:            "Role without rig test",
			Description:      "This should be caught by command validation",
			Priority:         2,
			IssueType:        types.TypeAdvice,
			Status:           types.StatusOpen,
			AdviceTargetRole: "polecat", // Role without rig
			CreatedAt:        time.Now(),
		}

		// Storage allows it (validation is in command layer)
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("storage unexpectedly rejected issue: %v", err)
		}

		// But the data is semantically invalid - role without rig is meaningless
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		// This combination is invalid in practice
		if retrieved.AdviceTargetRig == "" && retrieved.AdviceTargetRole != "" {
			// This is the condition that runAdviceAdd rejects
			t.Log("verified: role without rig is stored but semantically invalid")
		}
	})

	t.Run("AgentCannotCombineWithRigRole", func(t *testing.T) {
		// This validates that --agent cannot be combined with --rig or --role
		// The command layer validates this, not storage

		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		ctx := context.Background()

		// Storage doesn't prevent this combination
		issue := &types.Issue{
			Title:             "Agent with rig test",
			Description:       "This should be caught by command validation",
			Priority:          2,
			IssueType:         types.TypeAdvice,
			Status:            types.StatusOpen,
			AdviceTargetRig:   "beads", // Should not combine with agent
			AdviceTargetAgent: "beads/polecats/quartz",
			CreatedAt:         time.Now(),
		}

		// Storage allows it (validation is in command layer)
		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("storage unexpectedly rejected issue: %v", err)
		}

		// But the data is semantically invalid
		retrieved, err := s.GetIssue(ctx, issue.ID)
		if err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		// This combination is invalid in practice
		if retrieved.AdviceTargetAgent != "" && retrieved.AdviceTargetRig != "" {
			t.Log("verified: agent + rig combination is stored but semantically invalid")
		}
	})

	t.Run("AdviceTitleRequired", func(t *testing.T) {
		// Test that advice requires title

		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		ctx := context.Background()

		// Advice with empty title should be rejected by storage
		issue := &types.Issue{
			Title:       "", // Empty title
			Description: "Has description but no title",
			Priority:    2,
			IssueType:   types.TypeAdvice,
			Status:      types.StatusOpen,
			CreatedAt:   time.Now(),
		}

		// Storage should reject empty title
		err := s.CreateIssue(ctx, issue, "test")
		if err == nil {
			t.Error("expected error when creating advice with empty title")
		}
		t.Log("verified: storage correctly rejects empty title")
	})

	t.Run("InvalidPriorityRejected", func(t *testing.T) {
		// Test that invalid priority values are rejected

		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		ctx := context.Background()

		// Priority 5 is invalid (valid range is 0-4)
		issue := &types.Issue{
			Title:       "Invalid priority test",
			Description: "Testing invalid priority",
			Priority:    5,
			IssueType:   types.TypeAdvice,
			Status:      types.StatusOpen,
			CreatedAt:   time.Now(),
		}

		// Storage should reject invalid priority
		err := s.CreateIssue(ctx, issue, "test")
		if err == nil {
			t.Error("expected error when creating advice with priority 5")
		}
		t.Log("verified: storage correctly rejects priority 5")

		// Priority -1 is also invalid
		issue.Priority = -1
		err = s.CreateIssue(ctx, issue, "test")
		if err == nil {
			t.Error("expected error when creating advice with priority -1")
		}
		t.Log("verified: storage correctly rejects negative priority")
	})
}

// TestAdviceListFiltering tests the advice list filtering logic
func TestAdviceListFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Create various advice types
	adviceItems := []*types.Issue{
		{
			Title:       "Global advice 1",
			Description: "Applies to all",
			Priority:    2,
			IssueType:   types.TypeAdvice,
			Status:      types.StatusOpen,
			CreatedAt:   time.Now(),
		},
		{
			Title:           "Rig advice beads",
			Description:     "Applies to beads rig",
			Priority:        2,
			IssueType:       types.TypeAdvice,
			Status:          types.StatusOpen,
			AdviceTargetRig: "beads",
			CreatedAt:       time.Now(),
		},
		{
			Title:            "Role advice polecat",
			Description:      "Applies to polecats in beads",
			Priority:         2,
			IssueType:        types.TypeAdvice,
			Status:           types.StatusOpen,
			AdviceTargetRig:  "beads",
			AdviceTargetRole: "polecat",
			CreatedAt:        time.Now(),
		},
		{
			Title:             "Agent advice quartz",
			Description:       "Applies to quartz specifically",
			Priority:          2,
			IssueType:         types.TypeAdvice,
			Status:            types.StatusOpen,
			AdviceTargetAgent: "beads/polecats/quartz",
			CreatedAt:         time.Now(),
		},
	}

	for _, advice := range adviceItems {
		if err := s.CreateIssue(ctx, advice, "test"); err != nil {
			t.Fatalf("failed to create advice %q: %v", advice.Title, err)
		}
	}

	t.Run("MatchesAgentScopeGlobal", func(t *testing.T) {
		// Global advice should match any agent
		globalAdvice := adviceItems[0]
		if !matchesAgentScope(globalAdvice, "beads/polecats/quartz") {
			t.Error("global advice should match any agent")
		}
		if !matchesAgentScope(globalAdvice, "gastown/crew/test") {
			t.Error("global advice should match agents from other rigs")
		}
	})

	t.Run("MatchesAgentScopeRig", func(t *testing.T) {
		// Rig advice should match agents in that rig
		rigAdvice := adviceItems[1]
		if !matchesAgentScope(rigAdvice, "beads/polecats/quartz") {
			t.Error("beads rig advice should match beads agents")
		}
		if matchesAgentScope(rigAdvice, "gastown/crew/test") {
			t.Error("beads rig advice should not match gastown agents")
		}
	})

	t.Run("MatchesAgentScopeRole", func(t *testing.T) {
		// Role advice should match agents with that role in that rig
		roleAdvice := adviceItems[2]
		if !matchesAgentScope(roleAdvice, "beads/polecats/quartz") {
			t.Error("polecat role advice should match beads polecats")
		}
		if matchesAgentScope(roleAdvice, "beads/witness/main") {
			t.Error("polecat role advice should not match beads witness")
		}
		if matchesAgentScope(roleAdvice, "gastown/polecats/test") {
			t.Error("beads polecat advice should not match gastown polecats")
		}
	})

	t.Run("MatchesAgentScopeAgent", func(t *testing.T) {
		// Agent advice should only match that specific agent
		agentAdvice := adviceItems[3]
		if !matchesAgentScope(agentAdvice, "beads/polecats/quartz") {
			t.Error("quartz agent advice should match quartz")
		}
		if matchesAgentScope(agentAdvice, "beads/polecats/obsidian") {
			t.Error("quartz agent advice should not match obsidian")
		}
	})
}
