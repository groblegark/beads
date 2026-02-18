package eventbus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// AdviceHookStore is the minimal storage interface for fetching advice beads
// with hook commands. When set, the handler avoids subprocess round-trips.
type AdviceHookStore interface {
	ListAdviceWithHooks(ctx context.Context, trigger string) ([]*types.Issue, error)
}

// AdviceHookHandler executes advice hook commands at their trigger points.
// Priority 25 (after prime/gate, before inbox drain).
//
// Advice beads can carry hook commands that run at lifecycle points:
//   - session-end: runs on Stop events
//   - before-commit: runs on PreToolUse when tool is git commit (future)
//   - before-push: runs on PreToolUse when tool is git push (future)
//
// Each hook has an on_failure behavior: block, warn, or ignore.
type AdviceHookHandler struct {
	store AdviceHookStore
}

func (h *AdviceHookHandler) ID() string          { return "advice-hooks" }
func (h *AdviceHookHandler) Handles() []EventType { return []EventType{EventStop} }
func (h *AdviceHookHandler) Priority() int        { return 25 }

// SetAdviceHookStore wires in direct storage access.
func (h *AdviceHookHandler) SetAdviceHookStore(store AdviceHookStore) { h.store = store }

func (h *AdviceHookHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	if event.Actor == "" {
		return nil // No agent identity — skip
	}

	// Map event type to advice hook trigger
	trigger := eventToTrigger(event.Type)
	if trigger == "" {
		return nil
	}

	// Fetch matching advice via subprocess (always use subprocess for now
	// since advice filtering by agent subscriptions is complex)
	adviceItems, err := h.fetchMatchingAdvice(ctx, event, trigger)
	if err != nil {
		// Non-fatal — don't block agent because we couldn't fetch advice
		fmt.Fprintf(os.Stderr, "advice-hooks: fetch error: %v\n", err)
		return nil
	}

	if len(adviceItems) == 0 {
		return nil
	}

	// Run each hook command
	for _, advice := range adviceItems {
		hookResult := h.runHook(ctx, event, advice)

		switch {
		case hookResult.err != nil && advice.AdviceHookOnFailure == types.AdviceHookOnFailureBlock:
			result.Block = true
			result.Reason = fmt.Sprintf("Advice hook blocked: %s\nCommand: %s\nError: %s\nOutput: %s",
				advice.Title, advice.AdviceHookCommand, hookResult.err, hookResult.output)
			return nil // Stop processing on block
		case hookResult.err != nil && advice.AdviceHookOnFailure == types.AdviceHookOnFailureWarn:
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Advice hook warning: %s — %s (exit: %s)", advice.Title, hookResult.output, hookResult.err))
		case hookResult.err != nil:
			// ignore — do nothing
		}
	}

	return nil
}

// hookResult holds the output of running a single advice hook command.
type hookResult struct {
	output string
	err    error
}

// runHook executes a single advice hook command with its configured timeout.
func (h *AdviceHookHandler) runHook(ctx context.Context, event *Event, advice *types.Issue) hookResult {
	timeout := time.Duration(advice.AdviceHookTimeout) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(types.AdviceHookTimeoutDefault) * time.Second
	}
	if timeout > time.Duration(types.AdviceHookTimeoutMax)*time.Second {
		timeout = time.Duration(types.AdviceHookTimeoutMax) * time.Second
	}

	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(hookCtx, "sh", "-c", advice.AdviceHookCommand) //nolint:gosec // AdviceHookCommand is from trusted config, not user input
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if event.CWD != "" {
		if info, statErr := os.Stat(event.CWD); statErr == nil && info.IsDir() {
			cmd.Dir = event.CWD
		}
	}

	// Pass actor context
	cmd.Env = os.Environ()
	if event.Actor != "" {
		cmd.Env = append(cmd.Env, "BD_ACTOR="+event.Actor)
	}

	err := cmd.Run()
	output := strings.TrimSpace(stdout.String())
	if output == "" {
		output = strings.TrimSpace(stderr.String())
	}

	return hookResult{output: output, err: err}
}

// fetchMatchingAdvice uses a bd subprocess to get advice beads matching
// the agent and trigger.
func (h *AdviceHookHandler) fetchMatchingAdvice(ctx context.Context, event *Event, trigger string) ([]*types.Issue, error) {
	// Use bd advice list --for=<actor> --json to get matching advice
	args := []string{"advice", "list", "--for", event.Actor, "--json"}
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), args...)
	if err != nil {
		return nil, fmt.Errorf("list advice: %w", err)
	}

	if stdout == "" {
		return nil, nil
	}

	var issues []*types.Issue
	if err := json.Unmarshal([]byte(stdout), &issues); err != nil {
		return nil, fmt.Errorf("parse advice: %w", err)
	}

	// Filter for advice with matching trigger and non-empty hook command
	var matched []*types.Issue
	for _, issue := range issues {
		if issue.AdviceHookCommand != "" && issue.AdviceHookTrigger == trigger {
			matched = append(matched, issue)
		}
	}

	return matched, nil
}

// eventToTrigger maps an event bus event type to an advice hook trigger.
func eventToTrigger(eventType EventType) string {
	switch eventType {
	case EventStop:
		return types.AdviceHookTriggerSessionEnd
	// Future: PreToolUse could map to before-commit/before-push
	// based on tool_name and tool_input inspection
	default:
		return ""
	}
}
