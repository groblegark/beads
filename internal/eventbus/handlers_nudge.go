package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// BeadAssignmentStore is the minimal storage interface for the bead nudge
// handler to check whether an actor has in_progress beads.
type BeadAssignmentStore interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
}

// BeadNudgeHandler nudges agents that are actively working but don't have
// any in_progress bead assigned to them. Fires on PostToolUse events.
// Priority 40 (runs after inbox drain — low priority, informational only).
//
// Uses PresenceTracker for O(1) in-memory task checks when available (bd-tlckc).
// Falls back to store query or subprocess when presence tracking isn't wired.
//
// Rate-limited: at most one nudge per cooldown period (default 3 minutes)
// per actor, to avoid spamming agents with repeated reminders. (bd-0ttt3, bd-bstid)
type BeadNudgeHandler struct {
	store    BeadAssignmentStore
	presence *PresenceTracker
	cooldown time.Duration

	mu        sync.Mutex
	lastNudge map[string]time.Time // actor → last nudge time
}

func (h *BeadNudgeHandler) ID() string          { return "bead-nudge" }
func (h *BeadNudgeHandler) Handles() []EventType { return []EventType{EventPostToolUse} }
func (h *BeadNudgeHandler) Priority() int        { return 40 }

// SetBeadAssignmentStore wires in direct storage access for in-process bead lookups.
func (h *BeadNudgeHandler) SetBeadAssignmentStore(store BeadAssignmentStore) { h.store = store }

// SetPresenceTracker wires in the PresenceTracker for O(1) task checks. (bd-tlckc)
func (h *BeadNudgeHandler) SetPresenceTracker(pt *PresenceTracker) { h.presence = pt }

func (h *BeadNudgeHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	// Only nudge known agents (actor must be set by daemon).
	if event.Actor == "" {
		return nil
	}

	// Rate-limit: check cooldown.
	if !h.shouldNudge(event.Actor) {
		return nil
	}

	// Check if agent has any in_progress beads.
	hasTask, err := h.actorHasTask(ctx, event)
	if err != nil {
		// Don't fail the handler chain for nudge errors.
		return nil
	}

	if hasTask {
		return nil
	}

	// Nudge! Include ready task suggestions when possible. (bd-bstid)
	h.recordNudge(event.Actor)
	nudgeMsg := h.buildNudgeMessage(ctx, event)
	result.Warnings = append(result.Warnings, nudgeMsg)

	return nil
}

// buildNudgeMessage creates a nudge message, optionally including ready task
// suggestions so the agent can claim work immediately. (bd-bstid)
func (h *BeadNudgeHandler) buildNudgeMessage(ctx context.Context, event *Event) string {
	var b strings.Builder
	b.WriteString("You don't have a bead assigned. " +
		"Agents should always have an in_progress bead so others can see what you're doing.")

	// Try to get ready task suggestions.
	suggestions := h.getReadyTaskSuggestions(ctx, event)
	if len(suggestions) > 0 {
		b.WriteString("\n\nReady tasks you could claim:")
		for _, s := range suggestions {
			fmt.Fprintf(&b, "\n  - `bd update %s --status=in_progress` — %s", s.id, s.title)
		}
		b.WriteString("\n\nOr create a new one: `bd create --title=\"...\" --type=task`")
	} else {
		b.WriteString(" Run `bd ready` to find available work, " +
			"or `bd create --title=\"...\" --type=task` to track what you're working on.")
	}

	return b.String()
}

type readyTaskSuggestion struct {
	id    string
	title string
}

// getReadyTaskSuggestions returns up to 3 ready tasks for the nudge message.
func (h *BeadNudgeHandler) getReadyTaskSuggestions(ctx context.Context, event *Event) []readyTaskSuggestion {
	// Try subprocess: `bd ready --json` to get unblocked tasks.
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event),
		"ready", "--json", "--limit=3")
	if err != nil || stdout == "" || stdout == "[]" || stdout == "null" {
		return nil
	}

	// Parse JSON array of issues.
	var issues []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if jsonErr := json.Unmarshal([]byte(stdout), &issues); jsonErr != nil {
		return nil
	}

	suggestions := make([]readyTaskSuggestion, 0, 3)
	for _, issue := range issues {
		if len(suggestions) >= 3 {
			break
		}
		suggestions = append(suggestions, readyTaskSuggestion{id: issue.ID, title: issue.Title})
	}
	return suggestions
}

// shouldNudge returns true if enough time has passed since the last nudge
// for this actor.
func (h *BeadNudgeHandler) shouldNudge(actor string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.lastNudge == nil {
		h.lastNudge = make(map[string]time.Time)
	}

	cooldown := h.cooldown
	if cooldown == 0 {
		cooldown = 3 * time.Minute // (bd-bstid) reduced from 10min to nudge more frequently
	}

	last, ok := h.lastNudge[actor]
	if ok && time.Since(last) < cooldown {
		return false
	}
	return true
}

// recordNudge marks the current time as the last nudge for an actor.
func (h *BeadNudgeHandler) recordNudge(actor string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.lastNudge == nil {
		h.lastNudge = make(map[string]time.Time)
	}
	h.lastNudge[actor] = time.Now()
}

// actorHasTask checks whether the actor has any in_progress beads.
// Priority: PresenceTracker (O(1) in-memory) > store query > subprocess. (bd-tlckc)
func (h *BeadNudgeHandler) actorHasTask(ctx context.Context, event *Event) (bool, error) {
	// Fast path: PresenceTracker has in-memory task state from mutation events.
	if h.presence != nil {
		return h.presence.HasTask(event.Actor), nil
	}
	// Slow path: query the database directly.
	if h.store != nil {
		return h.checkInProcess(ctx, event.Actor)
	}
	// Fallback: subprocess.
	return h.checkSubprocess(ctx, event)
}

// checkInProcess queries the store directly for in_progress beads assigned to/created by the actor.
func (h *BeadNudgeHandler) checkInProcess(ctx context.Context, actor string) (bool, error) {
	inProgress := types.StatusInProgress
	// Check assignee first.
	issues, err := h.store.SearchIssues(ctx, "", types.IssueFilter{
		Status:   &inProgress,
		Assignee: &actor,
	})
	if err != nil {
		return false, err
	}
	if len(issues) > 0 {
		return true, nil
	}

	// Also check created_by (agents often create beads without explicit assignment).
	issues, err = h.store.SearchIssues(ctx, "", types.IssueFilter{
		Status: &inProgress,
	})
	if err != nil {
		return false, err
	}
	for _, issue := range issues {
		if issue.CreatedBy == actor {
			return true, nil
		}
	}
	return false, nil
}

// checkSubprocess falls back to running bd list as a subprocess.
// Filters results by actor to avoid false negatives in multi-agent environments
// where another agent's in_progress beads would suppress nudges. (bd-nqnyn)
func (h *BeadNudgeHandler) checkSubprocess(ctx context.Context, event *Event) (bool, error) {
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event),
		"list", "--status=in_progress", "--json")
	if err != nil {
		return false, err
	}
	if stdout == "" || stdout == "[]" || stdout == "null" {
		return false, nil
	}

	// Parse and filter by actor — only count beads owned by this agent.
	var issues []struct {
		Assignee  string `json:"assignee"`
		CreatedBy string `json:"created_by"`
	}
	if jsonErr := json.Unmarshal([]byte(stdout), &issues); jsonErr != nil {
		return false, nil
	}
	for _, issue := range issues {
		if issue.Assignee == event.Actor || issue.CreatedBy == event.Actor {
			return true, nil
		}
	}
	return false, nil
}
