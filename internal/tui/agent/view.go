package agent

import (
	"fmt"
	"strings"
	"time"

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
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  %d active, %d working, %d idle, uptime %s",
			len(m.roster.Actors), m.roster.Working, m.roster.Idle, m.roster.Uptime)))
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

	// Main content
	switch m.viewMode {
	case ViewAgents:
		b.WriteString(m.renderAgentList())
	case ViewEpics:
		b.WriteString(m.renderEpicView())
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
		b.WriteString(helpStyle.Render("j/k: navigate  tab: toggle view  r: refresh  ?: help  q: quit"))
	}
	b.WriteString("\n")

	return b.String()
}

// renderAgentList renders the flat agent list view.
func (m *Model) renderAgentList() string {
	var b strings.Builder

	sep := strings.Repeat("─", max(0, m.width-2))
	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	for i, a := range m.roster.Actors {
		selected := i == m.selected
		b.WriteString(m.renderAgent(a, selected, ""))
		b.WriteString("\n")
	}

	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	return b.String()
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
