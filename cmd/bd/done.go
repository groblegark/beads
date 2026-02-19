package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/ui"
)

var (
	doneReason string
)

func init() {
	rootCmd.AddCommand(doneCmd)
	doneCmd.Flags().StringVar(&doneReason, "reason", "", "Close reason (optional)")
}

// doneCmd is the "bd done" command — canonical teardown for finishing work.
// Closes the agent's current in-progress issue and optionally writes cleanup
// status. This replaces the old "bd done" (now "bd yield"). (hq-h80x9n)
var doneCmd = &cobra.Command{
	Use:   "done [issue-id...]",
	Short: "Mark issues as done and close them",
	Long: `Close one or more issues, marking them as done.

This is the canonical teardown command: agents call "bd done" when they finish
their current task. It closes the specified issues (or the agent's in-progress
issue if none specified).

If no issue IDs are given and BD_ACTOR is set, it finds and closes the agent's
current in-progress issue automatically.

For blocking/yielding (waiting for events), use "bd yield" instead.

Examples:
  bd done                                # Close agent's current in-progress issue
  bd done bd-abc123                      # Close a specific issue
  bd done bd-abc bd-def --reason="done"  # Close multiple with reason`,
	GroupID: "issues",
	RunE:    runDone,
}

func runDone(cmd *cobra.Command, args []string) error {
	CheckReadonly("done")

	// If issue IDs are given, close them directly.
	if len(args) > 0 {
		return doneCloseIssues(args, doneReason)
	}

	// No args: find agent's current in-progress issue.
	agent := getActor()
	if agent == "" {
		return fmt.Errorf("no issue IDs specified and BD_ACTOR not set — specify issues to close or set BD_ACTOR")
	}

	// Find in-progress issues assigned to this agent.
	if daemonClient != nil {
		resp, err := daemonClient.List(&rpc.ListArgs{
			Status:   "in_progress",
			Assignee: agent,
		})
		if err != nil {
			return fmt.Errorf("listing in-progress issues: %w", err)
		}

		var issues []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if resp.Data != nil {
			if err := json.Unmarshal(resp.Data, &issues); err != nil {
				return fmt.Errorf("parsing issues: %w", err)
			}
		}

		if len(issues) == 0 {
			fmt.Fprintf(os.Stderr, "No in-progress issues found for agent %q\n", agent)
			return nil
		}

		ids := make([]string, len(issues))
		for i, issue := range issues {
			ids[i] = issue.ID
		}
		return doneCloseIssues(ids, doneReason)
	}

	return fmt.Errorf("bd done requires the daemon when no issue IDs specified")
}

// doneCloseIssues closes one or more issues via the daemon or local store.
func doneCloseIssues(ids []string, reason string) error {
	for _, id := range ids {
		if daemonClient != nil {
			closeReason := reason
			if closeReason == "" {
				closeReason = "done"
			}
			_, err := daemonClient.CloseIssue(&rpc.CloseArgs{
				ID:     id,
				Reason: closeReason,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close %s: %v\n", id, err)
				continue
			}
		} else {
			if err := store.CloseIssue(rootCtx, id, reason, getActor(), ""); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to close %s: %v\n", id, err)
				continue
			}
		}
		fmt.Printf("%s Closed %s", ui.RenderPass("✓"), id)
		if reason != "" {
			fmt.Printf(": %s", reason)
		}
		fmt.Println()
	}
	return nil
}
