package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

// slingCmd dispatches a bead to an agent, waking it if idle.
// This is the beads equivalent of gastown's gt sling — adapted for bd's
// hook+inbox+eventbus architecture. No mux needed: Claude Code hooks
// are the control plane. (bd-yr8hp, bd-ihk05)
var slingCmd = &cobra.Command{
	Use:   "sling <bead-id> [target-agent]",
	Short: "Dispatch a bead to an agent (wakes if idle)",
	Long: `Dispatch a bead to an agent, assigning it and waking the agent if blocked on "bd done".

The target agent can be specified explicitly by name, or omitted to auto-select
the least-busy idle agent from the roster.

The bead is assigned to the target (status → in_progress, assignee → target),
and an inbox message is pushed to wake the agent via NATS JetStream.

Examples:
  bd sling bd-abc bright-lark        # Assign bd-abc to bright-lark
  bd sling bd-abc                    # Auto-select idle agent from roster
  bd sling bd-abc --self             # Assign to yourself
  bd sling bd-abc --args "focus on the error handling path"`,
	Args:    cobra.RangeArgs(1, 2),
	GroupID: "issues",
	RunE:    runSling,
}

var (
	slingArgs   string
	slingSelf   bool
	slingForce  bool
	slingDryRun bool
)

func init() {
	slingCmd.Flags().StringVar(&slingArgs, "args", "", "Additional context to include in the work assignment")
	slingCmd.Flags().BoolVar(&slingSelf, "self", false, "Assign to yourself (current agent)")
	slingCmd.Flags().BoolVar(&slingForce, "force", false, "Re-sling already-assigned beads")
	slingCmd.Flags().BoolVar(&slingDryRun, "dry-run", false, "Show what would happen without making changes")
	rootCmd.AddCommand(slingCmd)
}

func runSling(cmd *cobra.Command, args []string) error {
	CheckReadonly("sling")
	requireDaemon("sling")

	beadID := args[0]
	actor := getActor()

	// ── Step 1: Validate bead exists and is not closed ──────────────
	showResp, err := daemonClient.Show(&rpc.ShowArgs{ID: beadID})
	if err != nil {
		return fmt.Errorf("failed to fetch bead %s: %w", beadID, err)
	}
	if !showResp.Success {
		return fmt.Errorf("bead %s not found: %s", beadID, showResp.Error)
	}

	var details types.IssueDetails
	if err := json.Unmarshal(showResp.Data, &details); err != nil {
		return fmt.Errorf("failed to parse bead: %w", err)
	}
	issue := &details.Issue

	if issue.Status == types.StatusClosed || issue.Status == types.StatusTombstone {
		return fmt.Errorf("bead %s is %s — cannot sling closed work", beadID, issue.Status)
	}

	if issue.Assignee != "" && !slingForce {
		return fmt.Errorf("bead %s is already assigned to %s (use --force to reassign)", beadID, issue.Assignee)
	}

	// ── Step 2: Resolve target agent ────────────────────────────────
	var target string

	if slingSelf {
		target = actor
		if target == "" {
			return fmt.Errorf("--self requires BD_ACTOR to be set")
		}
	} else if len(args) > 1 {
		target = args[1]
	} else {
		// Auto-select: pick least-busy idle agent from roster
		selected, err := autoSelectAgent(actor)
		if err != nil {
			return fmt.Errorf("auto-select failed: %w", err)
		}
		target = selected
	}

	// Verify target exists in roster (unless it's self)
	if !slingSelf {
		roster, err := daemonClient.AgentRoster(&rpc.AgentRosterArgs{})
		if err == nil && roster != nil {
			found := false
			for _, entry := range roster.Actors {
				if entry.Actor == target {
					found = true
					if entry.Reaped {
						fmt.Fprintf(os.Stderr, "Warning: %s was reaped (crashed/disconnected) — sling may not wake it\n", target)
					}
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "Warning: %s not found in roster — agent may not be running\n", target)
			}
		}
	}

	// ── Dry run check ───────────────────────────────────────────────
	if slingDryRun {
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would sling %s (%s) → %s\n", beadID, issue.Title, target)
		if slingArgs != "" {
			fmt.Fprintf(os.Stderr, "[DRY RUN] Context: %s\n", slingArgs)
		}
		return nil
	}

	// ── Step 3: Assign bead to target ───────────────────────────────
	inProgress := string(types.StatusInProgress)
	updateResp, err := daemonClient.Update(&rpc.UpdateArgs{
		ID:       beadID,
		Status:   &inProgress,
		Assignee: &target,
	})
	if err != nil {
		return fmt.Errorf("failed to assign bead: %w", err)
	}
	if !updateResp.Success {
		return fmt.Errorf("failed to assign bead: %s", updateResp.Error)
	}

	// ── Step 4: Push inbox notification to wake agent ────────────────
	content := formatSlingMessage(beadID, issue.Title, actor, slingArgs)

	pushResp, err := daemonClient.InboxPush(&rpc.InboxPushArgs{
		AgentName: target,
		Type:      "agent",
		Source:    fmt.Sprintf("sling:%s", actor),
		Content:   content,
		Priority:  1, // High priority — this is a work assignment
	})
	if err != nil {
		return fmt.Errorf("assigned bead but failed to notify: %w", err)
	}
	_ = pushResp

	// ── Output ──────────────────────────────────────────────────────
	if jsonOutput {
		outputJSON(map[string]string{
			"bead":   beadID,
			"target": target,
			"title":  issue.Title,
			"status": "slung",
		})
	} else {
		fmt.Fprintf(os.Stderr, "Slung %s → %s\n", beadID, target)
		fmt.Fprintf(os.Stderr, "  %s\n", issue.Title)
		if slingArgs != "" {
			fmt.Fprintf(os.Stderr, "  Context: %s\n", slingArgs)
		}
	}

	return nil
}

// formatSlingMessage builds the inbox content for a sling assignment.
func formatSlingMessage(beadID, title, dispatcher, extraArgs string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("WORK: %s\n", title))
	b.WriteString(fmt.Sprintf("Bead: %s\n", beadID))
	b.WriteString(fmt.Sprintf("Dispatched by: %s\n", dispatcher))
	if extraArgs != "" {
		b.WriteString(fmt.Sprintf("\nContext: %s\n", extraArgs))
	}
	b.WriteString("\nRun 'bd show " + beadID + "' for full details, then start working.")
	return b.String()
}

// autoSelectAgent picks the least-busy idle agent from the roster.
func autoSelectAgent(self string) (string, error) {
	roster, err := daemonClient.AgentRoster(&rpc.AgentRosterArgs{})
	if err != nil {
		return "", fmt.Errorf("cannot query roster: %w", err)
	}
	if roster == nil || len(roster.Actors) == 0 {
		return "", fmt.Errorf("no agents in roster — specify a target or use --self")
	}

	// Find idle agents: not reaped, no current task, not self, idle > 5s
	type candidate struct {
		name    string
		idleSec float64
	}
	var candidates []candidate

	for _, a := range roster.Actors {
		if a.Reaped {
			continue
		}
		if a.Actor == self {
			continue
		}
		if a.TaskID != "" {
			continue // already has work
		}
		if a.IdleSecs < 5 {
			continue // too recently active, probably mid-work
		}
		candidates = append(candidates, candidate{name: a.Actor, idleSec: a.IdleSecs})
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no idle agents available — specify a target or use --self")
	}

	// Pick longest-idle agent (most available)
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.idleSec > best.idleSec {
			best = c
		}
	}

	return best.name, nil
}
