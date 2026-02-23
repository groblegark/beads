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

// jackOnCmd creates and activates a jack bead.
var jackOnCmd = &cobra.Command{
	Use:   "on [target]",
	Short: "Raise a jack — declare intent and begin infrastructure modification",
	Long: `Create and activate a jack bead for a temporary infrastructure modification.

The jack is created with status=in_progress (active). Every jack requires:
  --target (or positional arg): What is being modified
  --reason: Why this modification is needed
  --revert-plan: How to undo the changes (always required)

Optional flags:
  --ttl: Time-to-live before expiry alert (default 1h)
  --blocks: Bead ID this jack blocks (creates dependency)
  --labels: Additional labels (jack:* label auto-added if none specified)
  --priority: Priority 0-4 (default 2)

Examples:
  # Debug logging on a pod
  bd jack on pod/bd-daemon-abc \
    --reason="Adding NATS debug logging for timeout investigation" \
    --revert-plan="Restore config.yaml log_level to info, HUP pid 1" \
    --ttl=1h

  # Testing a fix, blocking current work
  bd jack on --target="deployment/bd-daemon" \
    --reason="Testing NATS reconnect fix for bd-abc123" \
    --revert-plan="kubectl rollout undo deployment/bd-daemon" \
    --blocks=bd-abc123 --ttl=30m

  # Emergency failover with labels
  bd jack on statefulset/dolt \
    --reason="Primary lost, manual failover" \
    --revert-plan="Remove manual primary label, let cluster auto-elect" \
    --priority=0 --labels=jack:failover`,
	Args: cobra.MaximumNArgs(1),
	Run:  runJackOn,
}

func init() {
	jackOnCmd.Flags().StringP("target", "t", "", "Target resource (e.g., pod/name, deployment/name)")
	jackOnCmd.Flags().StringP("reason", "r", "", "Why this modification is needed (required)")
	jackOnCmd.Flags().String("revert-plan", "", "How to undo the changes (required)")
	jackOnCmd.Flags().Duration("ttl", time.Hour, "Time-to-live before expiry alert (default 1h)")
	jackOnCmd.Flags().String("blocks", "", "Bead ID this jack blocks (creates dependency)")
	jackOnCmd.Flags().StringSlice("labels", nil, "Additional labels (jack:* auto-added if none)")
	jackOnCmd.Flags().IntP("priority", "p", 2, "Priority 0-4 (default 2)")

	jackCmd.AddCommand(jackOnCmd)
}

func runJackOn(cmd *cobra.Command, args []string) {
	CheckReadonly("jack on")
	requireDaemon("jack on")

	target, _ := cmd.Flags().GetString("target")
	reason, _ := cmd.Flags().GetString("reason")
	revertPlan, _ := cmd.Flags().GetString("revert-plan")
	ttl, _ := cmd.Flags().GetDuration("ttl")
	blocks, _ := cmd.Flags().GetString("blocks")
	labels, _ := cmd.Flags().GetStringSlice("labels")
	priority, _ := cmd.Flags().GetInt("priority")

	// Positional target overrides --target flag
	if len(args) > 0 {
		if target != "" {
			fmt.Fprintf(os.Stderr, "Error: cannot specify both positional target and --target flag\n")
			os.Exit(1)
		}
		target = args[0]
	}

	// Validate required fields
	if target == "" {
		fmt.Fprintf(os.Stderr, "Error: --target is required (or provide target as positional argument)\n")
		fmt.Fprintf(os.Stderr, "Example: bd jack on pod/my-app --reason=\"...\" --revert-plan=\"...\"\n")
		os.Exit(1)
	}
	if reason == "" {
		fmt.Fprintf(os.Stderr, "Error: --reason is required\n")
		os.Exit(1)
	}
	if revertPlan == "" {
		fmt.Fprintf(os.Stderr, "Error: --revert-plan is required (how to undo the changes)\n")
		os.Exit(1)
	}

	// Validate priority
	if priority < 0 || priority > 4 {
		fmt.Fprintf(os.Stderr, "Error: priority must be 0-4, got %d\n", priority)
		os.Exit(1)
	}

	// Ensure at least one jack:* label
	hasJackLabel := false
	for _, l := range labels {
		if strings.HasPrefix(l, types.JackLabelPrefix) {
			hasJackLabel = true
			break
		}
	}
	if !hasJackLabel {
		labels = append(labels, "jack:general")
	}

	// Compute expiry time
	now := time.Now()
	expiresAt := now.Add(ttl)

	// Build metadata JSON
	metadata := map[string]interface{}{
		"jack_target":      target,
		"jack_ttl":         ttl.String(),
		"jack_expires_at":  expiresAt.Format(time.RFC3339),
		"jack_revert_plan": revertPlan,
		"jack_reverted":    false,
		"jack_changes":     []interface{}{},
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to marshal metadata: %v\n", err)
		os.Exit(1)
	}

	// Build title from target and reason
	title := fmt.Sprintf("Jack: %s — %s", target, reason)
	if len(title) > 200 {
		title = title[:197] + "..."
	}

	// Step 1: Create the jack bead
	createArgs := &rpc.CreateArgs{
		Title:       title,
		Description: reason,
		IssueType:   string(types.TypeJack),
		Priority:    priority,
		Labels:      labels,
		Metadata:    json.RawMessage(metadataJSON),
	}

	// Set creator but not assignee — Claim step handles assignment
	if actor != "" {
		createArgs.CreatedBy = actor
	}

	resp, err := daemonClient.Create(createArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating jack: %v\n", err)
		os.Exit(1)
	}

	// Parse response to get the created issue
	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing create response: %v\n", err)
		os.Exit(1)
	}
	jackID := issue.ID

	// Step 2: Set status to in_progress (claim the jack)
	// Use ClaimForce since jacks are orthogonal to normal task claiming
	statusStr := "in_progress"
	updateArgs := &rpc.UpdateArgs{
		ID:         jackID,
		Status:     &statusStr,
		Claim:      true,
		ClaimForce: true,
	}
	_, err = daemonClient.Update(updateArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: jack created (%s) but failed to activate: %v\n", jackID, err)
		fmt.Fprintf(os.Stderr, "Run: bd update %s --status=in_progress\n", jackID)
	}

	// Step 3: Add dependency if --blocks specified
	if blocks != "" {
		depArgs := &rpc.DepAddArgs{
			FromID:  blocks,
			ToID:    jackID,
			DepType: "blocks",
		}
		_, depErr := daemonClient.Execute(rpc.OpDepAdd, depArgs)
		if depErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: jack created but failed to add dependency: %v\n", depErr)
			fmt.Fprintf(os.Stderr, "Run: bd dep add %s %s\n", blocks, jackID)
		}
	}

	// Output
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"id":         jackID,
			"target":     target,
			"ttl":        ttl.String(),
			"expires_at": expiresAt.Format(time.RFC3339),
			"status":     "in_progress",
			"labels":     labels,
		})
		return
	}

	fmt.Printf("%s Raised jack: %s\n\n", ui.RenderPass("✓"), ui.RenderID(jackID))
	fmt.Printf("  Target:      %s\n", target)
	fmt.Printf("  Reason:      %s\n", reason)
	fmt.Printf("  Revert plan: %s\n", revertPlan)
	fmt.Printf("  TTL:         %s (expires %s)\n", ttl, expiresAt.Format("2006-01-02 15:04 MST"))
	fmt.Printf("  Priority:    P%d\n", priority)
	fmt.Printf("  Labels:      %s\n", strings.Join(labels, ", "))
	if blocks != "" {
		fmt.Printf("  Blocks:      %s\n", blocks)
	}
	fmt.Println()
	fmt.Printf("  Record changes:  bd jack log %s --action=edit --target=...\n", jackID)
	fmt.Printf("  Lower the jack:  bd jack off %s --reason=\"...\"\n", jackID)
}
