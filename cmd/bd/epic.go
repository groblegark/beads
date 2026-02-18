package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var epicCmd = &cobra.Command{
	Use:     "epic",
	GroupID: "deps",
	Short:   "Epic management commands",
}

var epicStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show epic completion status",
	Long: `Show completion status for all open epics.

To see only epics eligible for closure, use:
  bd epic close-eligible --dry-run`,
	Run: func(cmd *cobra.Command, args []string) {
		requireDaemon("epic status")

		// Use global jsonOutput set by PersistentPreRun
		var epics []*types.EpicStatus
		resp, err := daemonClient.EpicStatus(&rpc.EpicStatusArgs{
			EligibleOnly: false,
		})
		if err != nil {
			FatalErrorRespectJSON("communicating with daemon: %v", err)
		}
		if !resp.Success {
			FatalErrorRespectJSON("getting epic status: %s", resp.Error)
		}
		if err := json.Unmarshal(resp.Data, &epics); err != nil {
			FatalErrorRespectJSON("parsing response: %v", err)
		}
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(epics); err != nil {
				FatalErrorRespectJSON("encoding JSON: %v", err)
			}
			return
		}
		// Human-readable output
		if len(epics) == 0 {
			fmt.Println("No open epics found")
			return
		}
		for _, epicStatus := range epics {
			epic := epicStatus.Epic
			percentage := 0
			if epicStatus.TotalChildren > 0 {
				percentage = (epicStatus.ClosedChildren * 100) / epicStatus.TotalChildren
			}
			statusIcon := ""
			if epicStatus.EligibleForClose {
				statusIcon = ui.RenderPass("✓")
			} else if percentage > 0 {
				statusIcon = ui.RenderWarn("○")
			} else {
				statusIcon = "○"
			}
			fmt.Printf("%s %s %s\n", statusIcon, ui.RenderAccent(epic.ID), ui.RenderBold(epic.Title))
			fmt.Printf("   Progress: %d/%d children closed (%d%%)\n",
				epicStatus.ClosedChildren, epicStatus.TotalChildren, percentage)
			if epicStatus.EligibleForClose {
				fmt.Printf("   %s\n", ui.RenderPass("Eligible for closure"))
			}
			fmt.Println()
		}
	},
}

var closeEligibleEpicsCmd = &cobra.Command{
	Use:   "close-eligible",
	Short: "Close epics where all children are complete",
	Run: func(cmd *cobra.Command, args []string) {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		// Block writes in readonly mode (closing modifies data)
		if !dryRun {
			CheckReadonly("epic close-eligible")
		}
		requireDaemon("epic close-eligible")

		// Use global jsonOutput set by PersistentPreRun
		var eligibleEpics []*types.EpicStatus
		resp, err := daemonClient.EpicStatus(&rpc.EpicStatusArgs{
			EligibleOnly: true,
		})
		if err != nil {
			FatalErrorRespectJSON("communicating with daemon: %v", err)
		}
		if !resp.Success {
			FatalErrorRespectJSON("getting eligible epics: %s", resp.Error)
		}
		if err := json.Unmarshal(resp.Data, &eligibleEpics); err != nil {
			FatalErrorRespectJSON("parsing response: %v", err)
		}
		if len(eligibleEpics) == 0 {
			if !jsonOutput {
				fmt.Println("No epics eligible for closure")
			} else {
				fmt.Println("[]")
			}
			return
		}
		if dryRun {
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(eligibleEpics); err != nil {
					FatalErrorRespectJSON("encoding JSON: %v", err)
				}
			} else {
				fmt.Printf("Would close %d epic(s):\n", len(eligibleEpics))
				for _, epicStatus := range eligibleEpics {
					fmt.Printf("  - %s: %s\n", epicStatus.Epic.ID, epicStatus.Epic.Title)
				}
			}
			return
		}
		// Actually close the epics via daemon RPC
		closedIDs := []string{}
		for _, epicStatus := range eligibleEpics {
			closeResp, closeErr := daemonClient.CloseIssue(&rpc.CloseArgs{
				ID:     epicStatus.Epic.ID,
				Reason: "All children completed",
			})
			if closeErr != nil || !closeResp.Success {
				errMsg := ""
				if closeErr != nil {
					errMsg = closeErr.Error()
				} else if !closeResp.Success {
					errMsg = closeResp.Error
				}
				fmt.Fprintf(os.Stderr, "Error closing %s: %s\n", epicStatus.Epic.ID, errMsg)
				continue
			}
			closedIDs = append(closedIDs, epicStatus.Epic.ID)
		}
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(map[string]interface{}{
				"closed": closedIDs,
				"count":  len(closedIDs),
			}); err != nil {
				FatalErrorRespectJSON("encoding JSON: %v", err)
			}
		} else {
			fmt.Printf("✓ Closed %d epic(s)\n", len(closedIDs))
			for _, id := range closedIDs {
				fmt.Printf("  - %s\n", id)
			}
		}
	},
}

// epicOverviewCmd shows all open epics with their children, assignees, and blockers
var epicOverviewCmd = &cobra.Command{
	Use:   "overview",
	Short: "Show all open epics with children, assignees, and blockers",
	Long: `Show a nested list of all open epics and their child issues.

Each child shows its status, assignee (if any), and blockers (if blocked).
Children are sorted by status: in_progress first, then open, blocked, closed.

Examples:
  bd epic overview                # Full overview
  bd epic overview --hide-closed  # Hide completed children
  bd epic overview --json         # Machine-readable output`,
	Run: func(cmd *cobra.Command, args []string) {
		requireDaemon("epic overview")
		hideClosed, _ := cmd.Flags().GetBool("hide-closed")

		var overviews []*types.EpicOverview
		resp, err := daemonClient.EpicOverview(&rpc.EpicOverviewArgs{})
		if err != nil {
			FatalErrorRespectJSON("communicating with daemon: %v", err)
		}
		if !resp.Success {
			FatalErrorRespectJSON("getting epic overview: %s", resp.Error)
		}
		if err := json.Unmarshal(resp.Data, &overviews); err != nil {
			FatalErrorRespectJSON("parsing response: %v", err)
		}

		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(overviews); err != nil {
				FatalErrorRespectJSON("encoding JSON: %v", err)
			}
			return
		}

		if len(overviews) == 0 {
			fmt.Println("No open epics found")
			return
		}

		for i, ov := range overviews {
			if i > 0 {
				fmt.Println()
			}

			// Epic header with progress
			progress := fmt.Sprintf("[%d/%d done]", ov.ClosedChildren, ov.TotalChildren)
			fmt.Printf("%s  %s  %s\n",
				ui.RenderAccent(ov.Epic.ID),
				ui.RenderBold(ov.Epic.Title),
				ui.RenderMuted(progress))

			// Sort children: in_progress, open, blocked, closed
			children := make([]types.EpicOverviewChild, len(ov.Children))
			copy(children, ov.Children)
			sort.Slice(children, func(a, b int) bool {
				return childSortOrder(children[a].Issue.Status) < childSortOrder(children[b].Issue.Status)
			})

			for _, child := range children {
				if hideClosed && child.Issue.Status == types.StatusClosed {
					continue
				}

				icon := childStatusIcon(child.Issue.Status)

				// Build the suffix: assignee and/or blockers
				var suffix string
				if child.Issue.Assignee != "" {
					suffix = fmt.Sprintf("(%s)", child.Issue.Assignee)
				}
				if len(child.BlockedBy) > 0 {
					blockerStr := "blocked by: " + strings.Join(child.BlockedBy, ", ")
					if suffix != "" {
						suffix += "  " + blockerStr
					} else {
						suffix = blockerStr
					}
				}

				if suffix != "" {
					fmt.Printf("  %s %s  %-45s %s\n",
						icon,
						ui.RenderID(child.Issue.ID),
						child.Issue.Title,
						ui.RenderMuted(suffix))
				} else {
					fmt.Printf("  %s %s  %s\n",
						icon,
						ui.RenderID(child.Issue.ID),
						child.Issue.Title)
				}
			}
		}
	},
}

// dashboardCmd shows a detailed dashboard for a single epic via daemon RPC
var dashboardCmd = &cobra.Command{
	Use:   "dashboard <epic-id>",
	Short: "Show epic dashboard with progress visualization",
	Long: `Display a visual dashboard for an epic showing progress, children status, and summary.

Example:
  bd epic dashboard bd-yb1az

Shows:
  - Progress bar with percentage
  - List of children with status, assignee, and blockers
  - Summary counts (blocked, ready, complete)`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireDaemon("epic dashboard")
		epicID := args[0]

		// Get overview data for all epics, then find the one we want
		var overviews []*types.EpicOverview
		resp, err := daemonClient.EpicOverview(&rpc.EpicOverviewArgs{})
		if err != nil {
			FatalErrorRespectJSON("communicating with daemon: %v", err)
		}
		if !resp.Success {
			FatalErrorRespectJSON("getting epic overview: %s", resp.Error)
		}
		if err := json.Unmarshal(resp.Data, &overviews); err != nil {
			FatalErrorRespectJSON("parsing response: %v", err)
		}

		// Find matching epic (support partial ID)
		var ov *types.EpicOverview
		for _, o := range overviews {
			if o.Epic.ID == epicID || strings.HasPrefix(o.Epic.ID, epicID) {
				ov = o
				break
			}
		}
		if ov == nil {
			FatalErrorRespectJSON("epic not found: %s", epicID)
		}

		epic := ov.Epic
		total := ov.TotalChildren
		complete := ov.ClosedChildren
		percentage := 0
		if total > 0 {
			percentage = (complete * 100) / total
		}

		// Count statuses from children
		var inProgress, blocked, open int
		for _, child := range ov.Children {
			switch child.Issue.Status {
			case types.StatusInProgress:
				inProgress++
			case types.StatusBlocked:
				blocked++
			case types.StatusClosed:
				// already counted
			default:
				open++
			}
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"epic":        epic,
				"children":    ov.Children,
				"total":       total,
				"complete":    complete,
				"in_progress": inProgress,
				"blocked":     blocked,
				"open":        open,
				"percentage":  percentage,
			})
			return
		}

		// Render dashboard
		width := 60
		titleLine := fmt.Sprintf("Epic: %s - %s", epic.ID, epic.Title)
		if len(titleLine) > width-4 {
			titleLine = titleLine[:width-7] + "..."
		}

		// Box top
		fmt.Println("╭" + repeatStr("─", width) + "╮")
		fmt.Printf("│ %-*s │\n", width-2, titleLine)
		fmt.Println("├" + repeatStr("─", width) + "┤")

		// Progress bar
		barWidth := width - 20
		filledWidth := (percentage * barWidth) / 100
		emptyWidth := barWidth - filledWidth
		progressBar := repeatStr("█", filledWidth) + repeatStr("░", emptyWidth)
		fmt.Printf("│ Progress: %s %3d%% (%d/%d) │\n", progressBar, percentage, complete, total)

		// Status
		fmt.Printf("│ Status: %-*s │\n", width-11, epic.Status)

		// Separator
		fmt.Println("│" + repeatStr(" ", width) + "│")

		// Children
		if len(ov.Children) > 0 {
			fmt.Printf("│ %-*s │\n", width-2, "Children:")

			// Sort children
			children := make([]types.EpicOverviewChild, len(ov.Children))
			copy(children, ov.Children)
			sort.Slice(children, func(a, b int) bool {
				return childSortOrder(children[a].Issue.Status) < childSortOrder(children[b].Issue.Status)
			})

			for _, child := range children {
				icon := childStatusIcon(child.Issue.Status)

				// Build child line with assignee
				childLine := fmt.Sprintf("  %s %s", icon, child.Issue.ID)
				titlePart := child.Issue.Title
				if child.Issue.Assignee != "" {
					titlePart += fmt.Sprintf(" (%s)", child.Issue.Assignee)
				}
				maxTitleLen := width - len(childLine) - 5
				if maxTitleLen > 0 && len(titlePart) > maxTitleLen {
					titlePart = titlePart[:maxTitleLen-3] + "..."
				}
				fullLine := fmt.Sprintf("%s %s", childLine, titlePart)
				fmt.Printf("│ %-*s │\n", width-2, fullLine)

				// Show blockers on next line if any
				if len(child.BlockedBy) > 0 {
					blockerLine := fmt.Sprintf("       blocked by: %s", strings.Join(child.BlockedBy, ", "))
					if len(blockerLine) > width-4 {
						blockerLine = blockerLine[:width-7] + "..."
					}
					fmt.Printf("│ %-*s │\n", width-2, blockerLine)
				}
			}
		} else {
			fmt.Printf("│ %-*s │\n", width-2, "(no children)")
		}

		// Separator
		fmt.Println("│" + repeatStr(" ", width) + "│")

		// Summary
		summary := fmt.Sprintf("Blocked: %d | In Progress: %d | Open: %d | Done: %d", blocked, inProgress, open, complete)
		fmt.Printf("│ %-*s │\n", width-2, summary)

		// Box bottom
		fmt.Println("╰" + repeatStr("─", width) + "╯")
	},
}

// orphanedChildrenCmd finds children whose parent epic is closed or missing (via daemon RPC)
var orphanedChildrenCmd = &cobra.Command{
	Use:   "orphaned-children",
	Short: "Find children whose parent epic is closed or missing",
	Long: `Find issues that are orphaned - their parent epic is either closed or doesn't exist.

Orphaned children may indicate:
  - Work that was abandoned when an epic was closed
  - Data corruption or missing parent beads
  - Issues that should be reparented to a new epic

Note: This differs from 'bd orphans' which finds issues mentioned in commits but not closed.

Examples:
  bd epic orphaned-children              # Find all orphaned children
  bd epic orphaned-children --json       # Machine-readable output`,
	Run: func(cmd *cobra.Command, args []string) {
		requireDaemon("epic orphaned-children")

		var orphans []*types.OrphanedChild
		resp, err := daemonClient.EpicOrphanedChildren(&rpc.EpicOrphanedChildrenArgs{})
		if err != nil {
			FatalErrorRespectJSON("communicating with daemon: %v", err)
		}
		if !resp.Success {
			FatalErrorRespectJSON("getting orphaned children: %s", resp.Error)
		}
		if err := json.Unmarshal(resp.Data, &orphans); err != nil {
			FatalErrorRespectJSON("parsing response: %v", err)
		}

		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(orphans); err != nil {
				FatalErrorRespectJSON("encoding JSON: %v", err)
			}
			return
		}

		if len(orphans) == 0 {
			fmt.Println("No orphaned children found")
			return
		}

		fmt.Printf("Found %d orphaned child issue(s):\n\n", len(orphans))
		for _, orphan := range orphans {
			reason := ""
			switch orphan.Reason {
			case "parent_closed":
				reason = fmt.Sprintf("parent %s closed", orphan.ParentID)
			case "parent_not_found":
				reason = fmt.Sprintf("parent %s not found", orphan.ParentID)
			}
			fmt.Printf("  %s %s - %s\n", ui.RenderWarn("⚠"), ui.RenderID(orphan.ID), orphan.Title)
			fmt.Printf("    %s\n", ui.RenderMuted(reason))
		}

		fmt.Printf("\nHint: Use 'bd update <id> --parent <new-parent>' to reparent orphaned issues\n")
	},
}

// childStatusIcon returns the display icon for a child issue status
func childStatusIcon(status types.Status) string {
	switch status {
	case types.StatusClosed:
		return ui.RenderPass("✓")
	case types.StatusInProgress:
		return ui.RenderWarn("◐")
	case types.StatusBlocked:
		return ui.RenderFail("⊘")
	default:
		return "○"
	}
}

// childSortOrder returns a sort key for child status ordering:
// in_progress=0, open=1, blocked=2, closed=3
func childSortOrder(status types.Status) int {
	switch status {
	case types.StatusInProgress:
		return 0
	case types.StatusOpen:
		return 1
	case types.StatusBlocked:
		return 2
	case types.StatusClosed:
		return 3
	default:
		return 1
	}
}

// repeatStr repeats a string n times
func repeatStr(s string, n int) string {
	if n <= 0 {
		return ""
	}
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}

func init() {
	epicCmd.AddCommand(epicStatusCmd)
	epicCmd.AddCommand(closeEligibleEpicsCmd)
	epicCmd.AddCommand(orphanedChildrenCmd)
	epicCmd.AddCommand(dashboardCmd)
	epicCmd.AddCommand(epicOverviewCmd)
	closeEligibleEpicsCmd.Flags().Bool("dry-run", false, "Preview what would be closed without making changes")
	epicOverviewCmd.Flags().Bool("hide-closed", false, "Hide completed children")
	rootCmd.AddCommand(epicCmd)
}
