package eventbus

import (
	"context"
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
// Rate-limited: at most one nudge per cooldown period (default 10 minutes)
// per actor, to avoid spamming agents with repeated reminders. (bd-0ttt3)
type BeadNudgeHandler struct {
	store    BeadAssignmentStore
	cooldown time.Duration

	mu        sync.Mutex
	lastNudge map[string]time.Time // actor → last nudge time
}

func (h *BeadNudgeHandler) ID() string          { return "bead-nudge" }
func (h *BeadNudgeHandler) Handles() []EventType { return []EventType{EventPostToolUse} }
func (h *BeadNudgeHandler) Priority() int        { return 40 }

// SetBeadAssignmentStore wires in direct storage access for in-process bead lookups.
func (h *BeadNudgeHandler) SetBeadAssignmentStore(store BeadAssignmentStore) { h.store = store }

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

	// Nudge!
	h.recordNudge(event.Actor)
	result.Warnings = append(result.Warnings,
		"You don't have a bead assigned. Run `bd ready` to find available work, "+
			"or `bd create --title=\"...\" --type=task` to track what you're working on. "+
			"Agents should always have an in_progress bead so others can see what you're doing.")

	return nil
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
		cooldown = 10 * time.Minute
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
// Uses in-process store when available, falls back to subprocess.
func (h *BeadNudgeHandler) actorHasTask(ctx context.Context, event *Event) (bool, error) {
	if h.store != nil {
		return h.checkInProcess(ctx, event.Actor)
	}
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
func (h *BeadNudgeHandler) checkSubprocess(ctx context.Context, event *Event) (bool, error) {
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event),
		"list", "--status=in_progress", "--json")
	if err != nil {
		return false, err
	}
	// If there's any output with issues, the agent likely has work.
	// The subprocess runs as the actor (BD_ACTOR is set), so the list
	// will show all in_progress beads. We check if any are assigned to this actor.
	return stdout != "" && stdout != "[]" && stdout != "null", nil
}
