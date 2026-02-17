package eventbus

import (
	"context"
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
