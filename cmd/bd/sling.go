package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

Use --spawn to create a new Claude Code agent session and dispatch the bead to it.
The spawned agent runs in the background with a generated name (e.g., "swift-fox")
and receives the bead assignment as its initial prompt.

Examples:
  bd sling bd-abc bright-lark        # Assign bd-abc to bright-lark
  bd sling bd-abc                    # Auto-select idle agent from roster
  bd sling bd-abc --self             # Assign to yourself
  bd sling bd-abc --spawn            # Spawn a new agent and assign bd-abc to it
  bd sling bd-abc --args "focus on the error handling path"`,
	Args:    cobra.RangeArgs(1, 2),
	GroupID: "issues",
	RunE:    runSling,
}

var (
	slingArgs   string
	slingSelf   bool
	slingSpawn  bool
	slingForce  bool
	slingDryRun bool
)

func init() {
	slingCmd.Flags().StringVar(&slingArgs, "args", "", "Additional context to include in the work assignment")
	slingCmd.Flags().BoolVar(&slingSelf, "self", false, "Assign to yourself (current agent)")
	slingCmd.Flags().BoolVar(&slingSpawn, "spawn", false, "Spawn a new Claude Code agent session for this bead")
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
	var spawned bool

	if slingSpawn {
		// Generate a unique agent name for the new session
		target = generateSpawnName()
		spawned = true
	} else if slingSelf {
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

	// Verify target exists in roster (unless spawning or self)
	if !slingSelf && !spawned {
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
		if spawned {
			fmt.Fprintf(os.Stderr, "[DRY RUN] Would spawn new agent %q\n", target)
		}
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would sling %s (%s) → %s\n", beadID, issue.Title, target)
		if slingArgs != "" {
			fmt.Fprintf(os.Stderr, "[DRY RUN] Context: %s\n", slingArgs)
		}
		return nil
	}

	// ── Step 2b: Spawn new agent if requested ───────────────────────
	if spawned {
		if err := spawnAgent(target, beadID, issue.Title, slingArgs); err != nil {
			return fmt.Errorf("failed to spawn agent: %w", err)
		}
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
		result := map[string]string{
			"bead":   beadID,
			"target": target,
			"title":  issue.Title,
			"status": "slung",
		}
		if spawned {
			result["spawned"] = "true"
		}
		outputJSON(result)
	} else {
		if spawned {
			fmt.Fprintf(os.Stderr, "Spawned %s\n", target)
		}
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

// generateSpawnName creates a unique agent name for a new spawned session.
// Uses rpc.GenerateAgentName with a random seed to get a fun adjective-noun pair.
func generateSpawnName() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use a simple counter-like approach
		return fmt.Sprintf("spawn-%d", os.Getpid())
	}
	seed := hex.EncodeToString(b)
	return rpc.GenerateAgentName(seed)
}

// spawnAgent launches a new Claude Code session in the background.
// The agent is started with BD_ACTOR set so it registers with the daemon,
// and receives a startup prompt telling it to work on the assigned bead.
func spawnAgent(agentName, beadID, title, extraArgs string) error {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude CLI not found in PATH: %w", err)
	}

	// Build the startup prompt — this is what the agent sees first
	prompt := formatSpawnPrompt(beadID, title, extraArgs)

	// Resolve project root for the agent's working directory
	projectRoot, _ := os.Getwd()
	if dbPath != "" {
		// Use the .beads directory's parent as project root
		projectRoot = filepath.Dir(filepath.Dir(dbPath))
	}

	// Launch claude in the background with the agent identity
	cmd := exec.Command(claudePath, "--dangerously-skip-permissions", "-p", prompt) //nolint:gosec // claudePath is from LookPath, prompt is controlled
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BD_ACTOR=%s", agentName),
		fmt.Sprintf("BEADS_AGENT_NAME=%s", agentName),
		fmt.Sprintf("GIT_AUTHOR_NAME=%s", agentName),
	)

	// Detach from parent — agent runs independently
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start claude session: %w", err)
	}

	// Release the child process so it runs independently
	if err := cmd.Process.Release(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not detach agent process: %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "Spawned agent %s (PID %d)\n", agentName, cmd.Process.Pid)
	return nil
}

// formatSpawnPrompt builds the initial prompt for a spawned agent session.
func formatSpawnPrompt(beadID, title, extraArgs string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("You have been assigned bead %s: %s\n\n", beadID, title))
	if extraArgs != "" {
		b.WriteString(fmt.Sprintf("Context: %s\n\n", extraArgs))
	}
	b.WriteString(fmt.Sprintf("Run 'bd show %s' for full details.\n", beadID))
	b.WriteString(fmt.Sprintf("Run 'bd update %s --status=in_progress' to claim it, then start working.\n", beadID))
	b.WriteString("When done, run 'bd close " + beadID + "' and push your changes.")
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
