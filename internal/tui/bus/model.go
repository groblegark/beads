// Package bus provides a Bubbletea TUI for live event bus monitoring.
// It connects to the daemon's SSE /bus/events endpoint and displays events
// in a scrollable timeline with color-coded streams and event summaries.
package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/steveyegge/beads/internal/rpc"
)

const defaultHistorySize = 2000

// inputTarget identifies what the text input is currently editing.
type inputTarget int

const (
	inputNone inputTarget = iota
	inputActor
	inputKeyword
)

// Model is the Bubbletea model for the bus watch TUI.
type Model struct {
	// Dimensions
	width, height int

	// Data — circular ring buffer of events
	history *RingBuffer

	// Navigation
	selected int  // cursor position (index into filtered view)
	paused   bool // when true, auto-scroll is disabled
	atBottom bool // true when cursor is at the latest event

	// Detail view
	showDetail     bool
	detailViewport viewport.Model

	// Filtering
	filter      Filter
	filtered    []int // indices into history that pass filter (rebuilt on change)
	streamPick  int   // index into allStreams for cycling stream filter

	// Text input mode
	inputMode   inputTarget // what we're editing (none = normal mode)
	inputBuf    string      // current text being typed
	inputPrompt string      // prompt shown to user

	// Connection
	sseOpts  rpc.BusSSEClientOptions
	connected bool
	cancel    context.CancelFunc
	sseCh     <-chan rpc.BusSSEEvent // SSE event channel (kept for chaining)
	sseErrCh  <-chan error           // SSE error channel
	sseCtx    context.Context

	// UI
	keys       KeyMap
	help       help.Model
	showHelp   bool
	err        error
	status     string
	copyStatus string // flash message after clipboard copy
}

// New creates a new bus watch TUI model.
func New(opts rpc.BusSSEClientOptions) *Model {
	h := help.New()
	h.ShowAll = false

	return &Model{
		history:        NewRingBuffer(defaultHistorySize),
		filter:         NewFilter(),
		atBottom:       true,
		sseOpts:        opts,
		keys:           DefaultKeyMap(),
		help:           h,
		detailViewport: viewport.New(0, 0),
	}
}

// SetHistorySize replaces the ring buffer with a new one of the given capacity.
func (m *Model) SetHistorySize(n int) {
	if n > 0 {
		m.history = NewRingBuffer(n)
	}
}

// sseEventMsg wraps a single BusSSEEvent arriving from the SSE goroutine.
type sseEventMsg struct {
	event rpc.BusSSEEvent
}

// sseErrorMsg signals an SSE connection error.
type sseErrorMsg struct {
	err error
}

// sseClosedMsg signals the SSE channel has closed.
type sseClosedMsg struct{}

// Init initializes the model and starts the SSE connection.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.startSSE(),
		tea.SetWindowTitle("BD Bus Watch"),
	)
}

// startSSE initiates the SSE connection and stores channels on the model.
func (m *Model) startSSE() tea.Cmd {
	// Cancel any existing connection
	if m.cancel != nil {
		m.cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.sseCtx = ctx

	events, errs := rpc.ConnectBusSSE(ctx, m.sseOpts)
	m.sseCh = events
	m.sseErrCh = errs
	m.connected = true
	m.err = nil
	m.status = "Connecting..."

	// Return a command that waits for the first event
	return m.waitForSSE()
}

// waitForSSE returns a tea.Cmd that waits for the next SSE event or error.
func (m *Model) waitForSSE() tea.Cmd {
	ch := m.sseCh
	errCh := m.sseErrCh
	ctx := m.sseCtx

	if ch == nil {
		return nil
	}

	return func() tea.Msg {
		select {
		case evt, ok := <-ch:
			if !ok {
				return sseClosedMsg{}
			}
			return sseEventMsg{event: evt}
		case err, ok := <-errCh:
			if !ok {
				return sseClosedMsg{}
			}
			return sseErrorMsg{err: err}
		case <-ctx.Done():
			return sseClosedMsg{}
		}
	}
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.detailViewport.Width = msg.Width - 4
		m.detailViewport.Height = msg.Height - 4

	case tea.KeyMsg:
		if m.inputMode != inputNone {
			return m.handleInputKeys(msg)
		}
		if m.showDetail {
			return m.handleDetailKeys(msg)
		}
		return m.handleTimelineKeys(msg)

	case sseEventMsg:
		m.appendEvent(msg.event)
		m.status = fmt.Sprintf("Events: %d (buf: %d/%d)", m.history.Total(), m.history.Len(), m.history.Cap())
		// Chain: wait for next event
		return m, m.waitForSSE()

	case sseErrorMsg:
		m.err = msg.err
		m.connected = false
		m.status = fmt.Sprintf("SSE error: %v", msg.err)

	case sseClosedMsg:
		m.connected = false
		m.status = "Disconnected — press r to reconnect"
	}

	return m, nil
}

// handleTimelineKeys handles key presses in the main timeline view.
func (m *Model) handleTimelineKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.showHelp = !m.showHelp

	case key.Matches(msg, m.keys.Up):
		if m.selected > 0 {
			m.selected--
			m.atBottom = false
			if !m.paused {
				m.paused = true
			}
		}

	case key.Matches(msg, m.keys.Down):
		count := m.filteredLen()
		if m.selected < count-1 {
			m.selected++
		}
		if m.selected == count-1 {
			m.atBottom = true
		}

	case key.Matches(msg, m.keys.PageUp):
		m.selected -= visibleLines(m.height)
		if m.selected < 0 {
			m.selected = 0
		}
		m.atBottom = false
		if !m.paused {
			m.paused = true
		}

	case key.Matches(msg, m.keys.PageDown):
		count := m.filteredLen()
		m.selected += visibleLines(m.height)
		if m.selected >= count {
			m.selected = count - 1
		}
		if m.selected < 0 {
			m.selected = 0
		}
		if m.selected == count-1 {
			m.atBottom = true
		}

	case key.Matches(msg, m.keys.Home):
		m.selected = 0
		m.atBottom = false
		if !m.paused {
			m.paused = true
		}

	case key.Matches(msg, m.keys.End):
		count := m.filteredLen()
		if count > 0 {
			m.selected = count - 1
		}
		m.atBottom = true
		m.paused = false

	case key.Matches(msg, m.keys.Pause):
		m.paused = !m.paused
		count := m.filteredLen()
		if !m.paused && count > 0 {
			m.selected = count - 1
			m.atBottom = true
		}

	case key.Matches(msg, m.keys.Detail):
		count := m.filteredLen()
		if count > 0 && m.selected < count {
			m.showDetail = true
			m.updateDetailViewport()
		}

	case key.Matches(msg, m.keys.Copy):
		m.copySelectedEvent()

	case key.Matches(msg, m.keys.FilterStream):
		// Cycle through stream filters: toggle next stream
		stream := allStreams[m.streamPick%len(allStreams)]
		m.filter.ToggleStream(stream)
		m.streamPick++
		m.rebuildFiltered()

	case key.Matches(msg, m.keys.FilterActor):
		m.inputMode = inputActor
		m.inputBuf = m.filter.Actor
		m.inputPrompt = "Actor filter: "

	case key.Matches(msg, m.keys.FilterSearch):
		m.inputMode = inputKeyword
		m.inputBuf = m.filter.Keyword
		m.inputPrompt = "Search: "

	case key.Matches(msg, m.keys.FilterClear):
		m.filter.Clear()
		m.streamPick = 0
		m.rebuildFiltered()

	case key.Matches(msg, m.keys.Refresh):
		m.status = "Reconnecting..."
		return m, m.startSSE()
	}

	return m, nil
}

// handleDetailKeys handles key presses in the detail view.
func (m *Model) handleDetailKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit), key.Matches(msg, m.keys.Detail):
		m.showDetail = false
		return m, nil
	case key.Matches(msg, m.keys.Copy):
		m.copySelectedEvent()
	case key.Matches(msg, m.keys.Up):
		m.detailViewport.ScrollUp(1)
	case key.Matches(msg, m.keys.Down):
		m.detailViewport.ScrollDown(1)
	case key.Matches(msg, m.keys.PageUp):
		m.detailViewport.HalfPageUp()
	case key.Matches(msg, m.keys.PageDown):
		m.detailViewport.HalfPageDown()
	case key.Matches(msg, m.keys.Home):
		m.detailViewport.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.detailViewport.GotoBottom()
	}
	return m, nil
}

// handleInputKeys handles key presses during text input mode (actor/keyword).
func (m *Model) handleInputKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		// Commit the input
		switch m.inputMode {
		case inputActor:
			m.filter.SetActor(m.inputBuf)
		case inputKeyword:
			m.filter.SetKeyword(m.inputBuf)
		}
		m.inputMode = inputNone
		m.inputBuf = ""
		m.inputPrompt = ""
		m.rebuildFiltered()

	case tea.KeyEscape:
		// Cancel without saving
		m.inputMode = inputNone
		m.inputBuf = ""
		m.inputPrompt = ""

	case tea.KeyBackspace:
		if len(m.inputBuf) > 0 {
			m.inputBuf = m.inputBuf[:len(m.inputBuf)-1]
		}

	default:
		if msg.Type == tea.KeyRunes {
			m.inputBuf += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.inputBuf += " "
		}
	}
	return m, nil
}

// rebuildFiltered rebuilds the filtered index list from the history.
func (m *Model) rebuildFiltered() {
	if m.filter.IsEmpty() {
		m.filtered = nil
		// Reset selected to end when clearing filters
		if m.history.Len() > 0 {
			m.selected = m.history.Len() - 1
		}
		m.atBottom = true
		return
	}

	m.filtered = m.filtered[:0]
	for i := 0; i < m.history.Len(); i++ {
		if m.filter.Matches(m.history.Get(i)) {
			m.filtered = append(m.filtered, i)
		}
	}

	// Clamp selected to filtered range
	if len(m.filtered) == 0 {
		m.selected = 0
	} else if m.selected >= len(m.filtered) {
		m.selected = len(m.filtered) - 1
	}
	m.atBottom = m.selected == len(m.filtered)-1
}

// filteredLen returns the count of visible events (filtered or total).
func (m *Model) filteredLen() int {
	if m.filter.IsEmpty() {
		return m.history.Len()
	}
	return len(m.filtered)
}

// filteredGet returns the history event at the given filtered index.
func (m *Model) filteredGet(i int) rpc.BusSSEEvent {
	if m.filter.IsEmpty() {
		return m.history.Get(i)
	}
	return m.history.Get(m.filtered[i])
}

// copySelectedEvent copies the selected event's JSON payload to the system clipboard.
func (m *Model) copySelectedEvent() {
	count := m.filteredLen()
	if m.selected < 0 || m.selected >= count {
		return
	}

	evt := m.filteredGet(m.selected)

	// Build a complete event JSON with metadata + payload
	type eventExport struct {
		Stream  string          `json:"stream"`
		Type    string          `json:"type"`
		Subject string          `json:"subject"`
		Seq     uint64          `json:"seq"`
		TS      string          `json:"ts"`
		Payload json.RawMessage `json:"payload"`
	}
	export := eventExport{
		Stream:  evt.Stream,
		Type:    evt.Type,
		Subject: evt.Subject,
		Seq:     evt.Seq,
		TS:      evt.TS,
		Payload: evt.Payload,
	}

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		m.copyStatus = "Copy failed: " + err.Error()
		return
	}

	if err := clipboard.WriteAll(string(data)); err != nil {
		m.copyStatus = "Copy failed: " + err.Error()
		return
	}
	m.copyStatus = "Copied to clipboard!"
}

// updateDetailViewport updates the detail viewport content for the selected event.
func (m *Model) updateDetailViewport() {
	if m.selected < 0 || m.selected >= m.filteredLen() {
		return
	}
	m.detailViewport.Width = m.width - 4
	m.detailViewport.Height = m.height - 8
	m.detailViewport.SetContent(m.renderDetailContent())
	m.detailViewport.GotoTop()
}

// appendEvent adds an event to the ring buffer.
func (m *Model) appendEvent(evt rpc.BusSSEEvent) {
	evicted := m.history.Push(evt)

	if !m.filter.IsEmpty() {
		// Maintain filtered index incrementally
		if evicted && len(m.filtered) > 0 {
			// The evicted event was at old history index 0.
			// All filtered indices shift down by 1.
			j := 0
			for _, idx := range m.filtered {
				if idx > 0 {
					m.filtered[j] = idx - 1
					j++
				}
			}
			m.filtered = m.filtered[:j]
			if m.selected > j-1 && j > 0 {
				m.selected = j - 1
			}
		}
		// Check if the new event passes the filter
		if m.filter.Matches(evt) {
			m.filtered = append(m.filtered, m.history.Len()-1)
		}
		// Auto-scroll filtered
		if !m.paused || m.atBottom {
			m.selected = len(m.filtered) - 1
			if m.selected < 0 {
				m.selected = 0
			}
			m.atBottom = true
		}
	} else {
		// No filter — adjust selected cursor when oldest event is evicted
		if evicted && m.selected > 0 {
			m.selected--
		}
		// Auto-scroll if not paused
		if !m.paused || m.atBottom {
			m.selected = m.history.Len() - 1
			m.atBottom = true
		}
	}
}

// visibleLines returns the number of event lines visible in the viewport.
func visibleLines(termHeight int) int {
	// Subtract header (2 lines), status (1 line), help (1 line), separator (2 lines)
	n := termHeight - 6
	if n < 1 {
		return 1
	}
	return n
}

// View renders the TUI.
func (m *Model) View() string {
	return m.renderView()
}

// eventSummary returns a brief text summary for an event's payload.
func eventSummary(evt rpc.BusSSEEvent) string {
	var info struct {
		ToolName   string `json:"tool_name,omitempty"`
		Actor      string `json:"actor,omitempty"`
		AgentName  string `json:"agent_name,omitempty"`
		From       string `json:"from,omitempty"`
		To         string `json:"to,omitempty"`
		IssueID    string `json:"issue_id,omitempty"`
		DecisionID string `json:"decision_id,omitempty"`
		Question   string `json:"question,omitempty"`
		SessionID  string `json:"session_id,omitempty"`
		JobID      string `json:"job_id,omitempty"`
		NewStatus  string `json:"new_status,omitempty"`
		Title      string `json:"title,omitempty"`
		Key        string `json:"key,omitempty"`
	}
	_ = json.Unmarshal(evt.Payload, &info)

	var parts []string
	if info.Actor != "" {
		parts = append(parts, info.Actor)
	} else if info.AgentName != "" {
		parts = append(parts, info.AgentName)
	}
	if info.ToolName != "" {
		parts = append(parts, "tool="+info.ToolName)
	}
	if info.From != "" && info.To != "" {
		parts = append(parts, info.From+" → "+info.To)
	}
	if info.IssueID != "" {
		s := info.IssueID
		if info.Title != "" {
			t := info.Title
			if len(t) > 30 {
				t = t[:27] + "..."
			}
			s += " " + t
		}
		parts = append(parts, s)
	}
	if info.DecisionID != "" {
		s := info.DecisionID
		if info.Question != "" {
			q := info.Question
			if len(q) > 30 {
				q = q[:27] + "..."
			}
			s += " " + q
		}
		parts = append(parts, s)
	}
	if info.NewStatus != "" {
		parts = append(parts, "→"+info.NewStatus)
	}
	if info.JobID != "" {
		parts = append(parts, "job="+info.JobID)
	}
	if info.Key != "" {
		parts = append(parts, "key="+info.Key)
	}
	if info.SessionID != "" {
		sid := info.SessionID
		if len(sid) > 12 {
			sid = sid[:12] + "..."
		}
		parts = append(parts, "session="+sid)
	}

	result := ""
	for i, p := range parts {
		if i > 0 {
			result += " "
		}
		result += p
	}
	return result
}

// formatTS formats an ISO 8601 timestamp to HH:MM:SS.mmm for display.
func formatTS(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		if len(ts) > 12 {
			return ts[:12]
		}
		return ts
	}
	return t.Local().Format("15:04:05.000")
}
