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

// TestDoneWaitMarksDelivered verifies that after DoneWait returns an inbox item,
// a second DoneWait call blocks (times out) because the item was marked as delivered.
// This is the regression test for bd-072x5.
func TestDoneWaitMarksDelivered(t *testing.T) {
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

	// Push an inbox item.
	now := time.Now().UTC()
	item := &types.InboxItem{
		ID:        uuid.New().String(),
		AgentName: "delivered-agent",
		Source:    "test-sender",
		Type:      "agent",
		Content:   "first message",
		Priority:  2,
		CreatedAt: now,
		DedupKey:  fmt.Sprintf("delivered-test:%d", now.UnixMilli()),
	}
	if err := store.InboxPush(ctx, item); err != nil {
		t.Fatalf("InboxPush failed: %v", err)
	}

	// First DoneWait should return the item immediately.
	result, err := client.DoneWait(&DoneWaitArgs{
		AgentName:  "delivered-agent",
		TimeoutSec: 5,
		On:         "inbox",
	})
	if err != nil {
		t.Fatalf("first DoneWait error: %v", err)
	}
	if result.TimedOut {
		t.Fatal("first DoneWait should have returned the item, got timeout")
	}
	if result.Content != "first message" {
		t.Errorf("expected 'first message', got %q", result.Content)
	}

	// Second DoneWait should timeout because the item is now marked as delivered.
	result2, err := client.DoneWait(&DoneWaitArgs{
		AgentName:  "delivered-agent",
		TimeoutSec: 2,
		On:         "inbox",
	})
	if err != nil {
		t.Fatalf("second DoneWait error: %v", err)
	}
	if !result2.TimedOut {
		t.Errorf("second DoneWait should have timed out (item already delivered), got event=%s content=%q", result2.EventType, result2.Content)
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

	// Decision-only wait with short timeout should timeout if no decisions are responded.
	result, err := client.DoneWait(&DoneWaitArgs{
		AgentName:  "filter-agent",
		TimeoutSec: 2,
		On:         "decision",
	})
	if err != nil {
		t.Fatalf("DoneWait error: %v", err)
	}
	if !result.TimedOut {
		t.Errorf("expected timeout for decision-only with no responded decisions, got event=%s", result.EventType)
	}
}

// TestDoneWaitDecisionPreCheck verifies that DoneWait returns immediately when
// a decision was already responded to before the wait starts. This is the
// regression test for bd-yvhxp (yield hangs forever when decision response
// arrives before NATS subscription).
func TestDoneWaitDecisionPreCheck(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a decision point and respond to it BEFORE calling DoneWait.
	dp := &types.DecisionPoint{
		IssueID:     "bd-precheck-test",
		Prompt:      "Pre-check test decision",
		Options:     `[{"id":"a","label":"A"},{"id":"b","label":"B"}]`,
		RequestedBy: "precheck-agent",
		CreatedAt:   time.Now().UTC(),
	}

	// Need a gate issue for the decision.
	issue := &types.Issue{
		ID:        dp.IssueID,
		Title:     "Pre-check gate",
		Status:    types.StatusOpen,
		IssueType: "gate",
		Priority:  2,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("CreateDecisionPoint failed: %v", err)
	}

	// Respond to the decision BEFORE calling DoneWait.
	now := time.Now().UTC()
	dp.SelectedOption = "a"
	dp.ResponseText = "Chose A"
	dp.RespondedBy = "human"
	dp.RespondedAt = &now
	if err := store.UpdateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("UpdateDecisionPoint failed: %v", err)
	}

	// DoneWait should find the pre-responded decision immediately via DB pre-check.
	start := time.Now()
	result, err := client.DoneWait(&DoneWaitArgs{
		AgentName:  "precheck-agent",
		TimeoutSec: 10,
		On:         "decision",
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("DoneWait error: %v", err)
	}
	if result == nil {
		t.Fatal("DoneWait returned nil")
	}
	if result.TimedOut {
		t.Fatal("expected decision event, got timeout — pre-check may have failed (bd-yvhxp regression)")
	}
	if result.EventType != "decision" {
		t.Errorf("expected event_type=decision, got %s", result.EventType)
	}
	if result.Source != "human" {
		t.Errorf("expected source=human, got %q", result.Source)
	}
	// Should be fast since the decision was pre-existing.
	if elapsed > 5*time.Second {
		t.Errorf("took too long for pre-existing decision: %v (expected <1s)", elapsed)
	}
}

// TestDoneWaitDecisionPollWake verifies that DoneWait detects a decision response
// that arrives during the poll loop (no NATS). This tests the polling fallback
// path added in bd-yvhxp.
func TestDoneWaitDecisionPollWake(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a decision point (not yet responded).
	dp := &types.DecisionPoint{
		IssueID:     "bd-pollwake-test",
		Prompt:      "Poll wake test decision",
		Options:     `[{"id":"x","label":"X"},{"id":"y","label":"Y"}]`,
		RequestedBy: "pollwake-agent",
		CreatedAt:   time.Now().UTC(),
	}
	issue := &types.Issue{
		ID:        dp.IssueID,
		Title:     "Poll wake gate",
		Status:    types.StatusOpen,
		IssueType: "gate",
		Priority:  2,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("CreateDecisionPoint failed: %v", err)
	}

	// Start DoneWait in a goroutine.
	type waitResult struct {
		result *DoneWaitResult
		err    error
	}
	resultCh := make(chan waitResult, 1)
	go func() {
		r, e := client.DoneWait(&DoneWaitArgs{
			AgentName:  "pollwake-agent",
			TimeoutSec: 30,
			On:         "decision",
		})
		resultCh <- waitResult{r, e}
	}()

	// Wait for the server to start polling, then respond to the decision.
	time.Sleep(3 * time.Second)

	now := time.Now().UTC()
	dp.SelectedOption = "x"
	dp.ResponseText = "Chose X during poll"
	dp.RespondedBy = "human"
	dp.RespondedAt = &now
	if err := store.UpdateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("UpdateDecisionPoint failed: %v", err)
	}

	// Wait for result — should arrive within a few seconds (poll interval is 2s).
	select {
	case wr := <-resultCh:
		if wr.err != nil {
			t.Fatalf("DoneWait error: %v", wr.err)
		}
		if wr.result.TimedOut {
			t.Fatal("expected decision event, got timeout — poll fallback may not check decisions (bd-yvhxp regression)")
		}
		if wr.result.EventType != "decision" {
			t.Errorf("expected event_type=decision, got %s", wr.result.EventType)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("DoneWait did not return in time — poll fallback may be broken")
	}
}

// TestDoneWaitDecisionAgentVariant verifies that DoneWait detects a decision
// response even when the agent name is a base-name variant of the requesting
// agent. E.g., decision created by "sharp-seal-1" but DoneWait called with
// "sharp-seal". This is the regression test for bd-zr20k.
func TestDoneWaitDecisionAgentVariant(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a decision as "variant-agent-1" (continuation session name).
	dp := &types.DecisionPoint{
		IssueID:     "bd-variant-test",
		Prompt:      "Agent variant test decision",
		Options:     `[{"id":"go","label":"Go"},{"id":"stop","label":"Stop"}]`,
		RequestedBy: "variant-agent-1", // Continuation variant name
		CreatedAt:   time.Now().UTC(),
	}
	issue := &types.Issue{
		ID:        dp.IssueID,
		Title:     "Variant gate",
		Status:    types.StatusOpen,
		IssueType: "gate",
		Priority:  2,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue failed: %v", err)
	}
	if err := store.CreateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("CreateDecisionPoint failed: %v", err)
	}

	// Respond to the decision.
	now := time.Now().UTC()
	dp.SelectedOption = "go"
	dp.ResponseText = "Go for it"
	dp.RespondedBy = "human"
	dp.RespondedAt = &now
	if err := store.UpdateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("UpdateDecisionPoint failed: %v", err)
	}

	// DoneWait with the BASE name "variant-agent" (not "variant-agent-1").
	// The findRecentDecisionForAgent helper should check both names. (bd-zr20k)
	start := time.Now()
	result, err := client.DoneWait(&DoneWaitArgs{
		AgentName:  "variant-agent", // Base name, not the variant
		TimeoutSec: 10,
		On:         "decision",
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("DoneWait error: %v", err)
	}
	if result == nil {
		t.Fatal("DoneWait returned nil")
	}
	if result.TimedOut {
		t.Fatal("expected decision event, got timeout — agent variant matching failed (bd-zr20k regression)")
	}
	if result.EventType != "decision" {
		t.Errorf("expected event_type=decision, got %s", result.EventType)
	}
	// Should be fast since the decision was pre-existing.
	if elapsed > 5*time.Second {
		t.Errorf("took too long: %v (expected <1s)", elapsed)
	}
}

// TestFindRecentDecisionForAgent verifies the helper function that checks
// both exact agent name and base name variant. (bd-zr20k)
func TestFindRecentDecisionForAgent(t *testing.T) {
	store := teststore.New(t)
	ctx := context.Background()

	// Create a decision from "test-seal-1" and respond to it.
	dp := &types.DecisionPoint{
		IssueID:     "bd-find-test",
		Prompt:      "Find test",
		Options:     `[{"id":"a","label":"A"}]`,
		RequestedBy: "test-seal-1",
		CreatedAt:   time.Now().UTC(),
	}
	issue := &types.Issue{
		ID:        dp.IssueID,
		Title:     "Find test gate",
		Status:    types.StatusOpen,
		IssueType: "gate",
		Priority:  2,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.CreateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("CreateDecisionPoint: %v", err)
	}
	now := time.Now().UTC()
	dp.SelectedOption = "a"
	dp.RespondedBy = "human"
	dp.RespondedAt = &now
	if err := store.UpdateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("UpdateDecisionPoint: %v", err)
	}

	since := time.Now().Add(-5 * time.Minute)

	// Exact match should work.
	found := findRecentDecisionForAgent(ctx, store, since, "test-seal-1")
	if found == nil {
		t.Error("expected to find decision by exact name 'test-seal-1'")
	}

	// Base name match should also work.
	found = findRecentDecisionForAgent(ctx, store, since, "test-seal")
	if found == nil {
		t.Error("expected to find decision by base name 'test-seal' (bd-zr20k)")
	}

	// Completely different name should NOT match.
	found = findRecentDecisionForAgent(ctx, store, since, "other-agent")
	if found != nil {
		t.Error("expected nil for unrelated agent name 'other-agent'")
	}
}
