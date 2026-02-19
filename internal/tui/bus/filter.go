package bus

import (
	"strings"

	"github.com/steveyegge/beads/internal/rpc"
)

// Filter defines client-side event filtering criteria.
// An empty filter matches all events. Multiple criteria are AND-ed.
type Filter struct {
	// Streams to include (empty = all). Matches event.Stream.
	Streams map[string]bool

	// Types to include (empty = all). Matches event.Type.
	Types map[string]bool

	// Actor substring match (empty = match all). Matches payload "actor" field.
	Actor string

	// Keyword substring match (empty = match all). Searches entire payload JSON.
	Keyword string
}

// NewFilter creates an empty filter that matches everything.
func NewFilter() Filter {
	return Filter{
		Streams: make(map[string]bool),
		Types:   make(map[string]bool),
	}
}

// IsEmpty returns true if the filter has no active criteria.
func (f *Filter) IsEmpty() bool {
	return len(f.Streams) == 0 && len(f.Types) == 0 && f.Actor == "" && f.Keyword == ""
}

// ActiveStreamCount returns the number of active stream filters.
func (f *Filter) ActiveStreamCount() int {
	return len(f.Streams)
}

// ToggleStream adds a stream to the filter if not present, or removes it.
func (f *Filter) ToggleStream(stream string) {
	if f.Streams[stream] {
		delete(f.Streams, stream)
	} else {
		f.Streams[stream] = true
	}
}

// SetActor sets the actor filter.
func (f *Filter) SetActor(actor string) {
	f.Actor = actor
}

// SetKeyword sets the keyword search filter.
func (f *Filter) SetKeyword(keyword string) {
	f.Keyword = keyword
}

// Clear resets all filter criteria.
func (f *Filter) Clear() {
	f.Streams = make(map[string]bool)
	f.Types = make(map[string]bool)
	f.Actor = ""
	f.Keyword = ""
}

// Matches returns true if the event passes all active filter criteria.
func (f *Filter) Matches(evt rpc.BusSSEEvent) bool {
	// Stream filter
	if len(f.Streams) > 0 && !f.Streams[evt.Stream] {
		return false
	}

	// Type filter
	if len(f.Types) > 0 && !f.Types[evt.Type] {
		return false
	}

	// Actor filter — substring match in payload
	if f.Actor != "" {
		payload := string(evt.Payload)
		if !strings.Contains(strings.ToLower(payload), strings.ToLower(f.Actor)) {
			return false
		}
	}

	// Keyword filter — substring match in entire payload
	if f.Keyword != "" {
		payload := string(evt.Payload)
		if !strings.Contains(strings.ToLower(payload), strings.ToLower(f.Keyword)) {
			return false
		}
	}

	return true
}

// Summary returns a short human-readable description of active filters.
func (f *Filter) Summary() string {
	if f.IsEmpty() {
		return ""
	}

	var parts []string

	if len(f.Streams) > 0 {
		var names []string
		for s := range f.Streams {
			names = append(names, s)
		}
		parts = append(parts, "streams:"+strings.Join(names, ","))
	}

	if len(f.Types) > 0 {
		var names []string
		for t := range f.Types {
			names = append(names, t)
		}
		parts = append(parts, "types:"+strings.Join(names, ","))
	}

	if f.Actor != "" {
		parts = append(parts, "actor:"+f.Actor)
	}

	if f.Keyword != "" {
		parts = append(parts, "keyword:"+f.Keyword)
	}

	return strings.Join(parts, " ")
}

// allStreams is the list of all known stream names.
var allStreams = []string{"hooks", "decisions", "oj", "agents", "mail", "mutations", "config", "inbox"}
