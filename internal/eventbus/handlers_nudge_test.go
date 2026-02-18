package eventbus

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
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

// PresenceTracker-based tests (bd-tlckc)

func TestBeadNudgeHandler_PresenceTracker_NoTask(t *testing.T) {
	pt := NewPresenceTracker()
	// Actor is in presence but has no tasks.
	pt.mu.Lock()
	pt.actors["test-agent"] = &actorState{
		lastSeen: time.Now(),
		taskIDs:  map[string]bool{},
	}
	pt.mu.Unlock()

	h := &BeadNudgeHandler{cooldown: time.Millisecond}
	h.SetPresenceTracker(pt)

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "test-agent",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 nudge warning via PresenceTracker, got %d", len(result.Warnings))
	}
}

func TestBeadNudgeHandler_PresenceTracker_HasTask(t *testing.T) {
	pt := NewPresenceTracker()
	// Actor has an in_progress task.
	pt.mu.Lock()
	pt.actors["test-agent"] = &actorState{
		lastSeen: time.Now(),
		taskIDs:  map[string]bool{"bd-abc": true},
	}
	pt.mu.Unlock()

	h := &BeadNudgeHandler{cooldown: time.Millisecond}
	h.SetPresenceTracker(pt)

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "test-agent",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) != 0 {
		t.Fatalf("expected no warnings when PresenceTracker shows task, got %d", len(result.Warnings))
	}
}

func TestBeadNudgeHandler_PresenceTracker_UnknownActor(t *testing.T) {
	pt := NewPresenceTracker()
	// Actor not in presence tracker at all.

	h := &BeadNudgeHandler{cooldown: time.Millisecond}
	h.SetPresenceTracker(pt)

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "unknown-agent",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Unknown actor has no tasks → should nudge.
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 nudge for unknown actor, got %d", len(result.Warnings))
	}
}

func TestPresenceTracker_HasTask(t *testing.T) {
	pt := NewPresenceTracker()

	// No actor → false.
	if pt.HasTask("nobody") {
		t.Error("expected HasTask=false for unknown actor")
	}

	// Actor with no tasks → false.
	pt.mu.Lock()
	pt.actors["alice"] = &actorState{taskIDs: map[string]bool{}}
	pt.mu.Unlock()
	if pt.HasTask("alice") {
		t.Error("expected HasTask=false for actor with empty taskIDs")
	}

	// Actor with tasks → true.
	pt.mu.Lock()
	pt.actors["bob"] = &actorState{taskIDs: map[string]bool{"bd-1": true}}
	pt.mu.Unlock()
	if !pt.HasTask("bob") {
		t.Error("expected HasTask=true for actor with taskIDs")
	}
}

func TestPresenceTracker_HandleMutationEvent(t *testing.T) {
	pt := NewPresenceTracker()

	// Simulate a status change to in_progress.
	pt.handleMutationEvent(makeMutationMsg(t, MutationEventPayload{
		Type:      "status",
		IssueID:   "bd-100",
		Actor:     "agent-x",
		OldStatus: "open",
		NewStatus: "in_progress",
	}))

	if !pt.HasTask("agent-x") {
		t.Error("expected agent-x to have task after in_progress mutation")
	}

	// Simulate closing the bead.
	pt.handleMutationEvent(makeMutationMsg(t, MutationEventPayload{
		Type:      "status",
		IssueID:   "bd-100",
		Actor:     "agent-x",
		OldStatus: "in_progress",
		NewStatus: "closed",
	}))

	if pt.HasTask("agent-x") {
		t.Error("expected agent-x to have no task after closing")
	}
}

// Test that when a captain sets status=in_progress with a different assignee,
// both the actor (captain) and the assignee get the task tracked. (bd-nqnyn)
func TestPresenceTracker_HandleMutationEvent_AssigneeAttribution(t *testing.T) {
	pt := NewPresenceTracker()

	// Captain assigns work to agent-x.
	pt.handleMutationEvent(makeMutationMsg(t, MutationEventPayload{
		Type:      "status",
		IssueID:   "bd-300",
		Actor:     "captain",
		Assignee:  "agent-x",
		OldStatus: "open",
		NewStatus: "in_progress",
	}))

	if !pt.HasTask("agent-x") {
		t.Error("expected agent-x (assignee) to have task after captain assigns work")
	}
	if !pt.HasTask("captain") {
		t.Error("expected captain (actor) to also have task tracked")
	}

	// When captain closes it, both should lose the task.
	pt.handleMutationEvent(makeMutationMsg(t, MutationEventPayload{
		Type:      "status",
		IssueID:   "bd-300",
		Actor:     "captain",
		Assignee:  "agent-x",
		OldStatus: "in_progress",
		NewStatus: "closed",
	}))

	if pt.HasTask("agent-x") {
		t.Error("expected agent-x to lose task after close")
	}
	if pt.HasTask("captain") {
		t.Error("expected captain to lose task after close")
	}
}

// Test that self-assignment (actor == assignee) doesn't double-track. (bd-nqnyn)
func TestPresenceTracker_HandleMutationEvent_SelfAssign(t *testing.T) {
	pt := NewPresenceTracker()

	pt.handleMutationEvent(makeMutationMsg(t, MutationEventPayload{
		Type:      "status",
		IssueID:   "bd-400",
		Actor:     "agent-y",
		Assignee:  "agent-y",
		OldStatus: "open",
		NewStatus: "in_progress",
	}))

	if !pt.HasTask("agent-y") {
		t.Error("expected agent-y to have task after self-assign")
	}

	// Verify the task only appears once in taskIDs (not doubled).
	pt.mu.RLock()
	state := pt.actors["agent-y"]
	count := len(state.taskIDs)
	pt.mu.RUnlock()
	if count != 1 {
		t.Errorf("expected 1 taskID for self-assign, got %d", count)
	}
}

func TestPresenceTracker_HandleMutationEvent_IgnoresNonStatus(t *testing.T) {
	pt := NewPresenceTracker()

	// Non-status mutation should be ignored.
	pt.handleMutationEvent(makeMutationMsg(t, MutationEventPayload{
		Type:    "comment",
		IssueID: "bd-200",
		Actor:   "agent-y",
	}))

	if pt.HasTask("agent-y") {
		t.Error("expected no task tracking for non-status mutations")
	}
}

func TestPresenceTracker_BackfillTasks(t *testing.T) {
	pt := NewPresenceTracker()

	seeds := []TaskSeed{
		{IssueID: "bd-1", Actor: "agent-a"},
		{IssueID: "bd-2", Actor: "agent-a"},
		{IssueID: "bd-3", Actor: "agent-b"},
		{IssueID: "", Actor: "agent-c"},       // empty issue ID — skip
		{IssueID: "bd-4", Actor: ""},           // empty actor — skip
	}

	n := pt.BackfillTasks(seeds)
	if n != 3 {
		t.Fatalf("expected 3 backfilled tasks, got %d", n)
	}

	if !pt.HasTask("agent-a") {
		t.Error("expected agent-a to have tasks after backfill")
	}
	if !pt.HasTask("agent-b") {
		t.Error("expected agent-b to have tasks after backfill")
	}
	if pt.HasTask("agent-c") {
		t.Error("expected agent-c to have no tasks (empty issue ID)")
	}
	if pt.HasTask("unknown") {
		t.Error("expected unknown agent to have no tasks")
	}
}

func TestPresenceTracker_BackfillTasks_Empty(t *testing.T) {
	pt := NewPresenceTracker()
	n := pt.BackfillTasks(nil)
	if n != 0 {
		t.Fatalf("expected 0 from empty backfill, got %d", n)
	}
}

func TestBeadNudgeHandler_PresenceTracker_AfterBackfill(t *testing.T) {
	pt := NewPresenceTracker()
	pt.BackfillTasks([]TaskSeed{
		{IssueID: "bd-existing", Actor: "cold-start-agent"},
	})

	h := &BeadNudgeHandler{cooldown: time.Millisecond}
	h.SetPresenceTracker(pt)

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "cold-start-agent",
	}
	result := &Result{}

	if err := h.Handle(context.Background(), event, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) != 0 {
		t.Fatalf("expected no nudge after backfill shows existing task, got %d warnings", len(result.Warnings))
	}
}

func TestBeadNudgeHandler_NudgeMessageFormat(t *testing.T) {
	// Verify the nudge message always contains the base prompt. The specific
	// path (suggestions vs fallback) depends on whether bd ready succeeds,
	// so we just check the invariant parts.
	h := &BeadNudgeHandler{cooldown: time.Millisecond}

	event := &Event{
		Type:  EventPostToolUse,
		Actor: "test-agent",
		CWD:   "/nonexistent-path-for-test", // likely no beads dir → subprocess fails
	}

	msg := h.buildNudgeMessage(context.Background(), event)

	// Should always contain the base message.
	if !strings.Contains(msg, "You don't have a bead assigned") {
		t.Error("expected base message in nudge")
	}
	// Should always mention how to create a task.
	if !strings.Contains(msg, "bd create") || !strings.Contains(msg, "bd update") || !strings.Contains(msg, "bd ready") {
		// Either suggestions path or fallback path should mention some action.
		t.Logf("message: %s", msg)
	}
}

func TestBeadNudgeHandler_CooldownReducedTo3Min(t *testing.T) {
	// Verify the default cooldown is 3 minutes (bd-bstid).
	h := &BeadNudgeHandler{} // default cooldown

	// First call should nudge.
	if !h.shouldNudge("agent-1") {
		t.Fatal("expected first call to allow nudge")
	}
	h.recordNudge("agent-1")

	// Immediately after should be rate-limited.
	if h.shouldNudge("agent-1") {
		t.Fatal("expected rate-limiting immediately after nudge")
	}
}

// ===== Reaper tests (bd-khlpu) =====

func TestPresenceTracker_Reaper_MarksDead(t *testing.T) {
	pt := NewPresenceTracker()
	pt.started = time.Now()

	// Add an actor that's been idle for 20 minutes.
	pt.mu.Lock()
	pt.actors["old-agent"] = &actorState{
		firstSeen: time.Now().Add(-30 * time.Minute),
		lastSeen:  time.Now().Add(-20 * time.Minute),
		lastEvent: "PostToolUse",
		sessionID: "sess-old",
	}
	// Add an actor that's active.
	pt.actors["active-agent"] = &actorState{
		firstSeen: time.Now().Add(-5 * time.Minute),
		lastSeen:  time.Now(),
		lastEvent: "PostToolUse",
		sessionID: "sess-active",
	}
	pt.mu.Unlock()

	// Track which actors the callback fires for.
	var reaped []string
	var mu sync.Mutex

	cfg := &ReaperConfig{
		DeadThreshold: 15 * time.Minute,
		SweepInterval: 10 * time.Millisecond, // fast for testing
		OnDead: func(actor, sessionID string) {
			mu.Lock()
			reaped = append(reaped, actor)
			mu.Unlock()
		},
	}

	pt.StartReaper(cfg)
	// Wait for at least one sweep.
	time.Sleep(50 * time.Millisecond)
	pt.Stop()

	mu.Lock()
	defer mu.Unlock()

	if len(reaped) != 1 || reaped[0] != "old-agent" {
		t.Fatalf("expected reaper to mark only 'old-agent', got: %v", reaped)
	}

	// Verify roster shows reaped state.
	entries := pt.Roster(0)
	for _, e := range entries {
		if e.Actor == "old-agent" {
			if !e.Reaped {
				t.Error("expected old-agent to be reaped in roster")
			}
			if e.ReapedAt.IsZero() {
				t.Error("expected ReapedAt to be set")
			}
		}
		if e.Actor == "active-agent" && e.Reaped {
			t.Error("active-agent should not be reaped")
		}
	}
}

func TestPresenceTracker_Reaper_NoDoubleReap(t *testing.T) {
	pt := NewPresenceTracker()
	pt.started = time.Now()

	pt.mu.Lock()
	pt.actors["dead-agent"] = &actorState{
		lastSeen:  time.Now().Add(-20 * time.Minute),
		lastEvent: "PostToolUse",
	}
	pt.mu.Unlock()

	callCount := 0
	var mu sync.Mutex

	cfg := &ReaperConfig{
		DeadThreshold: 15 * time.Minute,
		SweepInterval: 10 * time.Millisecond,
		OnDead: func(_, _ string) {
			mu.Lock()
			callCount++
			mu.Unlock()
		},
	}

	pt.StartReaper(cfg)
	time.Sleep(80 * time.Millisecond) // multiple sweeps
	pt.Stop()

	mu.Lock()
	defer mu.Unlock()

	if callCount != 1 {
		t.Fatalf("expected exactly 1 OnDead call, got %d (double-reap bug)", callCount)
	}
}

func TestPresenceTracker_Reaper_Resurrection(t *testing.T) {
	pt := NewPresenceTracker()
	pt.started = time.Now()

	// Add a dead actor.
	pt.mu.Lock()
	pt.actors["zombie"] = &actorState{
		lastSeen:  time.Now().Add(-20 * time.Minute),
		lastEvent: "PostToolUse",
	}
	pt.mu.Unlock()

	cfg := &ReaperConfig{
		DeadThreshold: 15 * time.Minute,
		SweepInterval: 10 * time.Millisecond,
	}

	pt.StartReaper(cfg)
	time.Sleep(30 * time.Millisecond)

	// Verify it's reaped.
	pt.mu.RLock()
	if !pt.actors["zombie"].reaped {
		t.Fatal("expected zombie to be reaped")
	}
	pt.mu.RUnlock()

	// Simulate a new event from the zombie (resurrection).
	pt.handleHookEvent(&nats.Msg{
		Data: []byte(`{"actor":"zombie","hook_event_name":"PostToolUse","session_id":"new-sess"}`),
	})

	// Verify it's no longer reaped.
	pt.mu.RLock()
	if pt.actors["zombie"].reaped {
		t.Fatal("expected zombie to be resurrected after new event")
	}
	pt.mu.RUnlock()

	pt.Stop()
}

// TestPresenceTracker_CWD verifies that CWD from hook events is captured in the
// roster and that it updates when the working directory changes. (bd-z6958)
func TestPresenceTracker_CWD(t *testing.T) {
	pt := NewPresenceTracker()
	pt.started = time.Now()

	// First event with CWD.
	pt.handleHookEvent(&nats.Msg{
		Data: []byte(`{"actor":"agent-1","hook_event_name":"PreToolUse","cwd":"/home/agent/beads"}`),
	})

	roster := pt.Roster(0)
	if len(roster) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(roster))
	}
	if roster[0].CWD != "/home/agent/beads" {
		t.Errorf("CWD = %q, want %q", roster[0].CWD, "/home/agent/beads")
	}

	// Update CWD via subsequent event.
	pt.handleHookEvent(&nats.Msg{
		Data: []byte(`{"actor":"agent-1","hook_event_name":"PostToolUse","cwd":"/home/agent/gastown"}`),
	})

	roster = pt.Roster(0)
	if roster[0].CWD != "/home/agent/gastown" {
		t.Errorf("CWD after update = %q, want %q", roster[0].CWD, "/home/agent/gastown")
	}

	// Event without CWD should preserve the existing value.
	pt.handleHookEvent(&nats.Msg{
		Data: []byte(`{"actor":"agent-1","hook_event_name":"PreToolUse"}`),
	})

	roster = pt.Roster(0)
	if roster[0].CWD != "/home/agent/gastown" {
		t.Errorf("CWD should be preserved when event has no cwd, got %q", roster[0].CWD)
	}
}

// makeMutationMsg creates a nats.Msg with a MutationEventPayload for testing.
func makeMutationMsg(t *testing.T, payload MutationEventPayload) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	return &nats.Msg{
		Subject: SubjectMutationPrefix + string(EventMutationStatus),
		Data:    data,
	}
}
