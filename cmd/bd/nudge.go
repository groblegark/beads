package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
)

// nudgeCmd sends a message to an agent's inbox, waking it if blocked on "bd done".
// This is the send half of the done/nudge pair: nudge sends, done receives. (bd-pxogr)
var nudgeCmd = &cobra.Command{
	Use:   "nudge <agent> <message>",
	Short: "Send a message to an agent (wakes bd done)",
	Long: `Send a message to an agent's inbox, waking it if blocked on "bd done".

This is a thin wrapper around "bd inbox push" with sensible defaults for
agent-to-agent communication. The message is pushed to the inbox (reliable,
persisted) and triggers a NATS publish that unblocks "bd done" in real-time.

No HTTP/coop dependency — works from anywhere with daemon access.

Examples:
  bd nudge mayor "CI passed, deploy when ready"
  bd nudge polecat "Your subtask bd-xyz is done"
  bd nudge all "Maintenance in 10 minutes"`,
	Args:    cobra.MinimumNArgs(2),
	GroupID: "issues",
	RunE:    runNudge,
}

func init() {
	nudgeCmd.Flags().Int("priority", 1, "Priority (0=critical, 1=high, 2=normal, 3=low)")
	rootCmd.AddCommand(nudgeCmd)
}

func runNudge(cmd *cobra.Command, args []string) error {
	CheckReadonly("nudge")
	requireDaemon("nudge")

	target := args[0]
	message := strings.Join(args[1:], " ")
	priority, _ := cmd.Flags().GetInt("priority")

	source := getActor()

	resp, err := daemonClient.InboxPush(&rpc.InboxPushArgs{
		AgentName: target,
		Type:      "agent",
		Source:    source,
		Content:   message,
		Priority:  priority,
	})
	if err != nil {
		return fmt.Errorf("nudge failed: %w", err)
	}
	_ = resp

	if !quietFlag {
		fmt.Fprintf(os.Stderr, "Nudged %s\n", target)
	}

	return nil
}
