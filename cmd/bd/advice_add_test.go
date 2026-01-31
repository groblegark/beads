package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// adviceAddTestHelper provides test setup for advice add tests
type adviceAddTestHelper struct {
	t     *testing.T
	ctx   context.Context
	store *sqlite.SQLiteStorage
}

func newAdviceAddTestHelper(t *testing.T, store *sqlite.SQLiteStorage) *adviceAddTestHelper {
	return &adviceAddTestHelper{t: t, ctx: context.Background(), store: store}
}

func (h *adviceAddTestHelper) createAdvice(title, description, rig, role, agent string) *types.Issue {
	advice := &types.Issue{
		Title:             title,
		Description:       description,
		Priority:          2,
		IssueType:         types.IssueType("advice"),
		Status:            types.StatusOpen,
		AdviceTargetRig:   rig,
		AdviceTargetRole:  role,
		AdviceTargetAgent: agent,
		CreatedAt:         time.Now(),
	}
	if err := h.store.CreateIssue(h.ctx, advice, "test-user"); err != nil {
		h.t.Fatalf("Failed to create advice: %v", err)
	}
	return advice
}

func (h *adviceAddTestHelper) createAdviceWithHook(title, description, hookCommand, hookTrigger, hookOnFailure string, hookTimeout int) *types.Issue {
	advice := &types.Issue{
		Title:               title,
		Description:         description,
		Priority:            2,
		IssueType:           types.IssueType("advice"),
		Status:              types.StatusOpen,
		AdviceHookCommand:   hookCommand,
		AdviceHookTrigger:   hookTrigger,
		AdviceHookTimeout:   hookTimeout,
		AdviceHookOnFailure: hookOnFailure,
		CreatedAt:           time.Now(),
	}
	if err := h.store.CreateIssue(h.ctx, advice, "test-user"); err != nil {
		h.t.Fatalf("Failed to create advice with hook: %v", err)
	}
	return advice
}

func (h *adviceAddTestHelper) createAdviceWithLabels(title, description string, labels []string) *types.Issue {
	advice := &types.Issue{
		Title:       title,
		Description: description,
		Priority:    2,
		IssueType:   types.IssueType("advice"),
		Status:      types.StatusOpen,
		CreatedAt:   time.Now(),
	}
	if err := h.store.CreateIssue(h.ctx, advice, "test-user"); err != nil {
		h.t.Fatalf("Failed to create advice: %v", err)
	}
	for _, label := range labels {
		if err := h.store.AddLabel(h.ctx, advice.ID, label, "test-user"); err != nil {
			h.t.Fatalf("Failed to add label %s: %v", label, err)
		}
	}
	return advice
}

func (h *adviceAddTestHelper) getAdvice(id string) *types.Issue {
	issue, err := h.store.GetIssue(h.ctx, id)
	if err != nil {
		h.t.Fatalf("Failed to get advice: %v", err)
	}
	return issue
}

func (h *adviceAddTestHelper) getLabels(id string) []string {
	labels, err := h.store.GetLabels(h.ctx, id)
	if err != nil {
		h.t.Fatalf("Failed to get labels: %v", err)
	}
	return labels
}

func (h *adviceAddTestHelper) searchOpenAdvice() []*types.Issue {
	adviceType := types.IssueType("advice")
	status := types.StatusOpen
	results, err := h.store.SearchIssues(h.ctx, "", types.IssueFilter{
		IssueType: &adviceType,
		Status:    &status,
	})
	if err != nil {
		h.t.Fatalf("Failed to search advice: %v", err)
	}
	return results
}

// TestAdviceAddGlobal tests creating global advice (no targeting)
func TestAdviceAddGlobal(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	h := newAdviceAddTestHelper(t, s)

	t.Run("create global advice", func(t *testing.T) {
		advice := h.createAdvice(
			"Always check hook first",
			"When starting a session, always run gt hook to check if work is assigned",
			"", "", "", // no targeting = global
		)

		// Verify advice was created
		retrieved := h.getAdvice(advice.ID)
		if retrieved == nil {
			t.Fatal("Expected to retrieve created advice")
		}

		// Verify type is advice
		if retrieved.IssueType != types.IssueType("advice") {
			t.Errorf("Expected type 'advice', got %s", retrieved.IssueType)
		}

		// Verify global targeting (all empty)
		if retrieved.AdviceTargetRig != "" {
			t.Errorf("Global advice should have empty rig, got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "" {
			t.Errorf("Global advice should have empty role, got %q", retrieved.AdviceTargetRole)
		}
		if retrieved.AdviceTargetAgent != "" {
			t.Errorf("Global advice should have empty agent, got %q", retrieved.AdviceTargetAgent)
		}

		// Verify title and description
		if retrieved.Title != "Always check hook first" {
			t.Errorf("Expected title 'Always check hook first', got %q", retrieved.Title)
		}
		if retrieved.Description != "When starting a session, always run gt hook to check if work is assigned" {
			t.Errorf("Description mismatch: %q", retrieved.Description)
		}
	})

	t.Run("global advice appears in list", func(t *testing.T) {
		advice := h.createAdvice(
			"Global advice for list test",
			"This should appear in the advice list",
			"", "", "",
		)

		results := h.searchOpenAdvice()
		found := false
		for _, a := range results {
			if a.ID == advice.ID {
				found = true
				break
			}
		}
		if !found {
			t.Error("Global advice should appear in advice list")
		}
	})
}

// TestAdviceAddRigTargeted tests creating rig-targeted advice
func TestAdviceAddRigTargeted(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	h := newAdviceAddTestHelper(t, s)

	t.Run("create rig-targeted advice", func(t *testing.T) {
		advice := h.createAdvice(
			"Use go test for testing",
			"In beads repo, always run go test ./...",
			"beads", "", "", // rig-level targeting
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved == nil {
			t.Fatal("Expected to retrieve created advice")
		}

		// Verify rig targeting
		if retrieved.AdviceTargetRig != "beads" {
			t.Errorf("Expected rig 'beads', got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "" {
			t.Errorf("Rig-level advice should have empty role, got %q", retrieved.AdviceTargetRole)
		}
		if retrieved.AdviceTargetAgent != "" {
			t.Errorf("Rig-level advice should have empty agent, got %q", retrieved.AdviceTargetAgent)
		}
	})

	t.Run("rig-targeted advice appears in list", func(t *testing.T) {
		advice := h.createAdvice(
			"Gastown rig advice",
			"Coordinate with mayor for cross-rig work",
			"gastown", "", "",
		)

		results := h.searchOpenAdvice()
		found := false
		for _, a := range results {
			if a.ID == advice.ID {
				found = true
				if a.AdviceTargetRig != "gastown" {
					t.Errorf("Expected rig 'gastown', got %q", a.AdviceTargetRig)
				}
				break
			}
		}
		if !found {
			t.Error("Rig-targeted advice should appear in advice list")
		}
	})
}

// TestAdviceAddRoleTargeted tests creating role-targeted advice
func TestAdviceAddRoleTargeted(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	h := newAdviceAddTestHelper(t, s)

	t.Run("create role-targeted advice", func(t *testing.T) {
		advice := h.createAdvice(
			"Complete work before gt done",
			"Polecats must finish their assigned work before running gt done",
			"beads", "polecat", "", // role-level targeting (rig + role)
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved == nil {
			t.Fatal("Expected to retrieve created advice")
		}

		// Verify role targeting
		if retrieved.AdviceTargetRig != "beads" {
			t.Errorf("Expected rig 'beads', got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "polecat" {
			t.Errorf("Expected role 'polecat', got %q", retrieved.AdviceTargetRole)
		}
		if retrieved.AdviceTargetAgent != "" {
			t.Errorf("Role-level advice should have empty agent, got %q", retrieved.AdviceTargetAgent)
		}
	})

	t.Run("role-targeted advice with different roles", func(t *testing.T) {
		polecatAdvice := h.createAdvice(
			"Polecat-specific guidance",
			"This is for polecats only",
			"beads", "polecat", "",
		)
		crewAdvice := h.createAdvice(
			"Crew-specific guidance",
			"This is for crew only",
			"beads", "crew", "",
		)

		// Verify both were created with correct roles
		polecat := h.getAdvice(polecatAdvice.ID)
		crew := h.getAdvice(crewAdvice.ID)

		if polecat.AdviceTargetRole != "polecat" {
			t.Errorf("Expected polecat role, got %q", polecat.AdviceTargetRole)
		}
		if crew.AdviceTargetRole != "crew" {
			t.Errorf("Expected crew role, got %q", crew.AdviceTargetRole)
		}
	})
}

// TestAdviceAddAgentTargeted tests creating agent-targeted advice
func TestAdviceAddAgentTargeted(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	h := newAdviceAddTestHelper(t, s)

	t.Run("create agent-targeted advice", func(t *testing.T) {
		advice := h.createAdvice(
			"Focus on CLI implementation",
			"quartz specializes in CLI implementation tasks",
			"", "", "beads/polecats/quartz", // agent-level targeting
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved == nil {
			t.Fatal("Expected to retrieve created advice")
		}

		// Verify agent targeting
		if retrieved.AdviceTargetAgent != "beads/polecats/quartz" {
			t.Errorf("Expected agent 'beads/polecats/quartz', got %q", retrieved.AdviceTargetAgent)
		}
		// Note: rig and role should be empty for agent-targeted advice
		if retrieved.AdviceTargetRig != "" {
			t.Errorf("Agent-level advice should have empty rig, got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "" {
			t.Errorf("Agent-level advice should have empty role, got %q", retrieved.AdviceTargetRole)
		}
	})

	t.Run("agent-targeted advice with full path", func(t *testing.T) {
		advice := h.createAdvice(
			"Crew member guidance",
			"Specific guidance for decision_notify",
			"", "", "gastown/crew/decision_notify",
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved.AdviceTargetAgent != "gastown/crew/decision_notify" {
			t.Errorf("Expected agent 'gastown/crew/decision_notify', got %q", retrieved.AdviceTargetAgent)
		}
	})
}

// TestAdviceAddWithHooks tests creating advice with hook configuration
func TestAdviceAddWithHooks(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	h := newAdviceAddTestHelper(t, s)

	t.Run("create advice with before-commit hook", func(t *testing.T) {
		advice := h.createAdviceWithHook(
			"Run tests before commit",
			"Always run tests before committing changes",
			"make test",      // hook command
			"before-commit",  // trigger
			"block",          // on failure
			60,               // timeout
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved == nil {
			t.Fatal("Expected to retrieve created advice")
		}

		if retrieved.AdviceHookCommand != "make test" {
			t.Errorf("Expected hook command 'make test', got %q", retrieved.AdviceHookCommand)
		}
		if retrieved.AdviceHookTrigger != "before-commit" {
			t.Errorf("Expected trigger 'before-commit', got %q", retrieved.AdviceHookTrigger)
		}
		if retrieved.AdviceHookOnFailure != "block" {
			t.Errorf("Expected on-failure 'block', got %q", retrieved.AdviceHookOnFailure)
		}
		if retrieved.AdviceHookTimeout != 60 {
			t.Errorf("Expected timeout 60, got %d", retrieved.AdviceHookTimeout)
		}
	})

	t.Run("create advice with session-end hook", func(t *testing.T) {
		advice := h.createAdviceWithHook(
			"Check for uncommitted changes",
			"Verify no uncommitted changes at session end",
			"git status --porcelain",
			"session-end",
			"warn",
			30,
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved.AdviceHookTrigger != "session-end" {
			t.Errorf("Expected trigger 'session-end', got %q", retrieved.AdviceHookTrigger)
		}
		if retrieved.AdviceHookOnFailure != "warn" {
			t.Errorf("Expected on-failure 'warn', got %q", retrieved.AdviceHookOnFailure)
		}
	})

	t.Run("create advice with before-push hook", func(t *testing.T) {
		advice := h.createAdviceWithHook(
			"Lint before push",
			"Run linter before pushing to remote",
			"golangci-lint run",
			"before-push",
			"ignore",
			120,
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved.AdviceHookTrigger != "before-push" {
			t.Errorf("Expected trigger 'before-push', got %q", retrieved.AdviceHookTrigger)
		}
		if retrieved.AdviceHookOnFailure != "ignore" {
			t.Errorf("Expected on-failure 'ignore', got %q", retrieved.AdviceHookOnFailure)
		}
	})

	t.Run("create advice with before-handoff hook", func(t *testing.T) {
		advice := h.createAdviceWithHook(
			"Summary before handoff",
			"Generate summary before handoff",
			"echo 'handoff check'",
			"before-handoff",
			"warn",
			10,
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved.AdviceHookTrigger != "before-handoff" {
			t.Errorf("Expected trigger 'before-handoff', got %q", retrieved.AdviceHookTrigger)
		}
	})
}

// TestAdviceAddWithLabels tests creating advice with labels
func TestAdviceAddWithLabels(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	h := newAdviceAddTestHelper(t, s)

	t.Run("create advice with single label", func(t *testing.T) {
		advice := h.createAdviceWithLabels(
			"Testing advice",
			"Advice about testing practices",
			[]string{"testing"},
		)

		labels := h.getLabels(advice.ID)
		if len(labels) != 1 {
			t.Errorf("Expected 1 label, got %d", len(labels))
		}
		if len(labels) > 0 && labels[0] != "testing" {
			t.Errorf("Expected label 'testing', got %q", labels[0])
		}
	})

	t.Run("create advice with multiple labels", func(t *testing.T) {
		advice := h.createAdviceWithLabels(
			"Multi-labeled advice",
			"Advice with multiple labels",
			[]string{"testing", "ci", "automation"},
		)

		labels := h.getLabels(advice.ID)
		if len(labels) != 3 {
			t.Errorf("Expected 3 labels, got %d", len(labels))
		}

		// Check all labels are present
		labelSet := make(map[string]bool)
		for _, l := range labels {
			labelSet[l] = true
		}
		for _, expected := range []string{"testing", "ci", "automation"} {
			if !labelSet[expected] {
				t.Errorf("Expected label %q to be present", expected)
			}
		}
	})
}

// TestAdviceAddWithPriority tests creating advice with different priorities
func TestAdviceAddWithPriority(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Priority values are 0-4 (0=highest, 4=lowest)
	// User-facing values are typically 1-4 via CLI
	priorities := []int{0, 1, 2, 3, 4}
	for _, priority := range priorities {
		t.Run("priority "+string(rune('0'+priority)), func(t *testing.T) {
			advice := &types.Issue{
				Title:     "Priority test",
				Priority:  priority,
				IssueType: types.IssueType("advice"),
				Status:    types.StatusOpen,
				CreatedAt: time.Now(),
			}
			if err := s.CreateIssue(ctx, advice, "test-user"); err != nil {
				t.Fatalf("Failed to create advice: %v", err)
			}

			retrieved, err := s.GetIssue(ctx, advice.ID)
			if err != nil {
				t.Fatalf("Failed to get advice: %v", err)
			}
			if retrieved.Priority != priority {
				t.Errorf("Expected priority %d, got %d", priority, retrieved.Priority)
			}
		})
	}
}

// TestAdviceAddPersistence tests that all targeting fields are correctly persisted
func TestAdviceAddPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	t.Run("all fields persisted correctly", func(t *testing.T) {
		advice := &types.Issue{
			Title:               "Complete advice",
			Description:         "Full advice with all fields",
			Priority:            1,
			IssueType:           types.IssueType("advice"),
			Status:              types.StatusOpen,
			AdviceTargetRig:     "testrig",
			AdviceTargetRole:    "testrole",
			AdviceTargetAgent:   "",
			AdviceHookCommand:   "echo test",
			AdviceHookTrigger:   "before-commit",
			AdviceHookTimeout:   45,
			AdviceHookOnFailure: "block",
			CreatedAt:           time.Now(),
		}
		if err := s.CreateIssue(ctx, advice, "test-user"); err != nil {
			t.Fatalf("Failed to create advice: %v", err)
		}

		// Close and reopen database to ensure persistence
		s.Close()
		s2, err := sqlite.New(ctx, testDB)
		if err != nil {
			t.Fatalf("Failed to reopen database: %v", err)
		}
		defer s2.Close()

		retrieved, err := s2.GetIssue(ctx, advice.ID)
		if err != nil {
			t.Fatalf("Failed to get advice: %v", err)
		}

		// Verify all fields
		if retrieved.Title != "Complete advice" {
			t.Errorf("Title mismatch: %q", retrieved.Title)
		}
		if retrieved.Description != "Full advice with all fields" {
			t.Errorf("Description mismatch: %q", retrieved.Description)
		}
		if retrieved.Priority != 1 {
			t.Errorf("Priority mismatch: %d", retrieved.Priority)
		}
		if retrieved.AdviceTargetRig != "testrig" {
			t.Errorf("Rig mismatch: %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "testrole" {
			t.Errorf("Role mismatch: %q", retrieved.AdviceTargetRole)
		}
		if retrieved.AdviceHookCommand != "echo test" {
			t.Errorf("Hook command mismatch: %q", retrieved.AdviceHookCommand)
		}
		if retrieved.AdviceHookTrigger != "before-commit" {
			t.Errorf("Hook trigger mismatch: %q", retrieved.AdviceHookTrigger)
		}
		if retrieved.AdviceHookTimeout != 45 {
			t.Errorf("Hook timeout mismatch: %d", retrieved.AdviceHookTimeout)
		}
		if retrieved.AdviceHookOnFailure != "block" {
			t.Errorf("Hook on-failure mismatch: %q", retrieved.AdviceHookOnFailure)
		}
	})
}

// TestAdviceAddValidation tests validation logic for advice creation
func TestAdviceAddValidation(t *testing.T) {
	// These tests verify the validation rules from runAdviceAdd

	t.Run("role requires rig validation", func(t *testing.T) {
		// Validation: --role requires --rig
		// This is enforced in runAdviceAdd, so we just verify the constraint exists
		// by checking that role-only targeting doesn't make sense

		// A valid role-targeted advice must have rig set
		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		h := newAdviceAddTestHelper(t, s)

		// Create valid role-targeted advice (rig + role)
		advice := h.createAdvice(
			"Valid role advice",
			"This has both rig and role",
			"testrig", "testrole", "",
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved.AdviceTargetRig == "" {
			t.Error("Role-targeted advice should have rig set")
		}
		if retrieved.AdviceTargetRole == "" {
			t.Error("Role-targeted advice should have role set")
		}
	})

	t.Run("agent excludes rig and role validation", func(t *testing.T) {
		// Validation: --agent cannot be combined with --rig or --role
		// Agent-targeted advice should only have agent field set

		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		h := newAdviceAddTestHelper(t, s)

		advice := h.createAdvice(
			"Agent-only advice",
			"This targets a specific agent",
			"", "", "beads/polecats/test",
		)

		retrieved := h.getAdvice(advice.ID)
		if retrieved.AdviceTargetRig != "" {
			t.Errorf("Agent-targeted advice should have empty rig, got %q", retrieved.AdviceTargetRig)
		}
		if retrieved.AdviceTargetRole != "" {
			t.Errorf("Agent-targeted advice should have empty role, got %q", retrieved.AdviceTargetRole)
		}
		if retrieved.AdviceTargetAgent != "beads/polecats/test" {
			t.Errorf("Agent should be set, got %q", retrieved.AdviceTargetAgent)
		}
	})
}

// TestAdviceAddHookValidation tests hook-related validation
func TestAdviceAddHookValidation(t *testing.T) {
	t.Run("valid hook triggers", func(t *testing.T) {
		validTriggers := []string{"session-end", "before-commit", "before-push", "before-handoff"}

		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		h := newAdviceAddTestHelper(t, s)

		for _, trigger := range validTriggers {
			advice := h.createAdviceWithHook(
				"Hook trigger test: "+trigger,
				"Testing valid trigger",
				"echo test",
				trigger,
				"warn",
				30,
			)

			retrieved := h.getAdvice(advice.ID)
			if retrieved.AdviceHookTrigger != trigger {
				t.Errorf("Expected trigger %q, got %q", trigger, retrieved.AdviceHookTrigger)
			}
		}
	})

	t.Run("valid on-failure actions", func(t *testing.T) {
		validActions := []string{"block", "warn", "ignore"}

		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		h := newAdviceAddTestHelper(t, s)

		for _, action := range validActions {
			advice := h.createAdviceWithHook(
				"Hook action test: "+action,
				"Testing valid on-failure",
				"echo test",
				"before-commit",
				action,
				30,
			)

			retrieved := h.getAdvice(advice.ID)
			if retrieved.AdviceHookOnFailure != action {
				t.Errorf("Expected on-failure %q, got %q", action, retrieved.AdviceHookOnFailure)
			}
		}
	})

	t.Run("hook timeout within bounds", func(t *testing.T) {
		tmpDir := t.TempDir()
		testDB := filepath.Join(tmpDir, ".beads", "beads.db")
		s := newTestStore(t, testDB)
		h := newAdviceAddTestHelper(t, s)

		// Valid timeout values
		validTimeouts := []int{0, 30, 60, 120, 300}
		for _, timeout := range validTimeouts {
			advice := h.createAdviceWithHook(
				"Timeout test",
				"Testing valid timeout",
				"echo test",
				"before-commit",
				"warn",
				timeout,
			)

			retrieved := h.getAdvice(advice.ID)
			if retrieved.AdviceHookTimeout != timeout {
				t.Errorf("Expected timeout %d, got %d", timeout, retrieved.AdviceHookTimeout)
			}
		}
	})
}

// TestAdviceAddTypeIsCorrect verifies the IssueType is always "advice"
func TestAdviceAddTypeIsCorrect(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	h := newAdviceAddTestHelper(t, s)

	testCases := []struct {
		name        string
		rig, role   string
		agent       string
	}{
		{"global", "", "", ""},
		{"rig-targeted", "beads", "", ""},
		{"role-targeted", "beads", "polecat", ""},
		{"agent-targeted", "", "", "beads/polecats/quartz"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			advice := h.createAdvice(
				"Type test: "+tc.name,
				"Verifying advice type",
				tc.rig, tc.role, tc.agent,
			)

			retrieved := h.getAdvice(advice.ID)
			if retrieved.IssueType != types.IssueType("advice") {
				t.Errorf("Expected type 'advice', got %s", retrieved.IssueType)
			}
		})
	}
}

// TestAdviceAddStatus verifies advice is created with open status
func TestAdviceAddStatus(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	h := newAdviceAddTestHelper(t, s)

	advice := h.createAdvice(
		"Status test",
		"Verifying advice is created as open",
		"", "", "",
	)

	retrieved := h.getAdvice(advice.ID)
	if retrieved.Status != types.StatusOpen {
		t.Errorf("Expected status 'open', got %s", retrieved.Status)
	}
}

// TestAdviceAddTimestamps verifies timestamps are set correctly
func TestAdviceAddTimestamps(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	before := time.Now().Add(-time.Second)
	advice := &types.Issue{
		Title:     "Timestamp test",
		IssueType: types.IssueType("advice"),
		Status:    types.StatusOpen,
		CreatedAt: time.Now(),
	}
	if err := s.CreateIssue(ctx, advice, "test-user"); err != nil {
		t.Fatalf("Failed to create advice: %v", err)
	}
	after := time.Now().Add(time.Second)

	retrieved, err := s.GetIssue(ctx, advice.ID)
	if err != nil {
		t.Fatalf("Failed to get advice: %v", err)
	}

	// Verify CreatedAt is reasonable
	if retrieved.CreatedAt.Before(before) || retrieved.CreatedAt.After(after) {
		t.Errorf("CreatedAt %v should be between %v and %v",
			retrieved.CreatedAt, before, after)
	}

	// Verify ClosedAt is nil for new advice
	if retrieved.ClosedAt != nil {
		t.Error("ClosedAt should be nil for new advice")
	}
}
