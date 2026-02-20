package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/gate"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

var (
	yieldTimeout        int
	yieldOn             string
	yieldAgent          string
	yieldRequireDecison bool
)

func init() {
	rootCmd.AddCommand(yieldCmd)
	yieldCmd.Flags().IntVar(&yieldTimeout, "timeout", 1800, "Timeout in seconds (default 30m, max 1h)")
	yieldCmd.Flags().StringVar(&yieldOn, "on", "", "Event types to wait for (comma-separated: inbox,decision; default: all)")
	yieldCmd.Flags().StringVar(&yieldAgent, "agent", "", "Agent name (default: BD_ACTOR)")
	yieldCmd.Flags().BoolVar(&yieldRequireDecison, "require-decision", false, "Fail immediately if no pending decision exists for this agent")
}

// yieldCmd is the "bd yield" command — blocks until a notification arrives.
// Renamed from "bd done" to free that name for teardown. (hq-h80x9n)
var yieldCmd = &cobra.Command{
	Use:   "yield",
	Short: "Block until an inbox message or decision response arrives",
	Long: `Block the calling process until a meaningful event arrives for this agent.

Agents call "bd yield" when they have no work, and the process blocks until
something happens — an inbox message, a decision response, or a timeout.

When the event arrives, the payload is printed to stdout and the command exits 0.
On timeout, exits 1. On interrupt (Ctrl+C), exits 0 silently.

This is designed to be called as a tool from Claude Code. The agent calls
"bd yield" via Bash, which blocks the tool call. When the event arrives,
the tool returns with the event payload and the agent wakes up with context.

Pair with "bd nudge" to send events: nudge sends, yield receives.

Examples:
  bd yield                              # Wait for any event (30m timeout)
  bd yield --timeout=600                # Wait 10 minutes
  bd yield --on=inbox                   # Only wait for inbox messages
  bd yield --on=decision                # Only wait for decision responses
  bd yield --agent=hq-mayor             # Wait for events to mayor`,
	GroupID: "issues",
	RunE:    runYield,
}

// yieldPreflightCheck validates agent state before blocking. Returns true if
// blocking is safe, false if the agent should not wait. Integrates gate checks
// and decision validation. (bd-r4ni3, bd-oowwa, bd-2r20u, bd-ihpb0)
func yieldPreflightCheck(agent string, requireDecision bool) bool {
	ok := true

	// Gate check: evaluate Stop gates to catch unsatisfied conditions.
	// Uses the same gate infrastructure as the Stop hook itself. (bd-2r20u)
	gateCheck := yieldGateCheck()
	if gateCheck != "" {
		fmt.Fprintf(os.Stderr, "%s\n", gateCheck)
	}

	// Decision check: warn or fail if no pending decision exists.
	pending := yieldPendingDecisions(agent)
	if len(pending) == 0 {
		if requireDecision {
			fmt.Fprintf(os.Stderr, "Error: --require-decision set but no pending decision found for agent %q.\n", agent)
			fmt.Fprintf(os.Stderr, "  Create one first: bd decision create --no-wait --requested-by=%s ...?\n", agent)
			ok = false
		} else {
			fmt.Fprintf(os.Stderr, "⚠ Warning: no pending decision found for agent %q — bd yield may block indefinitely.\n", agent)
			fmt.Fprintf(os.Stderr, "  Did you forget to run: bd decision create --no-wait --requested-by=%s ...?\n", agent)
		}
	} else {
		// Show what we're waiting for. (bd-oowwa)
		for _, dp := range pending {
			prompt := dp.Prompt
			if len(prompt) > 60 {
				prompt = prompt[:57] + "..."
			}
			optCount := countDecisionOptions(dp.Options)
			age := time.Since(dp.CreatedAt).Truncate(time.Second)
			fmt.Fprintf(os.Stderr, "  Pending: %s %q (%d options, created %s ago)\n",
				dp.IssueID, prompt, optCount, age)
		}
	}

	return ok
}

// yieldGateCheck evaluates Stop gates and returns warning text for unsatisfied
// gates, or empty string if all gates are satisfied (or no session available).
// This uses the same gate infrastructure as the Stop hook. (bd-2r20u)
func yieldGateCheck() string {
	sessionID := getSessionID()
	if sessionID == "" {
		return "" // No session — can't check gates
	}

	workDir := getWorkDir()
	opts := gate.CheckOpts{
		AgentName: eventbus.AgentBaseName(getActor()),
	}
	results, err := gate.CheckGatesWithOpts(workDir, sessionID, gate.HookStop, sessionGateRegistry, opts)
	if err != nil || len(results) == 0 {
		return ""
	}

	var warnings []string
	for _, r := range results {
		if !r.Satisfied {
			hint := ""
			if r.Hint != "" {
				hint = " → " + r.Hint
			}
			warnings = append(warnings, fmt.Sprintf("  ⚠ Gate %s: %s%s", r.GateID, r.Message, hint))
		}
	}

	if len(warnings) == 0 {
		return ""
	}
	return strings.Join(warnings, "\n")
}

// yieldPendingDecisions returns pending (unresolved) decisions for the agent.
// Matches both exact agent name and continuation variants (e.g., "sharp-seal"
// matches decisions from "sharp-seal-1", "sharp-seal-2", etc.). (bd-6vtx4)
func yieldPendingDecisions(agent string) []*types.DecisionPoint {
	resp, err := daemonClient.DecisionList(&rpc.DecisionListArgs{})
	if err != nil {
		return nil
	}

	var pending []*types.DecisionPoint
	for _, d := range resp.Decisions {
		if d.Decision == nil {
			continue
		}
		dp := d.Decision
		if matchesAgentVariant(agent, dp.RequestedBy) && dp.SelectedOption == "" && dp.ResponseText == "" {
			pending = append(pending, dp)
		}
	}
	return pending
}

// matchesAgentVariant returns true if requestedBy matches the agent name or
// is a continuation variant. Handles both directions:
//   - agent="sharp-seal", requestedBy="sharp-seal-1" → true (base matches variant)
//   - agent="sharp-seal-1", requestedBy="sharp-seal-1" → true (exact match)
//   - agent="sharp-seal-1", requestedBy="sharp-seal" → true (variant matches base)
//
// Uses the daemon's session registry for accurate base name resolution when
// available. Falls back to regex-based AgentBaseName() stripping if the daemon
// is unreachable. The registry-aware path correctly handles agents with
// naturally-numbered names (e.g., "obsidian-3" is a real agent, not a
// continuation of "obsidian"). (bd-6vtx4, bd-s0x1m)
func matchesAgentVariant(agent, requestedBy string) bool {
	if agent == requestedBy {
		return true
	}
	return resolveBaseName(agent) == resolveBaseName(requestedBy)
}

// sessionBaseNames caches the assignedName→baseName mapping from the daemon's
// session registry. Populated once per process by resolveBaseName(). (bd-s0x1m)
var sessionBaseNames map[string]string
var sessionBaseNamesLoaded bool

// resolveBaseName returns the base name for an agent, using the daemon's session
// registry when available. If "obsidian-3" is registered with baseName="obsidian-3",
// it returns "obsidian-3" (not "obsidian"). Falls back to AgentBaseName() regex
// stripping if the daemon is unreachable. (bd-s0x1m)
func resolveBaseName(name string) string {
	if !sessionBaseNamesLoaded {
		sessionBaseNamesLoaded = true
		sessionBaseNames = loadSessionBaseNames()
	}
	if sessionBaseNames != nil {
		if base, ok := sessionBaseNames[name]; ok {
			return base
		}
	}
	// Fallback: regex-based stripping (works when daemon is unavailable)
	return eventbus.AgentBaseName(name)
}

// loadSessionBaseNames fetches the session registry from the daemon and builds
// an assignedName→baseName map. Returns nil if the daemon is unreachable. (bd-s0x1m)
func loadSessionBaseNames() map[string]string {
	if daemonClient == nil {
		return nil
	}
	resp, err := daemonClient.SessionList(&rpc.SessionListArgs{})
	if err != nil || resp == nil {
		return nil
	}
	m := make(map[string]string, len(resp.Sessions))
	for _, s := range resp.Sessions {
		m[s.AssignedName] = s.BaseName
	}
	return m
}

// yieldResolveAgentFromDecisions checks if there are decisions (pending OR
// recently responded) under a variant of the agent name (e.g., "sharp-seal-1"
// when agent is "sharp-seal"). Returns the variant name if found, otherwise
// the original agent name.
//
// Must check recently-responded decisions too: if the decision was responded
// to before yield starts, pending-only lookup returns the base name, causing
// NATS subscriptions and DB pre-checks to use the wrong name. (bd-6vtx4, bd-zr20k)
func yieldResolveAgentFromDecisions(agent string) string {
	resp, err := daemonClient.DecisionList(&rpc.DecisionListArgs{})
	if err != nil {
		return agent
	}

	// First check: exact match on any recent decision (pending or responded) — no override needed.
	for _, d := range resp.Decisions {
		if d.Decision == nil {
			continue
		}
		dp := d.Decision
		if dp.RequestedBy == agent {
			return agent // Exact match found, use as-is.
		}
	}

	// Second check: look for a variant match (same base name, different suffix).
	// Prefer pending decisions, but accept recently-responded ones too. (bd-zr20k)
	var variantPending, variantAny string
	for _, d := range resp.Decisions {
		if d.Decision == nil {
			continue
		}
		dp := d.Decision
		if !matchesAgentVariant(agent, dp.RequestedBy) || dp.RequestedBy == agent {
			continue
		}
		if variantAny == "" {
			variantAny = dp.RequestedBy
		}
		if dp.SelectedOption == "" && dp.ResponseText == "" {
			variantPending = dp.RequestedBy
			break // Pending variant found — highest priority.
		}
	}

	if variantPending != "" {
		fmt.Fprintf(os.Stderr, "yield: agent identity override %q → %q (from pending decision)\n",
			agent, variantPending)
		return variantPending
	}
	if variantAny != "" {
		fmt.Fprintf(os.Stderr, "yield: agent identity override %q → %q (from recent decision)\n",
			agent, variantAny)
		return variantAny
	}

	return agent
}

// countDecisionOptions parses the JSON options array and returns the count.
func countDecisionOptions(optionsJSON string) int {
	if optionsJSON == "" {
		return 0
	}
	var opts []types.DecisionOption
	if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
		return 0
	}
	return len(opts)
}

func runYield(cmd *cobra.Command, args []string) error {
	requireDaemon("yield")

	agent := yieldAgent
	if agent == "" {
		agent = getActor()
	}

	// Auto-detect correct agent identity from pending decisions.
	// When a continuation session creates a decision with --requested-by=sharp-seal-1
	// but BD_ACTOR is "sharp-seal", the yield would listen on the wrong name.
	// Fix: if no pending decisions match the resolved agent name but a variant does,
	// use the variant so NATS subscriptions and inbox polls match. (bd-6vtx4)
	if yieldAgent == "" { // Only auto-detect when --agent wasn't explicitly set.
		agent = yieldResolveAgentFromDecisions(agent)
	}

	// Handle Ctrl+C gracefully — agent should exit cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		os.Exit(0)
	}()

	// Pre-flight checks: gate validation, decision check, waiting summary.
	// Catches common mistakes before entering the blocking wait. (bd-r4ni3, bd-2r20u, bd-ihpb0)
	if !quietFlag {
		if !yieldPreflightCheck(agent, yieldRequireDecison) {
			return fmt.Errorf("pre-flight check failed (see above)")
		}
	} else if yieldRequireDecison {
		// Even in quiet mode, --require-decision must be enforced.
		pending := yieldPendingDecisions(agent)
		if len(pending) == 0 {
			return fmt.Errorf("--require-decision: no pending decision for agent %q", agent)
		}
	}

	if !quietFlag {
		onDesc := yieldOn
		if onDesc == "" {
			onDesc = "inbox,decision"
		}
		fmt.Fprintf(os.Stderr, "Waiting for events (agent=%s, timeout=%ds, on=%s)...\n",
			agent, yieldTimeout, onDesc)
	}

	// Retry loop: each server poll is capped at maxPollSec to stay under
	// proxy timeouts (typically 60s). The client retries until the overall
	// yieldTimeout expires or an event arrives. (bd-wccdf)
	const maxPollSec = 50
	deadline := time.Now().Add(time.Duration(yieldTimeout) * time.Second)

	for {
		remaining := int(time.Until(deadline).Seconds())
		if remaining <= 0 {
			fmt.Fprintf(os.Stderr, "Timed out after %ds waiting for events\n", yieldTimeout)
			os.Exit(1)
		}

		pollSec := maxPollSec
		if remaining < pollSec {
			pollSec = remaining
		}

		// Set request timeout with buffer for this poll iteration.
		daemonClient.SetRequestTimeout((pollSec + 15) * 1000)

		result, err := daemonClient.DoneWait(&rpc.DoneWaitArgs{
			AgentName:  agent,
			TimeoutSec: remaining,
			On:         yieldOn,
			MaxPollSec: pollSec,
		})

		daemonClient.SetRequestTimeout(0)

		if err != nil {
			// On HTTP errors (504, timeout), retry if we have time left.
			if time.Now().Before(deadline) {
				time.Sleep(2 * time.Second)
				continue
			}
			return fmt.Errorf("yield_wait: %w", err)
		}

		if result.TimedOut {
			// Server poll expired — retry if overall deadline not reached.
			if time.Now().Before(deadline) {
				continue
			}
			fmt.Fprintf(os.Stderr, "Timed out after %ds waiting for events\n", yieldTimeout)
			os.Exit(1)
		}

		// Got a real event — print and exit.
		if result.Content != "" {
			fmt.Println(result.Content)
		}
		if result.Source != "" {
			fmt.Fprintf(os.Stderr, "event=%s source=%s\n", result.EventType, result.Source)
		}
		return nil
	}
}
