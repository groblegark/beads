package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
//	bd decision captain watch    — block until a new decision arrives
//	bd decision captain sweep   — auto-resolve stale decisions
//	bd decision captain auto    — long-running captain loop
//	bd decision captain send    — send message to agent inbox
var decisionCaptainCmd = &cobra.Command{
	Use:   "captain",
	Short: "Non-interactive captain mode for AI agents",
	Long: `Captain mode lets an AI agent supervise other agents' decisions
without a TUI or stdin/stdout piping.

Subcommands:
  list      List all pending decisions as JSON (one per line)
  respond   Respond to a decision (select option + optional rationale)
  poll      Poll once for new decisions, output as JSON, exit
  watch     Block until a new decision arrives, print it as JSON, exit
  sweep     Auto-resolve stale decisions from dead sessions
  auto      Long-running captain loop with configurable rules
  send      Send a message to an agent's inbox

Manual workflow:
  1. bd decision captain watch --timeout 5m
  2. Review, then: bd decision captain respond <id> --select=...
  3. Loop

Autonomous workflow:
  bd decision captain auto --sweep-age 30m --default-action continue

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

// captainWatchCmd blocks until a new decision arrives, then prints it and exits.
var captainWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Block until a new decision arrives",
	Long: `Block until a new decision arrives, print it as JSON, and exit.

Decisions that are already pending when the command starts are ignored
(they are "old news"). Only a decision that appears after the command
starts will trigger output. Use --include-existing to also match
decisions that are already pending.

Exit codes:
  0 - New decision found (printed as JSON to stdout)
  1 - Error
  2 - Timeout (no new decision within --timeout)

Examples:
  # Wait up to 5 minutes for a new decision
  bd decision captain watch --timeout 5m

  # Wait indefinitely, check every 2 seconds
  bd decision captain watch --poll-interval 2s

  # Include already-pending decisions (return immediately if any exist)
  bd decision captain watch --include-existing

Agent loop pattern:
  while true; do
    decision=$(bd decision captain watch --timeout 5m --json) || break
    id=$(echo "$decision" | jq -r .decision_id)
    # ... analyze decision ...
    bd decision captain respond "$id" --select=... --text="..."
  done`,
	RunE: runCaptainWatch,
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

// captainSendCmd sends a direct message to an agent's inbox.
var captainSendCmd = &cobra.Command{
	Use:   "send <agent-name> <message>",
	Short: "Send a message to an agent's inbox",
	Long: `Push a message directly to an agent's inbox for delivery on their
next hook fire (SessionStart or PreCompact).

The agent name should match the actor name used by the target agent
(typically from BD_ACTOR, git user.name, or $USER).

Messages are delivered as system-reminder injections into the agent's
Claude Code conversation.

Examples:
  bd decision captain send Matthew.Baker "Focus on the inbox integration next"
  bd decision captain send dog "Your build is failing, check CI"
  bd decision captain send --priority=0 mayor "URGENT: deploy freeze"
  bd decision captain send --type=alert all "Maintenance in 10 minutes"`,
	Args: cobra.MinimumNArgs(2),
	RunE: runCaptainSend,
}

// captainAutoCmd runs a long-running captain loop that watches for decisions,
// auto-sweeps stale ones, and applies configurable default actions.
var captainAutoCmd = &cobra.Command{
	Use:   "auto",
	Short: "Long-running captain loop with configurable rules",
	Long: `Run a persistent captain loop that combines watch + sweep + respond.

The auto captain continuously:
  1. Sweeps stale decisions older than --sweep-age (selecting "stop")
  2. Watches for new decisions
  3. Applies --default-action to decisions matching rules
  4. Logs all actions to stderr for observability

Default actions:
  continue    Select the first non-stop option (keep agents working)
  stop        Select the "stop" option (shut agents down)
  ask         Print to stdout and wait (manual intervention required)
  <option-id> Select a specific option ID if present

Rules are applied in order:
  1. If decision is older than --sweep-age → sweep with "stop"
  2. If --urgent-action is set and urgency=high → apply urgent action
  3. Otherwise → apply --default-action

The loop runs until interrupted (Ctrl+C / SIGTERM).

Examples:
  # Keep agents running, sweep stale decisions every 30m
  bd decision captain auto

  # Stop agents by default, but escalate urgent ones
  bd decision captain auto --default-action stop --urgent-action ask

  # Fast polling for responsive captain
  bd decision captain auto --poll-interval 2s

  # Dry run — show what would happen without acting
  bd decision captain auto --dry-run`,
	RunE: runCaptainAuto,
}

func init() {
	captainRespondCmd.Flags().StringP("select", "s", "", "Option ID to select (required)")
	captainRespondCmd.Flags().StringP("text", "t", "", "Rationale or additional guidance")
	captainRespondCmd.Flags().String("by", "captain", "Captain identity for audit trail")
	_ = captainRespondCmd.MarkFlagRequired("select")

	captainWatchCmd.Flags().Duration("timeout", 0, "Maximum time to wait (0 = wait forever)")
	captainWatchCmd.Flags().Duration("poll-interval", 3*time.Second, "How often to poll for new decisions")
	captainWatchCmd.Flags().Bool("include-existing", false, "Also match decisions already pending at startup")

	captainSweepCmd.Flags().Duration("age", 30*time.Minute, "Sweep decisions older than this")
	captainSweepCmd.Flags().Bool("dry-run", false, "Show what would be swept without resolving")

	captainSendCmd.Flags().Int("priority", 2, "Message priority (0=critical, 1=high, 2=normal, 3=low)")
	captainSendCmd.Flags().String("type", "agent", "Message type (agent, alert, system, event)")
	captainSendCmd.Flags().String("from", "captain", "Sender identity")

	captainAutoCmd.Flags().Duration("poll-interval", 5*time.Second, "How often to poll for new decisions")
	captainAutoCmd.Flags().Duration("sweep-age", 30*time.Minute, "Auto-sweep decisions older than this")
	captainAutoCmd.Flags().String("default-action", "continue", "Default action: continue, stop, ask, or an option ID")
	captainAutoCmd.Flags().String("urgent-action", "", "Override action for high-urgency decisions (empty = use default)")
	captainAutoCmd.Flags().Bool("dry-run", false, "Show what would happen without acting")
	captainAutoCmd.Flags().String("by", "captain-auto", "Captain identity for audit trail")

	decisionCaptainCmd.AddCommand(captainListCmd)
	decisionCaptainCmd.AddCommand(captainRespondCmd)
	decisionCaptainCmd.AddCommand(captainPollCmd)
	decisionCaptainCmd.AddCommand(captainWatchCmd)
	decisionCaptainCmd.AddCommand(captainSweepCmd)
	decisionCaptainCmd.AddCommand(captainSendCmd)
	decisionCaptainCmd.AddCommand(captainAutoCmd)

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

		// Find a "stop" option, or fall back to the last option.
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

func runCaptainWatch(cmd *cobra.Command, args []string) error {
	requireDaemon("decision captain watch")

	timeout, _ := cmd.Flags().GetDuration("timeout")
	pollInterval, _ := cmd.Flags().GetDuration("poll-interval")
	includeExisting, _ := cmd.Flags().GetBool("include-existing")

	// Snapshot existing decisions so we can detect new ones.
	known := make(map[string]bool)
	if !includeExisting {
		listArgs := &rpc.DecisionListArgs{All: false}
		result, err := daemonClient.DecisionList(listArgs)
		if err != nil {
			return fmt.Errorf("initial poll: %w", err)
		}
		for _, dr := range result.Decisions {
			if dr.Decision != nil {
				known[dr.Decision.IssueID] = true
			}
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}

	for {
		select {
		case <-deadline:
			fmt.Fprintln(os.Stderr, "captain watch: timeout waiting for new decision")
			os.Exit(2)

		case <-ticker.C:
			listArgs := &rpc.DecisionListArgs{All: false}
			result, err := daemonClient.DecisionList(listArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "captain watch: poll error: %v\n", err)
				continue
			}

			for _, dr := range result.Decisions {
				if dr.Decision == nil {
					continue
				}
				id := dr.Decision.IssueID
				if known[id] {
					continue
				}

				// New decision found — emit and exit.
				var options []types.DecisionOption
				if dr.Decision.Options != "" {
					_ = json.Unmarshal([]byte(dr.Decision.Options), &options)
				}

				d := captainDecision{
					DecisionID:  id,
					Prompt:      dr.Decision.Prompt,
					Context:     dr.Decision.Context,
					Options:     options,
					CreatedAt:   dr.Decision.CreatedAt.Format(time.RFC3339),
					RequestedBy: dr.Decision.RequestedBy,
					Urgency:     dr.Decision.Urgency,
					Age:         time.Since(dr.Decision.CreatedAt).Truncate(time.Second).String(),
				}

				if jsonOutput {
					outputJSON(d)
				} else {
					fmt.Printf("New decision: %s\n", d.DecisionID)
					fmt.Printf("  Prompt: %s\n", d.Prompt)
					if d.Context != "" {
						fmt.Printf("  Context: %s\n", d.Context)
					}
					fmt.Printf("  Options:")
					for _, opt := range d.Options {
						fmt.Printf(" [%s] %s", opt.ID, opt.Label)
					}
					fmt.Println()
				}
				return nil
			}
		}
	}
}

// autoResolveDecision picks an option based on the action string.
// Returns the option ID to select, or "" if the action is "ask".
func autoResolveDecision(options []types.DecisionOption, action string) string {
	switch action {
	case "ask":
		return ""
	case "stop":
		for _, opt := range options {
			if opt.ID == "stop" {
				return "stop"
			}
		}
		// Fallback: last option is conventionally stop-like
		if len(options) > 0 {
			return options[len(options)-1].ID
		}
		return ""
	case "continue":
		// First non-stop option
		for _, opt := range options {
			if opt.ID != "stop" {
				return opt.ID
			}
		}
		// All options are stop-like, pick first
		if len(options) > 0 {
			return options[0].ID
		}
		return ""
	default:
		// Treat as a specific option ID
		for _, opt := range options {
			if opt.ID == action {
				return action
			}
		}
		// Option not found, fall back to continue behavior
		return autoResolveDecision(options, "continue")
	}
}

func runCaptainAuto(cmd *cobra.Command, args []string) error {
	requireDaemon("decision captain auto")

	pollInterval, _ := cmd.Flags().GetDuration("poll-interval")
	sweepAge, _ := cmd.Flags().GetDuration("sweep-age")
	defaultAction, _ := cmd.Flags().GetString("default-action")
	urgentAction, _ := cmd.Flags().GetString("urgent-action")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	by, _ := cmd.Flags().GetString("by")

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Track seen decisions to detect new ones.
	seen := make(map[string]bool)

	// Seed with existing decisions.
	if result, err := daemonClient.DecisionList(&rpc.DecisionListArgs{All: false}); err == nil {
		for _, dr := range result.Decisions {
			if dr.Decision != nil {
				seen[dr.Decision.IssueID] = true
			}
		}
	}

	fmt.Fprintf(os.Stderr, "captain auto: started (poll=%s, sweep-age=%s, default=%s)\n",
		pollInterval, sweepAge, defaultAction)
	if dryRun {
		fmt.Fprintln(os.Stderr, "captain auto: DRY RUN — no actions will be taken")
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var acted, swept int

	for {
		select {
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "\ncaptain auto: shutting down (acted=%d, swept=%d)\n", acted, swept)
			return nil

		case <-ticker.C:
			result, err := daemonClient.DecisionList(&rpc.DecisionListArgs{All: false})
			if err != nil {
				fmt.Fprintf(os.Stderr, "captain auto: poll error: %v\n", err)
				continue
			}

			now := time.Now()

			// Clean up seen map for resolved decisions.
			pendingIDs := make(map[string]bool)
			for _, dr := range result.Decisions {
				if dr.Decision != nil {
					pendingIDs[dr.Decision.IssueID] = true
				}
			}
			for id := range seen {
				if !pendingIDs[id] {
					delete(seen, id)
				}
			}

			for _, dr := range result.Decisions {
				if dr.Decision == nil {
					continue
				}
				id := dr.Decision.IssueID
				age := now.Sub(dr.Decision.CreatedAt)

				var options []types.DecisionOption
				if dr.Decision.Options != "" {
					_ = json.Unmarshal([]byte(dr.Decision.Options), &options)
				}

				// Determine which action to apply.
				var action string
				var reason string
				if age >= sweepAge {
					action = "stop"
					reason = fmt.Sprintf("stale (%s old, sweep-age=%s)", age.Truncate(time.Second), sweepAge)
				} else if urgentAction != "" && dr.Decision.Urgency == "high" {
					action = urgentAction
					reason = "high urgency"
				} else if !seen[id] {
					action = defaultAction
					reason = "new decision"
				} else {
					continue // Already seen, not stale, skip
				}

				seen[id] = true

				selectID := autoResolveDecision(options, action)
				if selectID == "" {
					// "ask" mode — emit to stdout for manual handling
					d := captainDecision{
						DecisionID:  id,
						Prompt:      dr.Decision.Prompt,
						Context:     dr.Decision.Context,
						Options:     options,
						CreatedAt:   dr.Decision.CreatedAt.Format(time.RFC3339),
						RequestedBy: dr.Decision.RequestedBy,
						Urgency:     dr.Decision.Urgency,
						Age:         age.Truncate(time.Second).String(),
					}
					data, _ := json.Marshal(d)
					fmt.Println(string(data))
					fmt.Fprintf(os.Stderr, "captain auto: ASK %s — %s (%s)\n", id, dr.Decision.Prompt, reason)
					continue
				}

				if dryRun {
					fmt.Fprintf(os.Stderr, "captain auto: [dry-run] would select [%s] on %s — %s (%s)\n",
						selectID, id, dr.Decision.Prompt, reason)
					continue
				}

				resolveArgs := &rpc.DecisionResolveArgs{
					IssueID:        id,
					SelectedOption: selectID,
					ResponseText:   fmt.Sprintf("Auto-captain: %s", reason),
					RespondedBy:    by,
				}
				if _, err := daemonClient.DecisionResolve(resolveArgs); err != nil {
					fmt.Fprintf(os.Stderr, "captain auto: error resolving %s: %v\n", id, err)
					continue
				}

				if age >= sweepAge {
					swept++
				} else {
					acted++
				}
				fmt.Fprintf(os.Stderr, "captain auto: %s [%s] → %s (%s)\n", id, selectID, dr.Decision.Prompt, reason)
			}
		}
	}
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

func runCaptainSend(cmd *cobra.Command, args []string) error {
	requireDaemon("decision captain send")

	agentName := args[0]
	message := strings.Join(args[1:], " ")
	priority, _ := cmd.Flags().GetInt("priority")
	msgType, _ := cmd.Flags().GetString("type")
	from, _ := cmd.Flags().GetString("from")

	pushArgs := &rpc.InboxPushArgs{
		AgentName: agentName,
		Type:      msgType,
		Source:    from,
		Content:   message,
		Priority:  priority,
	}

	resp, err := daemonClient.InboxPush(pushArgs)
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("inbox push failed: %s", resp.Error)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"to":       agentName,
			"type":     msgType,
			"priority": priority,
			"from":     from,
			"content":  message,
			"status":   "sent",
		})
	} else {
		fmt.Printf("Sent to %s: %s\n", agentName, message)
	}

	return nil
}
