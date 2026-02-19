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

	// Filter indicator
	if !m.filter.IsEmpty() {
		b.WriteString("  ")
		b.WriteString(filterStyle.Render("FILTER: " + m.filter.Summary()))
		b.WriteString(mutedStyle.Render(fmt.Sprintf(" (%d/%d)", m.filteredLen(), m.history.Len())))
	}

	// Stats
	if m.history.Len() > 0 && m.filter.IsEmpty() {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  %d events (buf %d/%d)",
			m.history.Total(), m.history.Len(), m.history.Cap())))
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
	count := m.filteredLen()
	if count == 0 {
		b.WriteString("\n")
		if m.history.Len() == 0 {
			b.WriteString(statusStyle.Render("Waiting for events..."))
		} else {
			b.WriteString(statusStyle.Render("No events match filter"))
		}
		b.WriteString("\n")
	} else {
		visible := visibleLines(m.height)

		// Compute viewport window — keep selected centered
		start := m.selected - visible/2
		if start < 0 {
			start = 0
		}
		end := start + visible
		if end > count {
			end = count
			start = end - visible
			if start < 0 {
				start = 0
			}
		}

		for i := start; i < end; i++ {
			evt := m.filteredGet(i)
			selected := i == m.selected
			b.WriteString(m.renderEventLine(evt, selected))
			b.WriteString("\n")
		}
	}

	// Bottom separator
	b.WriteString(mutedStyle.Render(sep))
	b.WriteString("\n")

	// Status line
	if m.copyStatus != "" {
		b.WriteString(copySuccessStyle.Render(m.copyStatus))
		m.copyStatus = ""
		b.WriteString("  ")
	}
	if m.status != "" {
		b.WriteString(statusStyle.Render(m.status))
	}
	b.WriteString("\n")

	// Input mode or help
	if m.inputMode != inputNone {
		b.WriteString(inputStyle.Render(m.inputPrompt))
		b.WriteString(m.inputBuf)
		b.WriteString(inputStyle.Render("█"))
		b.WriteString(mutedStyle.Render("  (enter: apply, esc: cancel)"))
	} else if m.showHelp {
		b.WriteString("\n")
		b.WriteString(m.help.View(m.keys))
	} else {
		b.WriteString(helpStyle.Render("j/k: scroll  f: filter stream  /: search  a: actor  c: clear  ?: help  q: quit"))
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

	// Format: HH:MM:SS.mmm seq    stream.type summary
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

// renderDetailView renders the detail view with scrollable viewport.
func (m *Model) renderDetailView() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Event Detail"))

	count := m.filteredLen()
	if m.selected >= 0 && m.selected < count {
		evt := m.filteredGet(m.selected)
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  [%d/%d] seq=%d %s.%s",
			m.selected+1, count, evt.Seq, evt.Stream, evt.Type)))
	}
	b.WriteString("\n")

	// Scrollable viewport
	b.WriteString(m.detailViewport.View())
	b.WriteString("\n")

	// Scroll indicator + copy status
	pct := m.detailViewport.ScrollPercent()
	if m.copyStatus != "" {
		b.WriteString(copySuccessStyle.Render(m.copyStatus))
		b.WriteString("  ")
		m.copyStatus = "" // clear after one render
	}
	b.WriteString(helpStyle.Render(fmt.Sprintf("j/k: scroll  g/G: top/bottom  y: copy  %.0f%%  enter/q: back", pct*100)))
	b.WriteString("\n")

	return b.String()
}

// renderDetailContent generates the content string for the detail viewport.
func (m *Model) renderDetailContent() string {
	if m.selected < 0 || m.selected >= m.filteredLen() {
		return "No event selected"
	}

	evt := m.filteredGet(m.selected)
	var b strings.Builder

	b.WriteString(detailKeyStyle.Render("Stream:  "))
	b.WriteString(streamStyle(evt.Stream).Render(evt.Stream))
	b.WriteString("\n")

	b.WriteString(detailKeyStyle.Render("Type:    "))
	b.WriteString(detailValStyle.Render(evt.Type))
	b.WriteString("\n")

	b.WriteString(detailKeyStyle.Render("Subject: "))
	b.WriteString(detailValStyle.Render(evt.Subject))
	b.WriteString("\n")

	b.WriteString(detailKeyStyle.Render("Seq:     "))
	b.WriteString(detailValStyle.Render(fmt.Sprintf("%d", evt.Seq)))
	b.WriteString("\n")

	b.WriteString(detailKeyStyle.Render("Time:    "))
	b.WriteString(detailValStyle.Render(evt.TS))
	b.WriteString("\n")

	b.WriteString("\n")
	b.WriteString(detailKeyStyle.Render("Payload:"))
	b.WriteString("\n")

	var parsed interface{}
	if err := json.Unmarshal(evt.Payload, &parsed); err == nil {
		formatted, err := json.MarshalIndent(parsed, "", "  ")
		if err == nil {
			for _, line := range strings.Split(string(formatted), "\n") {
				b.WriteString("  ")
				b.WriteString(colorizeJSONLine(line))
				b.WriteString("\n")
			}
		} else {
			b.WriteString(detailValStyle.Render(string(evt.Payload)))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(detailValStyle.Render(string(evt.Payload)))
		b.WriteString("\n")
	}

	return b.String()
}

// colorizeJSONLine applies syntax highlighting to a single line of formatted JSON.
func colorizeJSONLine(line string) string {
	trimmed := strings.TrimSpace(line)
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]

	// Pure structural lines: { } [ ] or closing with comma
	if trimmed == "{" || trimmed == "}" || trimmed == "}," ||
		trimmed == "[" || trimmed == "]" || trimmed == "]," {
		return indent + jsonBraceStyle.Render(trimmed)
	}

	// Lines with key: value
	if strings.Contains(trimmed, ":") {
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 && strings.HasPrefix(strings.TrimSpace(parts[0]), "\"") {
			key := parts[0]
			value := parts[1]
			styledKey := jsonKeyStyle.Render(key)

			valTrimmed := strings.TrimSpace(value)
			var styledValue string
			switch {
			case valTrimmed == "{" || valTrimmed == "[" || valTrimmed == "{}" || valTrimmed == "[]":
				styledValue = " " + jsonBraceStyle.Render(valTrimmed)
			case strings.HasPrefix(valTrimmed, "\""):
				styledValue = " " + jsonStringStyle.Render(valTrimmed)
			case valTrimmed == "true" || valTrimmed == "true," ||
				valTrimmed == "false" || valTrimmed == "false," ||
				valTrimmed == "null" || valTrimmed == "null,":
				styledValue = " " + jsonBoolStyle.Render(valTrimmed)
			case len(valTrimmed) > 0 && (valTrimmed[0] >= '0' && valTrimmed[0] <= '9' || valTrimmed[0] == '-'):
				styledValue = " " + jsonNumberStyle.Render(valTrimmed)
			default:
				styledValue = " " + detailValStyle.Render(valTrimmed)
			}

			return indent + styledKey + ":" + styledValue
		}
	}

	// Standalone values in arrays
	trimmedNoComma := strings.TrimSuffix(trimmed, ",")
	switch {
	case strings.HasPrefix(trimmedNoComma, "\""):
		return indent + jsonStringStyle.Render(trimmed)
	case trimmedNoComma == "true" || trimmedNoComma == "false" || trimmedNoComma == "null":
		return indent + jsonBoolStyle.Render(trimmed)
	case len(trimmedNoComma) > 0 && (trimmedNoComma[0] >= '0' && trimmedNoComma[0] <= '9' || trimmedNoComma[0] == '-'):
		return indent + jsonNumberStyle.Render(trimmed)
	}

	return indent + detailValStyle.Render(trimmed)
}
