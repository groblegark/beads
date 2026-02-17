package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
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

func runDone(cmd *cobra.Command, args []string) error {
	requireDaemon("done")

	agent := doneAgent
	if agent == "" {
		agent = getActor()
	}

	// Set a long request timeout — this is a blocking call.
	// Add 30s buffer beyond the done timeout for RPC overhead.
	daemonClient.SetRequestTimeout((doneTimeout + 30) * 1000)
	defer daemonClient.SetRequestTimeout(0)

	// Handle Ctrl+C gracefully — agent should exit cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		os.Exit(0)
	}()

	if !quietFlag {
		onDesc := doneOn
		if onDesc == "" {
			onDesc = "inbox,decision"
		}
		fmt.Fprintf(os.Stderr, "Waiting for events (agent=%s, timeout=%ds, on=%s)...\n",
			agent, doneTimeout, onDesc)
	}

	result, err := daemonClient.DoneWait(&rpc.DoneWaitArgs{
		AgentName:  agent,
		TimeoutSec: doneTimeout,
		On:         doneOn,
	})
	if err != nil {
		return fmt.Errorf("done_wait: %w", err)
	}

	if result.TimedOut {
		fmt.Fprintf(os.Stderr, "Timed out after %ds waiting for events\n", doneTimeout)
		os.Exit(1)
	}

	// Print the event content to stdout — this is what the agent sees.
	if result.Content != "" {
		fmt.Println(result.Content)
	}
	if result.Source != "" {
		fmt.Fprintf(os.Stderr, "event=%s source=%s\n", result.EventType, result.Source)
	}

	return nil
}
