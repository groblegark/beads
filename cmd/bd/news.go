package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// newsWindow is the lookback window for recent activity (var for testing)
var newsWindow = 2 * time.Hour

// epicCollapseThreshold is the minimum number of children from the same epic
// before they get collapsed into a single epic summary line.
const epicCollapseThreshold = 3

// NewsOutput is the JSON output structure for bd news
type NewsOutput struct {
	InProgress     []*types.Issue `json:"in_progress"`
	RecentlyOpened []*types.Issue `json:"recently_opened"`
	RecentlyClosed []*types.Issue `json:"recently_closed"`
}

// newsLine represents a displayable line in bd news output.
// Either a single issue or a collapsed epic summary.
type newsLine struct {
	issue      *types.Issue   // the issue (or the epic for collapsed lines)
	collapsed  bool           // true if this represents a collapsed epic
	childCount int            // number of children collapsed into this line
	children   []*types.Issue // the collapsed children (for age calculation)
}

var newsCmd = &cobra.Command{
	Use:     "news",
	GroupID: "views",
	Short:   "Show recent activity and in-progress work by others",
	Long: `Show what's happening: in-progress work, recently opened, and recently closed issues.

Use this before starting work to get situational awareness:
- What's actively being worked on (potential conflicts)
- What was just opened (new work to consider)
- What was just closed (context on recent progress)

Issues by the current actor are excluded unless --all is specified.
When 3+ children of the same epic appear, they are collapsed into one line
showing the epic title and child count. Use --expand to see all individual issues.

Examples:
  bd news                    # Show recent activity by others (epics collapsed)
  bd news --expand           # Show all individual issues (no collapsing)
  bd news --all              # Include your own activity
  bd news --window 4h        # Look back 4 hours instead of default 2h
  bd news --limit 20         # Show more results per section
  bd news --agents           # Show agent activity summary for captain mode`,
	Run: func(cmd *cobra.Command, args []string) {
		limit, _ := cmd.Flags().GetInt("limit")
		showAll, _ := cmd.Flags().GetBool("all")
		expand, _ := cmd.Flags().GetBool("expand")
		windowStr, _ := cmd.Flags().GetString("window")
		showAgents, _ := cmd.Flags().GetBool("agents")

		// Parse custom window duration
		window := newsWindow
		if windowStr != "" {
			d, err := time.ParseDuration(windowStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: invalid --window duration %q (use e.g. 2h, 30m, 4h)\n", windowStr)
				os.Exit(1)
			}
			window = d
		}

		requireDaemon("news")

		if showAgents {
			runNewsAgents(window)
			return
		}

		cutoff := time.Now().UTC().Add(-window).Format(time.RFC3339)
		currentActor := getActor()

		// Fetch all three categories in sequence (daemon handles them fast)
		inProgress := fetchNewsList(daemonClient, &rpc.ListArgs{
			Status: "in_progress",
			Limit:  limit,
		})
		recentlyOpened := fetchNewsList(daemonClient, &rpc.ListArgs{
			CreatedAfter: cutoff,
			Limit:        limit,
		})
		recentlyClosed := fetchNewsList(daemonClient, &rpc.ListArgs{
			Status:      "closed",
			ClosedAfter: cutoff,
			Limit:       limit,
		})

		// Filter out current actor's work (unless --all)
		if !showAll {
			inProgress = filterOutActor(inProgress, currentActor)
			recentlyOpened = filterOutActor(recentlyOpened, currentActor)
			recentlyClosed = filterOutActor(recentlyClosed, currentActor)
		}

		// Deduplicate: remove in-progress and closed items from recently-opened
		excludeFromOpened := make(map[string]bool, len(inProgress)+len(recentlyClosed))
		for _, issue := range inProgress {
			excludeFromOpened[issue.ID] = true
		}
		for _, issue := range recentlyClosed {
			excludeFromOpened[issue.ID] = true
		}
		recentlyOpened = filterOutIDs(recentlyOpened, excludeFromOpened)

		// Filter out decision points from all sections
		inProgress = filterOutDecisions(inProgress)
		recentlyOpened = filterOutDecisions(recentlyOpened)
		recentlyClosed = filterOutDecisions(recentlyClosed)

		// Sort: most recently updated first (closed: by closed_at)
		sortByUpdatedDesc(inProgress)
		sortByUpdatedDesc(recentlyOpened)
		sortByClosedDesc(recentlyClosed)

		if jsonOutput {
			outputJSON(NewsOutput{
				InProgress:     coalesce(inProgress),
				RecentlyOpened: coalesce(recentlyOpened),
				RecentlyClosed: coalesce(recentlyClosed),
			})
			return
		}

		totalItems := len(inProgress) + len(recentlyOpened) + len(recentlyClosed)
		if totalItems == 0 {
			fmt.Printf("\n%s No recent activity%s\n\n",
				ui.RenderPass("✨"), windowSuffix(window))
			return
		}

		printed := false

		if len(inProgress) > 0 {
			lines := prepareNewsLines(inProgress, expand)
			fmt.Printf("\n%s In-progress by others (%d):\n\n",
				ui.RenderAccent("◐"), len(inProgress))
			for _, line := range lines {
				printNewsLine(line)
			}
			printed = true
		}

		if len(recentlyOpened) > 0 {
			if printed {
				fmt.Println()
			}
			lines := prepareNewsLines(recentlyOpened, expand)
			fmt.Printf("%s Opened in last %s (%d):\n\n",
				ui.RenderAccent("○"), formatDurationShort(window), len(recentlyOpened))
			for _, line := range lines {
				printNewsLine(line)
			}
			printed = true
		}

		if len(recentlyClosed) > 0 {
			if printed {
				fmt.Println()
			}
			lines := prepareNewsLines(recentlyClosed, expand)
			fmt.Printf("%s Closed in last %s (%d):\n\n",
				ui.RenderAccent("✓"), formatDurationShort(window), len(recentlyClosed))
			for _, line := range lines {
				printNewsLine(line)
			}
		}

		fmt.Printf("\n%s\n\n", ui.RenderMuted("Tip: Check for file overlap with in-progress work before starting on the same areas."))
	},
}

// fetchNewsList fetches issues from the daemon and returns them, exiting on error.
func fetchNewsList(client *rpc.Client, args *rpc.ListArgs) []*types.Issue {
	resp, err := client.List(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	var issues []*types.Issue
	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}
	return issues
}

// sortByUpdatedDesc sorts issues by UpdatedAt descending (most recent first).
func sortByUpdatedDesc(issues []*types.Issue) {
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		if a.UpdatedAt.After(b.UpdatedAt) {
			return -1
		}
		if a.UpdatedAt.Before(b.UpdatedAt) {
			return 1
		}
		return 0
	})
}

// sortByClosedDesc sorts issues by ClosedAt descending (most recently closed first).
// Falls back to UpdatedAt if ClosedAt is nil.
func sortByClosedDesc(issues []*types.Issue) {
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		aTime := a.UpdatedAt
		if a.ClosedAt != nil {
			aTime = *a.ClosedAt
		}
		bTime := b.UpdatedAt
		if b.ClosedAt != nil {
			bTime = *b.ClosedAt
		}
		if aTime.After(bTime) {
			return -1
		}
		if aTime.Before(bTime) {
			return 1
		}
		return 0
	})
}

// prepareNewsLines converts a list of issues into displayable lines,
// collapsing children of the same epic when there are >= epicCollapseThreshold.
// If expand is true, no collapsing is done.
func prepareNewsLines(issues []*types.Issue, expand bool) []newsLine {
	if expand || len(issues) == 0 {
		lines := make([]newsLine, len(issues))
		for i, issue := range issues {
			lines[i] = newsLine{issue: issue}
		}
		return lines
	}

	// Group children by their root epic ID
	epicChildren := make(map[string][]*types.Issue)
	epicIssue := make(map[string]*types.Issue) // root ID → epic issue (if present)
	var orphans []*types.Issue

	for _, issue := range issues {
		rootID, _, depth := types.ParseHierarchicalID(issue.ID)
		if depth == 0 {
			// This is a root-level issue (could be an epic or standalone)
			if issue.IssueType == types.TypeEpic {
				epicIssue[issue.ID] = issue
				// Also count the epic itself as appearing
				epicChildren[issue.ID] = append(epicChildren[issue.ID])
			} else {
				orphans = append(orphans, issue)
			}
		} else {
			// This is a child — group by root
			epicChildren[rootID] = append(epicChildren[rootID], issue)
		}
	}

	var lines []newsLine

	// Track which root IDs we've already emitted
	emitted := make(map[string]bool)

	// Walk original order to preserve sort; emit collapsed or individual
	for _, issue := range issues {
		rootID, _, depth := types.ParseHierarchicalID(issue.ID)

		if depth == 0 && issue.IssueType != types.TypeEpic {
			// Standalone non-epic issue — always show
			lines = append(lines, newsLine{issue: issue})
			continue
		}

		// For epics and children: check if this root should be collapsed
		if emitted[rootID] {
			continue // already handled
		}

		children := epicChildren[rootID]
		epic := epicIssue[rootID]

		if len(children) >= epicCollapseThreshold && epic != nil {
			// Collapse: show epic with child count
			emitted[rootID] = true
			lines = append(lines, newsLine{
				issue:      epic,
				collapsed:  true,
				childCount: len(children),
				children:   children,
			})
		} else if len(children) >= epicCollapseThreshold && epic == nil {
			// Children of an epic not in this list — synthesize a summary
			emitted[rootID] = true
			// Use first child's metadata as proxy, show root ID
			lines = append(lines, newsLine{
				issue: &types.Issue{
					ID:        rootID,
					Title:     fmt.Sprintf("(%d subtasks)", len(children)),
					Status:    children[0].Status,
					CreatedBy: children[0].CreatedBy,
					UpdatedAt: mostRecentUpdate(children),
				},
				collapsed:  true,
				childCount: len(children),
				children:   children,
			})
		} else {
			// Not enough children to collapse — show individually
			if depth == 0 {
				// Epic with < threshold children — show the epic itself
				lines = append(lines, newsLine{issue: issue})
				emitted[rootID] = true
			} else {
				// Individual child
				lines = append(lines, newsLine{issue: issue})
			}
		}
	}

	return lines
}

// mostRecentUpdate returns the most recent UpdatedAt time from a list of issues.
func mostRecentUpdate(issues []*types.Issue) time.Time {
	var latest time.Time
	for _, issue := range issues {
		if issue.UpdatedAt.After(latest) {
			latest = issue.UpdatedAt
		}
	}
	return latest
}

// printNewsLine prints a single news line (either a regular issue or a collapsed epic).
func printNewsLine(line newsLine) {
	if line.collapsed {
		printCollapsedEpic(line)
	} else {
		printNewsIssue(line.issue)
	}
}

// printCollapsedEpic prints a collapsed epic summary line.
func printCollapsedEpic(line newsLine) {
	issue := line.issue
	statusIcon := ui.RenderStatusIcon(string(issue.Status))

	age := formatRelativeTime(mostRecentUpdate(line.children))

	createdByStr := ""
	if issue.CreatedBy != "" {
		createdByStr = fmt.Sprintf(" by %s", ui.RenderMuted(issue.CreatedBy))
	}

	fmt.Printf("  %s %s  %s%s  %s %s\n",
		statusIcon,
		ui.RenderID(issue.ID),
		ui.RenderMuted(age),
		createdByStr,
		issue.Title,
		ui.RenderMuted(fmt.Sprintf("(+%d subtasks)", line.childCount)))
}

// filterOutActor removes issues where the assignee matches the current actor.
// Matching is case-insensitive and also checks suffix for role paths
// (e.g., "gastown/polecats/furiosa" matches actor "furiosa").
func filterOutActor(issues []*types.Issue, actor string) []*types.Issue {
	if actor == "" || actor == "unknown" {
		return issues
	}
	actorLower := strings.ToLower(actor)
	var filtered []*types.Issue
	for _, issue := range issues {
		assigneeLower := strings.ToLower(issue.Assignee)
		if assigneeLower == actorLower {
			continue
		}
		if strings.HasSuffix(assigneeLower, "/"+actorLower) {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}

// filterOutDecisions removes decision-point issues from the list.
func filterOutDecisions(issues []*types.Issue) []*types.Issue {
	var filtered []*types.Issue
	for _, issue := range issues {
		if issue.IssueType == "gate" || issue.IssueType == "decision" || strings.HasPrefix(issue.Title, "[DECISION]") {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}

// filterOutIDs removes issues whose ID is in the exclude set.
func filterOutIDs(issues []*types.Issue, exclude map[string]bool) []*types.Issue {
	var filtered []*types.Issue
	for _, issue := range issues {
		if !exclude[issue.ID] {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

// printNewsIssue prints a single issue in the bd news format.
func printNewsIssue(issue *types.Issue) {
	statusIcon := ui.RenderStatusIcon(string(issue.Status))

	assigneeStr := ui.RenderMuted("unassigned")
	if issue.Assignee != "" {
		assigneeStr = fmt.Sprintf("@%s", issue.Assignee)
	}

	// Use closed_at for closed issues, updated_at otherwise
	ageTime := issue.UpdatedAt
	if issue.Status == types.StatusClosed && issue.ClosedAt != nil {
		ageTime = *issue.ClosedAt
	}
	age := formatRelativeTime(ageTime)

	createdByStr := ""
	if issue.CreatedBy != "" {
		createdByStr = fmt.Sprintf(" by %s", ui.RenderMuted(issue.CreatedBy))
	}

	fmt.Printf("  %s %s  %s  %s%s  %s\n",
		statusIcon,
		ui.RenderID(issue.ID),
		assigneeStr,
		ui.RenderMuted(age),
		createdByStr,
		issue.Title)
}

// agentStatus represents the inferred activity status of an agent.
type agentStatus string

const (
	agentStatusActive  agentStatus = "active"
	agentStatusWorking agentStatus = "working"
	agentStatusStuck   agentStatus = "possibly stuck"
	agentStatusIdle    agentStatus = "idle"
)

// agentSummary holds the computed activity data for a single agent.
type agentSummary struct {
	Name            string         `json:"name"`
	InProgressCount int            `json:"in_progress_count"`
	PendingDecision bool           `json:"pending_decision"`
	LastDecisionAge *time.Duration `json:"last_decision_age,omitempty"`
	Status          agentStatus    `json:"status"`
}

// AgentsOutput is the JSON output structure for bd news --agents.
type AgentsOutput struct {
	Agents []agentSummary `json:"agents"`
	Window string         `json:"window"`
}

// runNewsAgents displays an agent activity summary for captain observability.
func runNewsAgents(window time.Duration) {
	// Fetch in-progress issues to find active agents
	inProgress := fetchNewsList(daemonClient, &rpc.ListArgs{
		Status: "in_progress",
		Limit:  200,
	})

	// Fetch pending decisions to find agents waiting for responses
	decisionResp, err := daemonClient.DecisionList(&rpc.DecisionListArgs{All: false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching decisions: %v\n", err)
		os.Exit(1)
	}

	// Build agent map: name → summary
	agents := make(map[string]*agentSummary)

	// Count in-progress work per agent (use assignee if set, else created_by)
	for _, issue := range inProgress {
		agentName := issue.Assignee
		if agentName == "" {
			agentName = issue.CreatedBy
		}
		if agentName == "" {
			continue
		}
		name := normalizeAgentName(agentName)
		if agents[name] == nil {
			agents[name] = &agentSummary{Name: name}
		}
		agents[name].InProgressCount++
	}

	// Track pending decisions and latest decision age per agent
	now := time.Now()
	for _, dr := range decisionResp.Decisions {
		if dr.Decision == nil || dr.Decision.RequestedBy == "" {
			continue
		}
		name := normalizeAgentName(dr.Decision.RequestedBy)
		if agents[name] == nil {
			agents[name] = &agentSummary{Name: name}
		}
		agents[name].PendingDecision = true
		age := now.Sub(dr.Decision.CreatedAt)
		if agents[name].LastDecisionAge == nil || age < *agents[name].LastDecisionAge {
			agents[name].LastDecisionAge = &age
		}
	}

	// Infer status for each agent
	for _, a := range agents {
		a.Status = inferAgentStatus(a)
	}

	// Sort agents: active first, then by name
	sorted := make([]agentSummary, 0, len(agents))
	for _, a := range agents {
		sorted = append(sorted, *a)
	}
	slices.SortFunc(sorted, func(a, b agentSummary) int {
		// Sort by status priority (active < working < stuck < idle)
		ap, bp := statusPriority(a.Status), statusPriority(b.Status)
		if ap != bp {
			return ap - bp
		}
		return strings.Compare(a.Name, b.Name)
	})

	if jsonOutput {
		outputJSON(AgentsOutput{
			Agents: sorted,
			Window: formatDurationShort(window),
		})
		return
	}

	if len(sorted) == 0 {
		fmt.Printf("\n%s No agent activity detected\n\n", ui.RenderPass("✨"))
		return
	}

	fmt.Printf("\n%s Active agents (%d):\n\n", ui.RenderAccent("👤"), len(sorted))

	// Calculate column widths for alignment
	maxNameLen := 0
	for _, a := range sorted {
		if len(a.Name) > maxNameLen {
			maxNameLen = len(a.Name)
		}
	}
	if maxNameLen < 8 {
		maxNameLen = 8
	}

	for _, a := range sorted {
		printAgentLine(a, maxNameLen)
	}

	fmt.Println()
}

// printAgentLine prints a single agent summary line.
func printAgentLine(a agentSummary, nameWidth int) {
	// Status icon
	var icon string
	switch a.Status {
	case agentStatusActive:
		icon = ui.RenderPass("●")
	case agentStatusWorking:
		icon = ui.RenderAccent("◐")
	case agentStatusStuck:
		icon = ui.RenderWarn("◌")
	default:
		icon = ui.RenderMuted("○")
	}

	// In-progress count
	workStr := fmt.Sprintf("%d in-progress", a.InProgressCount)

	// Decision info
	var decisionStr string
	if a.PendingDecision && a.LastDecisionAge != nil {
		decisionStr = fmt.Sprintf("awaiting decision %s", formatDurationShort(*a.LastDecisionAge))
	} else if a.LastDecisionAge != nil {
		decisionStr = fmt.Sprintf("last decision %s ago", formatDurationShort(*a.LastDecisionAge))
	}

	// Status label
	statusStr := ""
	if a.Status == agentStatusStuck {
		statusStr = ui.RenderWarn("(possibly stuck)")
	} else if a.Status == agentStatusIdle {
		statusStr = ui.RenderMuted("(idle)")
	}

	// Build the line
	parts := []string{workStr}
	if decisionStr != "" {
		parts = append(parts, decisionStr)
	}
	if statusStr != "" {
		parts = append(parts, statusStr)
	}

	fmt.Printf("  %s %-*s  %s\n", icon, nameWidth, a.Name, strings.Join(parts, ", "))
}

// inferAgentStatus infers an agent's status from their activity signals.
func inferAgentStatus(a *agentSummary) agentStatus {
	if a.PendingDecision && a.LastDecisionAge != nil {
		// Has a pending decision — how long has it been waiting?
		if *a.LastDecisionAge < 5*time.Minute {
			return agentStatusActive
		}
		if *a.LastDecisionAge < 30*time.Minute {
			return agentStatusWorking
		}
		return agentStatusStuck
	}

	if a.InProgressCount > 0 {
		// Has in-progress work but no pending decision — working autonomously
		return agentStatusWorking
	}

	return agentStatusIdle
}

// statusPriority returns a sort priority for agent status (lower = first).
func statusPriority(s agentStatus) int {
	switch s {
	case agentStatusActive:
		return 0
	case agentStatusWorking:
		return 1
	case agentStatusStuck:
		return 2
	case agentStatusIdle:
		return 3
	default:
		return 4
	}
}

// normalizeAgentName extracts the short agent name from a potentially
// path-qualified name (e.g., "gastown/polecats/furiosa" → "furiosa").
func normalizeAgentName(name string) string {
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// coalesce returns an empty slice instead of nil (for clean JSON output).
func coalesce(issues []*types.Issue) []*types.Issue {
	if issues == nil {
		return []*types.Issue{}
	}
	return issues
}

// formatDurationShort formats a duration as a human-friendly short string.
func formatDurationShort(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := d.Hours()
	if hours == float64(int(hours)) {
		return fmt.Sprintf("%dh", int(hours))
	}
	return fmt.Sprintf("%.1fh", hours)
}

// windowSuffix returns " (last Xh)" for the empty-state message.
func windowSuffix(d time.Duration) string {
	return fmt.Sprintf(" (last %s)", formatDurationShort(d))
}

func init() {
	newsCmd.Flags().IntP("limit", "n", 50, "Maximum issues to show per section")
	newsCmd.Flags().Bool("all", false, "Include your own activity")
	newsCmd.Flags().Bool("expand", false, "Show all individual issues (disable epic collapsing)")
	newsCmd.Flags().String("window", "", "Lookback window for recent activity (default 2h, e.g. 4h, 30m)")
	newsCmd.Flags().Bool("agents", false, "Show agent activity summary (for captain observability)")
	rootCmd.AddCommand(newsCmd)
}
