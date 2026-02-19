package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/steveyegge/beads/internal/rpc"
)

const maxToolNameLen = 40

// renderView renders the entire view.
func (m *Model) renderView() string {
	var b strings.Builder

	if m.width < 40 || m.height < 10 {
		return "Terminal too small. Please resize."
	}

	// Title bar
	viewLabel := "Agents"
	if m.viewMode == ViewEpics {
		viewLabel = "Epics"
	}
	b.WriteString(titleStyle.Render(fmt.Sprintf("Agent Watch [%s]", viewLabel)))

	// Summary stats
	if m.roster != nil && len(m.roster.Actors) > 0 {
		stats := fmt.Sprintf("  %d active, %d working, %d idle",
			len(m.roster.Actors), m.roster.Working, m.roster.Idle)
		if len(m.roster.UnclaimedTasks) > 0 {
			stats += fmt.Sprintf(", %d unclaimed", len(m.roster.UnclaimedTasks))
		}
		stats += fmt.Sprintf(", uptime %s", m.roster.Uptime)
		b.WriteString(mutedStyle.Render(stats))
	}
	b.WriteString("\n")

	// Error
	if m.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
		b.WriteString("\n")
	}

	// Empty state
	if m.roster == nil || len(m.roster.Actors) == 0 {
		b.WriteString("\n")
		b.WriteString(statusStyle.Render("No active agents."))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("Watching for agents... (press q to quit)"))
		b.WriteString("\n")
		return b.String()
	}

	// Main content: split or collapsed layout (bd-i19bt)
	if m.collapsed || m.viewMode == ViewEpics {
		// Collapsed mode or epic view: single pane
		switch m.viewMode {
		case ViewAgents:
			b.WriteString(m.renderCollapsedAgentList())
		case ViewEpics:
			b.WriteString(m.renderEpicView())
		}
	} else {
		// Split layout: list on left, detail on right
		b.WriteString(m.renderSplitView())
	}

	// Status line
	if m.status != "" {
		b.WriteString(statusStyle.Render(m.status))
		b.WriteString("\n")
	}

	// Help
	if m.showHelp {
		b.WriteString("\n")
		b.WriteString(m.help.View(m.keys))
	} else {
		if m.collapsed {
			b.WriteString(helpStyle.Render("j/k: navigate  enter: expand  tab: view  r: refresh  ?: help  q: quit"))
		} else {
			b.WriteString(helpStyle.Render("j/k: navigate  h/l: panes  tab: view  r: refresh  ?: help  q: quit"))
		}
	}
	b.WriteString("\n")

	return b.String()
}

// renderSplitView renders the split-pane layout: list on left, detail on right. (bd-i19bt)
func (m *Model) renderSplitView() string {
	listContent := m.renderListPane()
	detailContent := m.renderDetailPane()

	// Join horizontally with a border between
	return lipgloss.JoinHorizontal(lipgloss.Top,
		listPaneBorder.Width(m.listWidth).Render(listContent),
		detailContent,
	) + "\n"
}

// renderListPane renders the compact agent list for the left pane. (bd-i19bt)
func (m *Model) renderListPane() string {
	var b strings.Builder

	// Limit entries to fit within available height (2 lines per agent)
	contentHeight := m.height - 6
	if contentHeight < 4 {
		contentHeight = 4
	}
	maxAgents := contentHeight / 2

	for i, a := range m.roster.Actors {
		if i >= maxAgents {
			remaining := len(m.roster.Actors) - maxAgents
			b.WriteString(mutedStyle.Render(fmt.Sprintf("  ... +%d more", remaining)))
			b.WriteString("\n")
			break
		}
		selected := i == m.selected
		b.WriteString(m.renderCompactAgentInList(a, selected, m.listWidth))
		b.WriteString("\n")
	}

	return b.String()
}

// renderCollapsedAgentList renders the agent list in collapsed mode with
// inline expansion for the selected agent. (bd-i19bt)
func (m *Model) renderCollapsedAgentList() string {
	var b strings.Builder

	sep := strings.Repeat("─", max(0, m.width-2))
	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	for i, a := range m.roster.Actors {
		selected := i == m.selected
		b.WriteString(m.renderCompactAgent(a, selected))
		b.WriteString("\n")

		// Inline expansion in collapsed mode
		if m.expandedIdx == i {
			b.WriteString(m.renderInlineDetail(a))
			b.WriteString("\n")
		}
	}

	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	return b.String()
}

// renderDetailPane renders the right-side detail panel for the selected agent. (bd-i19bt, bd-czaca)
func (m *Model) renderDetailPane() string {
	if m.roster == nil || len(m.roster.Actors) == 0 || m.selected >= len(m.roster.Actors) {
		return mutedStyle.Render("  No agent selected")
	}

	a := m.roster.Actors[m.selected]
	return m.renderAgentDetail(a)
}

// renderAgentDetail renders the full detail view for a single agent. (bd-czaca)
func (m *Model) renderAgentDetail(a rpc.AgentRosterEntry) string {
	var b strings.Builder
	detailWidth := m.width - m.listWidth - 5
	if detailWidth < 20 {
		detailWidth = 20
	}

	// Section 1: Identity header
	b.WriteString("  ")
	b.WriteString(detailTitleStyle.Render(a.Actor))
	b.WriteString("\n\n")

	// Section 2: Status bar
	stateLabel, stateDot := agentStateLabel(a)
	b.WriteString(fmt.Sprintf("  %s %s", stateDot, stateLabel))
	rate := fmt.Sprintf("  %.1f/min", a.EventsPerMin)
	events := fmt.Sprintf("  %d events", a.EventCount)
	b.WriteString(mutedStyle.Render(rate))
	b.WriteString(mutedStyle.Render(events))
	b.WriteString("\n")

	dur := formatDuration(a.SessionDurationSecs)
	sessionLine := fmt.Sprintf("  Session: %s", dur)
	if a.SessionID != "" {
		sid := a.SessionID
		if len(sid) > 8 {
			sid = sid[:8] + "..."
		}
		sessionLine += fmt.Sprintf("  ID: %s", sid)
	}
	b.WriteString(mutedStyle.Render(sessionLine))
	b.WriteString("\n")

	// Section 3: Current Work
	if a.TaskID != "" {
		b.WriteString("\n")
		b.WriteString(sectionHeaderStyle.Render("  ── Current Work "))
		sep := strings.Repeat("─", max(0, detailWidth-18))
		b.WriteString(mutedStyle.Render(sep))
		b.WriteString("\n")

		b.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Task:"), accentStyle.Render(a.TaskID)))
		if a.TaskTitle != "" {
			// Wrap title to detail width
			b.WriteString(fmt.Sprintf("  %s\n", detailValueStyle.Render(wrapText(a.TaskTitle, detailWidth-4))))
		}

		if a.EpicID != "" {
			b.WriteString(fmt.Sprintf("\n  %s %s\n", detailLabelStyle.Render("Epic:"), epicStyle.Render(a.EpicID)))
			if a.EpicTitle != "" {
				b.WriteString(fmt.Sprintf("  %s\n", epicStyle.Render(wrapText(a.EpicTitle, detailWidth-4))))
			}
		}
	}

	// Section 4: Git Context
	if a.Repo != "" || a.Branch != "" || a.ProjectRoot != "" {
		b.WriteString("\n")
		b.WriteString(sectionHeaderStyle.Render("  ── Git "))
		sep := strings.Repeat("─", max(0, detailWidth-9))
		b.WriteString(mutedStyle.Render(sep))
		b.WriteString("\n")

		if a.Branch != "" {
			b.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Branch:"), detailValueStyle.Render(a.Branch)))
		}
		if a.Repo != "" {
			b.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Repo:"), detailValueStyle.Render(a.Repo)))
		}
		if a.ProjectRoot != "" {
			root := a.ProjectRoot
			if len(root) > detailWidth-10 {
				root = "..." + root[len(root)-(detailWidth-13):]
			}
			b.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Root:"), mutedStyle.Render(root)))
		}
	}

	// Section 5: Activity
	b.WriteString("\n")
	b.WriteString(sectionHeaderStyle.Render("  ── Activity "))
	sep := strings.Repeat("─", max(0, detailWidth-14))
	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	if a.ToolName != "" {
		b.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Tool:"), toolStyle.Render(a.ToolName)))
	}
	if a.LastEvent != "" {
		b.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Last event:"), detailValueStyle.Render(a.LastEvent)))
	}
	idle := formatDuration(a.IdleSecs)
	b.WriteString(fmt.Sprintf("  %s %s\n", detailLabelStyle.Render("Idle:"), agentStateStyle(a.IdleSecs).Render(idle)))

	// Section 6: Unclaimed Tasks (bd-s6fiy)
	if m.roster != nil && len(m.roster.UnclaimedTasks) > 0 {
		b.WriteString("\n")
		b.WriteString(m.renderUnclaimedTasks(detailWidth))
	}

	return b.String()
}

// renderUnclaimedTasks renders the unclaimed tasks section for the detail panel. (bd-s6fiy)
func (m *Model) renderUnclaimedTasks(width int) string {
	var b strings.Builder

	count := len(m.roster.UnclaimedTasks)
	header := fmt.Sprintf("  ── Unclaimed Tasks (%d) ", count)
	sepLen := width - len(header) + 4 // account for ANSI in header
	if sepLen < 0 {
		sepLen = 0
	}
	b.WriteString(sectionHeaderStyle.Render(header))
	b.WriteString(mutedStyle.Render(strings.Repeat("─", sepLen)))
	b.WriteString("\n")

	maxTitleLen := width - 18 // room for priority + ID + padding
	if maxTitleLen < 15 {
		maxTitleLen = 15
	}

	for _, t := range m.roster.UnclaimedTasks {
		title := t.Title
		if len(title) > maxTitleLen {
			title = title[:maxTitleLen-1] + "…"
		}

		prio := fmt.Sprintf("P%d", t.Priority)
		var prioStyle lipgloss.Style
		switch {
		case t.Priority <= 1:
			prioStyle = staleStyle // red for P0/P1
		case t.Priority == 2:
			prioStyle = idleStyle // yellow/warn for P2
		default:
			prioStyle = mutedStyle // muted for P3+
		}

		b.WriteString(fmt.Sprintf("  %s %s %s\n",
			prioStyle.Render(prio),
			accentStyle.Render(t.ID),
			detailValueStyle.Render(title),
		))
	}

	return b.String()
}

// renderInlineDetail renders a condensed detail for collapsed-mode inline expansion. (bd-i19bt)
func (m *Model) renderInlineDetail(a rpc.AgentRosterEntry) string {
	var b strings.Builder

	stateLabel, stateDot := agentStateLabel(a)
	rate := fmt.Sprintf("%.1f/min", a.EventsPerMin)
	events := fmt.Sprintf("%d events", a.EventCount)
	dur := formatDuration(a.SessionDurationSecs)
	b.WriteString(fmt.Sprintf("   %s %s · %s · %s · %s\n", stateDot, stateLabel, rate, events, dur))

	if a.TaskID != "" {
		b.WriteString(fmt.Sprintf("   %s %s %s\n", detailLabelStyle.Render("Task:"), accentStyle.Render(a.TaskID), detailValueStyle.Render(a.TaskTitle)))
	}
	if a.EpicID != "" {
		b.WriteString(fmt.Sprintf("   %s %s %s\n", detailLabelStyle.Render("Epic:"), epicStyle.Render(a.EpicID), epicStyle.Render(a.EpicTitle)))
	}
	if a.Repo != "" || a.Branch != "" {
		b.WriteString(fmt.Sprintf("   %s %s", detailLabelStyle.Render("Repo:"), detailValueStyle.Render(a.Repo)))
		if a.Branch != "" {
			b.WriteString(fmt.Sprintf("  %s %s", detailLabelStyle.Render("Branch:"), detailValueStyle.Render(a.Branch)))
		}
		b.WriteString("\n")
	}
	if a.ToolName != "" {
		b.WriteString(fmt.Sprintf("   %s %s  %s %s\n",
			detailLabelStyle.Render("Tool:"), toolStyle.Render(a.ToolName),
			detailLabelStyle.Render("Last:"), detailValueStyle.Render(a.LastEvent)))
	}

	return b.String()
}

// agentStateLabel returns a human-readable state label and its colored dot. (bd-4be5t)
func agentStateLabel(a rpc.AgentRosterEntry) (string, string) {
	if a.Reaped || a.LastEvent == "AgentCrashed" {
		return crashedStyle.Render("Crashed"), dotCrashed
	}
	switch {
	case a.IdleSecs < 30 && a.TaskID != "":
		return workingStyle.Render("Working"), dotWorking
	case a.IdleSecs < 30:
		return accentStyle.Render("Active"), dotActive
	case a.IdleSecs < 300:
		return idleStyle.Render("Idle"), dotIdle
	default:
		return staleStyle.Render("Stale"), dotStale
	}
}

// wrapText wraps text at the given width, breaking on spaces.
func wrapText(text string, width int) string {
	if width <= 0 || len(text) <= width {
		return text
	}
	var lines []string
	for len(text) > width {
		// Find last space before width
		idx := strings.LastIndex(text[:width], " ")
		if idx <= 0 {
			idx = width
		}
		lines = append(lines, text[:idx])
		text = strings.TrimLeft(text[idx:], " ")
	}
	if text != "" {
		lines = append(lines, text)
	}
	return strings.Join(lines, "\n  ")
}

// renderEpicView renders the epic-grouped view.
func (m *Model) renderEpicView() string {
	var b strings.Builder

	sep := strings.Repeat("─", max(0, m.width-2))
	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	for rowIdx, row := range m.epicViewRows {
		selected := rowIdx == m.selected
		if row.isEpic {
			eg := m.epicGroups[row.epicIdx]
			b.WriteString(m.renderEpicHeader(eg, selected))
			b.WriteString("\n")
		} else {
			eg := m.epicGroups[row.epicIdx]
			agent := eg.Agents[row.agentIdx]
			b.WriteString(m.renderAgent(agent, selected, "  "))
			b.WriteString("\n")
		}
	}

	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	return b.String()
}

// renderEpicHeader renders an epic group header.
func (m *Model) renderEpicHeader(eg EpicGroup, selected bool) string {
	expand := "▸"
	if eg.Expanded {
		expand = "▾"
	}

	var working, idle int
	for _, a := range eg.Agents {
		if a.TaskID != "" {
			working++
		} else {
			idle++
		}
	}

	epicID := eg.EpicID
	if epicID == "" {
		epicID = "(none)"
	}

	title := eg.EpicTitle
	if len(title) > 50 {
		title = title[:47] + "..."
	}

	line := fmt.Sprintf(" %s %s %s  [%d agents: %d working, %d idle]",
		expand,
		epicStyle.Render(epicID),
		epicStyle.Render(title),
		len(eg.Agents), working, idle,
	)

	if selected {
		return selectedStyle.Render(line)
	}
	return line
}

// renderAgent renders a single agent entry with full detail.
func (m *Model) renderAgent(a rpc.AgentRosterEntry, selected bool, indent string) string {
	var b strings.Builder

	// Line 1: Actor name + idle + duration + rate + events + last event
	idle := formatDuration(a.IdleSecs)
	dur := formatDuration(a.SessionDurationSecs)
	rate := fmt.Sprintf("%.1f/m", a.EventsPerMin)

	stateColor := agentStateStyle(a.IdleSecs)
	idleStr := stateColor.Render(fmt.Sprintf("idle=%s", idle))

	line1 := fmt.Sprintf("%s %-20s  %s  dur=%-8s  rate=%-7s  events=%-5d  last=%s",
		indent, accentStyle.Render(a.Actor),
		idleStr,
		dur,
		rate,
		a.EventCount,
		a.LastEvent,
	)

	if selected {
		b.WriteString(selectedStyle.Render(line1))
	} else {
		b.WriteString(line1)
	}

	// Line 2: Tool name (full, not truncated within limits)
	if a.ToolName != "" {
		toolName := a.ToolName
		if len(toolName) > maxToolNameLen {
			toolName = toolName[:maxToolNameLen-3] + "..."
		}
		toolLine := fmt.Sprintf("%s %20s  tool=%s", indent, "", toolStyle.Render(toolName))
		b.WriteString("\n")
		b.WriteString(toolLine)
	}

	// Line 3: Task assignment with full description
	if a.TaskID != "" {
		taskTitle := a.TaskTitle
		maxTitleLen := m.width - len(indent) - 30
		if maxTitleLen < 20 {
			maxTitleLen = 20
		}
		if len(taskTitle) > maxTitleLen {
			taskTitle = taskTitle[:maxTitleLen-3] + "..."
		}
		taskLine := fmt.Sprintf("%s %20s  %s %s %s",
			indent, "",
			detailLabelStyle.Render("task="),
			accentStyle.Render(a.TaskID),
			detailValueStyle.Render(taskTitle),
		)
		b.WriteString("\n")
		b.WriteString(taskLine)

		// Line 4: Epic (if in agent view, show parent epic)
		if a.EpicID != "" {
			epicTitle := a.EpicTitle
			if len(epicTitle) > 50 {
				epicTitle = epicTitle[:47] + "..."
			}
			epicLine := fmt.Sprintf("%s %20s  %s %s %s",
				indent, "",
				detailLabelStyle.Render("epic="),
				epicStyle.Render(a.EpicID),
				epicStyle.Render(epicTitle),
			)
			b.WriteString("\n")
			b.WriteString(epicLine)
		}
	}

	return b.String()
}

// renderCompactAgentInList renders a compact 2-line agent entry for the split list pane
// with a width parameter so snippets don't overflow the pane. (bd-i19bt)
// Line 1: [▸] ● agent-name        5s
// Line 2:      tool hint or task snippet
func (m *Model) renderCompactAgentInList(a rpc.AgentRosterEntry, selected bool, paneWidth int) string {
	const nameWidth = 18

	dot := agentStateDot(a)

	name := a.Actor
	if len(name) > nameWidth {
		name = name[:nameWidth-1] + "…"
	}

	idle := formatDuration(a.IdleSecs)

	selector := " "
	if selected {
		selector = "▸"
	}

	line1 := fmt.Sprintf("%s %s %-*s  %s", selector, dot, nameWidth, accentStyle.Render(name), agentStateStyle(a.IdleSecs).Render(idle))

	// Line 2: tool hint, task snippet, or state
	maxSnippet := paneWidth - 6
	if maxSnippet < 10 {
		maxSnippet = 10
	}
	var line2 string
	switch {
	case a.Reaped || a.LastEvent == "AgentCrashed":
		line2 = fmt.Sprintf("    %s", crashedStyle.Render("(crashed)"))
	case a.ToolName != "":
		tool := a.ToolName
		if len(tool) > maxSnippet {
			tool = tool[:maxSnippet-1] + "…"
		}
		line2 = fmt.Sprintf("    %s", toolStyle.Render(tool))
	case a.TaskID != "":
		snippet := a.TaskTitle
		if len(snippet) > maxSnippet {
			snippet = snippet[:maxSnippet-1] + "…"
		}
		line2 = fmt.Sprintf("    %s", detailValueStyle.Render(snippet))
	default:
		line2 = fmt.Sprintf("    %s", mutedStyle.Render("(no task)"))
	}

	if selected {
		return selectedStyle.Width(paneWidth).Render(line1) + "\n" + selectedStyle.Width(paneWidth).Render(line2)
	}
	return line1 + "\n" + line2
}

// renderCompactAgent renders a compact 2-line agent entry for collapsed/full-width mode. (bd-4be5t)
// Line 1: state-dot + name (padded) + idle time (right-aligned)
// Line 2: indented task snippet or state label
func (m *Model) renderCompactAgent(a rpc.AgentRosterEntry, selected bool) string {
	const nameWidth = 18

	dot := agentStateDot(a)

	name := a.Actor
	if len(name) > nameWidth {
		name = name[:nameWidth-3] + "..."
	}

	idle := formatDuration(a.IdleSecs)

	line1 := fmt.Sprintf(" %s %-*s  %s", dot, nameWidth, accentStyle.Render(name), agentStateStyle(a.IdleSecs).Render(idle))

	var line2 string
	maxSnippet := m.width - 6
	if maxSnippet < 10 {
		maxSnippet = 10
	}
	switch {
	case a.Reaped || a.LastEvent == "AgentCrashed":
		line2 = fmt.Sprintf("   %s", crashedStyle.Render("(crashed)"))
	case a.TaskID != "":
		snippet := a.TaskTitle
		if len(snippet) > maxSnippet {
			snippet = snippet[:maxSnippet-3] + "..."
		}
		line2 = fmt.Sprintf("   %s", detailValueStyle.Render(snippet))
	default:
		line2 = fmt.Sprintf("   %s", mutedStyle.Render("(idle, no task)"))
	}

	if selected {
		return selectedStyle.Render(line1) + "\n" + selectedStyle.Render(line2)
	}
	return line1 + "\n" + line2
}

// formatDuration formats seconds into human-readable duration.
func formatDuration(secs float64) string {
	d := time.Duration(secs * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
