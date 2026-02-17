package eventbus

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CommitNudgeHandler detects uncommitted changes in an agent's working tree
// at session Stop and nudges the agent to commit before going idle.
// Uncommitted WIP in shared repos breaks CI for other agents — RWX CI
// auto-patches uncommitted changes into builds, creating broken states.
//
// Priority 45 (runs after inbox drain and bead nudge — last-chance reminder).
// Rate-limited per actor to avoid spamming on repeated Stop events. (bd-z4a0u)
type CommitNudgeHandler struct {
	cooldown time.Duration

	mu        sync.Mutex
	lastNudge map[string]time.Time // actor → last nudge time
}

func (h *CommitNudgeHandler) ID() string           { return "commit-nudge" }
func (h *CommitNudgeHandler) Handles() []EventType { return []EventType{EventStop} }
func (h *CommitNudgeHandler) Priority() int         { return 45 }

func (h *CommitNudgeHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	// Need both actor and CWD to check git status.
	if event.Actor == "" || event.CWD == "" {
		return nil
	}

	// Rate-limit: don't spam the same agent.
	if !h.shouldNudge(event.Actor) {
		return nil
	}

	// Check for uncommitted changes in the agent's working directory.
	dirty, summary, err := h.checkGitDirty(ctx, event.CWD)
	if err != nil {
		// Not a git repo or git not available — skip silently.
		return nil
	}

	if !dirty {
		return nil
	}

	// Nudge!
	h.recordNudge(event.Actor)
	msg := "You have uncommitted changes in your working tree. " +
		"Please commit (or stash) before stopping — uncommitted files break CI for other agents.\n" +
		"Dirty files: " + summary
	result.Warnings = append(result.Warnings, msg)

	return nil
}

// checkGitDirty runs git status --porcelain in the given directory.
// Returns (dirty, summary, error). Error means not a git repo or git unavailable.
func (h *CommitNudgeHandler) checkGitDirty(ctx context.Context, cwd string) (bool, string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return false, "", err
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return false, "", nil
	}

	// Filter out daemon-managed and data files that are always dirty.
	var significant []string
	for _, line := range strings.Split(output, "\n") {
		if isNoisyFile(line) {
			continue
		}
		significant = append(significant, line)
	}

	if len(significant) == 0 {
		return false, "", nil
	}

	if len(significant) <= 5 {
		return true, strings.Join(significant, "\n"), nil
	}
	// Truncate long lists.
	summary := strings.Join(significant[:5], "\n") +
		fmt.Sprintf("\n... and %d more file(s)", len(significant)-5)
	return true, summary, nil
}

// isNoisyFile returns true for git status lines that represent
// daemon-managed or data files which are expected to be dirty.
func isNoisyFile(statusLine string) bool {
	// git status --porcelain format: "XY filename" (2 status chars + space + path)
	if len(statusLine) < 4 {
		return false
	}
	path := strings.TrimSpace(statusLine[2:])
	// Daemon directory — activity.json is always modified; directory shown as "daemon/" when untracked.
	if path == "daemon/activity.json" || path == "daemon/" || strings.HasPrefix(path, "daemon/") {
		return true
	}
	// Beads data directory — managed by beads, not user code.
	if strings.HasPrefix(path, ".beads/") || path == ".beads/" {
		return true
	}
	// Deploy directory — infrastructure config, not source code.
	if strings.HasPrefix(path, "deploy/") || path == "deploy/" {
		return true
	}
	return false
}

// shouldNudge returns true if enough time has passed since the last nudge
// for this actor.
func (h *CommitNudgeHandler) shouldNudge(actor string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.lastNudge == nil {
		h.lastNudge = make(map[string]time.Time)
	}

	cooldown := h.cooldown
	if cooldown == 0 {
		cooldown = 5 * time.Minute
	}

	last, ok := h.lastNudge[actor]
	if ok && time.Since(last) < cooldown {
		return false
	}
	return true
}

// recordNudge marks the current time as the last nudge for an actor.
func (h *CommitNudgeHandler) recordNudge(actor string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.lastNudge == nil {
		h.lastNudge = make(map[string]time.Time)
	}
	h.lastNudge[actor] = time.Now()
}
