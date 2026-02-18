package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

var (
	doneTimeout int
	doneOn      string
	doneAgent   string
)

func init() {
	rootCmd.AddCommand(doneCmd)
	doneCmd.Flags().IntVar(&doneTimeout, "timeout", 1800, "Timeout in seconds (default 30m, max 1h)")
	doneCmd.Flags().StringVar(&doneOn, "on", "", "Event types to wait for (comma-separated: inbox,decision; default: all)")
	doneCmd.Flags().StringVar(&doneAgent, "agent", "", "Agent name (default: BD_ACTOR)")
}

// doneCmd is the "bd done" command — blocks until a notification arrives.
var doneCmd = &cobra.Command{
	Use:   "done",
	Short: "Block until an inbox message or decision response arrives",
	Long: `Block the calling process until a meaningful event arrives for this agent.

This is the yield command: agents call "bd done" when they have no work, and
the process blocks until something happens — an inbox message, a decision
response, or a timeout.

When the event arrives, the payload is printed to stdout and the command exits 0.
On timeout, exits 1. On interrupt (Ctrl+C), exits 0 silently.

This is designed to be called as a tool from Claude Code. The agent calls
"bd done" via Bash, which blocks the tool call. When the event arrives,
the tool returns with the event payload and the agent wakes up with context.

Pair with "bd nudge" to send events: nudge sends, done receives.

Examples:
  bd done                              # Wait for any event (30m timeout)
  bd done --timeout=600                # Wait 10 minutes
  bd done --on=inbox                   # Only wait for inbox messages
  bd done --on=decision                # Only wait for decision responses
  bd done --agent=hq-mayor             # Wait for events to mayor`,
	GroupID: "issues",
	RunE:    runDone,
}

// donePreflightCheck warns if the agent has no pending decision points,
// and shows what decisions we're waiting for. (bd-r4ni3, bd-oowwa)
func donePreflightCheck(agent string) {
	resp, err := daemonClient.DecisionList(&rpc.DecisionListArgs{})
	if err != nil {
		// Can't check — daemon may not support this yet. Skip silently.
		return
	}

	var pending []*types.DecisionPoint
	for _, d := range resp.Decisions {
		if d.Decision == nil {
			continue
		}
		dp := d.Decision
		// Pending = requested by this agent and not yet resolved.
		if dp.RequestedBy == agent && dp.SelectedOption == "" && dp.ResponseText == "" {
			pending = append(pending, dp)
		}
	}

	if len(pending) == 0 {
		fmt.Fprintf(os.Stderr, "⚠ Warning: no pending decision found for agent %q — bd done may block indefinitely.\n", agent)
		fmt.Fprintf(os.Stderr, "  Did you forget to run: bd decision create --no-wait --requested-by=%s ...?\n", agent)
		return
	}

	// Show what we're waiting for.
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

func runDone(cmd *cobra.Command, args []string) error {
	requireDaemon("done")

	agent := doneAgent
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

	// Pre-flight check: warn if no pending decision exists for this agent.
	// This catches the common mistake of calling "bd done" without first
	// creating a decision point, which would block indefinitely. (bd-r4ni3)
	if !quietFlag {
		donePreflightCheck(agent)
	}

	if !quietFlag {
		onDesc := doneOn
		if onDesc == "" {
			onDesc = "inbox,decision"
		}
		fmt.Fprintf(os.Stderr, "Waiting for events (agent=%s, timeout=%ds, on=%s)...\n",
			agent, doneTimeout, onDesc)
	}

	// Retry loop: each server poll is capped at maxPollSec to stay under
	// proxy timeouts (typically 60s). The client retries until the overall
	// doneTimeout expires or an event arrives. (bd-wccdf)
	const maxPollSec = 50
	deadline := time.Now().Add(time.Duration(doneTimeout) * time.Second)

	for {
		remaining := int(time.Until(deadline).Seconds())
		if remaining <= 0 {
			fmt.Fprintf(os.Stderr, "Timed out after %ds waiting for events\n", doneTimeout)
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
			On:         doneOn,
			MaxPollSec: pollSec,
		})

		daemonClient.SetRequestTimeout(0)

		if err != nil {
			// On HTTP errors (504, timeout), retry if we have time left.
			if time.Now().Before(deadline) {
				time.Sleep(2 * time.Second)
				continue
			}
			return fmt.Errorf("done_wait: %w", err)
		}

		if result.TimedOut {
			// Server poll expired — retry if overall deadline not reached.
			if time.Now().Before(deadline) {
				continue
			}
			fmt.Fprintf(os.Stderr, "Timed out after %ds waiting for events\n", doneTimeout)
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
