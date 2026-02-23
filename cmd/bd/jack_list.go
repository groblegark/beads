package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// jackListCmd shows active jacks with target, TTL remaining, agent, and status.
var jackListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show active jacks (default) or all with filtering",
	Long: `Show infrastructure jacks with target, TTL remaining, agent, and status.

By default, shows only active (in_progress) jacks sorted by expiry.

Flags:
  -a, --all              Include closed jacks
      --expired          Show only expired (past TTL) jacks
      --target string    Filter by target resource
      --json             Output JSON

Examples:
  bd jack list
  bd jack list --all
  bd jack list --expired
  bd jack list --target=pod/bd-daemon-abc`,
	Run: runJackList,
}

func init() {
	jackListCmd.Flags().BoolP("all", "a", false, "Include closed jacks")
	jackListCmd.Flags().Bool("expired", false, "Show only expired (past TTL) jacks")
	jackListCmd.Flags().String("target", "", "Filter by target resource")

	jackCmd.AddCommand(jackListCmd)
}

// jackListMeta holds parsed jack-specific metadata from the issue's JSON blob.
type jackListMeta struct {
	Target     string           `json:"jack_target"`
	TTL        string           `json:"jack_ttl"`
	ExpiresAt  string           `json:"jack_expires_at"`
	RevertPlan string           `json:"jack_revert_plan"`
	Reverted   bool             `json:"jack_reverted"`
	Changes    []jackListChange `json:"jack_changes"`
}

type jackListChange struct {
	Timestamp string `json:"timestamp"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Agent     string `json:"agent,omitempty"`
}

// jackListItem holds a jack with parsed metadata for display.
type jackListItem struct {
	Issue      *types.Issue `json:"issue"`
	Meta       jackListMeta `json:"metadata"`
	ActiveTime string       `json:"active_time"`
	Expired    bool         `json:"expired"`
}

func runJackList(cmd *cobra.Command, args []string) {
	requireDaemon("jack list")

	showAll, _ := cmd.Flags().GetBool("all")
	expiredOnly, _ := cmd.Flags().GetBool("expired")
	targetFilter, _ := cmd.Flags().GetString("target")

	// Build list query: filter by type=jack
	listArgs := &rpc.ListArgs{
		IssueType: string(types.TypeJack),
	}

	// Default: only active (in_progress) jacks unless --all
	if !showAll {
		listArgs.Status = "in_progress"
	}

	resp, err := daemonClient.List(listArgs)
	if err != nil {
		FatalErrorRespectJSON("error listing jacks: %v", err)
	}
	if !resp.Success {
		FatalErrorRespectJSON("error listing jacks: %s", resp.Error)
	}

	// Parse response
	var issuesWithCounts []*types.IssueWithCounts
	if err := json.Unmarshal(resp.Data, &issuesWithCounts); err != nil {
		FatalErrorRespectJSON("error parsing response: %v", err)
	}

	// Build jack list items with parsed metadata
	now := time.Now()
	var items []jackListItem

	for _, iwc := range issuesWithCounts {
		issue := iwc.Issue
		if issue == nil {
			continue
		}

		// Parse jack metadata from issue
		var meta jackListMeta
		if len(issue.Metadata) > 0 {
			_ = json.Unmarshal(issue.Metadata, &meta)
		}

		// Check expiry
		expired := false
		if issue.JackExpiresAt != nil {
			expired = now.After(*issue.JackExpiresAt)
		} else if meta.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, meta.ExpiresAt); err == nil {
				expired = now.After(t)
			}
		}

		// Apply --expired filter
		if expiredOnly && !expired {
			continue
		}

		// Apply --target filter
		if targetFilter != "" && meta.Target != targetFilter {
			continue
		}

		// Calculate active time
		activeTime := jackFormatDuration(now.Sub(issue.CreatedAt))

		items = append(items, jackListItem{
			Issue:      issue,
			Meta:       meta,
			ActiveTime: activeTime,
			Expired:    expired,
		})
	}

	// JSON output
	if jsonOutput {
		outputJSON(items)
		return
	}

	// Human-readable output
	if len(items) == 0 {
		if expiredOnly {
			fmt.Println("No expired jacks")
		} else if showAll {
			fmt.Println("No jacks found")
		} else {
			fmt.Println("No active jacks")
		}
		return
	}

	// Header
	label := "ACTIVE JACKS"
	if showAll {
		label = "ALL JACKS"
	}
	if expiredOnly {
		label = "EXPIRED JACKS"
	}
	fmt.Printf("%s (%d):\n\n", label, len(items))

	for _, item := range items {
		issue := item.Issue

		// Line 1: ID  [active time, TTL]  target=...  ⚠ EXPIRED
		ttlStr := item.Meta.TTL
		if ttlStr == "" {
			ttlStr = "1h"
		}
		expiredMark := ""
		if item.Expired {
			expiredMark = fmt.Sprintf("  %s EXPIRED", ui.RenderWarn("⚠"))
		}

		targetStr := ""
		if item.Meta.Target != "" {
			targetStr = fmt.Sprintf("  target=%s", item.Meta.Target)
		}

		fmt.Printf("  %s  [%s active, TTL %s]%s%s\n",
			ui.RenderID(issue.ID),
			item.ActiveTime,
			ttlStr,
			targetStr,
			expiredMark,
		)

		// Line 2: Reason (from description)
		reason := issue.Description
		if reason == "" {
			reason = issue.Title
		}
		fmt.Printf("    Reason: %s\n", reason)

		// Line 3: Agent
		agent := issue.CreatedBy
		if issue.Assignee != "" {
			agent = issue.Assignee
		}
		if agent != "" {
			fmt.Printf("    Agent: %s\n", agent)
		}

		// Line 4: Changes count (if any)
		if len(item.Meta.Changes) > 0 {
			fmt.Printf("    Changes: %d recorded\n", len(item.Meta.Changes))
		}

		// Line 5: Status (only for --all, when showing closed jacks)
		if showAll && issue.Status == types.StatusClosed {
			closedAt := ""
			if issue.ClosedAt != nil {
				closedAt = fmt.Sprintf(" at %s", issue.ClosedAt.Format("2006-01-02 15:04"))
			}
			reverted := ""
			if item.Meta.Reverted {
				reverted = " (reverted)"
			}
			fmt.Printf("    Status: closed%s%s\n", closedAt, reverted)
		}

		fmt.Println()
	}
}

// jackFormatDuration returns a human-readable duration string like "1h23m" or "4h12m".
func jackFormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}
