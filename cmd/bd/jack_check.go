package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// jackCheckCmd scans for expired or expiring-soon jacks.
var jackCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Find expired jacks (for periodic sweeps)",
	Long: `Scan for expired or expiring-soon infrastructure jacks.

Lists jacks past their TTL with target, agent, and how long overdue.
Designed for periodic sweeps — the daemon sweeper calls this periodically.

With --auto-escalate, creates a decision point for each expired jack
requiring human response.

Examples:
  bd jack check                    # Show expired jacks
  bd jack check --auto-escalate    # Create alerts for expired jacks
  bd jack check --json             # Machine-readable output`,
	Run: func(cmd *cobra.Command, args []string) {
		autoEscalate, _ := cmd.Flags().GetBool("auto-escalate")

		requireDaemon("jack check")

		// List all in-progress jack beads
		listArgs := &rpc.ListArgs{
			IssueType:     "jack",
			ExcludeStatus: []string{"closed"},
			Limit:         500,
		}
		resp, err := daemonClient.List(listArgs)
		if err != nil {
			FatalErrorRespectJSON("error listing jacks: %v", err)
		}
		var jacks []*types.Issue
		if err := json.Unmarshal(resp.Data, &jacks); err != nil {
			FatalErrorRespectJSON("error parsing jacks: %v", err)
		}

		now := time.Now()
		var expired []*types.Issue
		var active []*types.Issue

		for _, jack := range jacks {
			if jack.JackExpiresAt != nil && now.After(*jack.JackExpiresAt) {
				expired = append(expired, jack)
			} else {
				active = append(active, jack)
			}
		}

		if jsonOutput {
			result := rpc.JackCheckResult{
				Expired: expired,
				Active:  len(active),
			}
			if autoEscalate {
				result.Escalated = len(expired)
			}
			outputJSON(result)
			return
		}

		if len(expired) == 0 && len(active) == 0 {
			fmt.Println("No active jacks.")
			return
		}

		if len(expired) == 0 {
			fmt.Printf("%s No expired jacks (%d active)\n", ui.RenderPass("✓"), len(active))
			return
		}

		// Display expired jacks
		fmt.Printf("%s %d expired jack(s):\n\n", ui.RenderFail("!"), len(expired))
		for _, jack := range expired {
			overdue := now.Sub(*jack.JackExpiresAt).Round(time.Minute)
			target := extractJackTarget(jack)
			fmt.Printf("  %s %s\n", jack.ID, jack.Title)
			fmt.Printf("    Target: %s\n", target)
			fmt.Printf("    Agent: %s\n", jack.Assignee)
			fmt.Printf("    Overdue: %s\n", overdue)
			fmt.Println()
		}

		if len(active) > 0 {
			fmt.Printf("  (%d active jack(s) within TTL)\n", len(active))
		}

		if autoEscalate && len(expired) > 0 {
			fmt.Printf("\n%s Auto-escalation: creating decision points for %d expired jack(s)...\n",
				ui.RenderWarn("→"), len(expired))
			escalated := 0
			for _, jack := range expired {
				target := extractJackTarget(jack)
				prompt := fmt.Sprintf("Expired jack %s on %s (agent: %s) — overdue %s. Revert infrastructure or extend TTL?",
					jack.ID, target, jack.Assignee, now.Sub(*jack.JackExpiresAt).Round(time.Minute))
				createArgs := &rpc.DecisionCreateArgs{
					Prompt:      prompt,
					RequestedBy: "jack-sweeper",
					Options: []types.DecisionOption{
						{ID: "revert", Label: "Revert changes and close jack"},
						{ID: "extend", Label: "Extend TTL by 1h"},
						{ID: "investigate", Label: "Investigate before deciding"},
					},
					NoWait: true,
				}
				_, cerr := daemonClient.DecisionCreate(createArgs)
				if cerr != nil {
					fmt.Fprintf(os.Stderr, "  Error escalating %s: %v\n", jack.ID, cerr)
					continue
				}
				escalated++
			}
			fmt.Printf("  %s Escalated %d jack(s)\n", ui.RenderPass("✓"), escalated)
		}
	},
}

// extractJackTarget gets the jack_target from issue metadata.
func extractJackTarget(jack *types.Issue) string {
	if len(jack.Metadata) == 0 {
		return "(unknown)"
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(jack.Metadata, &meta); err != nil {
		return "(unknown)"
	}
	if target, ok := meta["jack_target"].(string); ok && target != "" {
		return target
	}
	return "(unknown)"
}

func init() {
	jackCheckCmd.Flags().Bool("auto-escalate", false, "Create decision points for expired jacks")
	jackCheckCmd.Flags().Bool("json", false, "Output JSON")
	jackCmd.AddCommand(jackCheckCmd)
}
