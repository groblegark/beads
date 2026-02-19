package agent

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/steveyegge/beads/internal/rpc"
)

// Ayu-themed adaptive colors (matching internal/ui/styles.go)
var (
	colorPass   = lipgloss.AdaptiveColor{Light: "#86b300", Dark: "#c2d94c"}
	colorWarn   = lipgloss.AdaptiveColor{Light: "#f2ae49", Dark: "#ffb454"}
	colorFail   = lipgloss.AdaptiveColor{Light: "#f07171", Dark: "#f07178"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#828c99", Dark: "#6c7680"}
	colorAccent = lipgloss.AdaptiveColor{Light: "#399ee6", Dark: "#59c2ff"}
	colorEpic   = lipgloss.AdaptiveColor{Light: "#d2a6ff", Dark: "#d2a6ff"}
	colorWhite  = lipgloss.AdaptiveColor{Light: "#5c6166", Dark: "#bfbdb6"}
)

// TUI styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	headerStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	selectedStyle = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "#e8e8e8", Dark: "#2a2e33"}).
			Foreground(colorWhite).
			Bold(true)

	normalStyle = lipgloss.NewStyle().
			Foreground(colorWhite)

	mutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	accentStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	epicStyle = lipgloss.NewStyle().
			Foreground(colorEpic).
			Bold(true)

	workingStyle = lipgloss.NewStyle().
			Foreground(colorPass)

	idleStyle = lipgloss.NewStyle().
			Foreground(colorWarn)

	staleStyle = lipgloss.NewStyle().
			Foreground(colorFail)

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#4cbf99", Dark: "#95e6cb"})

	statusStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorFail)

	// Detail pane styles
	detailLabelStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	detailValueStyle = lipgloss.NewStyle().
				Foreground(colorWhite)

	detailTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	// Compact list state dot styles (bd-4be5t)
	crashedStyle = lipgloss.NewStyle().
			Foreground(colorFail).
			Faint(true)

	// Pane border for split layout (bd-i19bt)
	listPaneBorder = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, true, false, false).
			BorderForeground(colorMuted)

	sectionHeaderStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Bold(true)
)

// State dot strings for compact list items. (bd-4be5t)
var (
	dotWorking = lipgloss.NewStyle().Foreground(colorPass).Render("●")
	dotActive  = lipgloss.NewStyle().Foreground(colorAccent).Render("●")
	dotIdle    = lipgloss.NewStyle().Foreground(colorWarn).Render("○")
	dotStale   = lipgloss.NewStyle().Foreground(colorFail).Render("◌")
	dotCrashed = lipgloss.NewStyle().Foreground(colorFail).Faint(true).Render("✕")
)

// agentStateStyle returns the style for an agent's idle duration.
func agentStateStyle(idleSecs float64) lipgloss.Style {
	switch {
	case idleSecs < 30:
		return workingStyle
	case idleSecs < 300:
		return idleStyle
	default:
		return staleStyle
	}
}

// agentStateDot returns the colored state indicator dot for an agent. (bd-4be5t)
func agentStateDot(a rpc.AgentRosterEntry) string {
	if a.Reaped || a.LastEvent == "AgentCrashed" {
		return dotCrashed
	}
	switch {
	case a.IdleSecs < 30 && a.TaskID != "":
		return dotWorking
	case a.IdleSecs < 30:
		return dotActive
	case a.IdleSecs < 300:
		return dotIdle
	default:
		return dotStale
	}
}
