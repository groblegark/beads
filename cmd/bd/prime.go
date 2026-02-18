package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads"
	internalbeads "github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

// isDaemonAutoSyncing checks if daemon is running with auto-commit and auto-push enabled.
// Returns false if daemon is not running or check fails (fail-safe to show full protocol).
// This is a variable to allow stubbing in tests.
var isDaemonAutoSyncing = func() bool {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return false
	}

	socketPath := filepath.Join(beadsDir, "bd.sock")
	client, err := rpc.TryConnectAuto(socketPath)
	if err != nil || client == nil {
		return false
	}
	defer func() { _ = client.Close() }()

	status, err := client.Status()
	if err != nil {
		return false
	}

	// Only check auto-commit and auto-push (auto-pull is separate)
	return status.AutoCommit && status.AutoPush
}

var (
	primeFullMode    bool
	primeMCPMode     bool
	primeStealthMode bool
	primeExportMode  bool
	primeCaptainMode bool
	primeForAgent    string
	primeNoAdvice    bool
)

var primeCmd = &cobra.Command{
	Use:     "prime",
	GroupID: "setup",
	Short:   "Output AI-optimized workflow context",
	Long: `Output essential Beads workflow context in AI-optimized markdown format.

Automatically detects if MCP server is active and adapts output:
- MCP mode: Brief workflow reminders (~50 tokens)
- CLI mode: Full command reference (~1-2k tokens)

Designed for Claude Code hooks (SessionStart, PreCompact) to prevent
agents from forgetting bd workflow after context compaction.

Config options:
- no-git-ops: When true, outputs stealth mode (no git commands in session close protocol).
  Set via: bd config set no-git-ops true
  Useful when you want to control when commits happen manually.

Workflow customization:
- Place a .beads/PRIME.md file to override the default output entirely.
- Use --export to dump the default content for customization.`,
	Run: func(cmd *cobra.Command, args []string) {
		// Find .beads/ directory (supports both database and JSONL-only mode)
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			// Not in a beads project - silent exit with success
			// CRITICAL: No stderr output, exit 0
			// This enables cross-platform hook integration
			os.Exit(0)
		}

		// Captain mode: --captain flag or BD_ACTOR contains "captain"
		captainMode := primeCaptainMode || isCaptainActor()
		if captainMode {
			if err := outputCaptainContext(os.Stdout); err != nil {
				os.Exit(0)
			}
			return
		}

		// Detect MCP mode (unless overridden by flags)
		mcpMode := isMCPActive()
		if primeFullMode {
			mcpMode = false
		}
		if primeMCPMode {
			mcpMode = true
		}

		// Check for stealth mode: flag OR config (GH#593)
		// This allows users to disable git ops in session close protocol via config
		stealthMode := primeStealthMode || config.GetBool("no-git-ops")

		// Check for custom PRIME.md override (unless --export flag)
		// This allows users to fully customize workflow instructions
		// Check local .beads/ first (even if redirected), then redirected location
		if !primeExportMode {
			localPrimePath := filepath.Join(".beads", "PRIME.md")
			redirectedPrimePath := filepath.Join(beadsDir, "PRIME.md")

			// Try local first (user's clone-specific customization)
			// #nosec G304 -- path is relative to cwd
			if content, err := os.ReadFile(localPrimePath); err == nil {
				fmt.Print(string(content))
				return
			}
			// Fall back to redirected location (shared customization)
			// #nosec G304 -- path is constructed from beadsDir which we control
			if content, err := os.ReadFile(redirectedPrimePath); err == nil {
				fmt.Print(string(content))
				return
			}
		}

		// Output workflow context (adaptive based on MCP and stealth mode)
		if err := outputPrimeContext(os.Stdout, mcpMode, stealthMode); err != nil {
			// Suppress all errors - silent exit with success
			// Never write to stderr (breaks Windows compatibility)
			os.Exit(0)
		}

		// Append matching advice if not suppressed and not in export mode. (beads-it2j)
		if !primeNoAdvice && !primeExportMode {
			agentID := resolvePrimeAgentID(primeForAgent)
			if agentID != "" {
				outputAdviceSection(os.Stdout, agentID)
			}
		}

		// Append active agent roster to prevent work duplication. (bd-rzec3)
		if !primeExportMode {
			outputRosterSection(os.Stdout)
		}
	},
}

func init() {
	primeCmd.Flags().BoolVar(&primeFullMode, "full", false, "Force full CLI output (ignore MCP detection)")
	primeCmd.Flags().BoolVar(&primeMCPMode, "mcp", false, "Force MCP mode (minimal output)")
	primeCmd.Flags().BoolVar(&primeStealthMode, "stealth", false, "Stealth mode (no git operations, flush only)")
	primeCmd.Flags().BoolVar(&primeExportMode, "export", false, "Output default content (ignores PRIME.md override)")
	primeCmd.Flags().BoolVar(&primeCaptainMode, "captain", false, "Captain mode (supervisor workflow for AI captain agents)")
	primeCmd.Flags().StringVar(&primeForAgent, "for", "", "Agent ID to inject matching advice for")
	primeCmd.Flags().BoolVar(&primeNoAdvice, "no-advice", false, "Suppress advice injection")
	rootCmd.AddCommand(primeCmd)
}

// isMCPActive detects if MCP server is currently active
func isMCPActive() bool {
	// Get home directory with fallback
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback to HOME environment variable
		home = os.Getenv("HOME")
		if home == "" {
			// Can't determine home directory, assume no MCP
			return false
		}
	}

	settingsPath := filepath.Join(home, ".claude/settings.json")
	// #nosec G304 -- settings path derived from user home directory
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}

	// Check mcpServers section for beads
	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	if !ok {
		return false
	}

	// Look for beads server (any key containing "beads")
	for key := range mcpServers {
		if strings.Contains(strings.ToLower(key), "beads") {
			return true
		}
	}

	return false
}

// isEphemeralBranch detects if current branch has no upstream (ephemeral/local-only)
var isEphemeralBranch = func() bool {
	// git rev-parse --abbrev-ref --symbolic-full-name @{u}
	// Returns error code 128 if no upstream configured
	rc, err := internalbeads.GetRepoContext()
	if err != nil {
		return true // Default to ephemeral if we can't determine context
	}
	cmd := rc.GitCmdCWD(context.Background(), "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return cmd.Run() != nil
}

// primeHasGitRemote detects if any git remote is configured (stubbable for tests)
var primeHasGitRemote = func() bool {
	rc, err := internalbeads.GetRepoContext()
	if err != nil {
		return false
	}
	cmd := rc.GitCmdCWD(context.Background(), "remote")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// getRedirectNotice returns a notice string if beads is redirected
func getRedirectNotice(verbose bool) string {
	redirectInfo := beads.GetRedirectInfo()
	if !redirectInfo.IsRedirected {
		return ""
	}

	if verbose {
		return fmt.Sprintf(`> ⚠️ **Redirected**: Local .beads → %s
> You share issues with other clones using this redirect.

`, redirectInfo.TargetDir)
	}
	return fmt.Sprintf("**Note**: Beads redirected to %s (shared with other clones)\n\n", redirectInfo.TargetDir)
}

// outputPrimeContext outputs workflow context in markdown format
func outputPrimeContext(w io.Writer, mcpMode bool, stealthMode bool) error {
	if mcpMode {
		return outputMCPContext(w, stealthMode)
	}
	return outputCLIContext(w, stealthMode)
}

// outputMCPContext outputs minimal context for MCP users
func outputMCPContext(w io.Writer, stealthMode bool) error {
	ephemeral := isEphemeralBranch()
	noPush := config.GetBool("no-push")
	localOnly := !primeHasGitRemote()

	// beads-6mbp: bd sync removed. Dolt handles sync automatically.
	var closeProtocol string
	if stealthMode || localOnly {
		closeProtocol = "Before saying \"done\": beads changes are saved automatically (dolt backend)"
	} else if ephemeral {
		closeProtocol = "Before saying \"done\": git status → git add → git commit (no push - ephemeral branch; beads auto-synced)"
	} else if noPush {
		closeProtocol = "Before saying \"done\": git status → git add → git commit (push disabled; beads auto-synced)"
	} else {
		closeProtocol = "Before saying \"done\": git status → git add → git commit → git push (beads auto-synced)"
	}

	redirectNotice := getRedirectNotice(false)

	context := `# Beads Issue Tracker Active

` + redirectNotice + `# 🚨 SESSION CLOSE PROTOCOL 🚨

` + closeProtocol + `

## Core Rules
- **Default**: Use beads for ALL task tracking (` + "`bd create`" + `, ` + "`bd ready`" + `, ` + "`bd close`" + `)
- **Prohibited**: Do NOT use TodoWrite, TaskCreate, or markdown files for task tracking
- **Workflow**: Create beads issue BEFORE writing code, mark in_progress when starting
- Persistence you don't need beats lost context

Start: Run ` + "`bd news`" + ` to check for conflicts, then ` + "`bd ready`" + ` for available work.
`
	_, _ = fmt.Fprint(w, context)
	return nil
}

// isCaptainActor checks if the current actor name suggests a captain role.
var isCaptainActor = func() bool {
	actor := os.Getenv("BD_ACTOR")
	if actor == "" {
		actor = os.Getenv("BEADS_ACTOR")
	}
	return strings.Contains(strings.ToLower(actor), "captain")
}

// outputCaptainContext outputs workflow context for an AI captain agent.
func outputCaptainContext(w io.Writer) error {
	context := `# Captain Workflow Context

You are a **captain** — an intelligent supervisor for a team of free agents.
Your job is to watch for decisions from worker agents, reason about them,
and respond with thoughtful guidance.

## Your Core Loop

1. **Watch** for new decisions from agents:
   ` + "```bash" + `
   bd decision captain watch --timeout 5m
   ` + "```" + `

2. **Review** the decision — read the prompt, context, and options carefully.
   Think about what the agent should do next. Consider:
   - Is the agent's work complete or should they continue?
   - Are they going in the right direction?
   - Do they need to pivot to something else?
   - Should you escalate to the human?

3. **Respond** with your chosen option and rationale:
   ` + "```bash" + `
   bd decision captain respond <decision-id> --select=<option-id> --text="Your reasoning"
   ` + "```" + `

4. **Optionally send follow-up instructions** via inbox:
   ` + "```bash" + `
   bd decision captain send <agent-name> "Focus on X next"
   ` + "```" + `

5. **Repeat** — go back to step 1.

## Situational Awareness

Check what your agents are working on:
` + "```bash" + `
bd news                          # See in-progress work by others
bd list --status=in_progress     # All active work
bd decision captain list         # All pending decisions
` + "```" + `

## Stale Decision Cleanup

Periodically sweep stale decisions from dead agent sessions:
` + "```bash" + `
bd decision captain sweep              # Resolve decisions older than 30m
bd decision captain sweep --age 1h     # Custom age threshold
bd decision captain sweep --dry-run    # Preview without acting
` + "```" + `

## Work Management

Create and assign work for your agents:
` + "```bash" + `
bd create --title="..." --type=task --priority=2     # Create work
bd update <id> --assignee=<agent-name>               # Assign to specific agent
bd ready                                              # See unblocked work
` + "```" + `

## Key Principles

- **Reason, don't follow rules.** You are an intelligent agent. Read the context
  and make a judgment call, not a pattern match.
- **Trust your agents.** They are autonomous. Only intervene when they are stuck
  or going in the wrong direction.
- **Escalate when unsure.** If a decision is beyond your scope or requires human
  judgment, say so in your response text.
- **Keep agents productive.** The default should be to keep agents working unless
  there is a good reason to stop.
- **Communicate via inbox.** Use ` + "`bd decision captain send`" + ` to give agents
  additional context or redirect their work.
`
	_, _ = fmt.Fprint(w, context)
	return nil
}

// outputCLIContext outputs full CLI reference for non-MCP users
func outputCLIContext(w io.Writer, stealthMode bool) error {
	ephemeral := isEphemeralBranch()
	noPush := config.GetBool("no-push")
	localOnly := !primeHasGitRemote()

	var closeProtocol string
	var closeNote string
	var syncSection string
	var completingWorkflow string
	var gitWorkflowRule string

	// beads-6mbp: bd sync removed. Dolt handles sync automatically.
	if stealthMode || localOnly {
		closeProtocol = `[ ] bd close <ids>             (close completed issues)`
		syncSection = `### Sync & Collaboration
- Dolt backend handles synchronization automatically
- ` + "`bd export`" + ` - Export to JSONL (manual)
- ` + "`bd import`" + ` - Import from JSONL (manual)`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
` + "```"
		if localOnly && !stealthMode {
			closeNote = "**Note:** No git remote configured. Issues are saved locally only."
			gitWorkflowRule = "Git workflow: local-only (no git remote)"
		} else {
			gitWorkflowRule = "Git workflow: stealth mode (no git ops)"
		}
	} else if ephemeral {
		closeProtocol = `[ ] 1. git status              (check what changed)
[ ] 2. git add <files>         (stage code changes)
[ ] 3. git commit -m "..."     (commit code changes)`
		closeNote = "**Note:** Ephemeral branch (no upstream). Beads synced automatically by dolt."
		syncSection = `### Sync & Collaboration
- Dolt backend handles beads synchronization automatically`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
git add . && git commit -m "..."  # Commit your changes
# Merge to main when ready (local merge, not push)
` + "```"
		gitWorkflowRule = "Git workflow: beads auto-synced by dolt"
	} else if noPush {
		closeProtocol = `[ ] 1. git status              (check what changed)
[ ] 2. git add <files>         (stage code changes)
[ ] 3. git commit -m "..."     (commit code)`
		closeNote = "**Note:** Push disabled via config. Run `git push` manually when ready. Beads synced automatically by dolt."
		syncSection = `### Sync & Collaboration
- Dolt backend handles beads synchronization automatically`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
# git push                  # Run manually when ready
` + "```"
		gitWorkflowRule = "Git workflow: beads auto-synced by dolt (push disabled)"
	} else {
		closeProtocol = `[ ] 1. git status              (check what changed)
[ ] 2. git add <files>         (stage code changes)
[ ] 3. git commit -m "..."     (commit code)
[ ] 4. git push                (push to remote)`
		closeNote = "**NEVER skip this.** Work is not done until pushed."
		syncSection = `### Sync & Collaboration
- Dolt backend handles beads synchronization automatically`
		completingWorkflow = `**Completing work:**
` + "```bash" + `
bd close <id1> <id2> ...    # Close all completed issues at once
git push                    # Push to remote
` + "```"
		gitWorkflowRule = "Git workflow: beads auto-synced by dolt"
	}

	redirectNotice := getRedirectNotice(true)

	context := `# Beads Workflow Context

> **Context Recovery**: Run ` + "`bd prime`" + ` after compaction, clear, or new session
> Hooks auto-call this in Claude Code when .beads/ detected

` + redirectNotice + `# 🚨 SESSION CLOSE PROTOCOL 🚨

**CRITICAL**: Before saying "done" or "complete", you MUST run this checklist:

` + "```" + `
` + closeProtocol + `
` + "```" + `

` + closeNote + `

## Core Rules
- **Default**: Use beads for ALL task tracking (` + "`bd create`" + `, ` + "`bd ready`" + `, ` + "`bd close`" + `)
- **Prohibited**: Do NOT use TodoWrite, TaskCreate, or markdown files for task tracking
- **Workflow**: Create beads issue BEFORE writing code, mark in_progress when starting
- Persistence you don't need beats lost context
- ` + gitWorkflowRule + `
- Session management: check ` + "`bd ready`" + ` for available work

## Essential Commands

### Finding Work
- ` + "`bd ready`" + ` - Show issues ready to work (no blockers)
- ` + "`bd news`" + ` - Show in-progress work by others (check for conflicts before starting)
- ` + "`bd list --status=open`" + ` - All open issues
- ` + "`bd list --status=in_progress`" + ` - Your active work
- ` + "`bd show <id>`" + ` - Detailed issue view with dependencies

### Creating & Updating
- ` + "`bd create --title=\"...\" --type=task|bug|feature --priority=2`" + ` - New issue
  - Priority: 0-4 or P0-P4 (0=critical, 2=medium, 4=backlog). NOT "high"/"medium"/"low"
- ` + "`bd update <id> --status=in_progress`" + ` - Claim work
- ` + "`bd update <id> --assignee=username`" + ` - Assign to someone
- ` + "`bd update <id> --title/--description/--notes/--design`" + ` - Update fields inline
- ` + "`bd close <id>`" + ` - Mark complete
- ` + "`bd close <id1> <id2> ...`" + ` - Close multiple issues at once (more efficient)
- ` + "`bd close <id> --reason=\"explanation\"`" + ` - Close with reason
- **Tip**: When creating multiple issues/tasks/epics, use parallel subagents for efficiency
- **WARNING**: Do NOT use ` + "`bd edit`" + ` - it opens $EDITOR (vim/nano) which blocks agents

### Dependencies & Blocking
- ` + "`bd dep add <issue> <depends-on>`" + ` - Add dependency (issue depends on depends-on)
- ` + "`bd blocked`" + ` - Show all blocked issues
- ` + "`bd show <id>`" + ` - See what's blocking/blocked by this issue

` + syncSection + `

### Project Health
- ` + "`bd stats`" + ` - Project statistics (open/closed/blocked counts)
- ` + "`bd doctor`" + ` - Check for issues (sync problems, missing hooks)

### Configuration
- ` + "`bd config list-beads`" + ` - List config beads (hook prompts, MCP settings, etc.)
- ` + "`bd config show-bead <id>`" + ` - View a config bead's data
- ` + "`bd config explain <category>`" + ` - Docs for a config category (e.g. claude-hooks)

## Common Workflows

**Starting work:**
` + "```bash" + `
bd news            # Check what others are working on (avoid conflicts)
bd ready           # Find available work
bd show <id>       # Review issue details
bd update <id> --status=in_progress  # Claim it
` + "```" + `

` + completingWorkflow + `

**Creating dependent work:**
` + "```bash" + `
# Run bd create commands in parallel (use subagents for many items)
bd create --title="Implement feature X" --type=feature
bd create --title="Write tests for X" --type=task
bd dep add beads-yyy beads-xxx  # Tests depend on Feature (Feature blocks tests)
` + "```" + `

## Human Decisions

When you need human input (approval, choices, clarification), use decision points:

` + "```bash" + `
# Create a decision point — blocks until human responds (default --wait=true)
bd decision create --prompt="Deploy to production?" \
  --options='[{"id":"y","label":"Yes, deploy"},{"id":"n","label":"No, abort"}]'
# Blocks until responded. Use --json to get machine-readable output.
` + "```" + `

**Decision commands:**
- ` + "`bd decision create --prompt=\"...\" --options='[...]'`" + ` - Create and wait for response (blocks)
- ` + "`bd decision list`" + ` - Show pending decisions
- ` + "`bd decision show <id>`" + ` - Decision details
`
	_, _ = fmt.Fprint(w, context)
	return nil
}

// resolvePrimeAgentID resolves the agent identity for advice injection.
// Checks --for flag first, then BD_ACTOR / BEADS_ACTOR env vars.
func resolvePrimeAgentID(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if actor := os.Getenv("BD_ACTOR"); actor != "" {
		return actor
	}
	if actor := os.Getenv("BEADS_ACTOR"); actor != "" {
		return actor
	}
	return ""
}

// outputAdviceSection fetches matching advice beads for the given agent and
// appends them to the prime output. Creates its own daemon connection because
// prime is in noDbCommands (no daemon init). (beads-it2j)
func outputAdviceSection(w io.Writer, agentID string) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}

	socketPath := filepath.Join(beadsDir, "bd.sock")
	client, err := rpc.TryConnectAuto(socketPath)
	if err != nil || client == nil {
		return
	}
	defer func() { _ = client.Close() }()

	// Build subscriptions for this agent (no store needed — derives from ID structure).
	subscriptions := buildAgentSubscriptionsWithoutStore(agentID, nil)

	// Fetch all open advice via daemon RPC.
	listArgs := &rpc.ListArgs{
		IssueType: "advice",
		Status:    "open",
	}
	resp, err := client.List(listArgs)
	if err != nil || !resp.Success {
		return
	}

	var issuesWithCounts []*types.IssueWithCounts
	if err := json.Unmarshal(resp.Data, &issuesWithCounts); err != nil {
		return
	}

	// Build labels map and filter by subscriptions.
	labelsMap := make(map[string][]string)
	issues := make([]*types.Issue, len(issuesWithCounts))
	for i, iwc := range issuesWithCounts {
		issues[i] = iwc.Issue
		if iwc.Issue != nil && len(iwc.Issue.Labels) > 0 {
			labelsMap[iwc.Issue.ID] = iwc.Issue.Labels
		}
	}

	var matched []*advicePreviewItem
	for _, issue := range issues {
		issueLabels := labelsMap[issue.ID]
		if matchesSubscriptions(issue, issueLabels, subscriptions) {
			matchedLabels := findMatchedLabels(issueLabels, subscriptions)
			matched = append(matched, &advicePreviewItem{
				Issue:         issue,
				MatchedLabels: matchedLabels,
			})
		}
	}

	if len(matched) == 0 {
		return
	}

	// Group by scope and render in raw format.
	groups := groupByScope(matched)

	fmt.Fprintf(w, "\n## Advice (%d items)\n\n", len(matched))
	for _, group := range groups {
		for _, item := range group.Items {
			fmt.Fprintf(w, "**[%s]** %s\n", group.Header, item.Issue.Title)
			if item.Issue.Description != "" && item.Issue.Description != item.Issue.Title {
				lines := strings.Split(item.Issue.Description, "\n")
				for _, line := range lines {
					fmt.Fprintf(w, "  %s\n", line)
				}
			}
			_, _ = fmt.Fprintln(w)
		}
	}
}

// outputRosterSection appends a live agent roster to the prime output.
// Shows active agents, their current task/epic, and idle time so the
// receiving agent can avoid picking up work already in progress. (bd-rzec3)
//
// Agents idle > 10 minutes without a last_event of Stop are shown as stale.
// Crashed/reaped agents are labeled "Crashed"; others are labeled "Stale".
// Agents whose last event was Stop are excluded entirely (cleanly stopped). (bd-bstid, bd-bs7u8)
//
// Connects to the daemon RPC (same pattern as outputAdviceSection).
// Fails silently if daemon is unavailable or no agents are active.
func outputRosterSection(w io.Writer) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}

	socketPath := filepath.Join(beadsDir, "bd.sock")
	client, err := rpc.TryConnectAuto(socketPath)
	if err != nil || client == nil {
		return
	}
	defer func() { _ = client.Close() }()

	result, err := client.AgentRoster(&rpc.AgentRosterArgs{
		StaleThresholdSecs: 1800, // 30 minutes — fetch broadly, filter below
	})
	if err != nil || result == nil || len(result.Actors) == 0 {
		return
	}

	// Resolve the current agent's name so we can label their entry. (bd-snjni)
	self := resolvePrimeAgentID("")

	// Partition into active vs stale/dead. (bd-bstid, bd-khlpu, bd-bs7u8)
	// - Reaped agents (marked by reaper) → stale
	// - Agents whose last event was AgentCrashed → stale (crashed before reaper runs)
	// - Agents whose last event was Stop and idle >60s → excluded (cleanly stopped)
	// - Agents idle >10min → stale (fallback for pre-reaper detection)
	const staleThresholdSecs = 600 // 10 minutes
	var active, stale []rpc.AgentRosterEntry
	for _, a := range result.Actors {
		if a.Reaped || a.LastEvent == "AgentCrashed" {
			stale = append(stale, a)
			continue
		}
		isStopped := a.LastEvent == "Stop" && a.IdleSecs > 60
		if isStopped {
			continue // cleanly stopped — don't show at all
		}
		if a.IdleSecs > staleThresholdSecs {
			stale = append(stale, a)
		} else {
			active = append(active, a)
		}
	}

	if len(active) == 0 && len(stale) == 0 {
		return
	}

	fmt.Fprintf(w, "\n## Active Agents (%d)\n\n", len(active))
	if self != "" {
		fmt.Fprintf(w, "You are **%s**. Do not pick up other agents' in-progress tasks.\n\n", self)
	} else {
		_, _ = fmt.Fprintf(w, "Do not pick up other agents' in-progress tasks.\n\n")
	}

	for _, a := range active {
		idleStr := formatIdleDuration(a.IdleSecs)
		youTag := ""
		if self != "" && a.Actor == self {
			youTag = " ← you"
		}

		if a.TaskID != "" {
			epicStr := ""
			if a.EpicTitle != "" {
				epicStr = fmt.Sprintf(" (epic: %s)", a.EpicTitle)
			}
			fmt.Fprintf(w, "- **%s**%s — working on %s: %s%s (idle %s)\n",
				a.Actor, youTag, a.TaskID, a.TaskTitle, epicStr, idleStr)
		} else {
			fmt.Fprintf(w, "- **%s**%s — active, no claimed task (idle %s)\n",
				a.Actor, youTag, idleStr)
		}
	}

	// Show stale agents with their actual state so agents know whether they
	// crashed, were reaped, or are just idle. (bd-bstid, bd-bs7u8)
	if len(stale) > 0 {
		// Partition stale into crashed vs merely idle.
		var crashed, idle []string
		for _, a := range stale {
			idleStr := formatIdleDuration(a.IdleSecs)
			entry := fmt.Sprintf("%s (idle %s)", a.Actor, idleStr)
			if a.Reaped || a.LastEvent == "AgentCrashed" {
				crashed = append(crashed, entry)
			} else {
				idle = append(idle, entry)
			}
		}
		if len(crashed) > 0 {
			fmt.Fprintf(w, "\n_Crashed (%d): %s_\n", len(crashed), strings.Join(crashed, ", "))
		}
		if len(idle) > 0 {
			fmt.Fprintf(w, "\n_Stale (%d, likely disconnected): %s_\n", len(idle), strings.Join(idle, ", "))
		}
	}

	_, _ = fmt.Fprintln(w, "")
}

// formatIdleDuration is defined in agent.go (same package).
