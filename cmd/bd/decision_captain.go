package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

// decisionCaptainCmd provides a non-interactive captain mode for AI agents.
// Unlike --headless (which uses stdin/stdout JSON piping), captain mode uses
// standard CLI subcommands that an agent can call directly:
//
//	bd decision captain list     — list pending decisions as JSON
//	bd decision captain respond  — respond to a specific decision
//	bd decision captain poll     — one-shot poll that exits after listing
var decisionCaptainCmd = &cobra.Command{
	Use:   "captain",
	Short: "Non-interactive captain mode for AI agents",
	Long: `Captain mode lets an AI agent supervise other agents' decisions
without a TUI or stdin/stdout piping.

Subcommands:
  list      List all pending decisions as JSON (one per line)
  respond   Respond to a decision (select option + optional rationale)
  poll      Poll once for new decisions, output as JSON, exit
  sweep     Auto-resolve stale decisions from dead sessions

Typical agent workflow:
  1. Poll for pending decisions:
     bd decision captain list --json

  2. Review each decision's prompt, context, and options

  3. Respond to each decision:
     bd decision captain respond <id> --select=<option-id> --text="rationale"

  4. Repeat on a schedule (e.g., every 30s)

This is the recommended mode for AI agents acting as captains.
Use 'bd decision watch --headless' for a persistent stdin/stdout pipe instead.`,
}

// captainListCmd lists pending decisions as JSON for the captain.
var captainListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pending decisions as JSON",
	Long: `List all pending decisions in JSON format, one per line.

Each decision includes:
  - decision_id: Unique identifier
  - prompt: The question being asked
  - context: Background information (may be JSON or plain text)
  - options: Array of {id, short, label, description}
  - created_at: When the decision was created
  - requested_by: Who/what created the decision
  - urgency: high, medium, or low

Exit codes:
  0 - Decisions listed (may be empty)
  1 - Error`,
	RunE: runCaptainList,
}

// captainRespondCmd responds to a decision as the captain.
var captainRespondCmd = &cobra.Command{
	Use:   "respond <decision-id>",
	Short: "Respond to a decision as captain",
	Long: `Select an option and optionally provide rationale for a pending decision.

Examples:
  bd decision captain respond hq-abc123 --select=yes
  bd decision captain respond hq-abc123 --select=a --text="Because X"
  bd decision captain respond hq-abc123 --select=stop --text="Good work, take a break"`,
	Args: cobra.ExactArgs(1),
	RunE: runCaptainRespond,
}

// captainPollCmd is an alias for list that emphasizes the polling use case.
var captainPollCmd = &cobra.Command{
	Use:   "poll",
	Short: "One-shot poll for pending decisions",
	Long: `Poll once for pending decisions and output as JSON.
Same as 'captain list' but named for clarity in polling scripts.

Exit codes:
  0 - Success (output may be empty array if no decisions)
  1 - Error`,
	RunE: runCaptainList,
}

// captainSweepCmd auto-resolves stale decisions from dead sessions.
var captainSweepCmd = &cobra.Command{
	Use:   "sweep",
	Short: "Auto-resolve stale decisions from dead sessions",
	Long: `Sweep finds pending decisions older than a threshold and resolves them
with "stop" (or the first available stop-like option). This prevents stale
decisions from dead sessions from bypassing the stop-check guard for other sessions.

The captain should run sweep periodically to keep the decision queue clean.

Examples:
  bd decision captain sweep                    # sweep decisions older than 30m
  bd decision captain sweep --age 1h           # sweep decisions older than 1 hour
  bd decision captain sweep --dry-run          # show what would be swept`,
	RunE: runCaptainSweep,
}

func init() {
	captainRespondCmd.Flags().StringP("select", "s", "", "Option ID to select (required)")
	captainRespondCmd.Flags().StringP("text", "t", "", "Rationale or additional guidance")
	captainRespondCmd.Flags().String("by", "captain", "Captain identity for audit trail")
	_ = captainRespondCmd.MarkFlagRequired("select")

	captainSweepCmd.Flags().Duration("age", 30*time.Minute, "Sweep decisions older than this")
	captainSweepCmd.Flags().Bool("dry-run", false, "Show what would be swept without resolving")

	decisionCaptainCmd.AddCommand(captainListCmd)
	decisionCaptainCmd.AddCommand(captainRespondCmd)
	decisionCaptainCmd.AddCommand(captainPollCmd)
	decisionCaptainCmd.AddCommand(captainSweepCmd)

	decisionCmd.AddCommand(decisionCaptainCmd)
}

// captainDecision is the JSON output format for captain mode.
type captainDecision struct {
	DecisionID  string                 `json:"decision_id"`
	Prompt      string                 `json:"prompt"`
	Context     string                 `json:"context,omitempty"`
	Options     []types.DecisionOption `json:"options"`
	CreatedAt   string                 `json:"created_at"`
	RequestedBy string                 `json:"requested_by,omitempty"`
	Urgency     string                 `json:"urgency,omitempty"`
	Age         string                 `json:"age,omitempty"`
}

func runCaptainList(cmd *cobra.Command, args []string) error {
	requireDaemon("decision captain list")

	listArgs := &rpc.DecisionListArgs{All: false}
	result, err := daemonClient.DecisionList(listArgs)
	if err != nil {
		return fmt.Errorf("listing decisions: %w", err)
	}

	var decisions []captainDecision
	for _, dr := range result.Decisions {
		if dr.Decision == nil {
			continue
		}

		var options []types.DecisionOption
		if dr.Decision.Options != "" {
			_ = json.Unmarshal([]byte(dr.Decision.Options), &options)
		}

		age := time.Since(dr.Decision.CreatedAt).Truncate(time.Second).String()

		decisions = append(decisions, captainDecision{
			DecisionID:  dr.Decision.IssueID,
			Prompt:      dr.Decision.Prompt,
			Context:     dr.Decision.Context,
			Options:     options,
			CreatedAt:   dr.Decision.CreatedAt.Format(time.RFC3339),
			RequestedBy: dr.Decision.RequestedBy,
			Urgency:     dr.Decision.Urgency,
			Age:         age,
		})
	}

	if jsonOutput {
		outputJSON(decisions)
	} else {
		if len(decisions) == 0 {
			fmt.Println("No pending decisions.")
			return nil
		}
		fmt.Printf("Pending decisions (%d):\n\n", len(decisions))
		for _, d := range decisions {
			fmt.Printf("  %s [%s] (%s ago)\n", d.DecisionID, d.Urgency, d.Age)
			fmt.Printf("    %s\n", d.Prompt)
			if d.Context != "" {
				// Truncate context for display
				ctx := d.Context
				if len(ctx) > 120 {
					ctx = ctx[:117] + "..."
				}
				fmt.Printf("    Context: %s\n", ctx)
			}
			fmt.Printf("    Options:")
			for _, opt := range d.Options {
				fmt.Printf(" [%s]%s", opt.ID, opt.Label)
			}
			fmt.Println()
			fmt.Println()
		}
	}

	return nil
}

func runCaptainSweep(cmd *cobra.Command, args []string) error {
	requireDaemon("decision captain sweep")

	maxAge, _ := cmd.Flags().GetDuration("age")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	listArgs := &rpc.DecisionListArgs{All: false}
	result, err := daemonClient.DecisionList(listArgs)
	if err != nil {
		return fmt.Errorf("listing decisions: %w", err)
	}

	now := time.Now()
	var swept int
	for _, dr := range result.Decisions {
		if dr.Decision == nil {
			continue
		}
		age := now.Sub(dr.Decision.CreatedAt)
		if age < maxAge {
			continue
		}

		// Find a "stop" option, or fall back to the last option
		var options []types.DecisionOption
		if dr.Decision.Options != "" {
			_ = json.Unmarshal([]byte(dr.Decision.Options), &options)
		}
		selectID := "stop"
		found := false
		for _, opt := range options {
			if opt.ID == "stop" {
				found = true
				break
			}
		}
		if !found && len(options) > 0 {
			// Use last option as fallback (conventionally the "stop" equivalent)
			selectID = options[len(options)-1].ID
		}

		if dryRun {
			fmt.Printf("  [dry-run] Would sweep %s (age: %s, select: %s)\n",
				dr.Decision.IssueID, age.Truncate(time.Second), selectID)
			swept++
			continue
		}

		resolveArgs := &rpc.DecisionResolveArgs{
			IssueID:        dr.Decision.IssueID,
			SelectedOption: selectID,
			ResponseText:   fmt.Sprintf("Auto-swept by captain (stale: %s old)", age.Truncate(time.Second)),
			RespondedBy:    "captain-sweep",
		}
		if _, err := daemonClient.DecisionResolve(resolveArgs); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  Error sweeping %s: %v\n", dr.Decision.IssueID, err)
			continue
		}
		fmt.Printf("  Swept %s (age: %s, selected: %s)\n",
			dr.Decision.IssueID, age.Truncate(time.Second), selectID)
		swept++
	}

	if swept == 0 {
		fmt.Println("No stale decisions to sweep.")
	} else if dryRun {
		fmt.Printf("\n%d decision(s) would be swept. Run without --dry-run to execute.\n", swept)
	} else {
		fmt.Printf("\nSwept %d stale decision(s).\n", swept)
	}

	return nil
}

func runCaptainRespond(cmd *cobra.Command, args []string) error {
	requireDaemon("decision captain respond")

	decisionID := args[0]
	selectOpt, _ := cmd.Flags().GetString("select")
	text, _ := cmd.Flags().GetString("text")
	by, _ := cmd.Flags().GetString("by")

	resolveArgs := &rpc.DecisionResolveArgs{
		IssueID:        decisionID,
		SelectedOption: selectOpt,
		ResponseText:   text,
		RespondedBy:    by,
	}

	result, err := daemonClient.DecisionResolve(resolveArgs)
	if err != nil {
		return fmt.Errorf("resolving decision %s: %w", decisionID, err)
	}

	if jsonOutput {
		resp := map[string]interface{}{
			"decision_id": decisionID,
			"selected":    selectOpt,
			"text":        text,
			"by":          by,
			"status":      "resolved",
		}
		if result.Decision != nil {
			resp["prompt"] = result.Decision.Prompt
		}
		outputJSON(resp)
	} else {
		fmt.Printf("Resolved %s: selected [%s]", decisionID, selectOpt)
		if text != "" {
			fmt.Printf(" — %s", text)
		}
		fmt.Println()
	}

	return nil
}
