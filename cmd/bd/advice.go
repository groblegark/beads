package main

import (
	"github.com/spf13/cobra"
)

// adviceCmd is the parent command for advice operations
var adviceCmd = &cobra.Command{
	Use:     "advice",
	GroupID: "issues",
	Short:   "Manage agent advice (guidance beads for agents)",
	Long: `Manage agent advice - guidance beads that provide context to agents.

Advice beads are issues with type=advice that can be targeted to specific:
  - Rigs: All agents in a rig (e.g., "beads", "gastown")
  - Roles: All agents of a role type (e.g., "polecat", "witness")
  - Agents: Specific agent instances (e.g., "beads/polecats/obsidian")

Targeting hierarchy (most specific wins):
  1. Agent-specific (--agent flag)
  2. Role-specific (--role flag)
  3. Rig-specific (--rig flag)
  4. Global (no targeting flags)

Commands:
  add       Create a new advice bead with targeting
  list      List advice beads by scope
  remove    Remove an advice bead

Examples:
  # Add global advice
  bd advice add "Always check your hook first"

  # Add advice for all polecats
  bd advice add --role=polecat "Run tests before committing"

  # Add advice for a specific rig
  bd advice add --rig=gastown "Use gt commands for workflow"

  # Add advice for a specific agent
  bd advice add --agent=beads/polecats/obsidian "Focus on CLI development"

  # List all advice
  bd advice list

  # List advice for a specific agent (shows applicable advice)
  bd advice list --agent=beads/polecats/obsidian

  # Remove advice
  bd advice remove <advice-id>`,
}

func init() {
	rootCmd.AddCommand(adviceCmd)
}
