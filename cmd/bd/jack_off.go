package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// jackOffCmd lowers a jack — reverts changes and closes the infrastructure modification.
var jackOffCmd = &cobra.Command{
	Use:   "off <jack-id>",
	Short: "Lower a jack — revert changes and close",
	Long: `Close an infrastructure jack after reverting changes.

Before closing, you should have:
1. Executed the revert plan documented in the jack
2. Verified infrastructure is back to its original state

Use --skip-revert-check if the revert happened out-of-band (e.g., pod was
recycled, infrastructure was rebuilt). Requires --reason explaining why.

Examples:
  bd jack off bd-jack-abc --reason="Debug complete, reverted config"
  bd jack off bd-jack-abc --skip-revert-check --reason="Pod recycled by K8s"`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("jack off")

		jackID := args[0]
		reason, _ := cmd.Flags().GetString("reason")
		skipRevertCheck, _ := cmd.Flags().GetBool("skip-revert-check")

		if reason == "" {
			FatalErrorRespectJSON("--reason is required for bd jack off")
		}

		if skipRevertCheck {
			if len(reason) < 10 {
				FatalErrorRespectJSON("--reason must explain why revert check was skipped (min 10 chars)")
			}
		}

		// Verify it's a jack bead and is currently active
		requireDaemon("jack off")
		{
			resp, err := daemonClient.Show(&rpc.ShowArgs{ID: jackID})
			if err != nil {
				FatalErrorRespectJSON("jack not found: %v — run 'bd jack list' to see active jacks", err)
			}
			if !resp.Success {
				FatalErrorRespectJSON("jack not found: %s — run 'bd jack list' to see active jacks", resp.Error)
			}
			var details types.IssueDetails
			if err := json.Unmarshal(resp.Data, &details); err != nil {
				FatalErrorRespectJSON("error parsing jack: %v", err)
			}
			issue := &details.Issue

			if issue.IssueType != types.TypeJack {
				FatalErrorRespectJSON("%s is not a jack (type=%s) — use 'bd close' for regular issues", jackID, issue.IssueType)
			}

			if issue.Status == types.StatusClosed {
				closedBy := issue.Assignee
				closedAt := ""
				if issue.ClosedAt != nil {
					closedAt = " at " + issue.ClosedAt.Format("2006-01-02 15:04")
				}
				FatalErrorRespectJSON("jack %s already closed by %s%s", jackID, closedBy, closedAt)
			}
		}

		// Send jack off request to daemon
		offArgs := &rpc.JackOffArgs{
			ID:              jackID,
			Reason:          reason,
			SkipRevertCheck: skipRevertCheck,
		}
		resp, err := daemonClient.JackOff(offArgs)
		if err != nil {
			FatalErrorRespectJSON("error closing jack: %v", err)
		}
		if !resp.Success {
			FatalErrorRespectJSON("error closing jack: %s", resp.Error)
		}

		if jsonOutput {
			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err == nil {
				outputJSON(&issue)
			}
			return
		}

		fmt.Printf("%s Jack lowered: %s\n", ui.RenderPass("✓"), jackID)
		fmt.Printf("  Reason: %s\n", reason)
		if skipRevertCheck {
			fmt.Printf("  %s Revert check skipped\n", ui.RenderWarn("!"))
		}
	},
}

func init() {
	jackOffCmd.Flags().StringP("reason", "r", "", "What was found and/or why reverting [required]")
	jackOffCmd.Flags().Bool("skip-revert-check", false, "Close without verifying revert was executed (requires --reason)")
	jackOffCmd.Flags().Bool("json", false, "Output JSON")
	jackOffCmd.ValidArgsFunction = issueIDCompletion
	jackCmd.AddCommand(jackOffCmd)
}
