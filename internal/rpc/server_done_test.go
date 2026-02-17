package rpc

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/testutil/teststore"
	"github.com/steveyegge/beads/internal/types"
)

// TestDoneWaitTimeout verifies that DoneWait times out when no events arrive.
func TestDoneWaitTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)
	store := teststore.New(t)

	server := NewServer(socketPath, store, tmpDir, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = server.Start(ctx)
	}()
	<-server.WaitReady()
	defer server.Stop()

	client, err := TryConnect(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	// Short timeout — should return quickly with timeout.
	start := time.Now()
	result, err := client.DoneWait(&DoneWaitArgs{
		AgentName:  "test-agent",
		TimeoutSec: 2,
		On:         "inbox",
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("DoneWait returned error: %v", err)
	}
	if result == nil {
		t.Fatal("DoneWait returned nil result")
	}
	if !result.TimedOut {
		t.Errorf("expected TimedOut=true, got false (event=%s)", result.EventType)
	}
	if elapsed < 1*time.Second {
		t.Errorf("timed out too fast: %v", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Errorf("took too long: %v (expected ~2s)", elapsed)
	}
}

// TestDoneWaitInboxWake verifies that DoneWait unblocks when an inbox message arrives.
func TestDoneWaitInboxWake(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)
	store := teststore.New(t)

	server := NewServer(socketPath, store, tmpDir, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = server.Start(ctx)
	}()
	<-server.WaitReady()
	defer server.Stop()

	client, err := TryConnect(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	// Push an inbox item before starting DoneWait — tests the pre-existing check.
	now := time.Now().UTC()
	item := &types.InboxItem{
		ID:        uuid.New().String(),
		AgentName: "test-agent",
		Source:    "test-sender",
		Type:      "agent",
		Content:   "hello from test",
		Priority:  2,
		CreatedAt: now,
		DedupKey:  fmt.Sprintf("test:%d", now.UnixMilli()),
	}
	if err := store.InboxPush(ctx, item); err != nil {
		t.Fatalf("InboxPush failed: %v", err)
	}

	// DoneWait should find the pre-existing item immediately (poll path).
	start := time.Now()
	result, err := client.DoneWait(&DoneWaitArgs{
		AgentName:  "test-agent",
		TimeoutSec: 10,
		On:         "inbox",
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("DoneWait returned error: %v", err)
	}
	if result == nil {
		t.Fatal("DoneWait returned nil result")
	}
	if result.TimedOut {
		t.Fatal("expected event, got timeout")
	}
	if result.EventType != "inbox" {
		t.Errorf("expected event_type=inbox, got %s", result.EventType)
	}
	if result.Content != "hello from test" {
		t.Errorf("expected content 'hello from test', got %q", result.Content)
	}
	if result.Source != "test-sender" {
		t.Errorf("expected source 'test-sender', got %q", result.Source)
	}
	// Should be fast since the item was pre-existing.
	if elapsed > 5*time.Second {
		t.Errorf("took too long for pre-existing item: %v", elapsed)
	}
}

// TestDoneWaitInboxPollWake verifies that DoneWait unblocks when an inbox
// message arrives after the wait starts (via poll fallback path).
func TestDoneWaitInboxPollWake(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)
	store := teststore.New(t)

	server := NewServer(socketPath, store, tmpDir, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = server.Start(ctx)
	}()
	<-server.WaitReady()
	defer server.Stop()

	client, err := TryConnect(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	// Start DoneWait in a goroutine.
	type waitResult struct {
		result *DoneWaitResult
		err    error
	}
	resultCh := make(chan waitResult, 1)
	go func() {
		r, e := client.DoneWait(&DoneWaitArgs{
			AgentName:  "poll-agent",
			TimeoutSec: 30,
			On:         "inbox",
		})
		resultCh <- waitResult{r, e}
	}()

	// Wait a moment for the server to start polling, then push an inbox item.
	time.Sleep(1 * time.Second)

	now := time.Now().UTC()
	item := &types.InboxItem{
		ID:        uuid.New().String(),
		AgentName: "poll-agent",
		Source:    "delayed-sender",
		Type:      "agent",
		Content:   "delayed message",
		Priority:  2,
		CreatedAt: now,
		DedupKey:  fmt.Sprintf("poll-test:%d", now.UnixMilli()),
	}
	if err := store.InboxPush(ctx, item); err != nil {
		t.Fatalf("InboxPush failed: %v", err)
	}

	// Wait for result — should arrive within a few seconds (poll interval is 2s).
	select {
	case wr := <-resultCh:
		if wr.err != nil {
			t.Fatalf("DoneWait error: %v", wr.err)
		}
		if wr.result.TimedOut {
			t.Fatal("expected event, got timeout")
		}
		if wr.result.EventType != "inbox" {
			t.Errorf("expected event_type=inbox, got %s", wr.result.EventType)
		}
		if wr.result.Content != "delayed message" {
			t.Errorf("expected content 'delayed message', got %q", wr.result.Content)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("DoneWait did not return in time")
	}
}

// TestDoneWaitEventFilter verifies that the --on flag correctly filters events.
func TestDoneWaitEventFilter(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	socketPath := newTestSocketPath(t)
	store := teststore.New(t)

	server := NewServer(socketPath, store, tmpDir, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = server.Start(ctx)
	}()
	<-server.WaitReady()
	defer server.Stop()

	client, err := TryConnect(socketPath)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer client.Close()

	// Push an inbox item, then wait with --on=decision — should timeout
	// because we're only listening for decisions, not inbox.
	now := time.Now().UTC()
	item := &types.InboxItem{
		ID:        uuid.New().String(),
		AgentName: "filter-agent",
		Source:    "sender",
		Type:      "agent",
		Content:   "inbox message",
		Priority:  2,
		CreatedAt: now,
		DedupKey:  fmt.Sprintf("filter-test:%d", now.UnixMilli()),
	}
	if err := store.InboxPush(ctx, item); err != nil {
		t.Fatalf("InboxPush failed: %v", err)
	}

	// Decision-only wait with short timeout should timeout (no NATS = can't poll decisions).
	result, err := client.DoneWait(&DoneWaitArgs{
		AgentName:  "filter-agent",
		TimeoutSec: 2,
		On:         "decision",
	})
	if err != nil {
		t.Fatalf("DoneWait error: %v", err)
	}
	if !result.TimedOut {
		t.Errorf("expected timeout for decision-only with no NATS, got event=%s", result.EventType)
	}
}
