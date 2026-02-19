package bus

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

	if m.showStats {
		return m.renderStatsView()
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
			isMatch := m.isSearchMatch(i)
			b.WriteString(m.renderEventLine(evt, selected, isMatch))
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
	if m.searchTerm != "" {
		pos := ""
		if len(m.searchMatches) > 0 && m.searchPos >= 0 {
			pos = fmt.Sprintf(" [%d/%d]", m.searchPos+1, len(m.searchMatches))
		}
		b.WriteString(searchInfoStyle.Render(fmt.Sprintf("/%s%s", m.searchTerm, pos)))
		if len(m.searchMatches) == 0 {
			b.WriteString(mutedStyle.Render(" (no matches)"))
		}
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
		b.WriteString(helpStyle.Render("j/k: scroll  /: search  n/N: next/prev  f: stream  a: actor  c: clear  ?: help  q: quit"))
	}
	b.WriteString("\n")

	return b.String()
}

// renderEventLine renders a single event in the timeline.
func (m *Model) renderEventLine(evt rpc.BusSSEEvent, selected bool, isMatch bool) string {
	ts := formatTS(evt.TS)
	stream := evt.Stream
	evtType := evt.Type
	summary := eventSummary(evt)

	// Format: HH:MM:SS.mmm seq    stream.type [badge] summary
	prefix := fmt.Sprintf("%s %s", tsStyle.Render(ts), seqStyle.Render(fmt.Sprintf("%-6d", evt.Seq)))
	streamType := fmt.Sprintf("%s.%s", streamStyle(stream).Render(stream), eventTypeStyle(evtType).Render(evtType))

	badge := severityBadge(evtType)
	badgeLen := 0
	if badge != "" {
		badge = " " + badge
		badgeLen = 4 // space + 3-char badge
	}

	maxSummary := m.width - len(ts) - 8 - len(stream) - 1 - len(evtType) - badgeLen - 2
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

	line := fmt.Sprintf("%s %s%s %s", prefix, streamType, badge, summary)

	if selected {
		return selectedStyle.Render(line)
	}
	if isMatch {
		return searchMatchStyle.Render(line)
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

// renderStatsView renders the stats dashboard.
func (m *Model) renderStatsView() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Bus Stats"))
	b.WriteString(mutedStyle.Render(fmt.Sprintf("  %d total events (buf %d/%d)",
		m.history.Total(), m.history.Len(), m.history.Cap())))
	b.WriteString("\n\n")

	if m.history.Len() == 0 {
		b.WriteString(statusStyle.Render("  No events yet. Waiting for data..."))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("v: back to timeline  q: quit"))
		b.WriteString("\n")
		return b.String()
	}

	// Compute stats from the ring buffer
	streamCounts := make(map[string]int)
	typeCounts := make(map[string]int)
	actorCounts := make(map[string]int)

	for i := 0; i < m.history.Len(); i++ {
		evt := m.history.Get(i)
		streamCounts[evt.Stream]++
		typeCounts[evt.Type]++

		// Extract actor from payload
		var info struct {
			Actor     string `json:"actor"`
			AgentName string `json:"agent_name"`
		}
		_ = json.Unmarshal(evt.Payload, &info)
		actor := info.Actor
		if actor == "" {
			actor = info.AgentName
		}
		if actor != "" {
			actorCounts[actor]++
		}
	}

	// Events per second (using first and last event timestamps)
	eps := ""
	if m.history.Len() >= 2 {
		first := m.history.Get(0)
		last := m.history.Last()
		t0, err0 := time.Parse(time.RFC3339Nano, first.TS)
		t1, err1 := time.Parse(time.RFC3339Nano, last.TS)
		if err0 == nil && err1 == nil {
			dur := t1.Sub(t0).Seconds()
			if dur > 0 {
				eps = fmt.Sprintf("%.1f events/sec", float64(m.history.Len())/dur)
			}
		}
	}

	// Section: throughput
	b.WriteString(detailKeyStyle.Render("  Throughput: "))
	if eps != "" {
		b.WriteString(detailValStyle.Render(eps))
	} else {
		b.WriteString(mutedStyle.Render("calculating..."))
	}
	b.WriteString("\n\n")

	// Section: streams
	b.WriteString(detailKeyStyle.Render("  Streams"))
	b.WriteString("\n")
	for _, s := range allStreams {
		count := streamCounts[s]
		if count > 0 {
			bar := strings.Repeat("█", min(count*40/m.history.Len(), 40))
			pct := float64(count) * 100 / float64(m.history.Len())
			b.WriteString(fmt.Sprintf("    %s %s %s\n",
				streamStyle(s).Render(fmt.Sprintf("%-12s", s)),
				streamStyle(s).Render(bar),
				mutedStyle.Render(fmt.Sprintf("%d (%.0f%%)", count, pct))))
		}
	}
	b.WriteString("\n")

	// Section: top event types (top 8)
	b.WriteString(detailKeyStyle.Render("  Top Event Types"))
	b.WriteString("\n")
	topTypes := topN(typeCounts, 8)
	for _, kv := range topTypes {
		b.WriteString(fmt.Sprintf("    %s %s\n",
			eventTypeStyle(kv.key).Render(fmt.Sprintf("%-28s", kv.key)),
			mutedStyle.Render(fmt.Sprintf("%d", kv.count))))
	}
	b.WriteString("\n")

	// Section: active actors (top 8)
	if len(actorCounts) > 0 {
		b.WriteString(detailKeyStyle.Render("  Active Actors"))
		b.WriteString("\n")
		topActors := topN(actorCounts, 8)
		for _, kv := range topActors {
			b.WriteString(fmt.Sprintf("    %s %s\n",
				detailValStyle.Render(fmt.Sprintf("%-20s", kv.key)),
				mutedStyle.Render(fmt.Sprintf("%d events", kv.count))))
		}
		b.WriteString("\n")
	}

	// Help
	b.WriteString(helpStyle.Render("v: back to timeline  q: quit"))
	b.WriteString("\n")

	return b.String()
}

// kvPair is a key-value pair for sorting counts.
type kvPair struct {
	key   string
	count int
}

// topN returns the top N entries from a count map, sorted by count descending.
func topN(counts map[string]int, n int) []kvPair {
	pairs := make([]kvPair, 0, len(counts))
	for k, v := range counts {
		pairs = append(pairs, kvPair{k, v})
	}
	// Simple selection sort for small N
	for i := 0; i < len(pairs) && i < n; i++ {
		maxIdx := i
		for j := i + 1; j < len(pairs); j++ {
			if pairs[j].count > pairs[maxIdx].count {
				maxIdx = j
			}
		}
		pairs[i], pairs[maxIdx] = pairs[maxIdx], pairs[i]
	}
	if len(pairs) > n {
		pairs = pairs[:n]
	}
	return pairs
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
