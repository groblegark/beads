package eventbus

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// mockBeadAssignmentStore implements BeadAssignmentStore for testing.
type mockBeadAssignmentStore struct {
	issues []*types.Issue
}

func (m *mockBeadAssignmentStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	var result []*types.Issue
	for _, issue := range m.issues {
		if filter.Status != nil && issue.Status != *filter.Status {
			continue
		}
		if filter.Assignee != nil && issue.Assignee != *filter.Assignee {
			continue
		}
		result = append(result, issue)
	}
	return result, nil
}

func TestBeadNudgeHandler_NudgesUnassignedAgent(t *testing.T) {
	store := &mockBeadAssignmentStore{
		issues: []*types.Issue{}, // no in_progress beads
	}

	h := &BeadNudgeHandler{cooldown: time.Millisecond}
	h.SetBeadAssignmentStore(store)

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "test-agent",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) == 0 {
		t.Fatal("expected a nudge warning, got none")
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(result.Warnings))
	}
}

func TestBeadNudgeHandler_NoNudgeWhenAssigned(t *testing.T) {
	store := &mockBeadAssignmentStore{
		issues: []*types.Issue{
			{
				ID:       "bd-123",
				Title:    "Some task",
				Status:   types.StatusInProgress,
				Assignee: "test-agent",
			},
		},
	}

	h := &BeadNudgeHandler{cooldown: time.Millisecond}
	h.SetBeadAssignmentStore(store)

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "test-agent",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings when agent has a bead, got %d: %v", len(result.Warnings), result.Warnings)
	}
}

func TestBeadNudgeHandler_NoNudgeWhenCreatedBy(t *testing.T) {
	store := &mockBeadAssignmentStore{
		issues: []*types.Issue{
			{
				ID:        "bd-456",
				Title:     "Agent-created task",
				Status:    types.StatusInProgress,
				CreatedBy: "test-agent",
			},
		},
	}

	h := &BeadNudgeHandler{cooldown: time.Millisecond}
	h.SetBeadAssignmentStore(store)

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "test-agent",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings when agent created a bead, got %d", len(result.Warnings))
	}
}

func TestBeadNudgeHandler_RateLimiting(t *testing.T) {
	store := &mockBeadAssignmentStore{
		issues: []*types.Issue{}, // no beads
	}

	h := &BeadNudgeHandler{cooldown: time.Hour} // very long cooldown
	h.SetBeadAssignmentStore(store)

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "test-agent",
	}

	// First call should nudge.
	result1 := &Result{}
	if err := h.Handle(context.Background(), event, result1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result1.Warnings) != 1 {
		t.Fatalf("expected 1 warning on first call, got %d", len(result1.Warnings))
	}

	// Second call should be rate-limited.
	result2 := &Result{}
	if err := h.Handle(context.Background(), event, result2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result2.Warnings) != 0 {
		t.Fatalf("expected no warnings on rate-limited call, got %d", len(result2.Warnings))
	}
}

func TestBeadNudgeHandler_SkipsEmptyActor(t *testing.T) {
	h := &BeadNudgeHandler{cooldown: time.Millisecond}

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "", // no actor
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings for empty actor, got %d", len(result.Warnings))
	}
}
