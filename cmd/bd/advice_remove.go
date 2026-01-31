package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// adviceRemoveCmd removes an advice bead
var adviceRemoveCmd = &cobra.Command{
	Use:     "remove <advice-id>",
	Aliases: []string{"rm", "delete"},
	Short:   "Remove an advice bead",
	Long: `Remove an advice bead by ID.

This closes the advice bead with a deletion reason. The advice will no longer
appear in bd advice list or be applied to agents.

Examples:
  # Remove advice by ID
  bd advice remove gt-abc123

  # Remove with reason
  bd advice remove gt-abc123 --reason="No longer applicable"`,
	Args: cobra.ExactArgs(1),
	Run:  runAdviceRemove,
}

func init() {
	adviceRemoveCmd.Flags().String("reason", "Removed via bd advice remove", "Reason for removal")
	adviceRemoveCmd.Flags().Bool("hard", false, "Hard delete (tombstone) instead of close")

	adviceCmd.AddCommand(adviceRemoveCmd)
}

func runAdviceRemove(cmd *cobra.Command, args []string) {
	CheckReadonly("advice remove")

	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	adviceID := args[0]
	reason, _ := cmd.Flags().GetString("reason")
	hardDelete, _ := cmd.Flags().GetBool("hard")

	ctx := rootCtx

	// Verify the issue exists and is an advice bead
	issue, err := store.GetIssue(ctx, adviceID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if issue == nil {
		fmt.Fprintf(os.Stderr, "Error: advice %s not found\n", adviceID)
		os.Exit(1)
	}
	if issue.IssueType != types.TypeAdvice {
		fmt.Fprintf(os.Stderr, "Error: %s is not an advice bead (type=%s)\n", adviceID, issue.IssueType)
		os.Exit(1)
	}

	// Check if already closed
	if issue.Status == types.StatusClosed {
		fmt.Fprintf(os.Stderr, "Error: advice %s is already closed\n", adviceID)
		os.Exit(1)
	}

	if hardDelete {
		// Hard delete (creates tombstone)
		if err := store.DeleteIssue(ctx, adviceID); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting advice: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Soft delete (close with reason)
		if err := store.CloseIssue(ctx, adviceID, reason, actor, ""); err != nil {
			fmt.Fprintf(os.Stderr, "Error closing advice: %v\n", err)
			os.Exit(1)
		}
	}

	markDirtyAndScheduleFlush()

	// Output
	if jsonOutput {
		result := map[string]interface{}{
			"id":          adviceID,
			"removed":     true,
			"hard_delete": hardDelete,
		}
		outputJSON(result)
		return
	}

	// Human-readable output
	action := "Removed"
	if hardDelete {
		action = "Deleted"
	}
	fmt.Printf("%s %s advice: %s\n", ui.RenderPass("✓"), action, ui.RenderID(adviceID))
	fmt.Printf("   %s\n", issue.Title)
}
