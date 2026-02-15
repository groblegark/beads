package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var newsCmd = &cobra.Command{
	Use:     "news",
	GroupID: "views",
	Short:   "Show in-progress work by others (conflict awareness)",
	Long: `Show what's currently being worked on by other agents/people.

Use this before starting work to check for potential conflicts.
Issues assigned to the current actor are excluded — you already know
what you're working on.

This command is called automatically by bd prime when there is active
work by others, so agents get conflict awareness at session start.

Examples:
  bd news                    # Show in-progress work by others
  bd news --all              # Include your own in-progress work
  bd news --limit 20         # Show more results`,
	Run: func(cmd *cobra.Command, args []string) {
		limit, _ := cmd.Flags().GetInt("limit")
		showAll, _ := cmd.Flags().GetBool("all")

		requireDaemon("news")

		// Fetch all in-progress issues
		listArgs := &rpc.ListArgs{
			Status: "in_progress",
			Limit:  limit,
		}
		resp, err := daemonClient.List(listArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		var issues []*types.Issue
		if err := json.Unmarshal(resp.Data, &issues); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
			os.Exit(1)
		}

		// Filter out current actor's own work (unless --all)
		if !showAll {
			currentActor := getActor()
			issues = filterOutActor(issues, currentActor)
		}

		if jsonOutput {
			if issues == nil {
				issues = []*types.Issue{}
			}
			outputJSON(issues)
			return
		}

		if len(issues) == 0 {
			if showAll {
				fmt.Printf("\n%s No in-progress work\n\n", ui.RenderPass("✨"))
			} else {
				fmt.Printf("\n%s No in-progress work by others\n\n", ui.RenderPass("✨"))
			}
			return
		}

		header := "Active work by others"
		if showAll {
			header = "All active work"
		}
		fmt.Printf("\n%s %s (%d in-progress):\n\n",
			ui.RenderAccent("📡"), header, len(issues))
		for _, issue := range issues {
			printNewsIssue(issue)
		}
		fmt.Printf("\n%s\n\n", ui.RenderMuted("Tip: Check for file overlap before starting work that touches the same areas."))
	},
}

// filterOutActor removes issues where the assignee or created_by matches the current actor.
// Matching is case-insensitive and also checks if the actor is a substring of the assignee
// (to handle cases like "gastown/polecats/furiosa" matching actor "furiosa").
func filterOutActor(issues []*types.Issue, actor string) []*types.Issue {
	if actor == "" || actor == "unknown" {
		return issues
	}
	actorLower := strings.ToLower(actor)
	var filtered []*types.Issue
	for _, issue := range issues {
		assigneeLower := strings.ToLower(issue.Assignee)
		// Skip if assignee matches actor (exact or suffix match for role paths)
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

// printNewsIssue prints a single issue in the bd news format.
// Format: ◐ ID  @assignee  age  created_by  Title
func printNewsIssue(issue *types.Issue) {
	statusIcon := ui.RenderStatusIcon(string(issue.Status))

	// Assignee (or "unassigned")
	assigneeStr := ui.RenderMuted("unassigned")
	if issue.Assignee != "" {
		assigneeStr = fmt.Sprintf("@%s", issue.Assignee)
	}

	// Relative age from updated_at
	age := formatRelativeTime(issue.UpdatedAt)

	// Created by
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

func init() {
	newsCmd.Flags().IntP("limit", "n", 50, "Maximum issues to show")
	newsCmd.Flags().Bool("all", false, "Include your own in-progress work")
	rootCmd.AddCommand(newsCmd)
}
