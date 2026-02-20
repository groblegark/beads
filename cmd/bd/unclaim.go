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

var unclaimCmd = &cobra.Command{
	Use:     "unclaim <id> [id...]",
	GroupID: "issues",
	Short:   "Release a task without closing it (sets status=open, clears assignee)",
	Long: `Release one or more claimed tasks back to the available pool. Sets status
back to open and clears the assignee. This is a clean way to give up work you
can't finish without closing the issue.

This is a shorthand for:
  bd update <id> --status=open --assignee=""

Examples:
  bd unclaim bd-abc123              # Release a specific issue
  bd unclaim bd-abc123 bd-def456    # Release multiple issues
  bd unclaim                        # Release the last touched issue`,
	Args: cobra.ArbitraryArgs,
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("unclaim")

		// Resolve issue IDs
		var ids []string
		if len(args) > 0 {
			ids = args
		} else {
			id := GetLastTouchedID()
			if id == "" {
				FatalErrorRespectJSON("no issue ID provided and no last touched issue")
			}
			ids = []string{id}
		}

		ctx := rootCtx

		batch, err := resolveIDsWithRouting(ctx, store, daemonClient, ids)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}

		requireDaemon("unclaim")

		exitCode := 0
		openStatus := "open"
		emptyAssignee := ""

		// Handle local IDs
		for _, resolvedID := range batch.ResolvedIDs {
			updateArgs := &rpc.UpdateArgs{
				ID:       resolvedID,
				Status:   &openStatus,
				Assignee: &emptyAssignee,
			}

			resp, err := daemonClient.Update(updateArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error unclaiming %s: %v\n", resolvedID, err)
				exitCode = 1
				continue
			}

			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err == nil {
				if hookRunner != nil {
					hookRunner.Run(hooks.EventUpdate, &issue)
				}
				if jsonOutput {
					outputJSON(&issue)
					continue
				}
			}
			fmt.Printf("%s Unclaimed: %s\n", ui.RenderPass("✓"), resolvedID)
			SetLastTouchedID(resolvedID)
		}

		// Handle routed IDs
		forEachRoutedID(batch.RoutedArgs, func(resolvedID string, routedClient *rpc.Client) error {
			routedClient.SetActor(actor)
			updateArgs := &rpc.UpdateArgs{
				ID:       resolvedID,
				Status:   &openStatus,
				Assignee: &emptyAssignee,
			}

			resp, updateErr := routedClient.Update(updateArgs)
			if updateErr != nil {
				fmt.Fprintf(os.Stderr, "Error unclaiming %s: %v\n", resolvedID, updateErr)
				exitCode = 1
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
			fmt.Printf("%s Unclaimed: %s\n", ui.RenderPass("✓"), resolvedID)
			SetLastTouchedID(resolvedID)
			return nil
		})

		if exitCode != 0 {
			os.Exit(exitCode)
		}
	},
}

func init() {
	rootCmd.AddCommand(unclaimCmd)
}
