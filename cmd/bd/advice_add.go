package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// adviceAddCmd creates a new advice bead
var adviceAddCmd = &cobra.Command{
	Use:   "add <advice-text>",
	Short: "Create a new advice bead",
	Long: `Create an advice bead with optional targeting.

Advice beads provide guidance to agents. They can be targeted to:
  - All agents (no flags)
  - Agents in a specific rig (--rig)
  - Agents with a specific role (--role)
  - A specific agent (--agent)

The advice text becomes the title of the advice bead.
Use --description for longer explanations.

Examples:
  # Global advice (applies to all agents)
  bd advice add "Always check your hook first"

  # Advice for all polecats
  bd advice add --role=polecat "Run tests before committing"

  # Advice for the beads rig
  bd advice add --rig=beads "Use bd commands for issue tracking"

  # Advice for a specific agent
  bd advice add --agent=beads/polecats/obsidian "Focus on CLI development"

  # Advice with description
  bd advice add "Use parallel subagents" --description="When exploring code, spawn multiple agents to search in parallel"`,
	Args: cobra.ExactArgs(1),
	Run:  runAdviceAdd,
}

func init() {
	adviceAddCmd.Flags().String("rig", "", "Target rig (e.g., 'beads', 'gastown')")
	adviceAddCmd.Flags().String("role", "", "Target role type (e.g., 'polecat', 'witness', 'crew')")
	adviceAddCmd.Flags().String("agent", "", "Target agent ID (e.g., 'beads/polecats/obsidian')")
	adviceAddCmd.Flags().StringP("description", "d", "", "Detailed description of the advice")
	adviceAddCmd.Flags().IntP("priority", "p", 2, "Priority (0-4, default 2)")

	adviceCmd.AddCommand(adviceAddCmd)
}

func runAdviceAdd(cmd *cobra.Command, args []string) {
	CheckReadonly("advice add")

	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	adviceText := args[0]
	rig, _ := cmd.Flags().GetString("rig")
	role, _ := cmd.Flags().GetString("role")
	agent, _ := cmd.Flags().GetString("agent")
	description, _ := cmd.Flags().GetString("description")
	priority, _ := cmd.Flags().GetInt("priority")

	// Validate priority
	if priority < 0 || priority > 4 {
		fmt.Fprintf(os.Stderr, "Error: priority must be between 0 and 4\n")
		os.Exit(1)
	}

	// Build scope description for output
	var scopeDesc string
	switch {
	case agent != "":
		scopeDesc = fmt.Sprintf("agent %s", agent)
	case role != "" && rig != "":
		scopeDesc = fmt.Sprintf("%s in %s", role, rig)
	case role != "":
		scopeDesc = fmt.Sprintf("all %ss", role)
	case rig != "":
		scopeDesc = fmt.Sprintf("rig %s", rig)
	default:
		scopeDesc = "global"
	}

	ctx := rootCtx
	now := time.Now()

	// Create the advice issue
	issue := &types.Issue{
		Title:             truncateTitle(adviceText, 200),
		Description:       description,
		IssueType:         types.TypeAdvice,
		Status:            types.StatusOpen,
		Priority:          priority,
		AdviceTargetRig:   rig,
		AdviceTargetRole:  role,
		AdviceTargetAgent: agent,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	var adviceID string
	err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, issue, actor); err != nil {
			return fmt.Errorf("creating advice: %w", err)
		}
		adviceID = issue.ID
		return nil
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	markDirtyAndScheduleFlush()

	// Output
	if jsonOutput {
		result := map[string]interface{}{
			"id":           adviceID,
			"title":        adviceText,
			"scope":        scopeDesc,
			"target_rig":   rig,
			"target_role":  role,
			"target_agent": agent,
		}
		outputJSON(result)
		return
	}

	// Human-readable output
	fmt.Printf("%s Created advice: %s\n", ui.RenderPass("✓"), ui.RenderID(adviceID))
	fmt.Printf("   %s\n", adviceText)
	fmt.Printf("   Scope: %s\n", scopeDesc)
}
