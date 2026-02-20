package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/ui"
)

var commitCmd = &cobra.Command{
	Use:     "commit",
	GroupID: "issues",
	Short:   "Manage commit-bead associations",
	Long: `Track associations between git commits and beads (issues).

Subcommands:
  link   - Manually link a commit to a bead
  list   - Show commits associated with a bead
  show   - Show beads associated with a commit`,
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

var commitLinkCmd = &cobra.Command{
	Use:   "link <issue-id> <commit-sha>",
	Short: "Link a commit to a bead",
	Long: `Manually create an association between a git commit and a bead.

This is an escape hatch for when the post-commit hook doesn't automatically
capture the association (e.g., commits made before hooks were installed).

Examples:
  bd commit link bd-abc 1f765486
  bd commit link bd-abc 1f765486ab3d --branch main --message "feat: add feature"`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		requireDaemon("commit link")

		issueID := args[0]
		commitSHA := args[1]

		// Resolve partial issue ID
		resolveResp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: issueID})
		if err != nil {
			FatalErrorRespectJSON("resolving issue ID %s: %v", issueID, err)
		}
		var resolvedID string
		if err := json.Unmarshal(resolveResp.Data, &resolvedID); err != nil {
			FatalErrorRespectJSON("unmarshaling resolved ID: %v", err)
		}

		branch, _ := cmd.Flags().GetString("branch")
		repoURL, _ := cmd.Flags().GetString("repo")
		author, _ := cmd.Flags().GetString("author")
		message, _ := cmd.Flags().GetString("message")

		recordArgs := &rpc.RecordCommitArgs{
			IssueID:   resolvedID,
			CommitSHA: commitSHA,
			Branch:    branch,
			RepoURL:   repoURL,
			Author:    author,
			Message:   message,
		}

		_, err = daemonClient.RecordCommit(recordArgs)
		if err != nil {
			FatalErrorRespectJSON("recording commit: %v", err)
		}

		fmt.Printf("%s Linked commit %s to %s\n", ui.RenderPass("✓"), commitSHA[:minInt(8, len(commitSHA))], resolvedID)
	},
}

var commitListCmd = &cobra.Command{
	Use:   "list <issue-id>",
	Short: "Show commits for a bead",
	Long: `List all git commits associated with a bead.

Examples:
  bd commit list bd-abc
  bd commit list bd-abc --limit 5`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireDaemon("commit list")

		issueID := args[0]

		// Resolve partial issue ID
		resolveResp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: issueID})
		if err != nil {
			FatalErrorRespectJSON("resolving issue ID %s: %v", issueID, err)
		}
		var resolvedID string
		if err := json.Unmarshal(resolveResp.Data, &resolvedID); err != nil {
			FatalErrorRespectJSON("unmarshaling resolved ID: %v", err)
		}

		limit, _ := cmd.Flags().GetInt("limit")

		commits, err := daemonClient.CommitsForIssue(&rpc.CommitsForIssueArgs{
			IssueID: resolvedID,
			Limit:   limit,
		})
		if err != nil {
			FatalErrorRespectJSON("fetching commits for %s: %v", resolvedID, err)
		}

		if jsonOutput {
			data, _ := json.MarshalIndent(commits, "", "  ")
			fmt.Println(string(data))
			return
		}

		if len(commits) == 0 {
			fmt.Printf("No commits linked to %s\n", resolvedID)
			return
		}

		fmt.Printf("Commits for %s (%d):\n\n", ui.RenderID(resolvedID), len(commits))
		for _, c := range commits {
			sha := c.CommitSHA
			if len(sha) > 8 {
				sha = sha[:8]
			}

			// Format: sha  branch  message  (age)
			parts := []string{ui.RenderID(sha)}
			if c.Branch != "" {
				parts = append(parts, ui.RenderMuted("["+c.Branch+"]"))
			}
			if c.Message != "" {
				// Truncate long messages
				msg := c.Message
				if len(msg) > 60 {
					msg = msg[:57] + "..."
				}
				parts = append(parts, msg)
			}
			if c.CommittedAt != "" {
				if t, err := time.Parse(time.RFC3339, c.CommittedAt); err == nil {
					parts = append(parts, ui.RenderMuted("("+relativeTime(t)+")"))
				}
			}

			fmt.Printf("  %s\n", strings.Join(parts, "  "))

			// Show author on second line if present
			if c.Author != "" {
				fmt.Printf("    %s\n", ui.RenderMuted("by "+c.Author))
			}
		}
	},
}

var commitShowCmd = &cobra.Command{
	Use:   "show <commit-sha>",
	Short: "Show beads for a commit",
	Long: `Show all beads associated with a git commit SHA.

Examples:
  bd commit show 1f765486
  bd commit show 1f765486ab3d4e5f`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		requireDaemon("commit show")

		commitSHA := args[0]

		issues, err := daemonClient.IssuesForCommit(&rpc.IssuesForCommitArgs{
			CommitSHA: commitSHA,
		})
		if err != nil {
			FatalErrorRespectJSON("fetching issues for commit %s: %v", commitSHA, err)
		}

		if jsonOutput {
			data, _ := json.MarshalIndent(issues, "", "  ")
			fmt.Println(string(data))
			return
		}

		sha := commitSHA
		if len(sha) > 8 {
			sha = sha[:8]
		}

		if len(issues) == 0 {
			fmt.Printf("No beads linked to commit %s\n", sha)
			return
		}

		fmt.Printf("Beads for commit %s (%d):\n\n", ui.RenderID(sha), len(issues))
		for _, c := range issues {
			parts := []string{ui.RenderID(c.IssueID)}
			if c.Branch != "" {
				parts = append(parts, ui.RenderMuted("["+c.Branch+"]"))
			}
			if c.Message != "" {
				msg := c.Message
				if len(msg) > 60 {
					msg = msg[:57] + "..."
				}
				parts = append(parts, msg)
			}
			fmt.Printf("  %s\n", strings.Join(parts, "  "))
		}
	},
}

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// relativeTime formats a timestamp as relative time (e.g., "2m ago", "1h ago").
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func init() {
	commitLinkCmd.Flags().String("branch", "", "Branch name")
	commitLinkCmd.Flags().String("repo", "", "Repository URL")
	commitLinkCmd.Flags().String("author", "", "Commit author")
	commitLinkCmd.Flags().String("message", "", "Commit message")

	commitListCmd.Flags().Int("limit", 0, "Maximum commits to show (0 = all)")

	commitCmd.AddCommand(commitLinkCmd)
	commitCmd.AddCommand(commitListCmd)
	commitCmd.AddCommand(commitShowCmd)

	rootCmd.AddCommand(commitCmd)
}
