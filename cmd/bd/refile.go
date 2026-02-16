package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/ui"
)

var refileCmd = &cobra.Command{
	Use:     "refile <source-id> <target-rig>",
	GroupID: "issues",
	Short:   "Move an issue to a different rig",
	Long: `Move an issue from one rig to another.

This creates a new issue in the target rig with the same content,
then closes the source issue with a reference to the new location.

The target rig can be specified as:
  - A rig name: beads, gastown
  - A prefix: bd-, gt-
  - A prefix without hyphen: bd, gt

Examples:
  bd refile bd-8hea gastown     # Move to gastown by rig name
  bd refile bd-8hea gt-         # Move to gastown by prefix
  bd refile bd-8hea gt          # Move to gastown (prefix without hyphen)`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("refile")

		targetRig := args[1]
		keepOpen, _ := cmd.Flags().GetBool("keep-open")

		requireDaemon("refile")

		// Resolve partial/short/slug ID via daemon (bd-dktw5)
		resolveResp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[0]})
		if err != nil {
			FatalError("resolving ID %s: %v", args[0], err)
		}
		var sourceID string
		if err := json.Unmarshal(resolveResp.Data, &sourceID); err != nil {
			FatalError("parsing resolved ID: %v", err)
		}

		refileViaDaemon(sourceID, targetRig, keepOpen)
	},
}

// refileViaDaemon refiles an issue via the RPC daemon (bd-wj80).
func refileViaDaemon(sourceID, targetRig string, keepOpen bool) {
	args := &rpc.RefileArgs{
		IssueID:   sourceID,
		TargetRig: targetRig,
		KeepOpen:  keepOpen,
	}

	result, err := daemonClient.Refile(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"source": result.SourceID,
			"target": result.TargetID,
			"closed": result.Closed,
		})
	} else {
		fmt.Printf("%s Refiled %s → %s\n", ui.RenderPass("✓"), result.SourceID, result.TargetID)
		if result.Closed {
			fmt.Printf("  Source issue closed\n")
		}
	}
}

func init() {
	refileCmd.Flags().Bool("keep-open", false, "Keep the source issue open (don't close it)")
	refileCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(refileCmd)
}
