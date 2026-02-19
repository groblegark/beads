package bus

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/rpc"
)

// renderView renders the entire TUI.
func (m *Model) renderView() string {
	var b strings.Builder

	if m.width < 40 || m.height < 10 {
		return "Terminal too small. Please resize."
	}

	if m.showDetail {
		return m.renderDetailView()
	}

	// Title bar
	streamLabel := m.sseOpts.Stream
	if streamLabel == "" {
		streamLabel = "hooks"
	}
	title := fmt.Sprintf("Bus Watch [%s]", streamLabel)
	b.WriteString(titleStyle.Render(title))

	if m.paused {
		b.WriteString("  ")
		b.WriteString(pausedStyle.Render("PAUSED"))
	}

	// Stats
	if len(m.events) > 0 {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  %d events (buf %d/%d)",
			m.eventCount, len(m.events), m.maxLen)))
	}
	b.WriteString("\n")

	// Error
	if m.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
		b.WriteString("\n")
	}

	// Separator
	sep := strings.Repeat("─", max(0, m.width-2))
	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	// Event list
	if len(m.events) == 0 {
		b.WriteString("\n")
		b.WriteString(statusStyle.Render("Waiting for events..."))
		b.WriteString("\n")
	} else {
		visible := visibleLines(m.height)

		// Compute viewport window
		start := m.selected - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > len(m.events) {
			end = len(m.events)
			start = end - visible
			if start < 0 {
				start = 0
			}
		}

		for i := start; i < end; i++ {
			evt := m.events[i]
			selected := i == m.selected
			b.WriteString(m.renderEventLine(evt, selected))
			b.WriteString("\n")
		}
	}

	// Bottom separator
	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

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
		b.WriteString(helpStyle.Render("j/k: scroll  space: pause  enter: detail  r: reconnect  ?: help  q: quit"))
	}
	b.WriteString("\n")

	return b.String()
}

// renderEventLine renders a single event in the timeline.
func (m *Model) renderEventLine(evt rpc.BusSSEEvent, selected bool) string {
	ts := formatTS(evt.TS)
	stream := evt.Stream
	evtType := evt.Type
	summary := eventSummary(evt)

	// Truncate summary to fit width
	// Format: [HH:MM:SS.mmm] seq=NNNNN stream.type summary
	prefix := fmt.Sprintf("%s %s", tsStyle.Render(ts), seqStyle.Render(fmt.Sprintf("%-6d", evt.Seq)))
	streamType := fmt.Sprintf("%s.%s", streamStyle(stream).Render(stream), evtType)

	maxSummary := m.width - len(ts) - 8 - len(stream) - 1 - len(evtType) - 2
	if maxSummary < 0 {
		maxSummary = 0
	}
	if len(summary) > maxSummary {
		if maxSummary > 3 {
			summary = summary[:maxSummary-3] + "..."
		} else {
			summary = ""
		}
	}

	line := fmt.Sprintf("%s %s %s", prefix, streamType, summary)

	if selected {
		return selectedStyle.Render(line)
	}
	return line
}

// renderDetailView renders the full detail of the selected event.
func (m *Model) renderDetailView() string {
	var b strings.Builder

	if m.selected < 0 || m.selected >= len(m.events) {
		return "No event selected"
	}

	evt := m.events[m.selected]

	b.WriteString(titleStyle.Render("Event Detail"))
	b.WriteString("\n\n")

	// Metadata
	b.WriteString(detailKeyStyle.Render("  Stream:  "))
	b.WriteString(streamStyle(evt.Stream).Render(evt.Stream))
	b.WriteString("\n")

	b.WriteString(detailKeyStyle.Render("  Type:    "))
	b.WriteString(detailValStyle.Render(evt.Type))
	b.WriteString("\n")

	b.WriteString(detailKeyStyle.Render("  Subject: "))
	b.WriteString(detailValStyle.Render(evt.Subject))
	b.WriteString("\n")

	b.WriteString(detailKeyStyle.Render("  Seq:     "))
	b.WriteString(detailValStyle.Render(fmt.Sprintf("%d", evt.Seq)))
	b.WriteString("\n")

	b.WriteString(detailKeyStyle.Render("  Time:    "))
	b.WriteString(detailValStyle.Render(evt.TS))
	b.WriteString("\n")

	b.WriteString("\n")

	// Payload — pretty-printed JSON
	b.WriteString(detailKeyStyle.Render("  Payload:"))
	b.WriteString("\n")

	var prettyJSON json.RawMessage
	if err := json.Unmarshal(evt.Payload, &prettyJSON); err == nil {
		formatted, err := json.MarshalIndent(prettyJSON, "  ", "  ")
		if err == nil {
			b.WriteString("  ")
			b.WriteString(detailValStyle.Render(string(formatted)))
		} else {
			b.WriteString("  ")
			b.WriteString(detailValStyle.Render(string(evt.Payload)))
		}
	} else {
		b.WriteString("  ")
		b.WriteString(detailValStyle.Render(string(evt.Payload)))
	}
	b.WriteString("\n")

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("Press enter or q to return"))
	b.WriteString("\n")

	return b.String()
}
