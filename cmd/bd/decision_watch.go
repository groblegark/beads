package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/tui/decision"
	"github.com/steveyegge/beads/internal/types"
)

// decisionWatchCmd launches the interactive decision watch TUI
var decisionWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Interactive TUI for monitoring and responding to decisions",
	Long: `Launch an interactive terminal UI that monitors pending decisions,
lets you navigate between them, select options, add rationale, and
resolve or dismiss decisions — all with keyboard shortcuts.

Key bindings:
  j/k        Navigate between decisions
  1-4        Select option
  r          Add rationale before confirming
  Enter      Confirm selection
  d          Dismiss/cancel decision
  p          Peek at agent's terminal (coop)
  !          Filter to high urgency only
  a          Show all urgencies
  R          Refresh
  ?          Toggle help
  q/Ctrl+C   Quit

Headless mode (--headless):
  Non-interactive mode for AI agents. Outputs new decisions as JSON
  to stdout (one per line), reads responses from stdin as JSON.

  Output format (one per line):
    {"decision_id":"...","prompt":"...","context":"...","options":[...]}

  Input format (one per line):
    {"decision_id":"...","select":"option-id","text":"rationale"}

  The process blocks indefinitely, polling for new decisions.
  Use this for AI-supervised decision making ("captain mode").`,
	RunE: runDecisionWatch,
}

func init() {
	decisionWatchCmd.Flags().Bool("urgent-only", false, "Show only high urgency decisions")
	decisionWatchCmd.Flags().Bool("notify", false, "Enable desktop notifications")
	decisionWatchCmd.Flags().Bool("headless", false, "Non-interactive mode: JSON on stdout, responses on stdin")
	decisionWatchCmd.Flags().Duration("poll-interval", 5*time.Second, "Poll interval for headless mode")

	decisionCmd.AddCommand(decisionWatchCmd)
}

func runDecisionWatch(cmd *cobra.Command, args []string) error {
	headless, _ := cmd.Flags().GetBool("headless")
	if headless {
		return runDecisionWatchHeadless(cmd)
	}

	model := decision.New()

	urgentOnly, _ := cmd.Flags().GetBool("urgent-only")
	if urgentOnly {
		model.SetFilter("high")
	}

	notify, _ := cmd.Flags().GetBool("notify")
	if notify {
		model.SetNotify(true)
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running decision watch: %v\n", err)
		return err
	}

	return nil
}

// headlessDecision is the JSON output format for headless mode.
type headlessDecision struct {
	DecisionID  string                 `json:"decision_id"`
	Prompt      string                 `json:"prompt"`
	Context     string                 `json:"context,omitempty"`
	Options     []types.DecisionOption `json:"options"`
	CreatedAt   string                 `json:"created_at"`
	RequestedBy string                 `json:"requested_by,omitempty"`
	Urgency     string                 `json:"urgency,omitempty"`
}

// headlessResponse is the JSON input format for headless mode.
type headlessResponse struct {
	DecisionID string `json:"decision_id"`
	Select     string `json:"select"`
	Text       string `json:"text,omitempty"`
}

func runDecisionWatchHeadless(cmd *cobra.Command) error {
	requireDaemon("decision watch --headless")

	pollInterval, _ := cmd.Flags().GetDuration("poll-interval")

	// Track which decisions we've already emitted to avoid duplicates.
	emitted := make(map[string]bool)

	// Read responses from stdin in a goroutine.
	responses := make(chan headlessResponse, 16)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			var resp headlessResponse
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				fmt.Fprintf(os.Stderr, "headless: invalid input: %v\n", err)
				continue
			}
			if resp.DecisionID == "" || resp.Select == "" {
				fmt.Fprintf(os.Stderr, "headless: decision_id and select are required\n")
				continue
			}
			responses <- resp
		}
	}()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Initial poll immediately.
	if err := headlessPoll(daemonClient, emitted); err != nil {
		fmt.Fprintf(os.Stderr, "headless: poll error: %v\n", err)
	}

	for {
		select {
		case <-ticker.C:
			if err := headlessPoll(daemonClient, emitted); err != nil {
				fmt.Fprintf(os.Stderr, "headless: poll error: %v\n", err)
			}

		case resp := <-responses:
			resolveArgs := &rpc.DecisionResolveArgs{
				IssueID:        resp.DecisionID,
				SelectedOption: resp.Select,
				ResponseText:   resp.Text,
				RespondedBy:    "headless-watch",
			}
			result, err := daemonClient.DecisionResolve(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "headless: resolve error for %s: %v\n", resp.DecisionID, err)
				continue
			}

			// Emit confirmation.
			confirmation := map[string]interface{}{
				"type":        "resolved",
				"decision_id": resp.DecisionID,
				"selected":    resp.Select,
			}
			if result.Decision != nil {
				confirmation["prompt"] = result.Decision.Prompt
			}
			data, _ := json.Marshal(confirmation)
			fmt.Println(string(data))

			// Remove from emitted so if it reappears (iteration), we'll see it.
			delete(emitted, resp.DecisionID)
		}
	}
}

// headlessPoll fetches pending decisions and emits any new ones as JSON to stdout.
func headlessPoll(client *rpc.Client, emitted map[string]bool) error {
	listArgs := &rpc.DecisionListArgs{All: false}
	result, err := client.DecisionList(listArgs)
	if err != nil {
		return err
	}

	for _, dr := range result.Decisions {
		if dr.Decision == nil {
			continue
		}
		id := dr.Decision.IssueID
		if emitted[id] {
			continue
		}
		emitted[id] = true

		var options []types.DecisionOption
		if dr.Decision.Options != "" {
			_ = json.Unmarshal([]byte(dr.Decision.Options), &options)
		}

		out := headlessDecision{
			DecisionID:  id,
			Prompt:      dr.Decision.Prompt,
			Context:     dr.Decision.Context,
			Options:     options,
			CreatedAt:   dr.Decision.CreatedAt.Format(time.RFC3339),
			RequestedBy: dr.Decision.RequestedBy,
			Urgency:     dr.Decision.Urgency,
		}

		data, _ := json.Marshal(out)
		fmt.Println(string(data))
	}

	// Clean up emitted map: remove IDs that are no longer pending.
	pendingIDs := make(map[string]bool)
	for _, dr := range result.Decisions {
		if dr.Decision != nil {
			pendingIDs[dr.Decision.IssueID] = true
		}
	}
	for id := range emitted {
		if !pendingIDs[id] {
			delete(emitted, id)
		}
	}

	return nil
}
