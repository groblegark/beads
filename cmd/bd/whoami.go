package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// whoamiCmd shows the resolved session identity
var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show resolved session identity",
	Long: `Shows the identity this session resolves to, including the source of the name.

The identity resolution chain (highest priority first):
  1. --actor flag
  2. BD_ACTOR env var
  3. BEADS_ACTOR env var
  4. GT_ROLE env var (gastown-managed agents)
  5. Daemon session registry (auto-generated agent name, e.g., "swift-fox")

Examples:
  bd whoami
  bd whoami --json`,
	Run: runWhoami,
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
}

type whoamiResult struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	SessionKey string `json:"session_key,omitempty"`
	BaseName   string `json:"base_name,omitempty"`
	Daemon     bool   `json:"daemon_connected"`
}

func runWhoami(cmd *cobra.Command, args []string) {
	result := whoamiResult{
		Name:   getActor(),
		Daemon: daemonClient != nil,
	}

	// Determine source
	if actor != "" && cmd.Flags().Changed("actor") {
		result.Source = "flag (--actor)"
	} else if os.Getenv("BD_ACTOR") != "" {
		result.Source = "env (BD_ACTOR)"
	} else if os.Getenv("BEADS_ACTOR") != "" {
		result.Source = "env (BEADS_ACTOR)"
	} else if os.Getenv("GT_ROLE") != "" {
		result.Source = "env (GT_ROLE)"
	} else if sessionAssignedName != "" {
		result.Source = "daemon session registry (auto-generated)"
		projectRoot := ""
		if dbPath != "" {
			projectRoot = filepath.Dir(filepath.Dir(dbPath))
		}
		sessionKey := deriveSessionKey(projectRoot)
		result.BaseName = getBaseActorName(sessionKey)
		result.SessionKey = sessionKey
	} else {
		// No daemon, no env — show what getActorWithGit resolved
		result.Source = "git config user.name / $USER"
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("Identity: %s\n", result.Name)
	fmt.Printf("Source:   %s\n", result.Source)
	if result.BaseName != "" {
		fmt.Printf("Base:     %s\n", result.BaseName)
	}
	if result.SessionKey != "" {
		fmt.Printf("Session:  %s\n", result.SessionKey)
	}
	if result.Daemon {
		fmt.Printf("Daemon:   connected\n")
	} else {
		fmt.Printf("Daemon:   not connected\n")
	}
}
