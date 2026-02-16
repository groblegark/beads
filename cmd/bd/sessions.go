package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
)

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List registered agent sessions on the daemon",
	Long: `Show all sessions registered with the daemon's session registry.

Each connected CLI session (agent, human, or tool) registers with the daemon
and receives a unique assigned name. This command shows who is currently
connected.

Examples:
  bd sessions              # Active sessions (last 24h)
  bd sessions --all        # Include stale sessions
  bd sessions --json       # JSON output`,
	Run: runSessionsList,
}

func init() {
	sessionsCmd.Flags().Bool("all", false, "Include stale sessions (older than 24h)")
	rootCmd.AddCommand(sessionsCmd)
}

func runSessionsList(cmd *cobra.Command, args []string) {
	if daemonClient == nil {
		fmt.Fprintln(os.Stderr, "Error: not connected to daemon")
		os.Exit(1)
	}

	includeStale, _ := cmd.Flags().GetBool("all")

	result, err := daemonClient.SessionList(&rpc.SessionListArgs{
		IncludeStale: includeStale,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	if result.Count == 0 {
		fmt.Println("No active sessions.")
		return
	}

	// Sort by last seen (most recent first)
	sort.Slice(result.Sessions, func(i, j int) bool {
		return result.Sessions[i].LastSeen.After(result.Sessions[j].LastSeen)
	})

	// Mark current session
	currentName := sessionAssignedName

	fmt.Printf("%d active session(s):\n\n", result.Count)

	for _, s := range result.Sessions {
		ago := formatAge(s.LastSeen)
		marker := " "
		if s.AssignedName == currentName {
			marker = "*"
		}
		fmt.Printf("  %s %-20s  base=%-15s  last seen %s\n",
			marker, s.AssignedName, s.BaseName, ago)
	}

	if currentName != "" {
		fmt.Printf("\n  * = current session (%s)\n", currentName)
	}
}
