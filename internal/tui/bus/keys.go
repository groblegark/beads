package bus

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines the key bindings for the bus watch TUI.
type KeyMap struct {
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding

	// Actions
	Pause   key.Binding // Pause/resume auto-scroll
	Refresh key.Binding
	Detail  key.Binding // Show full JSON detail of selected event

	// Filtering
	FilterStream key.Binding // Cycle through stream filters
	FilterActor  key.Binding // Set actor filter (text input)
	FilterSearch key.Binding // Set keyword search filter
	FilterClear  key.Binding // Clear all filters

	// Views
	Stats key.Binding // Toggle stats view

	// Search navigation
	NextMatch key.Binding // Jump to next search match
	PrevMatch key.Binding // Jump to previous search match

	// Clipboard
	Copy key.Binding // Copy selected event JSON to clipboard

	// General
	Help key.Binding
	Quit key.Binding
}

// DefaultKeyMap returns the default key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("\u2191/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("\u2193/j", "down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+d"),
			key.WithHelp("pgdn", "page down"),
		),
		Home: key.NewBinding(
			key.WithKeys("home", "g"),
			key.WithHelp("g/home", "first event"),
		),
		End: key.NewBinding(
			key.WithKeys("end", "G"),
			key.WithHelp("G/end", "latest event"),
		),
		Pause: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "pause/resume"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r", "R"),
			key.WithHelp("r", "reconnect"),
		),
		Detail: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "detail view"),
		),
		FilterStream: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "filter stream"),
		),
		FilterActor: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "filter actor"),
		),
		FilterSearch: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "search keyword"),
		),
		FilterClear: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "clear filters"),
		),
		Stats: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "stats view"),
		),
		NextMatch: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "next match"),
		),
		PrevMatch: key.NewBinding(
			key.WithKeys("N"),
			key.WithHelp("N", "prev match"),
		),
		Copy: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "copy event JSON"),
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
	return []key.Binding{k.Up, k.Down, k.Pause, k.Detail, k.Help, k.Quit}
}

// FullHelp returns key bindings for the full help view.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.Home, k.End, k.Pause, k.Detail, k.Stats},
		{k.FilterStream, k.FilterActor, k.FilterSearch, k.FilterClear},
		{k.NextMatch, k.PrevMatch, k.Copy, k.Refresh},
		{k.Help, k.Quit},
	}
}
