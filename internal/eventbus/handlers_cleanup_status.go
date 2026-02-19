package eventbus

import (
	"context"
	"fmt"
	"log"
	"os"
)

// CleanupStatusHandler writes cleanup_status to the agent bead on Stop events,
// enabling the Witness to safely auto-nuke crashed polecats without waiting
// for gt done (which may never be called if the polecat crashes).
//
// Priority 85 (runs after all other Stop handlers but before DoneWaitHandler
// at 90 — cleanup_status should reflect the agent's state at exit time, not
// during the wait period).
//
// Only runs when BD_ACTOR is set (i.e., inside an agent context). Writes
// cleanup_status to the agent bead's metadata field:
//   - "clean" — no uncommitted changes, branch pushed to origin
//   - "has_uncommitted" — has modified/untracked files
//   - "has_stash" — has stashed work
//   - "has_unpushed" — local commits not on origin
//   - "unknown" — git checks failed
//
// (bd-6eflz)
type CleanupStatusHandler struct{}

func (h *CleanupStatusHandler) ID() string          { return "cleanup-status" }
func (h *CleanupStatusHandler) Handles() []EventType { return []EventType{EventStop} }
func (h *CleanupStatusHandler) Priority() int        { return 85 }

func (h *CleanupStatusHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	// Only run for agents (not bare daemon usage).
	actor := event.Actor
	if actor == "" {
		actor = os.Getenv("BD_ACTOR")
	}
	if actor == "" {
		return nil
	}

	// Run bd agent cleanup-status which checks git state and writes to bead.
	stdout, stderr, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), "agent", "cleanup-status")
	if err != nil {
		// Non-fatal — cleanup_status is best-effort. Log but don't block.
		log.Printf("[cleanup-status] failed for %s: %v (stderr: %s)", actor, err, stderr)
		return nil
	}

	if stdout != "" {
		// Inject the cleanup status as context for downstream handlers.
		result.Inject = append(result.Inject, fmt.Sprintf("[cleanup-status] %s", stdout))
	}

	return nil
}
