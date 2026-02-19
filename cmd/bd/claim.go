package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var claimCmd = &cobra.Command{
	Use:     "claim <id>",
	GroupID: "issues",
	Short:   "Claim an issue (set assignee + status=in_progress)",
	Long: `Claim an issue by atomically setting the assignee to yourself and
status to in_progress. Fails if the issue is already claimed by another agent.

This is a shorthand for:
  bd update <id> --status=in_progress --claim

Examples:
  bd claim bd-abc123       # Claim a specific issue
  bd claim                 # Claim the last touched issue`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("claim")

		// Resolve the issue ID
		var id string
		if len(args) > 0 {
			id = args[0]
		} else {
			id = GetLastTouchedID()
			if id == "" {
				FatalErrorRespectJSON("no issue ID provided and no last touched issue")
			}
		}

		ctx := rootCtx

		// Resolve partial IDs with routing support
		batch, err := resolveIDsWithRouting(ctx, store, daemonClient, []string{id})
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		requireDaemon("claim")

		// Handle local IDs
		for _, resolvedID := range batch.ResolvedIDs {
			updateArgs := &rpc.UpdateArgs{
				ID:    resolvedID,
				Claim: true,
			}

			resp, err := daemonClient.Update(updateArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", resolvedID, err)
				os.Exit(1)
			}

			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err == nil {
				if hookRunner != nil {
					hookRunner.Run(hooks.EventUpdate, &issue)
				}
				if jsonOutput {
					outputJSON(&issue)
					return
				}
			}
			fmt.Printf("%s Claimed: %s\n", ui.RenderPass("✓"), resolvedID)
			SetLastTouchedID(resolvedID)
		}

		// Handle routed IDs
		forEachRoutedID(batch.RoutedArgs, func(resolvedID string, routedClient *rpc.Client) error {
			routedClient.SetActor(actor)
			updateArgs := &rpc.UpdateArgs{
				ID:    resolvedID,
				Claim: true,
			}

			resp, updateErr := routedClient.Update(updateArgs)
			if updateErr != nil {
				fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", resolvedID, updateErr)
				return updateErr
			}

			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err == nil {
				if hookRunner != nil {
					hookRunner.Run(hooks.EventUpdate, &issue)
				}
				if jsonOutput {
					outputJSON(&issue)
					return nil
				}
			}
			fmt.Printf("%s Claimed: %s\n", ui.RenderPass("✓"), resolvedID)
			SetLastTouchedID(resolvedID)
			return nil
		})
	},
}

func init() {
	rootCmd.AddCommand(claimCmd)
}
