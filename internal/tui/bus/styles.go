package bus

import "github.com/charmbracelet/lipgloss"

// Ayu-themed adaptive colors (matching internal/tui/agent/styles.go)
var (
	colorPass   = lipgloss.AdaptiveColor{Light: "#86b300", Dark: "#c2d94c"}
	colorWarn   = lipgloss.AdaptiveColor{Light: "#f2ae49", Dark: "#ffb454"}
	colorFail   = lipgloss.AdaptiveColor{Light: "#f07171", Dark: "#f07178"}
	colorMuted  = lipgloss.AdaptiveColor{Light: "#828c99", Dark: "#6c7680"}
	colorAccent = lipgloss.AdaptiveColor{Light: "#399ee6", Dark: "#59c2ff"}
	colorWhite  = lipgloss.AdaptiveColor{Light: "#5c6166", Dark: "#bfbdb6"}
	colorPurple = lipgloss.AdaptiveColor{Light: "#d2a6ff", Dark: "#d2a6ff"}
	colorTeal   = lipgloss.AdaptiveColor{Light: "#4cbf99", Dark: "#95e6cb"}
	colorOrange = lipgloss.AdaptiveColor{Light: "#ff8f40", Dark: "#ff8f40"}
)

// TUI styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent)

	selectedStyle = lipgloss.NewStyle().
			Background(lipgloss.AdaptiveColor{Light: "#e8e8e8", Dark: "#2a2e33"}).
			Foreground(colorWhite).
			Bold(true)

	mutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	statusStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorFail)

	tsStyle = lipgloss.NewStyle().
		Foreground(colorMuted)

	seqStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	detailKeyStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	detailValStyle = lipgloss.NewStyle().
			Foreground(colorWhite)

	pausedStyle = lipgloss.NewStyle().
			Foreground(colorWarn).
			Bold(true)

	filterStyle = lipgloss.NewStyle().
			Foreground(colorTeal).
			Bold(true)

	inputStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	// JSON syntax highlighting styles (adaptive for light/dark terminals)
	jsonKeyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#399ee6", Dark: "#59c2ff"}) // cyan — keys

	jsonStringStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#86b300", Dark: "#c2d94c"}) // green — strings

	jsonNumberStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#d2a6ff", Dark: "#d2a6ff"}) // purple — numbers

	jsonBoolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#ff8f40", Dark: "#ff8f40"}) // orange — bools/null

	jsonBraceStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#828c99", Dark: "#6c7680"}) // muted — braces/brackets

	copySuccessStyle = lipgloss.NewStyle().
				Foreground(colorPass).
				Bold(true)

	searchMatchStyle = lipgloss.NewStyle().
				Background(lipgloss.AdaptiveColor{Light: "#ffeebb", Dark: "#4a3800"}).
				Foreground(lipgloss.AdaptiveColor{Light: "#5c6166", Dark: "#ffb454"})

	searchInfoStyle = lipgloss.NewStyle().
			Foreground(colorWarn).
			Bold(true)
)

// eventTypeStyle returns a style for a given event type.
// Error/failure types get red, start/create types get green,
// stop/end types get muted, everything else is white.
func eventTypeStyle(evtType string) lipgloss.Style {
	switch evtType {
	// Failure / error types — red
	case "PostToolUseFailure", "OjJobFailed", "AgentCrashed",
		"DecisionExpired", "MutationDelete", "FormulaDeleted",
		"ConfigUnset", "advice.deleted":
		return lipgloss.NewStyle().Foreground(colorFail)

	// Start / create types — green
	case "SessionStart", "AgentStarted", "OjJobCreated",
		"DecisionCreated", "MutationCreate", "MailSent",
		"ConfigSet", "FormulaSaved", "advice.created",
		"SubagentStart", "OjAgentSpawned":
		return lipgloss.NewStyle().Foreground(colorPass)

	// Warning / escalation types — orange/yellow
	case "DecisionEscalated", "OjAgentEscalated", "Stop",
		"PreCompact":
		return lipgloss.NewStyle().Foreground(colorWarn)

	// End / stop types — muted
	case "SessionEnd", "AgentStopped", "AgentIdle",
		"SubagentStop", "OjAgentIdle", "AgentHeartbeat",
		"OjWorkerPollComplete":
		return lipgloss.NewStyle().Foreground(colorMuted)

	// Default — white
	default:
		return lipgloss.NewStyle().Foreground(colorWhite)
	}
}

// severityBadge returns a short colored badge for error/warning events, or "".
func severityBadge(evtType string) string {
	switch evtType {
	case "PostToolUseFailure", "OjJobFailed", "AgentCrashed":
		return lipgloss.NewStyle().Foreground(colorFail).Bold(true).Render("ERR")
	case "DecisionExpired":
		return lipgloss.NewStyle().Foreground(colorFail).Render("EXP")
	case "DecisionEscalated", "OjAgentEscalated":
		return lipgloss.NewStyle().Foreground(colorWarn).Bold(true).Render("ESC")
	case "Stop":
		return lipgloss.NewStyle().Foreground(colorWarn).Render("STP")
	default:
		return ""
	}
}

// streamColor returns a style for a given stream name.
func streamStyle(stream string) lipgloss.Style {
	switch stream {
	case "hooks":
		return lipgloss.NewStyle().Foreground(colorAccent)
	case "decisions":
		return lipgloss.NewStyle().Foreground(colorPurple)
	case "oj":
		return lipgloss.NewStyle().Foreground(colorOrange)
	case "agents":
		return lipgloss.NewStyle().Foreground(colorPass)
	case "mail":
		return lipgloss.NewStyle().Foreground(colorTeal)
	case "mutations":
		return lipgloss.NewStyle().Foreground(colorWarn)
	case "config":
		return lipgloss.NewStyle().Foreground(colorWhite)
	case "inbox":
		return lipgloss.NewStyle().Foreground(colorPurple).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(colorMuted)
	}
}
