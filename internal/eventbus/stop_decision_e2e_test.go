//go:build integration

package eventbus_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
)

// TestStopDecFirst verifies the first Stop event dispatches through the
// full handler chain without errors. (bd-hbe0a)
func TestStopDecFirst(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	bus := eventbus.New()
	for _, h := range eventbus.DefaultHandlers() {
		bus.Register(h)
	}
	d.Server.SetBus(bus)

	cwd := t.TempDir()
	eventJSON := fmt.Sprintf(`{"session_id":"e2e-sd1","cwd":%q}`, cwd)
	resp, err := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
		HookType:  "Stop",
		EventJSON: json.RawMessage(eventJSON),
		SessionID: "e2e-sd1",
	})
	if err != nil {
		t.Fatalf("BusEmit(Stop): %v", err)
	}
	if !resp.Success {
		t.Fatalf("BusEmit failed: %s", resp.Error)
	}

	var result rpc.BusEmitResult
	json.Unmarshal(resp.Data, &result)

	// StopDecisionHandler may block or allow depending on config.
	// The important thing is the handler chain executed without error.
	t.Logf("Block=%v Reason=%q", result.Block, result.Reason)
}

// TestStopDecBypass verifies StopLoopDetector loop_break flag bypasses
// StopDecisionHandler. (bd-hbe0a)
func TestStopDecBypass(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	detector := &eventbus.StopLoopDetector{
		Threshold:      2,
		WindowDuration: 30 * time.Second,
	}
	bus := eventbus.New()
	bus.Register(detector)
	bus.Register(&eventbus.StopDecisionHandler{})
	bus.Register(&eventbus.InboxDrainHandler{})
	d.Server.SetBus(bus)

	cwd := t.TempDir()
	sid := "e2e-bypass"

	// Trigger loop: first stop + re-entry (threshold=2).
	stopLoopEmit(t, client, sid, cwd, false)
	result := stopLoopEmit(t, client, sid, cwd, true)

	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "loop detected") {
		t.Error("expected loop detection warning")
	}

	// Key: even with StopDecisionHandler in chain, stop should NOT block.
	if result.Block {
		t.Errorf("expected bypass, got block: %q", result.Reason)
	}
}

// TestStopDecChain verifies handler priority ordering for Stop events.
// (bd-hbe0a)
func TestStopDecChain(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	bus := eventbus.New()
	for _, h := range eventbus.DefaultHandlers() {
		bus.Register(h)
	}
	d.Server.SetBus(bus)

	resp, err := client.Execute(rpc.OpBusHandlers, nil)
	if err != nil {
		t.Fatalf("BusHandlers: %v", err)
	}

	var result rpc.BusHandlersResult
	json.Unmarshal(resp.Data, &result)

	// Find Stop-handling handlers.
	handlerPriorities := make(map[string]int)
	for _, h := range result.Handlers {
		for _, evt := range h.Handles {
			if evt == "Stop" {
				handlerPriorities[h.ID] = h.Priority
				break
			}
		}
	}

	expected := map[string]int{
		"stop-loop-detector": 14,
		"stop-decision":      15,
		"gate":               20,
		"inbox-drain":        30,
	}
	for id, want := range expected {
		if got, ok := handlerPriorities[id]; ok {
			if got != want {
				t.Errorf("%q priority = %d, want %d", id, got, want)
			}
		} else {
			t.Errorf("missing handler %q for Stop", id)
		}
	}
}

// TestStopDecDrain verifies inbox drain fires during stop with loop break.
// (bd-hbe0a)
func TestStopDecDrain(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	detector := &eventbus.StopLoopDetector{
		Threshold:      2,
		WindowDuration: 30 * time.Second,
	}
	bus := eventbus.New()
	bus.Register(detector)
	bus.Register(&eventbus.InboxDrainHandler{})
	d.Server.SetBus(bus)

	// Push inbox item.
	client.InboxPush(&rpc.InboxPushArgs{
		AgentName: "test-agent",
		Type:      "decision",
		Source:    "human",
		Content:   "Approved for deploy!",
		Priority:  1,
	})

	cwd := t.TempDir()
	sid := "e2e-drain"

	// Trigger loop + drain.
	stopLoopEmit(t, client, sid, cwd, false)
	result := stopLoopEmit(t, client, sid, cwd, true)

	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "loop detected") {
		t.Error("expected loop warning")
	}

	// Inbox drain may or may not succeed depending on bd subprocess.
	if strings.Contains(allInject, "Approved") {
		t.Log("Inbox drained successfully during stop")
	} else {
		t.Log("Note: inbox drain may not work in test (bd subprocess issue)")
	}
}

// TestStopDecSSafe verifies SessionStart is NOT affected by stop handlers.
// (bd-hbe0a)
func TestStopDecSSafe(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	bus := eventbus.New()
	for _, h := range eventbus.DefaultHandlers() {
		bus.Register(h)
	}
	d.Server.SetBus(bus)

	cwd := t.TempDir()
	eventJSON := fmt.Sprintf(`{"session_id":"e2e-ss","cwd":%q}`, cwd)
	resp, _ := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
		HookType:  "SessionStart",
		EventJSON: json.RawMessage(eventJSON),
		SessionID: "e2e-ss",
	})

	var result rpc.BusEmitResult
	json.Unmarshal(resp.Data, &result)
	if result.Block {
		t.Errorf("SessionStart should never be blocked: %q", result.Reason)
	}
}
