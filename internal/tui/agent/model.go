// Package agent provides a Bubbletea TUI for live agent roster monitoring.
// It polls the daemon for presence data and displays agents with their
// current tool use, assigned issues, and epic groupings.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/steveyegge/beads/internal/rpc"
)

const pollInterval = 3 * time.Second

// ViewMode represents which view is active.
type ViewMode int

const (
	ViewAgents ViewMode = iota // Flat agent list
	ViewEpics                  // Epic-grouped view
)

// Pane identifies which pane has keyboard focus in split layout. (bd-i19bt)
type Pane int

const (
	PaneList   Pane = iota // Agent list (left)
	PaneDetail             // Detail panel (right)
)

// splitBreakpoint is the minimum terminal width for split-pane layout. (bd-i19bt)
const splitBreakpoint = 100

// SortMode determines how agents are ordered in the list. (bd-edx55)
type SortMode int

const (
	SortDefault  SortMode = iota // Server order (by last_seen)
	SortName                     // Alphabetical by actor name
	SortIdle                     // By idle time (most active first)
	SortRate                     // By event rate (highest first)
	sortModeCount                // Sentinel for cycling
)

// EpicGroup holds an epic and its agents.
type EpicGroup struct {
	EpicID    string
	EpicTitle string
	Agents    []rpc.AgentRosterEntry
	Expanded  bool
}

// Model is the Bubbletea model for the agent watch TUI.
type Model struct {
	// Dimensions
	width, height int

	// Data
	roster   *rpc.AgentRosterResult
	selected int

	// View
	viewMode   ViewMode
	epicGroups []EpicGroup // Computed from roster for epic view
	keys       KeyMap
	help       help.Model
	showHelp   bool
	viewport   viewport.Model
	err        error
	status     string

	// Polling
	done chan struct{}

	// Config
	staleThreshold int
	showAll        bool

	// Visible rows for navigation in epic view
	epicViewRows []epicViewRow

	// Split pane layout (bd-i19bt)
	focusPane   Pane // Which pane has keyboard focus
	listWidth   int  // Computed list pane width
	collapsed   bool // True when terminal < splitBreakpoint
	expandedIdx int  // Which agent expanded in collapsed mode (-1 = none)

	// Detail pane scroll
	detailViewport viewport.Model

	// Sort and filter (bd-edx55)
	sortMode SortMode
	filter   string // Name filter text (empty = show all)
	filtering bool  // True when filter input is active

	// Sorted/filtered view of actors
	displayActors []rpc.AgentRosterEntry

	// Overlay for bd show output (bd-edx55)
	overlay         string // Non-empty = overlay text visible
	overlayViewport viewport.Model

	// Recent events for selected agent (bd-1xgft)
	recentEvents      []rpc.AgentEvent // Cached recent events for selected agent
	recentEventsActor string           // Actor name of cached events
}

// epicViewRow represents one navigable row in the epic view.
type epicViewRow struct {
	isEpic   bool
	epicIdx  int
	agentIdx int // -1 for epic header rows
}

// New creates a new agent watch TUI model.
func New() *Model {
	h := help.New()
	h.ShowAll = false

	return &Model{
		keys:            DefaultKeyMap(),
		help:            h,
		viewport:        viewport.New(0, 0),
		detailViewport:  viewport.New(0, 0),
		overlayViewport: viewport.New(0, 0),
		done:            make(chan struct{}),
		staleThreshold:  3600,
		expandedIdx:     -1,
	}
}

// SetStaleThreshold sets the stale threshold in seconds.
func (m *Model) SetStaleThreshold(secs int) {
	m.staleThreshold = secs
}

// SetShowAll shows all actors regardless of staleness.
func (m *Model) SetShowAll(v bool) {
	m.showAll = v
}

// Init initializes the model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchRoster(),
		m.startPolling(),
		tea.SetWindowTitle("BD Agent Watch"),
	)
}

// fetchRosterMsg is sent when roster data arrives.
type fetchRosterMsg struct {
	roster *rpc.AgentRosterResult
	err    error
}

// fetchTaskMsg is sent when task detail arrives. (bd-edx55)
type fetchTaskMsg struct {
	output string
	err    error
}

// fetchRecentEventsMsg is sent when recent events arrive. (bd-1xgft)
type fetchRecentEventsMsg struct {
	actor  string
	events []rpc.AgentEvent
	err    error
}

// tickMsg is sent on each poll interval.
type tickMsg time.Time

// fetchRoster fetches the agent roster via bd agent roster --json.
func (m *Model) fetchRoster() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		args := []string{"agent", "roster", "--json"}
		if m.showAll {
			args = append(args, "--all")
		} else if m.staleThreshold > 0 {
			args = append(args, fmt.Sprintf("--stale=%d", m.staleThreshold))
		}

		cmd := exec.CommandContext(ctx, "bd", args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return fetchRosterMsg{err: fmt.Errorf("bd agent roster: %v (%s)", err, stderr.String())}
		}

		raw := stdout.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			return fetchRosterMsg{roster: &rpc.AgentRosterResult{}}
		}

		var result rpc.AgentRosterResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return fetchRosterMsg{err: fmt.Errorf("parse roster JSON: %w", err)}
		}

		return fetchRosterMsg{roster: &result}
	}
}

// startPolling starts the poll ticker.
func (m *Model) startPolling() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.collapsed = msg.Width < splitBreakpoint
		m.updatePaneGeometry()

	case tea.KeyMsg:
		// Overlay mode: any key dismisses the overlay
		if m.overlay != "" {
			if key.Matches(msg, m.keys.Quit) {
				m.overlay = ""
			} else if key.Matches(msg, m.keys.Up) || key.Matches(msg, m.keys.PageUp) {
				m.overlayViewport.ScrollUp(3)
			} else if key.Matches(msg, m.keys.Down) || key.Matches(msg, m.keys.PageDown) {
				m.overlayViewport.ScrollDown(3)
			} else {
				m.overlay = ""
			}
			return m, nil
		}

		// Filter input mode: capture keystrokes for filter text
		if m.filtering {
			switch {
			case key.Matches(msg, m.keys.Quit):
				m.filtering = false
				m.filter = ""
				m.rebuildDisplayActors()
			case msg.Type == tea.KeyEnter:
				m.filtering = false
				// Keep filter active
			case msg.Type == tea.KeyBackspace:
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
					m.rebuildDisplayActors()
				}
			default:
				if msg.Type == tea.KeyRunes {
					m.filter += string(msg.Runes)
					m.rebuildDisplayActors()
				}
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, m.keys.Quit):
			close(m.done)
			return m, tea.Quit

		case key.Matches(msg, m.keys.Help):
			m.showHelp = !m.showHelp

		case key.Matches(msg, m.keys.Up):
			if m.selected > 0 {
				m.selected--
			}

		case key.Matches(msg, m.keys.Down):
			maxIdx := m.maxSelectable()
			if m.selected < maxIdx {
				m.selected++
			}

		case key.Matches(msg, m.keys.PageUp):
			m.selected -= 10
			if m.selected < 0 {
				m.selected = 0
			}

		case key.Matches(msg, m.keys.PageDown):
			maxIdx := m.maxSelectable()
			m.selected += 10
			if m.selected > maxIdx {
				m.selected = maxIdx
			}

		case key.Matches(msg, m.keys.ToggleView):
			if m.viewMode == ViewAgents {
				m.viewMode = ViewEpics
			} else {
				m.viewMode = ViewAgents
			}
			m.selected = 0
			m.focusPane = PaneList
			m.buildEpicGroups()

		case key.Matches(msg, m.keys.Expand):
			if m.collapsed {
				// Toggle inline expansion in collapsed mode
				if m.expandedIdx == m.selected {
					m.expandedIdx = -1
				} else {
					m.expandedIdx = m.selected
				}
			} else if m.viewMode == ViewEpics && len(m.epicViewRows) > 0 && m.selected < len(m.epicViewRows) {
				row := m.epicViewRows[m.selected]
				if row.isEpic {
					m.epicGroups[row.epicIdx].Expanded = !m.epicGroups[row.epicIdx].Expanded
					m.rebuildEpicViewRows()
				}
			}

		case key.Matches(msg, m.keys.FocusLeft):
			m.focusPane = PaneList

		case key.Matches(msg, m.keys.FocusRight):
			if !m.collapsed {
				m.focusPane = PaneDetail
			}

		case key.Matches(msg, m.keys.NextWorking):
			m.jumpToWorking(1)

		case key.Matches(msg, m.keys.PrevWorking):
			m.jumpToWorking(-1)

		case key.Matches(msg, m.keys.Sort):
			m.sortMode = (m.sortMode + 1) % sortModeCount
			m.rebuildDisplayActors()
			m.status = fmt.Sprintf("Sort: %s", m.sortLabel())

		case key.Matches(msg, m.keys.Filter):
			m.filtering = true
			m.filter = ""

		case key.Matches(msg, m.keys.ShowTask):
			if a := m.selectedAgent(); a != nil && a.TaskID != "" {
				cmds = append(cmds, m.fetchTaskDetail(a.TaskID))
				m.status = fmt.Sprintf("Loading %s...", a.TaskID)
			}

		case key.Matches(msg, m.keys.Refresh):
			cmds = append(cmds, m.fetchRoster())
			m.status = "Refreshing..."
		}

		// Fetch recent events when selected agent changes. (bd-1xgft)
		if a := m.selectedAgent(); a != nil && a.Actor != m.recentEventsActor {
			cmds = append(cmds, m.fetchRecentEvents(a.Actor))
		}

	case fetchTaskMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.overlay = msg.output
			m.overlayViewport.SetContent(msg.output)
			m.overlayViewport.Width = m.width - 4
			m.overlayViewport.Height = m.height - 4
			m.overlayViewport.GotoTop()
		}

	case fetchRecentEventsMsg:
		if msg.err == nil && msg.actor != "" {
			m.recentEvents = msg.events
			m.recentEventsActor = msg.actor
		}

	case fetchRosterMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.err = nil
			m.roster = msg.roster
			m.rebuildDisplayActors()
			m.buildEpicGroups()
			m.status = fmt.Sprintf("Updated: %d agents", len(m.displayActors))
			// Re-fetch recent events for the selected agent on each poll cycle. (bd-1xgft)
			if a := m.selectedAgent(); a != nil {
				cmds = append(cmds, m.fetchRecentEvents(a.Actor))
			}
		}

	case tickMsg:
		cmds = append(cmds, m.fetchRoster())
		cmds = append(cmds, m.startPolling())
	}

	return m, tea.Batch(cmds...)
}

// maxSelectable returns the maximum selectable index for the current view.
func (m *Model) maxSelectable() int {
	if m.roster == nil {
		return 0
	}
	switch m.viewMode {
	case ViewEpics:
		if len(m.epicViewRows) == 0 {
			return 0
		}
		return len(m.epicViewRows) - 1
	default:
		if len(m.displayActors) == 0 {
			return 0
		}
		return len(m.displayActors) - 1
	}
}

// buildEpicGroups organizes agents into epic groups.
func (m *Model) buildEpicGroups() {
	if m.roster == nil {
		m.epicGroups = nil
		m.epicViewRows = nil
		return
	}

	epicMap := make(map[string]*EpicGroup)
	var noEpicAgents []rpc.AgentRosterEntry

	for _, a := range m.roster.Actors {
		if a.EpicID != "" {
			eg, ok := epicMap[a.EpicID]
			if !ok {
				eg = &EpicGroup{
					EpicID:    a.EpicID,
					EpicTitle: a.EpicTitle,
					Expanded:  true,
				}
				epicMap[a.EpicID] = eg
			}
			eg.Agents = append(eg.Agents, a)
		} else {
			noEpicAgents = append(noEpicAgents, a)
		}
	}

	// Preserve expansion state from previous groups
	oldExpanded := make(map[string]bool)
	for _, eg := range m.epicGroups {
		oldExpanded[eg.EpicID] = eg.Expanded
	}

	// Build sorted list of epic groups
	var groups []EpicGroup
	for _, eg := range epicMap {
		if expanded, ok := oldExpanded[eg.EpicID]; ok {
			eg.Expanded = expanded
		}
		groups = append(groups, *eg)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].EpicID < groups[j].EpicID
	})

	// Add "No Epic" group at the end if there are unassigned agents
	if len(noEpicAgents) > 0 {
		groups = append(groups, EpicGroup{
			EpicID:    "",
			EpicTitle: "Unassigned",
			Agents:    noEpicAgents,
			Expanded:  true,
		})
		if expanded, ok := oldExpanded[""]; ok {
			groups[len(groups)-1].Expanded = expanded
		}
	}

	m.epicGroups = groups
	m.rebuildEpicViewRows()
}

// rebuildEpicViewRows rebuilds the flat list of navigable rows for epic view.
func (m *Model) rebuildEpicViewRows() {
	var rows []epicViewRow
	for i, eg := range m.epicGroups {
		rows = append(rows, epicViewRow{isEpic: true, epicIdx: i, agentIdx: -1})
		if eg.Expanded {
			for j := range eg.Agents {
				rows = append(rows, epicViewRow{isEpic: false, epicIdx: i, agentIdx: j})
			}
		}
	}
	m.epicViewRows = rows

	// Clamp selected
	if m.selected >= len(rows) {
		m.selected = max(0, len(rows)-1)
	}
}

// rebuildDisplayActors applies sort and filter to build the display list. (bd-edx55)
func (m *Model) rebuildDisplayActors() {
	if m.roster == nil {
		m.displayActors = nil
		return
	}

	// Filter
	actors := m.roster.Actors
	if m.filter != "" {
		var filtered []rpc.AgentRosterEntry
		lower := strings.ToLower(m.filter)
		for _, a := range actors {
			if strings.Contains(strings.ToLower(a.Actor), lower) {
				filtered = append(filtered, a)
			}
		}
		actors = filtered
	}

	// Sort
	sorted := make([]rpc.AgentRosterEntry, len(actors))
	copy(sorted, actors)

	switch m.sortMode {
	case SortName:
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].Actor < sorted[j].Actor
		})
	case SortIdle:
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].IdleSecs < sorted[j].IdleSecs
		})
	case SortRate:
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].EventsPerMin > sorted[j].EventsPerMin
		})
	}

	m.displayActors = sorted

	// Clamp selection
	if m.selected >= len(m.displayActors) {
		m.selected = max(0, len(m.displayActors)-1)
	}
}

// sortLabel returns a human-readable label for the current sort mode. (bd-edx55)
func (m *Model) sortLabel() string {
	switch m.sortMode {
	case SortName:
		return "name"
	case SortIdle:
		return "idle (active first)"
	case SortRate:
		return "event rate"
	default:
		return "default"
	}
}

// selectedAgent returns the currently selected agent, or nil. (bd-edx55)
func (m *Model) selectedAgent() *rpc.AgentRosterEntry {
	if len(m.displayActors) == 0 || m.selected >= len(m.displayActors) {
		return nil
	}
	return &m.displayActors[m.selected]
}

// jumpToWorking jumps to the next (dir=1) or previous (dir=-1) working agent. (bd-edx55)
func (m *Model) jumpToWorking(dir int) {
	if len(m.displayActors) == 0 {
		return
	}
	start := m.selected
	for i := 1; i < len(m.displayActors); i++ {
		idx := (start + i*dir + len(m.displayActors)) % len(m.displayActors)
		a := m.displayActors[idx]
		if a.TaskID != "" && a.IdleSecs < 30 && !a.Reaped && a.LastEvent != "AgentCrashed" {
			m.selected = idx
			return
		}
	}
}

// fetchTaskDetail runs bd show <taskID> and returns the output. (bd-edx55)
func (m *Model) fetchTaskDetail(taskID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "bd", "show", taskID)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return fetchTaskMsg{err: fmt.Errorf("bd show %s: %v (%s)", taskID, err, stderr.String())}
		}
		return fetchTaskMsg{output: stdout.String()}
	}
}

// fetchRecentEvents fetches recent events for the given actor via bd agent recent-events --json. (bd-1xgft)
func (m *Model) fetchRecentEvents(actor string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "bd", "agent", "recent-events", actor, "--json", "--limit=20")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return fetchRecentEventsMsg{actor: actor, err: fmt.Errorf("bd agent recent-events: %v", err)}
		}

		var result rpc.AgentRecentEventsResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			return fetchRecentEventsMsg{actor: actor, err: fmt.Errorf("parse recent events: %w", err)}
		}

		return fetchRecentEventsMsg{actor: actor, events: result.Events}
	}
}

// updatePaneGeometry recalculates pane dimensions after resize. (bd-i19bt)
func (m *Model) updatePaneGeometry() {
	if m.collapsed {
		// Single pane: full width
		m.viewport.Width = m.width - 2
		m.viewport.Height = m.height - 8
		m.listWidth = m.width
	} else {
		// Split pane: list on left, detail on right
		if m.width >= 150 {
			m.listWidth = 40
		} else {
			m.listWidth = 35
		}
		detailWidth := m.width - m.listWidth - 3 // 3 for border + padding
		if detailWidth < 20 {
			detailWidth = 20
		}
		contentHeight := m.height - 6 // title + stats + help + status
		m.viewport.Width = m.listWidth
		m.viewport.Height = contentHeight
		m.detailViewport.Width = detailWidth
		m.detailViewport.Height = contentHeight
	}
}

// View renders the TUI.
func (m *Model) View() string {
	return m.renderView()
}
