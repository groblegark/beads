package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// decisionRespondCmd records a human response to a decision point
var decisionRespondCmd = &cobra.Command{
	Use:   "respond <decision-id>",
	Short: "Record a human response to a decision point",
	Long: `Record a response to a pending decision point gate.

The response can be:
  1. Select an option: --select=<option-id>
  2. Provide text guidance: --text="..."
  3. Both: select an option AND provide additional text
  4. Accept guidance as-is: --accept-guidance (skips iteration, uses text directly)

When an option is selected, the decision gate closes and any blocked issues are unblocked.
When only text is provided (no selection), iterative refinement is triggered - a new
decision point is created with your guidance, allowing the agent to refine options.

Examples:
  # Select an option
  bd decision respond gt-abc.decision-1 --select=a

  # Provide text guidance (may trigger iteration)
  bd decision respond gt-abc.decision-1 --text="I'd prefer a hybrid approach"

  # Select with additional context
  bd decision respond gt-abc.decision-1 --select=b --text="But make sure to handle edge case X"

  # Accept text guidance directly without iteration
  bd decision respond gt-abc.decision-1 --text="Just do X" --accept-guidance

  # Specify who responded (for audit)
  bd decision respond gt-abc.decision-1 --select=yes --by=user@example.com`,
	Args: cobra.ExactArgs(1),
	Run:  runDecisionRespond,
}

func init() {
	decisionRespondCmd.Flags().StringP("select", "s", "", "Option ID to select")
	decisionRespondCmd.Flags().StringP("text", "t", "", "Custom text response/guidance")
	decisionRespondCmd.Flags().String("by", "", "Respondent identity (email, user ID)")
	decisionRespondCmd.Flags().Bool("accept-guidance", false, "Skip iteration, accept text as directive")

	decisionCmd.AddCommand(decisionRespondCmd)
}

func runDecisionRespond(cmd *cobra.Command, args []string) {
	CheckReadonly("decision respond")

	selectOpt, _ := cmd.Flags().GetString("select")
	textResponse, _ := cmd.Flags().GetString("text")
	respondedBy, _ := cmd.Flags().GetString("by")
	acceptGuidance, _ := cmd.Flags().GetBool("accept-guidance")

	// Must provide either --select or --text
	if selectOpt == "" && textResponse == "" {
		fmt.Fprintf(os.Stderr, "Error: must provide --select and/or --text\n")
		os.Exit(1)
	}

	// --accept-guidance requires --text
	if acceptGuidance && textResponse == "" {
		fmt.Fprintf(os.Stderr, "Error: --accept-guidance requires --text\n")
		os.Exit(1)
	}

	var dp *types.DecisionPoint
	var options []types.DecisionOption
	var resolvedID string
	now := time.Now()

	// Determine next action based on response type
	shouldCloseGate := selectOpt != "" || acceptGuidance
	shouldIterate := textResponse != "" && selectOpt == "" && !acceptGuidance

	requireDaemon("decision respond")

	// Resolve partial/short/slug ID via daemon (bd-dktw5)
	resolveResp, resolveErr := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[0]})
	if resolveErr != nil {
		fmt.Fprintf(os.Stderr, "Error resolving ID %s: %v\n", args[0], resolveErr)
		os.Exit(1)
	}
	var decisionID string
	if err := json.Unmarshal(resolveResp.Data, &decisionID); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing resolved ID: %v\n", err)
		os.Exit(1)
	}

	// Build guidance field for iteration if needed
	guidance := ""
	if shouldIterate {
		guidance = textResponse
	}

	resolveArgs := &rpc.DecisionResolveArgs{
		IssueID:        decisionID,
		SelectedOption: selectOpt,
		ResponseText:   textResponse,
		RespondedBy:    respondedBy,
		Guidance:       guidance,
	}
	result, err := daemonClient.DecisionResolve(resolveArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving decision via daemon: %v\n", err)
		os.Exit(1)
	}

	dp = result.Decision
	resolvedID = dp.IssueID

	// Parse options for output
	if dp.Options != "" {
		if err := json.Unmarshal([]byte(dp.Options), &options); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not parse options: %v\n", err)
		}
	}

	// Trigger decision respond hook (hq-e0adf6.4)
	// This allows external systems (like gt nudge) to wake the requesting agent
	// Use RunDecisionSync to ensure hook completes before program exits
	if hookRunner != nil {
		response := &hooks.DecisionResponsePayload{
			Selected:    selectOpt,
			Text:        textResponse,
			RespondedBy: respondedBy,
			IsTimeout:   false,
		}
		_ = hookRunner.RunDecisionSync(hooks.EventDecisionRespond, dp, response, dp.RequestedBy)
	}

	// Note: EventDecisionResponded is emitted server-side in handleDecisionResolve.
	// No CLI-side emission needed — it would cause duplicate handler dispatch. (gt-2md5kf)

	// Output
	if jsonOutput {
		jsonResult := map[string]interface{}{
			"id":              resolvedID,
			"selected_option": selectOpt,
			"response_text":   textResponse,
			"responded_by":    respondedBy,
			"responded_at":    now.Format(time.RFC3339),
			"gate_closed":     shouldCloseGate,
			"iteration":       shouldIterate,
		}
		// Include iteration details from server response
		if result.IterationMaxHit {
			jsonResult["max_reached"] = true
		}
		if result.IterationCreated && result.NewDecision != nil {
			jsonResult["new_decision_id"] = result.NewDecision.IssueID
			jsonResult["new_iteration"] = result.NewDecision.Iteration
		}
		outputJSON(jsonResult)
		return
	}

	// Human-readable output
	fmt.Printf("%s Decision response recorded: %s\n\n", ui.RenderPass("✓"), ui.RenderID(resolvedID))
	fmt.Printf("  Prompt: %s\n", dp.Prompt)

	if selectOpt != "" {
		for _, opt := range options {
			if opt.ID == selectOpt {
				fmt.Printf("  Selected: [%s] %s\n", opt.ID, opt.Label)
				break
			}
		}
	}

	if textResponse != "" {
		fmt.Printf("  Text: %s\n", textResponse)
	}

	if respondedBy != "" {
		fmt.Printf("  By: %s\n", respondedBy)
	}

	fmt.Println()

	if shouldCloseGate {
		fmt.Printf("  %s Gate closed - blocked issues now unblocked\n", ui.RenderPass("✓"))
	} else if shouldIterate {
		// Show iteration result from server response (bd-u4r9a)
		if result.IterationMaxHit {
			fmt.Printf("  %s Max iterations (%d) reached\n", ui.RenderWarn("⚠"), dp.MaxIterations)
			fmt.Printf("  Use --accept-guidance to proceed with this guidance directly,\n")
			fmt.Printf("  or --select to choose an existing option.\n")
		} else if result.IterationCreated && result.NewDecision != nil {
			fmt.Printf("  %s Created iteration %d: %s\n", ui.RenderPass("✓"),
				result.NewDecision.Iteration, ui.RenderID(result.NewDecision.IssueID))
			fmt.Printf("  The agent will refine options based on your guidance.\n")
			fmt.Printf("  Original decision: %s (closed)\n", ui.RenderID(resolvedID))
		} else {
			// Server didn't create iteration (unexpected) — inform user
			fmt.Printf("  Guidance recorded. The agent will refine options.\n")
		}
	}
}
