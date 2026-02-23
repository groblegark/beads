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

// inboxCmd is the parent command for inbox operations (bd-xtahx)
var inboxCmd = &cobra.Command{
	Use:     "inbox",
	GroupID: "issues",
	Short:   "Manage agent inbox (async notifications)",
	Long: `The inbox is an async, non-blocking notification system for agents.

External processes (CI, other agents, mail system, decision responses) push
messages to an agent's inbox. Messages are delivered on the next hook fire
(SessionStart, PreCompact) via the InboxDrainHandler.

Commands:
  push     Push a message to an agent's inbox
  list     List inbox items
  drain    Drain undelivered items (called by hook handler)

Examples:
  bd inbox push --to=mayor --type=alert "CI build failed on main"
  bd inbox push --to=dog --type=agent "Your subtask bd-xyz is done"
  bd inbox list
  bd inbox drain --json`,
}

// inboxPushCmd pushes a message to an agent's inbox
var inboxPushCmd = &cobra.Command{
	Use:   "push [message]",
	Short: "Push a message to an agent's inbox",
	Long: `Push a notification message to an agent's inbox.

Messages are stored in the inbox table and delivered on the next hook fire.
Dedup keys prevent duplicate messages (same type+source within the same millisecond).

Examples:
  bd inbox push --to=mayor --type=alert "CI build failed on main"
  bd inbox push --to=dog --type=agent "Your subtask bd-xyz is done"
  bd inbox push --to=all --type=system "Maintenance in 10 minutes"
  bd inbox push --rig=gastown --to=mayor "Deploy failed"
  bd inbox push --to=mayor --type=event --ttl=10m --dedup-key=ci-run-42 "CI started"`,
	Args: cobra.MinimumNArgs(1),
	Run:  runInboxPush,
}

// inboxListCmd lists inbox items
var inboxListCmd = &cobra.Command{
	Use:   "list",
	Short: "List inbox items",
	Long: `List inbox items for the current agent.

By default shows only undelivered items. Use --all to include delivered items.

Examples:
  bd inbox list
  bd inbox list --all
  bd inbox list --delivered`,
	Run: runInboxList,
}

// inboxDrainCmd drains undelivered inbox items
var inboxDrainCmd = &cobra.Command{
	Use:   "drain",
	Short: "Drain undelivered inbox items (called by hook handler)",
	Long: `Drain undelivered inbox items for the current agent and output them for injection.

This command is typically called by the InboxDrainHandler during SessionStart
and PreCompact hooks. It fetches undelivered items, formats them for display,
marks them as delivered, and outputs the formatted text.

Use --reconcile on SessionStart to also check the DB for missed items.

Examples:
  bd inbox drain --json
  bd inbox drain --reconcile`,
	Run: runInboxDrain,
}

func init() {
	// Push flags
	inboxPushCmd.Flags().String("to", "", "Target agent name")
	inboxPushCmd.Flags().String("rig", "", "Target rig (for cross-rig messaging)")
	inboxPushCmd.Flags().String("type", "agent", "Message type (alert, agent, mail, decision, gate, system, event)")
	inboxPushCmd.Flags().String("source", "", "Source identifier (defaults to current actor)")
	inboxPushCmd.Flags().String("ttl", "", "Time-to-live duration (e.g., 10m, 1h)")
	inboxPushCmd.Flags().String("dedup-key", "", "Deduplication key (auto-generated if not set)")
	inboxPushCmd.Flags().Int("priority", 0, "Priority (0=critical, 1=high, 2=normal, 3=low, 4=bulk)")
	inboxPushCmd.Flags().String("subject", "", "Short summary/title for the message")
	inboxPushCmd.Flags().String("team", "", "Send to all active agents in roster (instead of --to)")

	// List flags
	inboxListCmd.Flags().BoolP("all", "a", false, "Show all items (including delivered)")
	inboxListCmd.Flags().Bool("delivered", false, "Show only delivered items")

	// Drain flags
	inboxDrainCmd.Flags().Bool("reconcile", false, "Also check DB for missed items (use on SessionStart)")
	inboxDrainCmd.Flags().Bool("urgent", false, "Only drain urgent items (P0 critical + P1 high)")

	inboxCmd.AddCommand(inboxPushCmd)
	inboxCmd.AddCommand(inboxListCmd)
	inboxCmd.AddCommand(inboxDrainCmd)
	rootCmd.AddCommand(inboxCmd)
}

func runInboxPush(cmd *cobra.Command, args []string) {
	CheckReadonly("inbox push")
	requireDaemon("inbox push")

	to, _ := cmd.Flags().GetString("to")
	rig, _ := cmd.Flags().GetString("rig")
	msgType, _ := cmd.Flags().GetString("type")
	source, _ := cmd.Flags().GetString("source")
	ttl, _ := cmd.Flags().GetString("ttl")
	dedupKey, _ := cmd.Flags().GetString("dedup-key")
	priority, _ := cmd.Flags().GetInt("priority")
	subject, _ := cmd.Flags().GetString("subject")
	team, _ := cmd.Flags().GetString("team")

	if to == "" && team == "" {
		fmt.Fprintln(os.Stderr, "Error: --to or --team is required")
		os.Exit(1)
	}

	content := strings.Join(args, " ")
	if content == "" {
		fmt.Fprintln(os.Stderr, "Error: message content is required")
		os.Exit(1)
	}

	if source == "" {
		source = getActor()
	}

	pushArgs := &rpc.InboxPushArgs{
		AgentName: to,
		Rig:       rig,
		Type:      msgType,
		Source:    source,
		Content:   content,
		Subject:   subject,
		Priority:  priority,
		DedupKey:  dedupKey,
		TTL:       ttl,
		Team:      team,
	}

	resp, err := daemonClient.InboxPush(pushArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error pushing to inbox: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		if resp != nil && len(resp.Data) > 0 {
			var item types.InboxItem
			if err := json.Unmarshal(resp.Data, &item); err == nil {
				outputJSON(item)
				return
			}
		}
		outputJSON(map[string]bool{"success": true})
		return
	}

	if team != "" {
		fmt.Printf("Broadcast to team %s\n", team)
	} else {
		fmt.Printf("Pushed to %s's inbox\n", to)
	}
}

func runInboxList(cmd *cobra.Command, args []string) {
	requireDaemon("inbox list")

	showAll, _ := cmd.Flags().GetBool("all")

	agentName := getActor()

	// Always fetch all items from daemon; filter client-side so we can
	// include recently-delivered items in the default view. (bd-4luku)
	listArgs := &rpc.InboxListArgs{
		AgentName:        agentName,
		IncludeDelivered: true,
	}

	result, err := daemonClient.InboxList(listArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing inbox: %v\n", err)
		os.Exit(1)
	}

	// Client-side filter: without --all, show undelivered + delivered in last 60s.
	// The PostToolUse hook auto-drains inbox between tool calls, so messages get
	// marked delivered before agents can manually list them. (bd-4luku)
	if !showAll {
		recentCutoff := time.Now().Add(-60 * time.Second)
		var filtered []*types.InboxItem
		for _, item := range result.Items {
			if item.DeliveredAt == nil || item.DeliveredAt.After(recentCutoff) {
				filtered = append(filtered, item)
			}
		}
		result.Items = filtered
		result.Count = len(filtered)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	if result.Count == 0 {
		fmt.Println("Inbox is empty")
		return
	}

	fmt.Printf("Inbox (%d items):\n\n", result.Count)

	for i, item := range result.Items {
		status := "  "
		if item.DeliveredAt != nil {
			if time.Since(*item.DeliveredAt) < 60*time.Second {
				status = ui.RenderMuted("↳") // just delivered (bd-4luku)
			} else {
				status = ui.RenderMuted("✓")
			}
		}

		age := formatInboxAge(item.CreatedAt)
		typeStr := ui.RenderMuted("[" + item.Type + "]")
		sourceStr := ""
		if item.Source != "" {
			sourceStr = ui.RenderMuted(" from " + item.Source)
		}

		// Show subject as title line if present (bd-pwoii).
		if item.Subject != "" {
			fmt.Printf("%d. %s %s %s%s %s\n", i+1, status, typeStr, item.Subject, sourceStr, age)
			fmt.Printf("   %s\n", item.Content)
		} else {
			fmt.Printf("%d. %s %s %s%s %s\n", i+1, status, typeStr, item.Content, sourceStr, age)
		}
	}
}

func runInboxDrain(cmd *cobra.Command, args []string) {
	requireDaemon("inbox drain")

	agentName := getActor()

	urgent, _ := cmd.Flags().GetBool("urgent")

	drainArgs := &rpc.InboxDrainArgs{
		AgentName: agentName,
	}
	if urgent {
		drainArgs.MaxPriority = 1 // P0 (critical) + P1 (high)
	}

	result, err := daemonClient.InboxDrain(drainArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error draining inbox: %v\n", err)
		os.Exit(1)
	}

	if result.Count == 0 {
		// Nothing to drain — silent exit
		return
	}

	// Mark items as delivered
	ids := make([]string, len(result.Items))
	for i, item := range result.Items {
		ids[i] = item.ID
	}
	_, markErr := daemonClient.InboxMarkDelivered(&rpc.InboxMarkDeliveredArgs{IDs: ids, AgentName: getActor()})
	if markErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to mark items delivered: %v\n", markErr)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	// Format for injection into agent context
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📬 Inbox: %d new notification(s)\n\n", result.Count))

	for _, item := range result.Items {
		sourceStr := ""
		if item.Source != "" {
			sourceStr = fmt.Sprintf(" (from %s)", item.Source)
		}
		if item.Subject != "" {
			sb.WriteString(fmt.Sprintf("- [%s]%s: %s — %s\n", item.Type, sourceStr, item.Subject, item.Content))
		} else {
			sb.WriteString(fmt.Sprintf("- [%s]%s: %s\n", item.Type, sourceStr, item.Content))
		}
	}

	fmt.Print(sb.String())
}

// formatInboxAge returns a concise relative time string for inbox items.
func formatInboxAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return ui.RenderMuted("just now")
	case d < time.Hour:
		mins := int(d.Minutes())
		if mins == 1 {
			return ui.RenderMuted("1m ago")
		}
		return ui.RenderMuted(fmt.Sprintf("%dm ago", mins))
	case d < 24*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return ui.RenderMuted("1h ago")
		}
		return ui.RenderMuted(fmt.Sprintf("%dh ago", hours))
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return ui.RenderMuted("1d ago")
		}
		return ui.RenderMuted(fmt.Sprintf("%dd ago", days))
	}
}
