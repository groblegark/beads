package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/gate"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// sessionGateRegistry is the default registry for session-level gates.
// Built-in gates (decision, commit-push, bead-update) are registered at init.
var sessionGateRegistry = newSessionGateRegistry()

func newSessionGateRegistry() *gate.Registry {
	reg := gate.NewRegistry()
	gate.RegisterBuiltinGates(reg)
	gate.RegisterPreToolUseGates(reg)
	gate.RegisterPreCompactGates(reg)
	gate.RegisterUserPromptSubmitGates(reg)
	gate.RegisterBridgeGates(reg)
	return reg
}

// getSessionID returns the current Claude Code session ID.
// Checks CLAUDE_SESSION_ID first, then falls back to a session alias file
// written by `bd bus emit` that maps TERM_SESSION_ID → Claude session_id.
// This bridges the gap between Claude Code's hook JSON (which contains session_id)
// and direct CLI commands (which only have TERM_SESSION_ID in their env). (bd-02qeb)
func getSessionID() string {
	if sid := os.Getenv("CLAUDE_SESSION_ID"); sid != "" {
		return sid
	}
	// Fall back to session alias written by bus_emit.
	termSID := os.Getenv("TERM_SESSION_ID")
	if termSID == "" {
		return ""
	}
	wd := getWorkDir()
	aliasPath := filepath.Join(wd, ".runtime", "session-alias", termSID)
	data, err := os.ReadFile(aliasPath) //nolint:gosec // G304: path constructed from controlled .runtime/ directory
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// getWorkDir returns the working directory for gate marker storage.
func getWorkDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// gateMarkCmd marks a session gate as satisfied.
var gateMarkCmd = &cobra.Command{
	Use:   "mark <gate-id>",
	Short: "Mark a session gate as satisfied",
	Long: `Mark a session gate as satisfied for the current Claude Code session.

The session ID is read from the CLAUDE_SESSION_ID environment variable.
Gate markers are stored in .runtime/gates/<session-id>/<gate-id>.

Examples:
  bd gate mark decision         # Mark decision gate satisfied
  bd gate mark commit-push      # Mark commit-push gate satisfied`,
	Args: cobra.ExactArgs(1),
	Run: func(_ *cobra.Command, args []string) {
		gateID := args[0]
		sessionID := getSessionID()
		if sessionID == "" {
			fmt.Fprintln(os.Stderr, "Error: CLAUDE_SESSION_ID not set")
			os.Exit(1)
		}

		workDir := getWorkDir()
		if err := gate.MarkGate(workDir, sessionID, gateID); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"gate_id":    gateID,
				"session_id": sessionID,
				"marked":     true,
			})
			return
		}

		fmt.Printf("%s Gate marked: %s\n", ui.RenderPass("✓"), gateID)
	},
}

// gateClearCmd clears session gate markers.
var gateClearCmd = &cobra.Command{
	Use:   "clear [gate-id]",
	Short: "Clear session gate markers",
	Long: `Clear session gate markers for the current Claude Code session.

Without arguments, clears all gates. With --hook, clears gates for a
specific hook type. With a gate-id argument, clears a specific gate.

Examples:
  bd gate clear                    # Clear all gate markers
  bd gate clear decision           # Clear specific gate marker
  bd gate clear --hook Stop        # Clear all Stop hook gate markers`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		sessionID := getSessionID()
		if sessionID == "" {
			fmt.Fprintln(os.Stderr, "Error: CLAUDE_SESSION_ID not set")
			os.Exit(1)
		}

		workDir := getWorkDir()
		hookFlag, _ := cmd.Flags().GetString("hook")

		switch {
		case len(args) == 1:
			// Clear specific gate
			gate.ClearGate(workDir, sessionID, args[0])
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"gate_id":    args[0],
					"session_id": sessionID,
					"cleared":    true,
				})
				return
			}
			fmt.Printf("%s Gate cleared: %s\n", ui.RenderPass("✓"), args[0])

		case hookFlag != "":
			// Clear all gates for a hook type
			hookType, err := gate.ParseHookType(hookFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			gate.ClearGatesForHook(workDir, sessionID, hookType, sessionGateRegistry)
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"hook":       string(hookType),
					"session_id": sessionID,
					"cleared":    true,
				})
				return
			}
			fmt.Printf("%s Cleared all %s gate markers\n", ui.RenderPass("✓"), hookType)

		default:
			// Clear all gates
			gate.ClearAllGates(workDir, sessionID)
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"session_id": sessionID,
					"cleared":    "all",
				})
				return
			}
			fmt.Printf("%s Cleared all gate markers\n", ui.RenderPass("✓"))
		}
	},
}

// gateSessionCheckCmd checks session gates for a specific hook type.
var gateSessionCheckCmd = &cobra.Command{
	Use:   "session-check --hook <type>",
	Short: "Check session gates for a hook type",
	Long: `Evaluate all registered session gates for a Claude Code hook type.

Returns exit 0 if all strict gates are satisfied (allow).
Returns exit 1 with block JSON if any strict gate is unsatisfied (block).
Soft gates produce warnings but do not block.

The --soft flag makes all gates soft (for autonomous mode).
The --json flag outputs the full check response as JSON.

Hook types: Stop, PreToolUse, UserPromptSubmit, PreCompact

This command is designed to be called from Claude Code hook scripts:
  bd gate session-check --hook Stop --json

Examples:
  bd gate session-check --hook Stop          # Check Stop gates
  bd gate session-check --hook Stop --json   # JSON output for hooks
  bd gate session-check --hook Stop --soft   # Soft mode (warn only)`,
	Run: func(cmd *cobra.Command, _ []string) {
		hookFlag, _ := cmd.Flags().GetString("hook")
		softMode, _ := cmd.Flags().GetBool("soft")
		sessionFlag, _ := cmd.Flags().GetString("session")

		if hookFlag == "" {
			fmt.Fprintln(os.Stderr, "Error: --hook flag is required")
			os.Exit(1)
		}

		hookType, err := gate.ParseHookType(hookFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// --session flag takes priority over CLAUDE_SESSION_ID env var.
		sessionID := sessionFlag
		if sessionID == "" {
			sessionID = getSessionID()
		}
		if sessionID == "" {
			// No session — allow by default
			if jsonOutput {
				outputJSON(gate.CheckResponse{Decision: "allow", Reason: "no session"})
			}
			return
		}

		workDir := getWorkDir()

		// If soft mode, temporarily override gate modes
		reg := sessionGateRegistry
		if softMode {
			reg = softCopyRegistry(reg, hookType)
		}

		opts := gate.CheckOpts{
			TerminalSessionID: os.Getenv("TERM_SESSION_ID"),
			AgentName:         eventbus.AgentBaseName(getActor()),
		}
		resp, err := gate.EvaluateHookWithOpts(workDir, sessionID, hookType, reg, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error evaluating gates: %v\n", err)
			os.Exit(1)
		}

		// If blocked by the decision gate, try to load a richer prompt
		// from the claude-hooks config bead to give the agent detailed
		// wrap-up instructions instead of a bare gate description.
		if resp.Decision == "block" && hookType == gate.HookStop {
			if prompt := loadGatePromptFromConfig(); prompt != "" {
				resp.Reason = prompt
			}
		}

		if jsonOutput {
			outputJSON(resp)
			if resp.Decision == "block" {
				os.Exit(1)
			}
			return
		}

		// Human-readable output
		if resp.Decision == "block" {
			fmt.Printf("%s Blocked: %s\n", ui.RenderFail("✗"), resp.Reason)
			for _, r := range resp.Results {
				if !r.Satisfied && r.Mode == gate.GateModeStrict {
					hint := ""
					if r.Hint != "" {
						hint = fmt.Sprintf(" → %s", r.Hint)
					}
					fmt.Printf("  %s %s: %s%s\n", ui.RenderFail("●"), r.GateID, r.Message, hint)
				}
			}
			os.Exit(1)
		}

		// Warnings
		for _, w := range resp.Warnings {
			fmt.Printf("  %s %s\n", ui.RenderWarn("⚠"), w)
		}

		if len(resp.Warnings) == 0 {
			fmt.Printf("%s All %s gates satisfied\n", ui.RenderPass("✓"), hookType)
		} else {
			fmt.Printf("%s %s gates allow (with %d warnings)\n", ui.RenderPass("✓"), hookType, len(resp.Warnings))
		}
	},
}

// gateStatusCmd shows current session gate status.
var gateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show session gate status",
	Long: `Show the current status of all session gates for debugging.

Displays all registered gates grouped by hook type, showing which
are satisfied and which are pending.

Examples:
  bd gate status                   # Show all gate status
  bd gate status --session abc123  # Show for specific session`,
	Run: func(cmd *cobra.Command, _ []string) {
		sessionFlag, _ := cmd.Flags().GetString("session")

		sessionID := sessionFlag
		if sessionID == "" {
			sessionID = getSessionID()
		}
		if sessionID == "" {
			fmt.Fprintln(os.Stderr, "Error: no session ID (set CLAUDE_SESSION_ID or use --session)")
			os.Exit(1)
		}

		workDir := getWorkDir()

		type gateStatus struct {
			ID          string        `json:"id"`
			Hook        gate.HookType `json:"hook"`
			Mode        gate.GateMode `json:"mode"`
			Satisfied   bool          `json:"satisfied"`
			Description string        `json:"description"`
			Hint        string        `json:"hint,omitempty"`
		}

		var allStatus []gateStatus
		for _, hookType := range gate.ValidHookTypes() {
			gates := sessionGateRegistry.GatesForHook(hookType)
			for _, g := range gates {
				allStatus = append(allStatus, gateStatus{
					ID:          g.ID,
					Hook:        g.Hook,
					Mode:        g.Mode,
					Satisfied:   gate.IsGateSatisfied(workDir, sessionID, g.ID),
					Description: g.Description,
					Hint:        g.Hint,
				})
			}
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"session_id": sessionID,
				"gates":      allStatus,
			})
			return
		}

		if len(allStatus) == 0 {
			fmt.Println("No session gates registered.")
			return
		}

		fmt.Printf("Session: %s\n\n", sessionID)

		// Group by hook type
		currentHook := gate.HookType("")
		for _, s := range allStatus {
			if s.Hook != currentHook {
				currentHook = s.Hook
				fmt.Printf("%s gates:\n", ui.RenderAccent(string(s.Hook)))
			}

			sym := ui.RenderFail("○")
			label := "pending"
			if s.Satisfied {
				sym = ui.RenderPass("●")
				label = "satisfied"
			}

			modeLabel := ""
			if s.Mode == gate.GateModeSoft {
				modeLabel = " (soft)"
			}

			fmt.Printf("  %s %s: %s%s\n", sym, s.ID, label, modeLabel)
			if !s.Satisfied && s.Hint != "" {
				fmt.Printf("    → %s\n", s.Hint)
			}
		}
	},
}

// gateSessionListCmd lists registered session gates.
var gateSessionListCmd = &cobra.Command{
	Use:   "session-list [--hook <type>]",
	Short: "List registered session gates",
	Long: `List all registered session gates, optionally filtered by hook type.

This lists the gate definitions (not their satisfaction status).
Use 'bd gate status' to see which gates are currently satisfied.

Examples:
  bd gate session-list               # List all session gates
  bd gate session-list --hook Stop   # List only Stop gates`,
	Run: func(cmd *cobra.Command, _ []string) {
		hookFlag, _ := cmd.Flags().GetString("hook")

		var gates []*gate.Gate
		if hookFlag != "" {
			hookType, err := gate.ParseHookType(hookFlag)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			gates = sessionGateRegistry.GatesForHook(hookType)
		} else {
			gates = sessionGateRegistry.AllGates()
		}

		if jsonOutput {
			type gateInfo struct {
				ID           string        `json:"id"`
				Hook         gate.HookType `json:"hook"`
				Mode         gate.GateMode `json:"mode"`
				Description  string        `json:"description"`
				Hint         string        `json:"hint,omitempty"`
				HasAutoCheck bool          `json:"has_auto_check"`
			}
			var infos []gateInfo
			for _, g := range gates {
				infos = append(infos, gateInfo{
					ID:           g.ID,
					Hook:         g.Hook,
					Mode:         g.Mode,
					Description:  g.Description,
					Hint:         g.Hint,
					HasAutoCheck: g.AutoCheck != nil,
				})
			}
			outputJSON(infos)
			return
		}

		if len(gates) == 0 {
			if hookFlag != "" {
				fmt.Printf("No session gates registered for hook %s.\n", hookFlag)
			} else {
				fmt.Println("No session gates registered.")
			}
			return
		}

		fmt.Printf("Session gates (%d):\n\n", len(gates))
		for _, g := range gates {
			modeLabel := "strict"
			if g.Mode == gate.GateModeSoft {
				modeLabel = "soft"
			}
			fmt.Printf("  %s  hook=%s  mode=%s\n", ui.RenderID(g.ID), g.Hook, modeLabel)
			if g.Description != "" {
				fmt.Printf("    %s\n", g.Description)
			}
		}
	},
}

// gateRegisterCmd registers a custom session gate.
var gateRegisterCmd = &cobra.Command{
	Use:   "register <gate-id> --hook <type> --description '...' --mode strict",
	Short: "Register a custom session gate",
	Long: `Register a custom session gate beyond the built-in ones.

Custom gates participate in session-check alongside built-in gates.
They must be marked manually with 'bd gate mark'.

Examples:
  bd gate register my-check --hook Stop --description 'Custom check' --mode strict
  bd gate register review --hook Stop --description 'Code reviewed' --mode soft`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		gateID := args[0]
		hookFlag, _ := cmd.Flags().GetString("hook")
		description, _ := cmd.Flags().GetString("description")
		modeFlag, _ := cmd.Flags().GetString("mode")

		if hookFlag == "" {
			fmt.Fprintln(os.Stderr, "Error: --hook flag is required")
			os.Exit(1)
		}

		hookType, err := gate.ParseHookType(hookFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		mode := gate.GateModeStrict
		if modeFlag == "soft" {
			mode = gate.GateModeSoft
		}

		g := &gate.Gate{
			ID:          gateID,
			Hook:        hookType,
			Description: description,
			Mode:        mode,
		}

		if err := sessionGateRegistry.Register(g); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"gate_id":    gateID,
				"hook":       string(hookType),
				"mode":       string(mode),
				"registered": true,
			})
			return
		}

		fmt.Printf("%s Registered gate: %s (hook=%s, mode=%s)\n",
			ui.RenderPass("✓"), gateID, hookType, mode)
	},
}

// softCopyRegistry creates a copy of the registry with all gates for the given
// hook type set to soft mode. Used for autonomous/soft mode operation.
func softCopyRegistry(reg *gate.Registry, hookType gate.HookType) *gate.Registry {
	softReg := gate.NewRegistry()
	for _, g := range reg.AllGates() {
		softGate := *g
		if softGate.Hook == hookType {
			softGate.Mode = gate.GateModeSoft
		}
		_ = softReg.Register(&softGate)
	}
	return softReg
}

func init() {
	// gate clear flags
	gateClearCmd.Flags().String("hook", "", "Clear gates for a specific hook type")

	// gate session-check flags
	gateSessionCheckCmd.Flags().String("hook", "", "Hook type to check (required)")
	gateSessionCheckCmd.Flags().String("session", "", "Session ID (default: CLAUDE_SESSION_ID env var)")
	gateSessionCheckCmd.Flags().Bool("soft", false, "Treat all gates as soft (warn only)")

	// gate status flags
	gateStatusCmd.Flags().String("session", "", "Session ID (default: CLAUDE_SESSION_ID)")

	// gate session-list flags
	gateSessionListCmd.Flags().String("hook", "", "Filter by hook type")

	// gate register flags
	gateRegisterCmd.Flags().String("hook", "", "Hook type (required)")
	gateRegisterCmd.Flags().String("description", "", "Gate description")
	gateRegisterCmd.Flags().String("mode", "strict", "Gate mode: strict or soft")

	// Add subcommands to gateCmd
	gateCmd.AddCommand(gateMarkCmd)
	gateCmd.AddCommand(gateClearCmd)
	gateCmd.AddCommand(gateSessionCheckCmd)
	gateCmd.AddCommand(gateStatusCmd)
	gateCmd.AddCommand(gateSessionListCmd)
	gateCmd.AddCommand(gateRegisterCmd)
}

// readStdinHookInput reads and parses JSON from stdin for hook input.
// Returns parsed fields or nil if no stdin available.
func readStdinHookInput() map[string]interface{} {
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		// stdin is a terminal, not piped
		return nil
	}

	var input map[string]interface{}
	dec := json.NewDecoder(os.Stdin)
	if err := dec.Decode(&input); err != nil {
		return nil
	}
	return input
}

// loadGatePromptFromConfig loads a rich prompt for a gate from the claude-hooks
// config bead. This lets the block reason be editable via config beads instead
// of hardcoded in the gate definition.
//
// Lookup path in config bead metadata:
//   - stop_decision.agent_decision_prompt (current schema)
//
// Returns empty string if daemon is unavailable, no config bead exists, or
// the prompt field is not set. Fail-open: gate still blocks with its default
// description if this returns empty.
func loadGatePromptFromConfig() string {
	if daemonClient == nil {
		return ""
	}

	listArgs := &rpc.ListArgs{
		IssueType: "config",
		Labels:    []string{"config:claude-hooks"},
		Status:    "open",
		Limit:     10,
	}
	resp, err := daemonClient.List(listArgs)
	if err != nil {
		return ""
	}

	var issues []*types.IssueWithCounts
	if resp.Data == nil {
		return ""
	}
	if err := json.Unmarshal(resp.Data, &issues); err != nil || len(issues) == 0 {
		return ""
	}

	// Merge config beads (lowest specificity first — global beads have score 0).
	// For simplicity, just iterate and let later beads override earlier ones.
	var prompt string
	var enabled *bool
	for _, iwc := range issues {
		if iwc.Issue.Metadata == nil {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal(iwc.Issue.Metadata, &data); err != nil {
			continue
		}

		// Look up stop_decision fields: prompt, enabled, and agent_overrides.
		if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
			if p, ok := sd["agent_decision_prompt"].(string); ok && p != "" {
				prompt = p
			}
			if e, ok := sd["enabled"].(bool); ok {
				enabled = &e
			}
		}
	}

	// Resolve effective enabled state: per-agent override > global.
	resolvedActor := getActorWithGit()
	effectiveEnabled := true
	if enabled != nil {
		effectiveEnabled = *enabled
	}
	// Check per-agent override in the last-seen config bead.
	for _, iwc := range issues {
		if iwc.Issue.Metadata == nil {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal(iwc.Issue.Metadata, &data); err != nil {
			continue
		}
		if sd, ok := data["stop_decision"].(map[string]interface{}); ok {
			if overrides, ok := sd["agent_overrides"].(map[string]interface{}); ok {
				if agentEnabled, ok := overrides[resolvedActor].(bool); ok {
					effectiveEnabled = agentEnabled
				}
			}
		}
	}

	if !effectiveEnabled {
		return ""
	}

	// Inject the resolved actor name so the LLM can pass it to
	// bd decision create --requested-by, ensuring decisions are attributed
	// to the real agent identity instead of a generic fallback. (bd-2rs8c)
	if prompt != "" {
		prompt = strings.ReplaceAll(prompt, "{{ACTOR}}", resolvedActor)
	}

	// Inject live roster summary so the Stop hook prompt can include
	// real-time agent state for smarter checkpoint options. (bd-b7b60)
	if prompt != "" && strings.Contains(prompt, "{{ROSTER_SUMMARY}}") {
		prompt = strings.ReplaceAll(prompt, "{{ROSTER_SUMMARY}}", buildRosterSummaryForHook(resolvedActor))
	}

	return prompt
}

// buildRosterSummaryForHook generates a compact roster summary for injection
// into hook prompts. Includes active agents with tasks, idle agents available
// for handoff, crashed agents with orphaned work, and unclaimed ready work.
// (bd-b7b60, bd-mj1pu)
func buildRosterSummaryForHook(self string) string {
	if daemonClient == nil {
		return "(roster unavailable)"
	}

	result, err := daemonClient.AgentRoster(&rpc.AgentRosterArgs{
		StaleThresholdSecs: 1800,
	})
	if err != nil || result == nil || len(result.Actors) == 0 {
		return "(no agents in roster)"
	}

	var sb strings.Builder

	// Partition agents.
	const staleThresholdSecs = 600
	var active, crashed []rpc.AgentRosterEntry
	for _, a := range result.Actors {
		if a.Reaped || a.LastEvent == "AgentCrashed" {
			crashed = append(crashed, a)
			continue
		}
		if a.LastEvent == "Stop" && a.IdleSecs > 60 {
			continue // cleanly stopped
		}
		if a.IdleSecs > staleThresholdSecs {
			crashed = append(crashed, a) // treat stale as crashed
		} else {
			active = append(active, a)
		}
	}

	// Active agents — only show agents with in-progress work.
	// Idle no-task agents are noise in the roster. (bd-kpudsl)
	var working []rpc.AgentRosterEntry
	for _, a := range active {
		if a.TaskID != "" {
			working = append(working, a)
		}
	}
	if len(working) > 0 {
		sb.WriteString(fmt.Sprintf("Working agents: %d\n", len(working)))
		for _, a := range working {
			// Build repo/branch suffix if available. (bd-z6958)
			repoTag := ""
			if a.Repo != "" {
				repoTag = " [" + a.Repo
				if a.Branch != "" && a.Branch != "main" && a.Branch != "master" {
					repoTag += "@" + a.Branch
				}
				repoTag += "]"
			}
			sb.WriteString(fmt.Sprintf("  %s%s → %s: %s\n",
				a.Actor, repoTag, a.TaskID, a.TaskTitle))
		}
	}

	// Idle agents — skip, no task means no listing. (bd-kpudsl)

	// Crashed agents with orphaned work.
	if len(crashed) > 0 {
		var orphaned []string
		for _, a := range crashed {
			if a.TaskID != "" {
				orphaned = append(orphaned, fmt.Sprintf("%s had %s: %s", a.Actor, a.TaskID, a.TaskTitle))
			}
		}
		sb.WriteString(fmt.Sprintf("Crashed: %d", len(crashed)))
		if len(orphaned) > 0 {
			sb.WriteString(fmt.Sprintf(" (%d with orphaned work)\n", len(orphaned)))
			for _, o := range orphaned {
				sb.WriteString(fmt.Sprintf("  %s\n", o))
			}
		} else {
			sb.WriteString("\n")
		}
	}

	// Epic analysis — crowding, completion detection, gap analysis. (bd-o39m9)
	epicInsights := buildEpicInsights(self, active)
	if epicInsights != "" {
		sb.WriteString(epicInsights)
	}

	// Ready work — unclaimed tasks not being worked by any active agent.
	readyWork := getReadyWorkForHook(active)
	if len(readyWork) > 0 {
		sb.WriteString(fmt.Sprintf("Unclaimed ready work: %d\n", len(readyWork)))
		for _, rw := range readyWork {
			sb.WriteString(fmt.Sprintf("  %s [P%d]: %s\n", rw.id, rw.priority, rw.title))
		}
	}

	// Unclaimed in_progress beads — assignment gaps from roster. (bd-oenjf)
	if len(result.UnclaimedTasks) > 0 {
		sb.WriteString(fmt.Sprintf("Unclaimed in-progress beads: %d\n", len(result.UnclaimedTasks)))
		for _, t := range result.UnclaimedTasks {
			sb.WriteString(fmt.Sprintf("  %s [P%d]: %s\n", t.ID, t.Priority, t.Title))
		}
	}

	// Session activity diff — what changed since this agent's session started. (bd-3zsda)
	if activity := buildSessionActivityDiff(active, crashed); activity != "" {
		sb.WriteString(activity)
	}

	return sb.String()
}

// buildEpicInsights generates epic-level insights for the Stop hook:
// crowded epics (3+ agents), near-complete epics (for closing), and uncovered
// epics (open children but no agents working on them). (bd-o39m9)
func buildEpicInsights(self string, active []rpc.AgentRosterEntry) string {
	if daemonClient == nil {
		return ""
	}

	// Build agent→epic mapping from roster.
	epicAgents := make(map[string][]string)
	for _, a := range active {
		if a.EpicID != "" {
			epicAgents[a.EpicID] = append(epicAgents[a.EpicID], a.Actor)
		}
	}

	// Fetch epic overview for completion data.
	resp, err := daemonClient.EpicOverview(&rpc.EpicOverviewArgs{})
	if err != nil || resp == nil || resp.Data == nil {
		// Fall back to roster-only crowding check.
		return buildCrowdingOnly(epicAgents)
	}

	var epics []*types.EpicOverview
	if err := json.Unmarshal(resp.Data, &epics); err != nil {
		return buildCrowdingOnly(epicAgents)
	}

	var sb strings.Builder
	var crowded, nearComplete, uncovered []string

	for _, eo := range epics {
		if eo.Epic == nil || eo.TotalChildren == 0 {
			continue
		}
		epicID := eo.Epic.ID
		title := eo.Epic.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		agents := epicAgents[epicID]
		remaining := eo.TotalChildren - eo.ClosedChildren

		// Crowded: 3+ agents on one epic.
		if len(agents) >= 3 {
			crowded = append(crowded, fmt.Sprintf("%s (%d agents, %d/%d done)",
				title, len(agents), eo.ClosedChildren, eo.TotalChildren))
		}

		// Near-complete: <=2 open children and this agent is working on it.
		if remaining <= 2 && remaining > 0 {
			for _, a := range agents {
				if a == self {
					nearComplete = append(nearComplete, fmt.Sprintf("%s (%d/%d done, %d remaining)",
						epicID, eo.ClosedChildren, eo.TotalChildren, remaining))
					break
				}
			}
		}

		// Uncovered: has open children but no agents working on it.
		if len(agents) == 0 && remaining > 0 && eo.Epic.Priority <= 2 {
			uncovered = append(uncovered, fmt.Sprintf("%s [P%d]: %s (%d/%d done)",
				epicID, eo.Epic.Priority, title, eo.ClosedChildren, eo.TotalChildren))
		}
	}

	if len(crowded) > 0 {
		sb.WriteString(fmt.Sprintf("Crowded epics: %s\n", strings.Join(crowded, "; ")))
	}
	if len(nearComplete) > 0 {
		sb.WriteString("Near-complete epics (you're on them — consider closing):\n")
		for _, nc := range nearComplete {
			sb.WriteString(fmt.Sprintf("  %s\n", nc))
		}
	}
	if len(uncovered) > 0 {
		limit := 5
		if len(uncovered) < limit {
			limit = len(uncovered)
		}
		sb.WriteString(fmt.Sprintf("Uncovered epics (no agents, P0-P2, open children): %d\n", len(uncovered)))
		for _, uc := range uncovered[:limit] {
			sb.WriteString(fmt.Sprintf("  %s\n", uc))
		}
	}

	return sb.String()
}

// buildCrowdingOnly is a fallback when EpicOverview is unavailable.
func buildCrowdingOnly(epicAgents map[string][]string) string {
	var crowded []string
	for epicID, agents := range epicAgents {
		if len(agents) >= 3 {
			crowded = append(crowded, fmt.Sprintf("%s (%d agents)", epicID, len(agents)))
		}
	}
	if len(crowded) == 0 {
		return ""
	}
	return fmt.Sprintf("Crowded epics: %s\n", strings.Join(crowded, ", "))
}

// readyWorkItem is a compact representation of a ready issue for hook summaries.
type readyWorkItem struct {
	id       string
	title    string
	priority int
}

// getReadyWorkForHook fetches unclaimed ready work, filtering out tasks
// already in progress by active agents. Returns at most 8 items.
func getReadyWorkForHook(active []rpc.AgentRosterEntry) []readyWorkItem {
	if daemonClient == nil {
		return nil
	}

	// Build a set of task IDs currently being worked on.
	claimed := make(map[string]bool)
	for _, a := range active {
		if a.TaskID != "" {
			claimed[a.TaskID] = true
		}
	}

	resp, err := daemonClient.Ready(&rpc.ReadyArgs{
		Limit:      20, // fetch extra to filter out claimed
		SortPolicy: "priority",
	})
	if err != nil || resp == nil || resp.Data == nil {
		return nil
	}

	var issues []*types.IssueWithCounts
	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		return nil
	}

	var result []readyWorkItem
	for _, iwc := range issues {
		if claimed[iwc.Issue.ID] {
			continue
		}
		// Skip epics and internal types — focus on actionable work.
		if iwc.Issue.IssueType == "epic" || iwc.Issue.IssueType == "config" ||
			iwc.Issue.IssueType == "advice" || iwc.Issue.IssueType == "gate" {
			continue
		}
		result = append(result, readyWorkItem{
			id:       iwc.Issue.ID,
			title:    iwc.Issue.Title,
			priority: iwc.Issue.Priority,
		})
		if len(result) >= 8 {
			break
		}
	}
	return result
}

// buildSessionActivityDiff compares the current roster against the snapshot
// saved at session start, producing a summary of what changed during this
// agent's session (agents joined/left, task changes). (bd-3zsda)
func buildSessionActivityDiff(active, crashed []rpc.AgentRosterEntry) string {
	sessionID := getSessionID()
	if sessionID == "" {
		return ""
	}

	wd := getWorkDir()
	snapPath := filepath.Join(wd, ".runtime", "roster-snapshot", sessionID+".json")
	data, err := os.ReadFile(snapPath) //nolint:gosec // G304: path from controlled .runtime/ directory
	if err != nil {
		return "" // no snapshot — first session or file missing
	}

	type snapshotEntry struct {
		Actor     string `json:"actor"`
		TaskID    string `json:"task_id,omitempty"`
		TaskTitle string `json:"task_title,omitempty"`
	}
	type snapshot struct {
		Timestamp string          `json:"timestamp"`
		Agents    []snapshotEntry `json:"agents"`
	}

	var snap snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return ""
	}

	// Build lookup of agents at session start.
	startAgents := make(map[string]snapshotEntry)
	for _, a := range snap.Agents {
		startAgents[a.Actor] = a
	}

	// Build lookup of current agents (active + crashed).
	currentAgents := make(map[string]bool)
	for _, a := range active {
		currentAgents[a.Actor] = true
	}
	for _, a := range crashed {
		currentAgents[a.Actor] = true
	}

	// Compute diffs.
	var joined, left, taskChanged []string

	// Agents that are now present but weren't at session start.
	for _, a := range active {
		if _, existed := startAgents[a.Actor]; !existed {
			joined = append(joined, a.Actor)
		}
	}

	// Agents that were present at start but are now gone or crashed.
	for actor, startEntry := range startAgents {
		if !currentAgents[actor] {
			left = append(left, actor)
			continue
		}
		// Check if they switched tasks.
		for _, a := range active {
			if a.Actor == actor && a.TaskID != startEntry.TaskID {
				if startEntry.TaskID != "" && a.TaskID != "" {
					taskChanged = append(taskChanged, fmt.Sprintf("%s: %s → %s", actor, startEntry.TaskID, a.TaskID))
				} else if startEntry.TaskID == "" && a.TaskID != "" {
					taskChanged = append(taskChanged, fmt.Sprintf("%s: picked up %s", actor, a.TaskID))
				} else if startEntry.TaskID != "" && a.TaskID == "" {
					taskChanged = append(taskChanged, fmt.Sprintf("%s: finished %s", actor, startEntry.TaskID))
				}
				break
			}
		}
	}

	// Only show if something interesting happened.
	if len(joined) == 0 && len(left) == 0 && len(taskChanged) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session activity (since %s):\n", snap.Timestamp[:16]))
	if len(joined) > 0 {
		sb.WriteString(fmt.Sprintf("  Joined: %s\n", strings.Join(joined, ", ")))
	}
	if len(left) > 0 {
		sb.WriteString(fmt.Sprintf("  Left: %s\n", strings.Join(left, ", ")))
	}
	if len(taskChanged) > 0 {
		for _, tc := range taskChanged {
			sb.WriteString(fmt.Sprintf("  %s\n", tc))
		}
	}
	return sb.String()
}

// formatGateResults formats gate results as a human-readable string.
func formatGateResults(results []gate.GateResult) string {
	var parts []string
	for _, r := range results {
		status := "○"
		if r.Satisfied {
			status = "●"
		}
		parts = append(parts, fmt.Sprintf("%s %s", status, r.GateID))
	}
	return strings.Join(parts, ", ")
}
