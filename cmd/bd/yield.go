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
	results, err := gate.CheckGatesForHook(workDir, sessionID, gate.HookStop, sessionGateRegistry)
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
		if dp.RequestedBy == agent && dp.SelectedOption == "" && dp.ResponseText == "" {
			pending = append(pending, dp)
		}
	}
	return pending
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
