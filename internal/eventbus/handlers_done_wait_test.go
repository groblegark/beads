package eventbus

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestDoneWaitHandlerMetadata(t *testing.T) {
	h := &DoneWaitHandler{}
	if h.ID() != "done-wait" {
		t.Errorf("expected ID 'done-wait', got %q", h.ID())
	}
	if h.Priority() != 90 {
		t.Errorf("expected priority 90, got %d", h.Priority())
	}
	handles := h.Handles()
	if len(handles) != 1 || handles[0] != EventStop {
		t.Errorf("expected [Stop], got %v", handles)
	}
}

func TestDoneWaitHandlerDisabled(t *testing.T) {
	t.Setenv("BD_DONE_DISABLED", "1")

	h := &DoneWaitHandler{}
	event := &Event{Type: EventStop, Actor: "test-agent"}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Block {
		t.Error("expected no block when disabled")
	}
}

func TestDoneWaitHandlerNoActor(t *testing.T) {
	h := &DoneWaitHandler{}
	event := &Event{Type: EventStop} // no Actor
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Block {
		t.Error("expected no block when actor is empty")
	}
}

func TestDoneWaitHandlerExistingBlock(t *testing.T) {
	h := &DoneWaitHandler{}
	event := &Event{Type: EventStop, Actor: "test-agent"}
	result := &Result{Block: true, Reason: "existing block"}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Reason != "existing block" {
		t.Errorf("expected existing block reason preserved, got %q", result.Reason)
	}
}

func TestDoneWaitHandlerPollTimeout(t *testing.T) {
	store := &mockInboxStore{}
	h := &DoneWaitHandler{}
	h.SetInboxStore(store)
	h.SetTimeout(2 * time.Second)

	event := &Event{Type: EventStop, Actor: "test-agent"}
	result := &Result{}

	start := time.Now()
	err := h.Handle(context.Background(), event, result)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Block {
		t.Error("expected no block on timeout")
	}
	if elapsed < 1*time.Second {
		t.Errorf("timed out too fast: %v", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Errorf("took too long: %v (expected ~2s)", elapsed)
	}
}

func TestDoneWaitHandlerPreExistingItem(t *testing.T) {
	store := &mockInboxStore{
		items: []*types.InboxItem{
			{ID: "msg-0", AgentName: "pre-agent", Source: "pre-sender", Type: "agent", Content: "pre-existing"},
		},
	}
	h := &DoneWaitHandler{}
	h.SetInboxStore(store)
	h.SetTimeout(10 * time.Second)

	event := &Event{Type: EventStop, Actor: "pre-agent"}
	result := &Result{}

	start := time.Now()
	err := h.Handle(context.Background(), event, result)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Block {
		t.Fatal("expected block for pre-existing inbox item")
	}
	if elapsed > 2*time.Second {
		t.Errorf("took too long for pre-existing item: %v", elapsed)
	}
}

// concurrentMockInboxStore is a thread-safe mock that allows adding items mid-wait.
type concurrentMockInboxStore struct {
	mu    sync.Mutex
	items []*types.InboxItem
}

func (m *concurrentMockInboxStore) InboxDrain(_ context.Context, _ string, _ ...int) ([]*types.InboxItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := m.items
	m.items = nil // drain clears items
	return result, nil
}

func (m *concurrentMockInboxStore) InboxMarkDelivered(_ context.Context, _ string, _ []string) error {
	return nil
}

func TestDoneWaitHandlerPollWake(t *testing.T) {
	store := &concurrentMockInboxStore{}
	h := &DoneWaitHandler{}
	h.SetInboxStore(store)
	h.SetTimeout(30 * time.Second)

	event := &Event{Type: EventStop, Actor: "wake-agent"}
	result := &Result{}

	// Push an item after 1 second.
	go func() {
		time.Sleep(1 * time.Second)
		store.mu.Lock()
		store.items = []*types.InboxItem{
			{ID: "msg-1", AgentName: "wake-agent", Source: "test-sender", Type: "agent", Content: "wake up"},
		}
		store.mu.Unlock()
	}()

	start := time.Now()
	err := h.Handle(context.Background(), event, result)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Block {
		t.Fatal("expected block when inbox item arrives")
	}
	if result.Reason == "" {
		t.Error("expected non-empty block reason")
	}
	// Should have woken within ~3s (1s delay + 2s poll cycle).
	if elapsed > 10*time.Second {
		t.Errorf("took too long to wake: %v", elapsed)
	}
}

func TestIsSlingEvent(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{"sling dispatch", "sling:bright-lark", true},
		{"sling self", "sling:self", true},
		{"regular agent", "agent:bright-lark", false},
		{"decision", "decision:hq-abc", false},
		{"empty", "", false},
		{"sling prefix only", "sling:", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := &doneWaitEvent{source: tt.source}
			if got := isSlingEvent(evt); got != tt.want {
				t.Errorf("isSlingEvent(source=%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}

func TestFormatSlingBlockReason(t *testing.T) {
	evt := &doneWaitEvent{
		eventType: "inbox",
		source:    "sling:bright-lark",
		content:   "WORK: Fix auth bug\nBead: bd-abc123\nDispatched by: bright-lark\n\nRun 'bd show bd-abc123' for full details, then start working.",
	}
	reason := formatSlingBlockReason(evt)

	if !strings.Contains(reason, "## New Work Assignment") {
		t.Error("missing heading")
	}
	if !strings.Contains(reason, "Dispatched by: bright-lark") {
		t.Error("missing dispatcher")
	}
	if !strings.Contains(reason, "WORK: Fix auth bug") {
		t.Error("missing work content")
	}
	if !strings.Contains(reason, "bd-abc123") {
		t.Error("missing bead ID")
	}
	if !strings.Contains(reason, "Start working on it now") {
		t.Error("missing action instruction")
	}
}

func TestFormatBlockReason_SlingVsGeneric(t *testing.T) {
	h := &DoneWaitHandler{}

	// Sling event should use sling formatter.
	slingEvt := &doneWaitEvent{
		eventType: "inbox",
		source:    "sling:dispatcher",
		content:   "WORK: Test task\nBead: bd-xyz",
	}
	slingReason := h.formatBlockReason(slingEvt)
	if !strings.Contains(slingReason, "## New Work Assignment") {
		t.Error("sling event should use sling formatter")
	}
	if strings.Contains(slingReason, "An event arrived") {
		t.Error("sling event should NOT use generic formatter")
	}

	// Non-sling event should use generic formatter.
	genericEvt := &doneWaitEvent{
		eventType: "inbox",
		source:    "agent:someone",
		content:   "hello",
	}
	genericReason := h.formatBlockReason(genericEvt)
	if !strings.Contains(genericReason, "An event arrived") {
		t.Error("generic event should use generic formatter")
	}
	if strings.Contains(genericReason, "## New Work Assignment") {
		t.Error("generic event should NOT use sling formatter")
	}
}

func TestInboxItemsToEvent_SingleSling(t *testing.T) {
	items := []*types.InboxItem{
		{ID: "msg-1", Source: "sling:bright-lark", Content: "WORK: Fix bug\nBead: bd-abc"},
	}
	evt := inboxItemsToEvent(items)

	if evt.content != "WORK: Fix bug\nBead: bd-abc" {
		t.Errorf("sling item should preserve raw content, got %q", evt.content)
	}
	if evt.source != "sling:bright-lark" {
		t.Errorf("source should be preserved, got %q", evt.source)
	}
}

func TestInboxItemsToEvent_NonSling(t *testing.T) {
	items := []*types.InboxItem{
		{ID: "msg-1", Source: "agent:someone", Type: "agent", Content: "hello"},
	}
	evt := inboxItemsToEvent(items)

	// Should use formatInboxItems (wrapped format).
	if !strings.Contains(evt.content, "Inbox: 1 new notification") {
		t.Errorf("non-sling should use formatInboxItems, got %q", evt.content)
	}
}

func TestInboxItemsToEvent_MultipleMixed(t *testing.T) {
	items := []*types.InboxItem{
		{ID: "msg-1", Source: "sling:dispatcher", Type: "agent", Content: "WORK: task 1"},
		{ID: "msg-2", Source: "agent:other", Type: "agent", Content: "hello"},
	}
	evt := inboxItemsToEvent(items)

	// Multiple items should always use formatInboxItems (even if first is sling).
	if !strings.Contains(evt.content, "Inbox: 2 new notification") {
		t.Errorf("multiple items should use formatInboxItems, got %q", evt.content)
	}
}

func TestPollWake_SlingItem(t *testing.T) {
	store := &concurrentMockInboxStore{}
	h := &DoneWaitHandler{}
	h.SetInboxStore(store)
	h.SetTimeout(30 * time.Second)

	event := &Event{Type: EventStop, Actor: "sling-target"}
	result := &Result{}

	// Push a sling item after 1 second.
	go func() {
		time.Sleep(1 * time.Second)
		store.mu.Lock()
		store.items = []*types.InboxItem{
			{ID: "msg-sling", AgentName: "sling-target", Source: "sling:dispatcher", Type: "agent", Content: "WORK: Fix auth\nBead: bd-test"},
		}
		store.mu.Unlock()
	}()

	start := time.Now()
	err := h.Handle(context.Background(), event, result)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Block {
		t.Fatal("expected block when sling item arrives")
	}
	if !strings.Contains(result.Reason, "## New Work Assignment") {
		t.Errorf("expected sling-formatted reason, got %q", result.Reason)
	}
	if !strings.Contains(result.Reason, "WORK: Fix auth") {
		t.Error("expected work content in reason")
	}
	if elapsed > 10*time.Second {
		t.Errorf("took too long to wake: %v", elapsed)
	}
}

func TestDoneWaitHandlerEnvDisable(t *testing.T) {
	store := &mockInboxStore{
		items: []*types.InboxItem{
			{ID: "msg-0", AgentName: "disabled-agent", Source: "sender", Type: "agent", Content: "should not wake"},
		},
	}
	h := &DoneWaitHandler{}
	h.SetInboxStore(store)

	t.Setenv("BD_DONE_DISABLED", "1")

	event := &Event{Type: EventStop, Actor: "disabled-agent"}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Block {
		t.Error("expected no block when BD_DONE_DISABLED=1")
	}
}
