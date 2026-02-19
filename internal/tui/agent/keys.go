package agent

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines the key bindings for the agent watch TUI.
type KeyMap struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding

	// View modes
	ToggleView key.Binding // Switch between agent list and epic view
	Expand     key.Binding // Expand/collapse selected epic or inline detail

	// Pane focus (bd-i19bt)
	FocusLeft  key.Binding // Switch focus to list pane
	FocusRight key.Binding // Switch focus to detail pane

	// Agent navigation (bd-edx55)
	NextWorking key.Binding // Jump to next working agent
	PrevWorking key.Binding // Jump to previous working agent
	Sort        key.Binding // Cycle sort mode
	Filter      key.Binding // Start agent name filter
	ShowTask    key.Binding // Show selected agent's task details

	// Actions
	Refresh key.Binding

	// General
	Help key.Binding
	Quit key.Binding
}

// DefaultKeyMap returns the default key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+d"),
			key.WithHelp("pgdn", "page down"),
		),
		ToggleView: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "toggle view"),
		),
		Expand: key.NewBinding(
			key.WithKeys("enter", " "),
			key.WithHelp("enter", "expand/collapse"),
		),
		FocusLeft: key.NewBinding(
			key.WithKeys("h", "left"),
			key.WithHelp("h/←", "list pane"),
		),
		FocusRight: key.NewBinding(
			key.WithKeys("l", "right"),
			key.WithHelp("l/→", "detail pane"),
		),
		NextWorking: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "next working"),
		),
		PrevWorking: key.NewBinding(
			key.WithKeys("N"),
			key.WithHelp("N", "prev working"),
		),
		Sort: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "sort"),
		),
		Filter: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "filter"),
		),
		ShowTask: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "show task"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r", "R"),
			key.WithHelp("r", "refresh"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c", "esc"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns key bindings for the short help view.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.FocusLeft, k.FocusRight, k.NextWorking, k.Sort, k.Filter, k.ShowTask, k.Help, k.Quit}
}

// FullHelp returns key bindings for the full help view.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.FocusLeft, k.FocusRight, k.ToggleView, k.Expand},
		{k.NextWorking, k.PrevWorking, k.Sort, k.Filter, k.ShowTask},
		{k.Refresh, k.Help, k.Quit},
	}
}
