package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// newsWindow is the lookback window for recent activity (var for testing)
var newsWindow = 2 * time.Hour

// NewsOutput is the JSON output structure for bd news
type NewsOutput struct {
	InProgress     []*types.Issue `json:"in_progress"`
	RecentlyOpened []*types.Issue `json:"recently_opened"`
	RecentlyClosed []*types.Issue `json:"recently_closed"`
}

var newsCmd = &cobra.Command{
	Use:     "news",
	GroupID: "views",
	Short:   "Show recent activity and in-progress work by others",
	Long: `Show what's happening: in-progress work, recently opened, and recently closed issues.

Use this before starting work to get situational awareness:
- What's actively being worked on (potential conflicts)
- What was just opened (new work to consider)
- What was just closed (context on recent progress)

Issues by the current actor are excluded unless --all is specified.

Examples:
  bd news                    # Show recent activity by others
  bd news --all              # Include your own activity
  bd news --window 4h        # Look back 4 hours instead of default 2h
  bd news --limit 20         # Show more results per section`,
	Run: func(cmd *cobra.Command, args []string) {
		limit, _ := cmd.Flags().GetInt("limit")
		showAll, _ := cmd.Flags().GetBool("all")
		windowStr, _ := cmd.Flags().GetString("window")

		// Parse custom window duration
		window := newsWindow
		if windowStr != "" {
			d, err := time.ParseDuration(windowStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --window duration %q (use e.g. 2h, 30m, 4h)\n", windowStr)
				os.Exit(1)
			}
			window = d
		}

		requireDaemon("news")

		cutoff := time.Now().UTC().Add(-window).Format(time.RFC3339)
		currentActor := getActor()

		// Fetch all three categories in sequence (daemon handles them fast)
		inProgress := fetchNewsList(daemonClient, &rpc.ListArgs{
			Status: "in_progress",
			Limit:  limit,
		})
		recentlyOpened := fetchNewsList(daemonClient, &rpc.ListArgs{
			CreatedAfter: cutoff,
			Limit:        limit,
		})
		recentlyClosed := fetchNewsList(daemonClient, &rpc.ListArgs{
			Status:      "closed",
			ClosedAfter: cutoff,
			Limit:       limit,
		})

		// Filter out current actor's work (unless --all)
		if !showAll {
			inProgress = filterOutActor(inProgress, currentActor)
			recentlyOpened = filterOutActor(recentlyOpened, currentActor)
			recentlyClosed = filterOutActor(recentlyClosed, currentActor)
		}

		// Deduplicate: remove in-progress and closed items from recently-opened
		// (an issue opened and immediately claimed, or opened and closed, would appear in multiple sections)
		excludeFromOpened := make(map[string]bool, len(inProgress)+len(recentlyClosed))
		for _, issue := range inProgress {
			excludeFromOpened[issue.ID] = true
		}
		for _, issue := range recentlyClosed {
			excludeFromOpened[issue.ID] = true
		}
		recentlyOpened = filterOutIDs(recentlyOpened, excludeFromOpened)

		// Filter out decision points from all sections (they clutter the news feed)
		inProgress = filterOutDecisions(inProgress)
		recentlyOpened = filterOutDecisions(recentlyOpened)
		recentlyClosed = filterOutDecisions(recentlyClosed)

		if jsonOutput {
			outputJSON(NewsOutput{
				InProgress:     coalesce(inProgress),
				RecentlyOpened: coalesce(recentlyOpened),
				RecentlyClosed: coalesce(recentlyClosed),
			})
			return
		}

		totalItems := len(inProgress) + len(recentlyOpened) + len(recentlyClosed)
		if totalItems == 0 {
			fmt.Printf("\n%s No recent activity%s\n\n",
				ui.RenderPass("✨"), windowSuffix(window))
			return
		}

		printed := false

		if len(inProgress) > 0 {
			fmt.Printf("\n%s In-progress by others (%d):\n\n",
				ui.RenderAccent("◐"), len(inProgress))
			for _, issue := range inProgress {
				printNewsIssue(issue)
			}
			printed = true
		}

		if len(recentlyOpened) > 0 {
			if printed {
				fmt.Println()
			}
			fmt.Printf("%s Opened in last %s (%d):\n\n",
				ui.RenderAccent("○"), formatDurationShort(window), len(recentlyOpened))
			for _, issue := range recentlyOpened {
				printNewsIssue(issue)
			}
			printed = true
		}

		if len(recentlyClosed) > 0 {
			if printed {
				fmt.Println()
			}
			fmt.Printf("%s Closed in last %s (%d):\n\n",
				ui.RenderAccent("✓"), formatDurationShort(window), len(recentlyClosed))
			for _, issue := range recentlyClosed {
				printNewsIssue(issue)
			}
		}

		fmt.Printf("\n%s\n\n", ui.RenderMuted("Tip: Check for file overlap with in-progress work before starting on the same areas."))
	},
}

// fetchNewsList fetches issues from the daemon and returns them, exiting on error.
func fetchNewsList(client *rpc.Client, args *rpc.ListArgs) []*types.Issue {
	resp, err := client.List(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	var issues []*types.Issue
	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}
	return issues
}

// filterOutActor removes issues where the assignee matches the current actor.
// Matching is case-insensitive and also checks suffix for role paths
// (e.g., "gastown/polecats/furiosa" matches actor "furiosa").
func filterOutActor(issues []*types.Issue, actor string) []*types.Issue {
	if actor == "" || actor == "unknown" {
		return issues
	}
	actorLower := strings.ToLower(actor)
	var filtered []*types.Issue
	for _, issue := range issues {
		assigneeLower := strings.ToLower(issue.Assignee)
		if assigneeLower == actorLower {
			continue
		}
		if strings.HasSuffix(assigneeLower, "/"+actorLower) {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}

// filterOutDecisions removes decision-point issues from the list.
func filterOutDecisions(issues []*types.Issue) []*types.Issue {
	var filtered []*types.Issue
	for _, issue := range issues {
		if issue.IssueType == "gate" || issue.IssueType == "decision" || strings.HasPrefix(issue.Title, "[DECISION]") {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}

// filterOutIDs removes issues whose ID is in the exclude set.
func filterOutIDs(issues []*types.Issue, exclude map[string]bool) []*types.Issue {
	var filtered []*types.Issue
	for _, issue := range issues {
		if !exclude[issue.ID] {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

// printNewsIssue prints a single issue in the bd news format.
func printNewsIssue(issue *types.Issue) {
	statusIcon := ui.RenderStatusIcon(string(issue.Status))

	assigneeStr := ui.RenderMuted("unassigned")
	if issue.Assignee != "" {
		assigneeStr = fmt.Sprintf("@%s", issue.Assignee)
	}

	// Use closed_at for closed issues, updated_at otherwise
	ageTime := issue.UpdatedAt
	if issue.Status == types.StatusClosed && issue.ClosedAt != nil {
		ageTime = *issue.ClosedAt
	}
	age := formatRelativeTime(ageTime)

	createdByStr := ""
	if issue.CreatedBy != "" {
		createdByStr = fmt.Sprintf(" by %s", ui.RenderMuted(issue.CreatedBy))
	}

	fmt.Printf("  %s %s  %s  %s%s  %s\n",
		statusIcon,
		ui.RenderID(issue.ID),
		assigneeStr,
		ui.RenderMuted(age),
		createdByStr,
		issue.Title)
}

// coalesce returns an empty slice instead of nil (for clean JSON output).
func coalesce(issues []*types.Issue) []*types.Issue {
	if issues == nil {
		return []*types.Issue{}
	}
	return issues
}

// formatDurationShort formats a duration as a human-friendly short string.
func formatDurationShort(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := d.Hours()
	if hours == float64(int(hours)) {
		return fmt.Sprintf("%dh", int(hours))
	}
	return fmt.Sprintf("%.1fh", hours)
}

// windowSuffix returns " (last Xh)" for the empty-state message.
func windowSuffix(d time.Duration) string {
	return fmt.Sprintf(" (last %s)", formatDurationShort(d))
}

func init() {
	newsCmd.Flags().IntP("limit", "n", 50, "Maximum issues to show per section")
	newsCmd.Flags().Bool("all", false, "Include your own activity")
	newsCmd.Flags().String("window", "", "Lookback window for recent activity (default 2h, e.g. 4h, 30m)")
	rootCmd.AddCommand(newsCmd)
}
