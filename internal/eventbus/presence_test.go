//go:build integration

package eventbus_test

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/steveyegge/beads/internal/eventbus"
)

// startTestNATS starts an embedded NATS server with JetStream for testing.
func startTestNATS(t *testing.T) (nats.JetStreamContext, *nats.Conn, func()) {
	t.Helper()
	dir := t.TempDir()
	opts := &natsserver.Options{
		Port:               -1,
		JetStream:          true,
		JetStreamMaxMemory: 1024 << 20,
		JetStreamMaxStore:  1024 << 20,
		StoreDir:           dir,
		NoLog:              true,
		NoSigs:             true,
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("create NATS: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS not ready")
	}

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		t.Fatalf("connect NATS: %v", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatalf("JetStream: %v", err)
	}

	if err := eventbus.EnsureStreams(js); err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatalf("EnsureStreams: %v", err)
	}

	cleanup := func() {
		nc.Close()
		ns.Shutdown()
	}
	return js, nc, cleanup
}

// TestOnNewAgentCallback verifies that the PresenceTracker fires OnNewAgent
// when it first sees an agent via a NATS agent lifecycle event. (bd-3zjmy)
func TestOnNewAgentCallback(t *testing.T) {
	js, nc, cleanup := startTestNATS(t)
	defer cleanup()

	pt := eventbus.NewPresenceTracker()

	var mu sync.Mutex
	var seenAgents []string
	pt.OnNewAgent = func(actor string) {
		mu.Lock()
		seenAgents = append(seenAgents, actor)
		mu.Unlock()
	}

	if err := pt.Start(js); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pt.Stop()

	// Publish an AgentStarted event for a new agent.
	payload := eventbus.AgentEventPayload{
		AgentName: "gt-beads-polecat-testbot",
		SessionID: "test-session-1",
	}
	data, _ := json.Marshal(payload)
	if _, err := js.Publish("agents."+string(eventbus.EventAgentStarted), data); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Wait for the callback to fire.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(seenAgents)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("OnNewAgent callback not called within 5s")
		case <-time.After(50 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenAgents) != 1 || seenAgents[0] != "gt-beads-polecat-testbot" {
		t.Fatalf("expected [gt-beads-polecat-testbot], got %v", seenAgents)
	}

	// Publish another event for the SAME agent — should NOT fire callback again.
	payload2 := eventbus.AgentEventPayload{
		AgentName: "gt-beads-polecat-testbot",
		SessionID: "test-session-1",
	}
	data2, _ := json.Marshal(payload2)
	if _, err := js.Publish("agents."+string(eventbus.EventAgentHeartbeat), data2); err != nil {
		t.Fatalf("Publish heartbeat: %v", err)
	}

	// Short wait — callback should NOT fire again.
	_ = nc.Flush()
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	count := len(seenAgents)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("OnNewAgent fired %d times (expected 1 — idempotency check)", count)
	}
}

// TestOnNewAgentCallbackFromHookEvent verifies that the callback also fires
// when an agent is first seen via a hook event (not just agent lifecycle). (bd-3zjmy)
func TestOnNewAgentCallbackFromHookEvent(t *testing.T) {
	js, _, cleanup := startTestNATS(t)
	defer cleanup()

	pt := eventbus.NewPresenceTracker()

	var mu sync.Mutex
	var seenAgents []string
	pt.OnNewAgent = func(actor string) {
		mu.Lock()
		seenAgents = append(seenAgents, actor)
		mu.Unlock()
	}

	if err := pt.Start(js); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pt.Stop()

	// Publish a hook event (PreToolUse) for a new agent.
	hookPayload := map[string]interface{}{
		"actor":           "gt-gastown-witness",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Read",
		"session_id":      "test-session-2",
	}
	data, _ := json.Marshal(hookPayload)
	if _, err := js.Publish("hooks.PreToolUse", data); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Wait for the callback to fire.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(seenAgents)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("OnNewAgent callback not called within 5s for hook event")
		case <-time.After(50 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenAgents) != 1 || seenAgents[0] != "gt-gastown-witness" {
		t.Fatalf("expected [gt-gastown-witness], got %v", seenAgents)
	}
}

// TestOnNewAgentCallbackNil verifies that a nil callback doesn't panic. (bd-3zjmy)
func TestOnNewAgentCallbackNil(t *testing.T) {
	js, _, cleanup := startTestNATS(t)
	defer cleanup()

	pt := eventbus.NewPresenceTracker()
	// Explicitly NOT setting OnNewAgent — should be nil and not panic.

	if err := pt.Start(js); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pt.Stop()

	// Publish an agent event — should not panic.
	payload := eventbus.AgentEventPayload{
		AgentName: "gt-test-agent",
	}
	data, _ := json.Marshal(payload)
	if _, err := js.Publish("agents."+string(eventbus.EventAgentStarted), data); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// If we get here without panic, the test passes.
	time.Sleep(500 * time.Millisecond)

	// Verify the agent is in the roster.
	roster := pt.Roster(0)
	found := false
	for _, e := range roster {
		if e.Actor == "gt-test-agent" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("agent not in roster after event")
	}
}
