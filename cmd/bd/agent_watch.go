package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/tui/agent"
)

// agentWatchCmd launches the interactive agent watch TUI
var agentWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Interactive TUI for monitoring live agent activity",
	Long: `Launch an interactive terminal UI that monitors the live agent roster.

Shows agents with their current tool use, assigned issues, and epic groupings.
The display auto-refreshes every 3 seconds.

Views:
  Agents    Flat list of all active agents with full detail
  Epics     Agents grouped by their parent epic, expandable/collapsible

Key bindings:
  j/k        Navigate between agents/epics
  tab        Toggle between Agent and Epic views
  enter      Expand/collapse epic group (in Epic view)
  r          Refresh now
  ?          Toggle help
  q/Ctrl+C   Quit`,
	RunE: runAgentWatch,
}

var watchStaleThreshold int
var watchShowAll bool

func init() {
	agentWatchCmd.Flags().IntVar(&watchStaleThreshold, "stale", 3600, "Exclude actors idle longer than N seconds")
	agentWatchCmd.Flags().BoolVar(&watchShowAll, "all", false, "Show all actors (no stale filtering)")

	agentCmd.AddCommand(agentWatchCmd)
}

func runAgentWatch(cmd *cobra.Command, args []string) error {
	model := agent.New()
	model.SetStaleThreshold(watchStaleThreshold)
	model.SetShowAll(watchShowAll)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running agent watch: %v\n", err)
		return err
	}

	return nil
}
