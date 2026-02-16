package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

// decisionCheckCmd checks for decision responses (for hooks)
var decisionCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Check for decision responses (for Claude Code hooks)",
	Long: `Check for recently responded decisions.

This command is designed for Claude Code hooks to notify about decision responses.

Exit codes (normal mode):
  0 - One or more decisions have been responded to
  1 - No responded decisions found

Exit codes (--inject mode):
  0 - Always (hooks should never block)
  Output: <system-reminder> if responses exist, silent otherwise

The --inject mode outputs responses wrapped in <system-reminder> tags
that Claude Code recognizes and injects into the conversation context.

Examples:
  # Check for responses (for scripting)
  bd decision check && echo "Responses available"

  # For Claude Code hooks (SessionStart)
  bd decision check --inject

  # Check for specific decision
  bd decision check --id gt-abc.decision-1`,
	Run: runDecisionCheck,
}

var (
	checkInject      bool
	checkID          string
	checkSince       string
	checkRequestedBy string
)

func init() {
	decisionCheckCmd.Flags().BoolVar(&checkInject, "inject", false, "Output format for Claude Code hooks")
	decisionCheckCmd.Flags().StringVar(&checkID, "id", "", "Check specific decision ID")
	decisionCheckCmd.Flags().StringVar(&checkSince, "since", "5m", "Time window for recently responded decisions (e.g., 5m, 1h)")
	decisionCheckCmd.Flags().StringVar(&checkRequestedBy, "requested-by", "", "Filter by requesting agent")

	decisionCmd.AddCommand(decisionCheckCmd)
}

// DecisionCheckResult for JSON output
type DecisionCheckResult struct {
	HasResponses bool                  `json:"has_responses"`
	Responses    []DecisionResponseSum `json:"responses,omitempty"`
}

type DecisionResponseSum struct {
	ID          string `json:"id"`
	Prompt      string `json:"prompt"`
	Selected    string `json:"selected,omitempty"`
	SelectedLbl string `json:"selected_label,omitempty"`
	Text        string `json:"text,omitempty"`
	RespondedBy string `json:"responded_by,omitempty"`
}

func runDecisionCheck(cmd *cobra.Command, args []string) {
	// Phase 5: --inject mode is a no-op. Decision responses are delivered via inbox.
	if checkInject {
		fmt.Fprintf(os.Stderr, "Warning: 'bd decision check --inject' is deprecated and is now a no-op; use 'bd inbox drain' instead\n")
		os.Exit(0)
	}

	requireDaemon("decision check")

	var responses []DecisionResponseSum

	if checkID != "" {
		// Check specific decision via daemon RPC
		getArgs := &rpc.DecisionGetArgs{IssueID: checkID}
		result, err := daemonClient.DecisionGet(getArgs)
		if err != nil || result == nil || result.Decision == nil {
			os.Exit(1)
		}

		dp := result.Decision
		if dp.RespondedAt != nil {
			// Parse options to get label
			var options []types.DecisionOption
			if dp.Options != "" {
				_ = json.Unmarshal([]byte(dp.Options), &options)
			}

			selectedLabel := dp.SelectedOption
			for _, opt := range options {
				if opt.ID == dp.SelectedOption {
					selectedLabel = opt.Label
					break
				}
			}

			responses = append(responses, DecisionResponseSum{
				ID:          checkID,
				Prompt:      dp.Prompt,
				Selected:    dp.SelectedOption,
				SelectedLbl: selectedLabel,
				Text:        dp.ResponseText,
				RespondedBy: dp.RespondedBy,
			})
		}
	} else {
		// Parse the --since duration
		sinceDuration, err := time.ParseDuration(checkSince)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing --since duration: %v\n", err)
			os.Exit(1)
		}
		sinceTime := time.Now().Add(-sinceDuration)

		// Get recently responded decisions via daemon RPC
		listArgs := &rpc.DecisionListRecentArgs{
			Since:       sinceTime.Format(time.RFC3339),
			RequestedBy: checkRequestedBy,
		}
		listResp, err := daemonClient.DecisionListRecent(listArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing decisions: %v\n", err)
			os.Exit(1)
		}

		for _, dr := range listResp.Decisions {
			dp := dr.Decision
			// Parse options for label
			var options []types.DecisionOption
			if dp.Options != "" {
				_ = json.Unmarshal([]byte(dp.Options), &options)
			}

			selectedLabel := dp.SelectedOption
			for _, opt := range options {
				if opt.ID == dp.SelectedOption {
					selectedLabel = opt.Label
					break
				}
			}

			responses = append(responses, DecisionResponseSum{
				ID:          dp.IssueID,
				Prompt:      dp.Prompt,
				Selected:    dp.SelectedOption,
				SelectedLbl: selectedLabel,
				Text:        dp.ResponseText,
				RespondedBy: dp.RespondedBy,
			})
		}
	}

	// JSON output
	if jsonOutput {
		result := DecisionCheckResult{
			HasResponses: len(responses) > 0,
			Responses:    responses,
		}
		outputJSON(result)
		if len(responses) > 0 {
			os.Exit(0)
		}
		os.Exit(1)
	}

	// Normal output
	if len(responses) == 0 {
		fmt.Println("No decision responses found")
		os.Exit(1)
	}

	fmt.Printf("Found %d decision response(s):\n\n", len(responses))
	for _, r := range responses {
		fmt.Printf("  %s\n", r.ID)
		fmt.Printf("    Prompt: %s\n", r.Prompt)
		if r.Selected != "" {
			fmt.Printf("    Selected: %s (%s)\n", r.Selected, r.SelectedLbl)
		}
		if r.Text != "" {
			fmt.Printf("    Text: %s\n", r.Text)
		}
		fmt.Println()
	}
	os.Exit(0)
}
