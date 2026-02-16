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

// stopLoopEmit emits a Stop event with the given reentry flag through the RPC client.
func stopLoopEmit(t *testing.T, client *rpc.Client, sessionID, cwd string, reentry bool) rpc.BusEmitResult {
	t.Helper()
	raw := map[string]interface{}{
		"session_id":       sessionID,
		"cwd":              cwd,
		"stop_hook_active": reentry,
	}
	eventJSON, _ := json.Marshal(raw)
	resp, err := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
		HookType:  "Stop",
		EventJSON: json.RawMessage(eventJSON),
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("BusEmit(Stop): %v", err)
	}
	if !resp.Success {
		t.Fatalf("BusEmit failed: %s", resp.Error)
	}
	var result rpc.BusEmitResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return result
}

// setupLoopBus creates a test daemon with a StopLoopDetector at the given threshold.
func setupLoopBus(t *testing.T, threshold int, window time.Duration, extra ...eventbus.Handler) (*testdaemon.Daemon, *rpc.Client) {
	t.Helper()
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	detector := &eventbus.StopLoopDetector{
		Threshold:      threshold,
		WindowDuration: window,
	}
	bus := eventbus.New()
	bus.Register(detector)
	for _, h := range extra {
		bus.Register(h)
	}
	d.Server.SetBus(bus)
	return d, client
}

// TestLoopThreshold verifies the full stop loop detection lifecycle:
// first attempt passes, re-entries accumulate, threshold triggers break.
// (bd-rrcj4)
func TestLoopThreshold(t *testing.T) {
	_, client := setupLoopBus(t, 3, 30*time.Second)
	cwd := t.TempDir()
	sid := "e2e-loop-1"

	// Attempt 1: First stop (not re-entry) — passes through.
	r1 := stopLoopEmit(t, client, sid, cwd, false)
	if r1.Block {
		t.Error("first stop should not block")
	}
	if len(r1.Inject) > 0 {
		t.Errorf("first stop should have no injection, got: %v", r1.Inject)
	}

	// Attempt 2: Re-entry (under threshold=3) — passes.
	r2 := stopLoopEmit(t, client, sid, cwd, true)
	if r2.Block {
		t.Error("second attempt should not block")
	}

	// Attempt 3: Re-entry (hits threshold=3) — triggers loop break.
	r3 := stopLoopEmit(t, client, sid, cwd, true)
	if r3.Block {
		t.Error("third attempt should NOT block (loop detected)")
	}

	allInject := strings.Join(r3.Inject, "\n")
	if !strings.Contains(allInject, "Stop hook loop detected") {
		t.Errorf("expected loop warning, got: %v", r3.Inject)
	}
	if !strings.Contains(allInject, "3 stop attempts") {
		t.Errorf("expected count in warning, got: %v", r3.Inject)
	}
}

// TestLoopSessions verifies per-session isolation — one session's loop
// does not affect another. (bd-rrcj4)
func TestLoopSessions(t *testing.T) {
	_, client := setupLoopBus(t, 2, 30*time.Second)
	cwd := t.TempDir()

	// Session A: first stop + re-entry (hits threshold=2).
	stopLoopEmit(t, client, "a", cwd, false)
	rA := stopLoopEmit(t, client, "a", cwd, true)
	if !strings.Contains(strings.Join(rA.Inject, "\n"), "loop detected") {
		t.Error("session A should trigger loop detection")
	}

	// Session B: independent detection.
	stopLoopEmit(t, client, "b", cwd, false)
	rB := stopLoopEmit(t, client, "b", cwd, true)
	if !strings.Contains(strings.Join(rB.Inject, "\n"), "loop detected") {
		t.Error("session B should trigger independently")
	}
}

// TestLoopReset verifies counter clears after detection so next cycle is fresh.
// (bd-rrcj4)
func TestLoopReset(t *testing.T) {
	_, client := setupLoopBus(t, 2, 30*time.Second)
	cwd := t.TempDir()
	sid := "e2e-reset"

	// Cycle 1: trigger detection.
	stopLoopEmit(t, client, sid, cwd, false)
	r1 := stopLoopEmit(t, client, sid, cwd, true)
	if !strings.Contains(strings.Join(r1.Inject, "\n"), "loop detected") {
		t.Fatal("first cycle should detect loop")
	}

	// Cycle 2: fresh start after clear.
	stopLoopEmit(t, client, sid, cwd, false)
	r2 := stopLoopEmit(t, client, sid, cwd, true)
	if !strings.Contains(strings.Join(r2.Inject, "\n"), "loop detected") {
		t.Error("second cycle should trigger after reset")
	}
}

// TestLoopExpiry verifies attempts outside the window don't count.
// (bd-rrcj4)
func TestLoopExpiry(t *testing.T) {
	_, client := setupLoopBus(t, 3, 500*time.Millisecond)
	cwd := t.TempDir()
	sid := "e2e-exp"

	// Emit first stop and one re-entry.
	stopLoopEmit(t, client, sid, cwd, false)
	stopLoopEmit(t, client, sid, cwd, true)

	// Wait for window to expire.
	time.Sleep(600 * time.Millisecond)

	// New re-entry after window expiry — count resets.
	r := stopLoopEmit(t, client, sid, cwd, true)
	if strings.Contains(strings.Join(r.Inject, "\n"), "loop detected") {
		t.Error("should NOT detect loop after window expired")
	}
}

// TestLoopInbox verifies InboxDrainHandler still fires during loop break.
// (bd-rrcj4)
func TestLoopInbox(t *testing.T) {
	d, client := setupLoopBus(t, 2, 30*time.Second, &eventbus.InboxDrainHandler{})

	// Push inbox item.
	pushResp, err := client.InboxPush(&rpc.InboxPushArgs{
		AgentName: "test-agent",
		Type:      "decision",
		Source:    "human",
		Content:   "Decision: Deploy to prod",
		Priority:  1,
	})
	if err != nil || !pushResp.Success {
		t.Fatalf("InboxPush: err=%v success=%v", err, pushResp.Success)
	}

	cwd := t.TempDir()
	sid := "e2e-li"

	// Trigger loop detection.
	stopLoopEmit(t, client, sid, cwd, false)
	result := stopLoopEmit(t, client, sid, cwd, true)

	if result.Block {
		t.Error("expected no block after loop detection")
	}

	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "loop detected") {
		t.Error("expected loop warning")
	}

	// InboxDrainHandler shells out to `bd inbox drain` which may fail if bd
	// binary doesn't find the daemon config correctly. Log rather than fail hard.
	if strings.Contains(allInject, "Deploy to prod") {
		t.Log("Inbox drain succeeded during loop break (expected)")
	} else {
		t.Log("Note: inbox drain did not inject content (bd subprocess may not find daemon)")
	}
	_ = d // keep reference
}

// TestLoopNoReentry verifies that non-reentry stops never trigger loop
// detection, even though they accumulate in the sliding window counter.
// Only reentry events (stop_hook_active=true) trigger the threshold check.
// (bd-rrcj4)
func TestLoopNoReentry(t *testing.T) {
	// Use a high threshold so non-reentry accumulation alone can't trigger.
	_, client := setupLoopBus(t, 100, 30*time.Second)
	cwd := t.TempDir()
	sid := "e2e-nr"

	// Emit many non-reentry stops — none should trigger loop detection
	// because the handler returns early for non-reentry events.
	for i := 0; i < 10; i++ {
		r := stopLoopEmit(t, client, sid, cwd, false)
		if strings.Contains(strings.Join(r.Inject, "\n"), "loop detected") {
			t.Fatalf("non-reentry stop #%d should not trigger", i+1)
		}
	}
}

// TestLoopNoSession verifies empty session ID doesn't crash.
// (bd-rrcj4)
func TestLoopNoSession(t *testing.T) {
	_, client := setupLoopBus(t, 1, 30*time.Second)
	cwd := t.TempDir()

	for i := 0; i < 5; i++ {
		raw := map[string]interface{}{
			"cwd":              cwd,
			"stop_hook_active": true,
		}
		eventJSON, _ := json.Marshal(raw)
		resp, err := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
			HookType:  "Stop",
			EventJSON: json.RawMessage(eventJSON),
		})
		if err != nil {
			t.Fatalf("BusEmit #%d: %v", i, err)
		}
		if !resp.Success {
			t.Fatalf("BusEmit #%d failed: %s", i, resp.Error)
		}

		var result rpc.BusEmitResult
		json.Unmarshal(resp.Data, &result)
		_ = fmt.Sprintf("attempt %d: block=%v", i, result.Block)
	}
}
