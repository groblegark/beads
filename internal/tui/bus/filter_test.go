package bus

import (
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/rpc"
)

func makeFilterEvent(stream, typ, actor string) rpc.BusSSEEvent {
	payload := map[string]string{"actor": actor, "tool_name": "Bash"}
	data, _ := json.Marshal(payload)
	return rpc.BusSSEEvent{
		Stream:  stream,
		Type:    typ,
		Payload: data,
	}
}

func TestFilter_Empty(t *testing.T) {
	f := NewFilter()
	if !f.IsEmpty() {
		t.Fatal("new filter should be empty")
	}

	evt := makeFilterEvent("hooks", "PreToolUse", "bright-lark")
	if !f.Matches(evt) {
		t.Fatal("empty filter should match all events")
	}
}

func TestFilter_Streams(t *testing.T) {
	f := NewFilter()
	f.ToggleStream("hooks")

	if f.IsEmpty() {
		t.Fatal("filter with stream should not be empty")
	}

	// Should match hooks
	if !f.Matches(makeFilterEvent("hooks", "PreToolUse", "agent1")) {
		t.Fatal("should match hooks stream")
	}

	// Should not match decisions
	if f.Matches(makeFilterEvent("decisions", "DecisionCreated", "agent1")) {
		t.Fatal("should not match decisions stream")
	}

	// Toggle hooks off — filter becomes empty
	f.ToggleStream("hooks")
	if !f.IsEmpty() {
		t.Fatal("toggling off last stream should make filter empty")
	}
}

func TestFilter_MultipleStreams(t *testing.T) {
	f := NewFilter()
	f.ToggleStream("hooks")
	f.ToggleStream("mail")

	if f.Matches(makeFilterEvent("decisions", "DecisionCreated", "a")) {
		t.Fatal("should not match decisions")
	}
	if !f.Matches(makeFilterEvent("hooks", "PreToolUse", "a")) {
		t.Fatal("should match hooks")
	}
	if !f.Matches(makeFilterEvent("mail", "MailSent", "a")) {
		t.Fatal("should match mail")
	}
}

func TestFilter_Actor(t *testing.T) {
	f := NewFilter()
	f.SetActor("bright-lark")

	if !f.Matches(makeFilterEvent("hooks", "PreToolUse", "bright-lark")) {
		t.Fatal("should match bright-lark actor")
	}

	if f.Matches(makeFilterEvent("hooks", "PreToolUse", "tall-yak")) {
		t.Fatal("should not match tall-yak actor")
	}

	// Case insensitive
	f.SetActor("Bright-Lark")
	if !f.Matches(makeFilterEvent("hooks", "PreToolUse", "bright-lark")) {
		t.Fatal("actor match should be case-insensitive")
	}
}

func TestFilter_Keyword(t *testing.T) {
	f := NewFilter()
	f.SetKeyword("Bash")

	if !f.Matches(makeFilterEvent("hooks", "PreToolUse", "a")) {
		t.Fatal("should match 'Bash' in payload")
	}

	f.SetKeyword("nonexistent")
	if f.Matches(makeFilterEvent("hooks", "PreToolUse", "a")) {
		t.Fatal("should not match nonexistent keyword")
	}
}

func TestFilter_Combined(t *testing.T) {
	f := NewFilter()
	f.ToggleStream("hooks")
	f.SetActor("bright-lark")

	// Matches both: hooks stream AND bright-lark actor
	if !f.Matches(makeFilterEvent("hooks", "PreToolUse", "bright-lark")) {
		t.Fatal("should match combined filter")
	}

	// Fails stream
	if f.Matches(makeFilterEvent("decisions", "DecisionCreated", "bright-lark")) {
		t.Fatal("should fail stream filter")
	}

	// Fails actor
	if f.Matches(makeFilterEvent("hooks", "PreToolUse", "tall-yak")) {
		t.Fatal("should fail actor filter")
	}
}

func TestFilter_Clear(t *testing.T) {
	f := NewFilter()
	f.ToggleStream("hooks")
	f.SetActor("agent1")
	f.SetKeyword("test")

	f.Clear()
	if !f.IsEmpty() {
		t.Fatal("cleared filter should be empty")
	}
}

func TestFilter_Summary(t *testing.T) {
	f := NewFilter()
	if f.Summary() != "" {
		t.Fatal("empty filter summary should be empty string")
	}

	f.ToggleStream("hooks")
	s := f.Summary()
	if s == "" {
		t.Fatal("filter with stream should have non-empty summary")
	}
}
