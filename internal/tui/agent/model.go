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
		keys:           DefaultKeyMap(),
		help:           h,
		viewport:       viewport.New(0, 0),
		done:           make(chan struct{}),
		staleThreshold: 3600,
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
		m.viewport.Width = msg.Width - 2
		m.viewport.Height = msg.Height - 8

	case tea.KeyMsg:
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
			m.buildEpicGroups()

		case key.Matches(msg, m.keys.Expand):
			if m.viewMode == ViewEpics && len(m.epicViewRows) > 0 && m.selected < len(m.epicViewRows) {
				row := m.epicViewRows[m.selected]
				if row.isEpic {
					m.epicGroups[row.epicIdx].Expanded = !m.epicGroups[row.epicIdx].Expanded
					m.rebuildEpicViewRows()
				}
			}

		case key.Matches(msg, m.keys.Refresh):
			cmds = append(cmds, m.fetchRoster())
			m.status = "Refreshing..."
		}

	case fetchRosterMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = fmt.Sprintf("Error: %v", msg.err)
		} else {
			m.err = nil
			m.roster = msg.roster
			m.buildEpicGroups()
			m.status = fmt.Sprintf("Updated: %d agents", len(msg.roster.Actors))
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
		if len(m.roster.Actors) == 0 {
			return 0
		}
		return len(m.roster.Actors) - 1
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

// View renders the TUI.
func (m *Model) View() string {
	return m.renderView()
}
