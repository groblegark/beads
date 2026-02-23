package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/sensitive"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// jackShowCmd displays detailed jack information.
var jackShowCmd = &cobra.Command{
	Use:   "show <jack-id>",
	Short: "Show detailed jack information",
	Long: `Display detailed information about a jack including target, TTL status,
revert plan, and all logged changes.

Use --reveal to show full before/after values that may contain secrets.
Without --reveal, fields flagged as sensitive are redacted.

Examples:
  bd jack show bd-jack-abc
  bd jack show bd-jack-abc --reveal
  bd jack show bd-jack-abc --json`,
	Args: cobra.ExactArgs(1),
	Run:  runJackShow,
}

func init() {
	jackShowCmd.Flags().Bool("reveal", false, "Show full values including potentially sensitive data")
	jackShowCmd.ValidArgsFunction = issueIDCompletion
	jackCmd.AddCommand(jackShowCmd)
}

func runJackShow(cmd *cobra.Command, args []string) {
	requireDaemon("jack show")

	jackID := args[0]
	reveal, _ := cmd.Flags().GetBool("reveal")

	// Fetch the jack
	showResp, err := daemonClient.Show(&rpc.ShowArgs{ID: jackID})
	if err != nil {
		FatalErrorRespectJSON("jack not found: %v — run 'bd jack list' to see active jacks", err)
	}
	if !showResp.Success {
		FatalErrorRespectJSON("jack not found: %s — run 'bd jack list' to see active jacks", showResp.Error)
	}

	var details types.IssueDetails
	if err := json.Unmarshal(showResp.Data, &details); err != nil {
		FatalErrorRespectJSON("error parsing jack: %v", err)
	}
	issue := &details.Issue

	if issue.IssueType != types.TypeJack {
		FatalErrorRespectJSON("%s is not a jack (type=%s) — use 'bd show' for regular issues", jackID, issue.IssueType)
	}

	// Parse metadata
	metadata := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
			FatalErrorRespectJSON("error parsing jack metadata: %v", err)
		}
	}

	jackTarget, _ := metadata["jack_target"].(string)
	jackTTL, _ := metadata["jack_ttl"].(string)
	jackExpiresAt, _ := metadata["jack_expires_at"].(string)
	jackRevertPlan, _ := metadata["jack_revert_plan"].(string)
	jackReverted, _ := metadata["jack_reverted"].(bool)

	// Parse changes
	var changes []rpc.JackChange
	if rawChanges, ok := metadata["jack_changes"]; ok {
		changesJSON, err := json.Marshal(rawChanges)
		if err == nil {
			_ = json.Unmarshal(changesJSON, &changes)
		}
	}

	// Parse expiry time
	var expiresAt time.Time
	var expired bool
	var activeFor time.Duration
	if jackExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, jackExpiresAt); err == nil {
			expiresAt = t
			expired = time.Now().After(t)
			activeFor = time.Since(issue.CreatedAt)
		}
	}

	// JSON output
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"id":           jackID,
			"title":        issue.Title,
			"status":       issue.Status,
			"priority":     issue.Priority,
			"target":       jackTarget,
			"ttl":          jackTTL,
			"expires_at":   jackExpiresAt,
			"expired":      expired,
			"active_for":   activeFor.String(),
			"reason":       issue.Description,
			"revert_plan":  jackRevertPlan,
			"reverted":     jackReverted,
			"assignee":     issue.Assignee,
			"created_at":   issue.CreatedAt.Format(time.RFC3339),
			"changes":      changes,
			"change_count": len(changes),
			"labels":       details.Labels,
		})
		return
	}

	agentMode := ui.IsAgentMode()
	sectionHeader := func(name string) string {
		if agentMode {
			return name
		}
		return ui.RenderBold(name)
	}

	// Header line: ◐ bd-jack-abc [JACK] · Title   [● P2 · IN_PROGRESS]
	fmt.Println()
	fmt.Println(formatJackHeader(issue))

	// Agent · Target line
	agentStr := issue.Assignee
	if agentStr == "" {
		agentStr = issue.CreatedBy
	}
	fmt.Printf("Agent: %s · Target: %s\n", agentStr, jackTarget)

	// TTL · Active · Expires line
	ttlParts := []string{}
	if jackTTL != "" {
		ttlParts = append(ttlParts, fmt.Sprintf("TTL: %s", jackTTL))
	}
	if activeFor > 0 {
		ttlParts = append(ttlParts, fmt.Sprintf("Active: %s", jackFmtDuration(activeFor)))
	}
	if !expiresAt.IsZero() {
		expiryStr := expiresAt.Format(time.RFC3339)
		if expired {
			overdue := time.Since(expiresAt)
			if agentMode {
				expiryStr = fmt.Sprintf("%s (EXPIRED %s ago)", expiryStr, jackFmtDuration(overdue))
			} else {
				expiryStr = fmt.Sprintf("%s %s", expiryStr, ui.RenderWarn(fmt.Sprintf("(EXPIRED %s ago)", jackFmtDuration(overdue))))
			}
		}
		ttlParts = append(ttlParts, fmt.Sprintf("Expires: %s", expiryStr))
	}
	if len(ttlParts) > 0 {
		fmt.Println(strings.Join(ttlParts, " · "))
	}

	// Reason section
	if issue.Description != "" {
		fmt.Printf("\n%s\n", sectionHeader("REASON"))
		fmt.Printf("  %s\n", issue.Description)
	}

	// Revert plan section
	if jackRevertPlan != "" {
		fmt.Printf("\n%s", sectionHeader("REVERT PLAN"))
		if jackReverted {
			if agentMode {
				fmt.Print(" [REVERTED]")
			} else {
				fmt.Printf(" %s", ui.RenderPass("[REVERTED]"))
			}
		}
		fmt.Println()
		for _, line := range strings.Split(jackRevertPlan, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}

	// Changes section
	sensitiveCount := 0
	if len(changes) > 0 {
		fmt.Printf("\n%s (%d):\n", sectionHeader("CHANGES"), len(changes))
		for _, ch := range changes {
			if ch.Sensitive {
				sensitiveCount++
			}
			timeStr := ch.Timestamp
			if t, err := time.Parse(time.RFC3339, ch.Timestamp); err == nil {
				timeStr = t.Format("15:04")
			}

			sensitiveTag := ""
			if ch.Sensitive {
				sensitiveTag = " [S]"
			}
			parts := []string{fmt.Sprintf("  [%s] %s %s%s", timeStr, ch.Action, ch.Target, sensitiveTag)}

			if ch.Cmd != "" {
				parts = append(parts, fmt.Sprintf(" %s", ch.Cmd))
			}

			if ch.Before != "" && ch.After != "" {
				beforeDisplay := ch.Before
				afterDisplay := ch.After
				if !reveal {
					beforeDisplay = redactIfSensitive(beforeDisplay)
					afterDisplay = redactIfSensitive(afterDisplay)
				}
				if len(beforeDisplay) > 40 {
					beforeDisplay = beforeDisplay[:37] + "..."
				}
				if len(afterDisplay) > 40 {
					afterDisplay = afterDisplay[:37] + "..."
				}
				parts = append(parts, fmt.Sprintf("  %s → %s", beforeDisplay, afterDisplay))
			} else if ch.After != "" {
				afterDisplay := ch.After
				if !reveal {
					afterDisplay = redactIfSensitive(afterDisplay)
				}
				if len(afterDisplay) > 40 {
					afterDisplay = afterDisplay[:37] + "..."
				}
				parts = append(parts, fmt.Sprintf("  → %s", afterDisplay))
			}

			fmt.Println(strings.Join(parts, ""))
		}

		if sensitiveCount > 0 && !reveal {
			fmt.Printf("\n  [S] = %d change(s) contain redacted sensitive data — use --reveal to show\n", sensitiveCount)
		}

		if len(changes) >= maxJackChanges*4/5 {
			fmt.Printf("\n  %s Change log is %.0f%% full\n",
				ui.RenderWarn("!"), float64(len(changes))/float64(maxJackChanges)*100)
		}
	} else {
		fmt.Printf("\n%s: none\n", sectionHeader("CHANGES"))
	}

	// Labels
	if len(details.Labels) > 0 {
		fmt.Printf("\n%s %s\n", sectionHeader("LABELS:"), strings.Join(details.Labels, ", "))
	}

	// Dependencies
	if len(details.Dependencies) > 0 {
		fmt.Printf("\n%s\n", sectionHeader("DEPENDS ON"))
		for _, dep := range details.Dependencies {
			fmt.Println(formatDependencyLine("→", dep))
		}
	}
	if len(details.Dependents) > 0 {
		fmt.Printf("\n%s\n", sectionHeader("BLOCKS"))
		for _, dep := range details.Dependents {
			fmt.Println(formatDependencyLine("←", dep))
		}
	}

	fmt.Println()
}

// formatJackHeader creates the jack-specific header line.
func formatJackHeader(issue *types.Issue) string {
	if ui.IsAgentMode() {
		icon := ui.GetStatusIcon(string(issue.Status))
		return fmt.Sprintf("%s %s [JACK] · %s   [● P%d · %s]",
			icon, issue.ID, issue.Title, issue.Priority, strings.ToUpper(string(issue.Status)))
	}

	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	statusStyle := ui.GetStatusStyle(string(issue.Status))
	statusStr := statusStyle.Render(strings.ToUpper(string(issue.Status)))
	priorityTag := ui.RenderPriority(issue.Priority)
	idStyled := ui.RenderAccent(issue.ID)

	return fmt.Sprintf("%s %s [JACK] · %s   [%s · %s]",
		statusIcon, idStyled, issue.Title, priorityTag, statusStr)
}

// jackFmtDuration formats a time.Duration into a human-readable string.
func jackFmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours < 24 {
		if mins > 0 {
			return fmt.Sprintf("%dh%dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := hours / 24
	hours = hours % 24
	if hours > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}

// redactIfSensitive checks if a value matches known secret patterns and redacts it.
func redactIfSensitive(value string) string {
	if sensitive.ContainsSecret(value) {
		return sensitive.RedactedPlaceholder
	}
	return value
}
