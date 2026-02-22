package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

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

		force, _ := cmd.Flags().GetBool("force")

		// Detect git branch/commit for metadata (bd-kg4lw)
		claimBranch, claimCommit := getClaimGitContext()

		// Handle local IDs
		for _, resolvedID := range batch.ResolvedIDs {
			updateArgs := &rpc.UpdateArgs{
				ID:          resolvedID,
				Claim:       true,
				ClaimForce:  force,
				ClaimBranch: claimBranch,
				ClaimCommit: claimCommit,
			}

			resp, err := daemonClient.Update(updateArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", resolvedID, err)
				os.Exit(1)
			}

			// Show auto-released tasks when --force was used (bd-nl8zj)
			if resp.Message != "" {
				fmt.Fprintf(os.Stderr, "%s\n", resp.Message)
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
			if issue.Rig != "" {
				fmt.Printf("  Rig: %s\n", issue.Rig)
			}
			SetLastTouchedID(resolvedID)
		}

		// Handle routed IDs
		forEachRoutedID(batch.RoutedArgs, func(resolvedID string, routedClient *rpc.Client) error {
			routedClient.SetActor(actor)
			updateArgs := &rpc.UpdateArgs{
				ID:          resolvedID,
				Claim:       true,
				ClaimForce:  force,
				ClaimBranch: claimBranch,
				ClaimCommit: claimCommit,
			}

			resp, updateErr := routedClient.Update(updateArgs)
			if updateErr != nil {
				fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", resolvedID, updateErr)
				return updateErr
			}

			// Show auto-released tasks when --force was used (bd-nl8zj)
			if resp.Message != "" {
				fmt.Fprintf(os.Stderr, "%s\n", resp.Message)
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
			if issue.Rig != "" {
				fmt.Printf("  Rig: %s\n", issue.Rig)
			}
			SetLastTouchedID(resolvedID)
			return nil
		})
	},
}

// getClaimGitContext detects the current git branch and commit SHA. (bd-kg4lw)
// Returns nil pointers if not in a git repo or git is unavailable.
func getClaimGitContext() (branch, commit *string) {
	ctx := context.Background()

	// Get branch
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD") // #nosec G204
	if out, err := cmd.Output(); err == nil {
		b := strings.TrimSpace(string(out))
		if b != "" && b != "HEAD" {
			branch = &b
		}
	}

	// Get commit SHA (short)
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--short", "HEAD") // #nosec G204
	if out, err := cmd.Output(); err == nil {
		c := strings.TrimSpace(string(out))
		if c != "" {
			commit = &c
		}
	}

	return branch, commit
}

func init() {
	claimCmd.Flags().BoolP("force", "f", false, "Force claim even if you already have in_progress work or target is an epic")
	rootCmd.AddCommand(claimCmd)
}
