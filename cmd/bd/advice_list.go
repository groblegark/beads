package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// adviceListCmd lists advice beads
var adviceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List advice beads",
	Long: `List advice beads, optionally filtered by targeting scope.

Without flags, lists all advice beads. With targeting flags, shows advice
that would apply to the specified agent/role/rig.

When --agent is specified, advice is shown in priority order (most specific first):
  1. Agent-specific advice
  2. Role-specific advice (if agent has that role)
  3. Rig-specific advice (if agent is in that rig)
  4. Global advice

Examples:
  # List all advice
  bd advice list

  # List advice for a specific agent (shows applicable advice)
  bd advice list --agent=beads/polecats/obsidian

  # List advice for a role
  bd advice list --role=polecat

  # List advice for a rig
  bd advice list --rig=beads

  # Show global advice only
  bd advice list --global`,
	Run: runAdviceList,
}

func init() {
	adviceListCmd.Flags().String("rig", "", "Filter by target rig")
	adviceListCmd.Flags().String("role", "", "Filter by target role")
	adviceListCmd.Flags().String("agent", "", "Show advice applicable to this agent")
	adviceListCmd.Flags().Bool("global", false, "Show only global advice (no targeting)")
	adviceListCmd.Flags().BoolP("verbose", "v", false, "Show detailed output")

	adviceCmd.AddCommand(adviceListCmd)
}

func runAdviceList(cmd *cobra.Command, args []string) {
	// Ensure store is initialized
	if err := ensureStoreActive(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	rig, _ := cmd.Flags().GetString("rig")
	role, _ := cmd.Flags().GetString("role")
	agent, _ := cmd.Flags().GetString("agent")
	globalOnly, _ := cmd.Flags().GetBool("global")
	verbose, _ := cmd.Flags().GetBool("verbose")

	ctx := rootCtx

	// Query all open advice beads (closed advice is "removed")
	adviceType := types.TypeAdvice
	openStatus := types.StatusOpen
	filter := types.IssueFilter{
		IssueType: &adviceType,
		Status:    &openStatus,
	}
	issues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing advice: %v\n", err)
		os.Exit(1)
	}

	// Filter and categorize advice
	var filtered []*types.Issue
	if agent != "" {
		// Show advice applicable to this agent
		filtered = filterAdviceForAgent(issues, agent, rig, role)
	} else if globalOnly {
		// Show only global advice
		for _, issue := range issues {
			if issue.AdviceTargetRig == "" && issue.AdviceTargetRole == "" && issue.AdviceTargetAgent == "" {
				filtered = append(filtered, issue)
			}
		}
	} else {
		// Filter by specified criteria
		filtered = filterAdviceByCriteria(issues, rig, role)
	}

	// JSON output
	if jsonOutput {
		outputJSON(filtered)
		return
	}

	// Human-readable output
	if len(filtered) == 0 {
		if agent != "" {
			fmt.Printf("No advice found for agent %s\n", agent)
		} else if globalOnly {
			fmt.Println("No global advice found")
		} else {
			fmt.Println("No advice found")
		}
		return
	}

	// Group advice by scope for display
	printAdviceList(filtered, verbose, agent != "")
}

// filterAdviceForAgent returns advice applicable to an agent, sorted by specificity
func filterAdviceForAgent(issues []*types.Issue, agentID, agentRig, agentRole string) []*types.Issue {
	// Parse agent ID to extract rig and role if not provided
	// Agent ID format: rig/polecats/name or rig/crew/name
	if agentRig == "" || agentRole == "" {
		parts := strings.Split(agentID, "/")
		if len(parts) >= 2 {
			if agentRig == "" {
				agentRig = parts[0]
			}
			if agentRole == "" && len(parts) >= 2 {
				// Infer role from path: "polecats" -> "polecat", "crew" -> "crew"
				roleFolder := parts[1]
				switch roleFolder {
				case "polecats":
					agentRole = "polecat"
				case "crew":
					agentRole = "crew"
				case "witness":
					agentRole = "witness"
				case "refinery":
					agentRole = "refinery"
				default:
					agentRole = roleFolder
				}
			}
		}
	}

	type scoredAdvice struct {
		issue    *types.Issue
		priority int // Lower = more specific = higher priority
	}

	var scored []scoredAdvice
	for _, issue := range issues {
		// Check if advice applies to this agent
		matches, priority := adviceMatchesAgent(issue, agentID, agentRig, agentRole)
		if matches {
			scored = append(scored, scoredAdvice{issue: issue, priority: priority})
		}
	}

	// Sort by priority (most specific first), then by creation date
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].priority != scored[j].priority {
			return scored[i].priority < scored[j].priority
		}
		return scored[i].issue.CreatedAt.After(scored[j].issue.CreatedAt)
	})

	result := make([]*types.Issue, len(scored))
	for i, s := range scored {
		result[i] = s.issue
	}
	return result
}

// adviceMatchesAgent checks if advice applies to an agent and returns priority
// Priority: 1=agent-specific, 2=role-specific, 3=rig-specific, 4=global
func adviceMatchesAgent(advice *types.Issue, agentID, agentRig, agentRole string) (bool, int) {
	// Agent-specific (highest priority)
	if advice.AdviceTargetAgent != "" {
		if advice.AdviceTargetAgent == agentID {
			return true, 1
		}
		return false, 0
	}

	// Role-specific (with optional rig constraint)
	if advice.AdviceTargetRole != "" {
		if advice.AdviceTargetRole != agentRole {
			return false, 0
		}
		// Role matches, check rig constraint if present
		if advice.AdviceTargetRig != "" && advice.AdviceTargetRig != agentRig {
			return false, 0
		}
		return true, 2
	}

	// Rig-specific
	if advice.AdviceTargetRig != "" {
		if advice.AdviceTargetRig == agentRig {
			return true, 3
		}
		return false, 0
	}

	// Global advice (lowest priority)
	return true, 4
}

// filterAdviceByCriteria filters advice by rig/role criteria
func filterAdviceByCriteria(issues []*types.Issue, rig, role string) []*types.Issue {
	if rig == "" && role == "" {
		return issues
	}

	var filtered []*types.Issue
	for _, issue := range issues {
		match := true
		if rig != "" && issue.AdviceTargetRig != rig {
			match = false
		}
		if role != "" && issue.AdviceTargetRole != role {
			match = false
		}
		if match {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

// printAdviceList prints advice in a readable format
func printAdviceList(issues []*types.Issue, verbose, showPriority bool) {
	// Group by scope
	var global, rigLevel, roleLevel, agentLevel []*types.Issue
	for _, issue := range issues {
		switch {
		case issue.AdviceTargetAgent != "":
			agentLevel = append(agentLevel, issue)
		case issue.AdviceTargetRole != "":
			roleLevel = append(roleLevel, issue)
		case issue.AdviceTargetRig != "":
			rigLevel = append(rigLevel, issue)
		default:
			global = append(global, issue)
		}
	}

	printCount := 0

	if len(agentLevel) > 0 {
		fmt.Printf("%s Agent-specific advice:\n", ui.RenderAccent("👤"))
		printAdviceItems(agentLevel, verbose, &printCount)
		fmt.Println()
	}

	if len(roleLevel) > 0 {
		fmt.Printf("%s Role-specific advice:\n", ui.RenderAccent("🎭"))
		printAdviceItems(roleLevel, verbose, &printCount)
		fmt.Println()
	}

	if len(rigLevel) > 0 {
		fmt.Printf("%s Rig-specific advice:\n", ui.RenderAccent("⚙️"))
		printAdviceItems(rigLevel, verbose, &printCount)
		fmt.Println()
	}

	if len(global) > 0 {
		fmt.Printf("%s Global advice:\n", ui.RenderAccent("🌍"))
		printAdviceItems(global, verbose, &printCount)
		fmt.Println()
	}

	fmt.Printf("Total: %d advice items\n", len(issues))
}

// printAdviceItems prints a list of advice items
func printAdviceItems(issues []*types.Issue, verbose bool, count *int) {
	for _, issue := range issues {
		*count++

		// Build scope indicator
		var scope string
		switch {
		case issue.AdviceTargetAgent != "":
			scope = fmt.Sprintf("→ %s", issue.AdviceTargetAgent)
		case issue.AdviceTargetRole != "" && issue.AdviceTargetRig != "":
			scope = fmt.Sprintf("→ %s in %s", issue.AdviceTargetRole, issue.AdviceTargetRig)
		case issue.AdviceTargetRole != "":
			scope = fmt.Sprintf("→ %s", issue.AdviceTargetRole)
		case issue.AdviceTargetRig != "":
			scope = fmt.Sprintf("→ %s", issue.AdviceTargetRig)
		default:
			scope = "→ global"
		}

		fmt.Printf("  %s %s\n", ui.RenderID(issue.ID), scope)
		fmt.Printf("    %s\n", issue.Title)

		if verbose && issue.Description != "" {
			// Indent description lines
			lines := strings.Split(issue.Description, "\n")
			for _, line := range lines {
				fmt.Printf("    %s\n", line)
			}
		}
	}
}

// getAdviceForAgent is a helper to get applicable advice for an agent
// Used by bd prime and other commands
func getAdviceForAgent(ctx context.Context, s interface{ SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) }, agentID, rig, role string) ([]*types.Issue, error) {
	adviceType := types.TypeAdvice
	filter := types.IssueFilter{
		IssueType: &adviceType,
	}
	issues, err := s.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}

	return filterAdviceForAgent(issues, agentID, rig, role), nil
}
