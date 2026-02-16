//go:build integration

package eventbus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
	"github.com/steveyegge/beads/internal/types"
)

// storeInboxDrainHandler is a test handler that drains inbox items by calling
// the Dolt store directly, bypassing RPC. This avoids the re-entrant RPC
// deadlock that occurs when a handler makes RPC calls during bus_emit dispatch
// (the server blocks waiting for the handler while the handler blocks waiting
// for the server). Using the store directly still exercises real Dolt state.
type storeInboxDrainHandler struct {
	store     storage.Storage
	agentName string
	urgent    bool // when true, only drain P0/P1
}

func (h *storeInboxDrainHandler) ID() string { return "inbox-drain" }
func (h *storeInboxDrainHandler) Handles() []eventbus.EventType {
	if h.urgent {
		return []eventbus.EventType{eventbus.EventPostToolUse}
	}
	return []eventbus.EventType{eventbus.EventSessionStart, eventbus.EventPreCompact, eventbus.EventStop}
}
func (h *storeInboxDrainHandler) Priority() int { return 30 }

func (h *storeInboxDrainHandler) Handle(ctx context.Context, event *eventbus.Event, result *eventbus.Result) error {
	var items []*types.InboxItem
	var err error
	if h.urgent {
		items, err = h.store.InboxDrain(ctx, h.agentName, 1) // P0 + P1 only
	} else {
		items, err = h.store.InboxDrain(ctx, h.agentName)
	}
	if err != nil {
		return fmt.Errorf("inbox-drain: %w", err)
	}
	if len(items) == 0 {
		return nil
	}

	// Mark as delivered.
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	_ = h.store.InboxMarkDelivered(ctx, ids)

	// Format for injection.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Inbox: %d notification(s)\n", len(items)))
	for _, item := range items {
		sb.WriteString(fmt.Sprintf("- [%s] %s\n", item.Type, item.Content))
	}
	result.Inject = append(result.Inject, sb.String())
	return nil
}

// pushInboxItem is a test helper to push an item to an agent's inbox.
func pushInboxItem(t *testing.T, client *rpc.Client, agent, content, msgType string, priority int) {
	t.Helper()
	resp, err := client.InboxPush(&rpc.InboxPushArgs{
		AgentName: agent,
		Type:      msgType,
		Source:    "e2e-test",
		Content:   content,
		Priority:  priority,
	})
	if err != nil {
		t.Fatalf("InboxPush(%q): %v", content, err)
	}
	if !resp.Success {
		t.Fatalf("InboxPush(%q) failed: %s", content, resp.Error)
	}
}

// emitEvent is a test helper that emits an event via bus_emit RPC and returns the result.
func emitEvent(t *testing.T, client *rpc.Client, hookType, sessionID, cwd string) rpc.BusEmitResult {
	t.Helper()
	eventJSON := fmt.Sprintf(`{"session_id":%q,"cwd":%q}`, sessionID, cwd)
	resp, err := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
		HookType:  hookType,
		EventJSON: json.RawMessage(eventJSON),
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("BusEmit(%s): %v", hookType, err)
	}
	if !resp.Success {
		t.Fatalf("BusEmit(%s) failed: %s", hookType, resp.Error)
	}
	var result rpc.BusEmitResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal BusEmitResult: %v", err)
	}
	return result
}

// TestSessionStartE2E_InboxDrain verifies the full SessionStart hook lifecycle:
// push inbox items → emit SessionStart → verify items appear in result.Inject
// and are marked as delivered in Dolt.
func TestSessionStartE2E_InboxDrain(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "test-agent"})
	d.Server.SetBus(bus)

	// 1. Push inbox items at different priorities.
	pushInboxItem(t, client, "test-agent", "Critical alert: creds expired", "alert", 0)
	pushInboxItem(t, client, "test-agent", "High priority: decision response", "decision", 1)
	pushInboxItem(t, client, "test-agent", "Normal: teammate mail", "mail", 2)

	// 2. Verify items are in the inbox (not yet delivered).
	listResult, err := client.InboxList(&rpc.InboxListArgs{AgentName: "test-agent"})
	if err != nil {
		t.Fatalf("InboxList: %v", err)
	}
	if listResult.Count != 3 {
		t.Fatalf("expected 3 inbox items before drain, got %d", listResult.Count)
	}

	// 3. Emit SessionStart.
	cwd := t.TempDir()
	result := emitEvent(t, client, "SessionStart", "e2e-sess-1", cwd)

	// 4. Should NOT block.
	if result.Block {
		t.Errorf("expected Block=false, got Block=true reason=%q", result.Reason)
	}

	// 5. Verify injections contain all inbox items.
	allInject := strings.Join(result.Inject, "\n")
	for _, want := range []string{"creds expired", "decision response", "teammate mail"} {
		if !strings.Contains(allInject, want) {
			t.Errorf("expected injection to contain %q, got: %s", want, allInject)
		}
	}

	// 6. Verify items are marked delivered.
	afterList, err := client.InboxList(&rpc.InboxListArgs{
		AgentName:        "test-agent",
		IncludeDelivered: false,
	})
	if err != nil {
		t.Fatalf("InboxList after drain: %v", err)
	}
	if afterList.Count != 0 {
		t.Errorf("expected 0 undelivered items after drain, got %d", afterList.Count)
	}

	// Delivered items should still be in DB.
	allItems, err := client.InboxList(&rpc.InboxListArgs{
		AgentName:        "test-agent",
		IncludeDelivered: true,
	})
	if err != nil {
		t.Fatalf("InboxList (include delivered): %v", err)
	}
	if allItems.Count != 3 {
		t.Errorf("expected 3 total items (delivered), got %d", allItems.Count)
	}
}

// TestSessionStartE2E_EmptyInbox verifies SessionStart with no inbox items
// completes without inbox injection.
func TestSessionStartE2E_EmptyInbox(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "test-agent"})
	d.Server.SetBus(bus)

	result := emitEvent(t, client, "SessionStart", "e2e-empty", t.TempDir())

	if result.Block {
		t.Error("expected no block for empty inbox SessionStart")
	}
	// No inbox items → no inbox injection.
	for _, inject := range result.Inject {
		if strings.Contains(inject, "Inbox:") {
			t.Errorf("expected no inbox injection with empty inbox, got: %q", inject)
		}
	}
}

// TestPostToolUseE2E_UrgentOnly verifies that PostToolUse only drains
// urgent (P0/P1) inbox items, leaving lower-priority items for SessionStart.
func TestPostToolUseE2E_UrgentOnly(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "test-agent"})
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "test-agent", urgent: true})
	d.Server.SetBus(bus)

	// Push items at different priorities.
	pushInboxItem(t, client, "test-agent", "CRITICAL: system down", "alert", 0)
	pushInboxItem(t, client, "test-agent", "HIGH: decision needed", "decision", 1)
	pushInboxItem(t, client, "test-agent", "NORMAL: new mail", "mail", 2)
	pushInboxItem(t, client, "test-agent", "LOW: background job done", "event", 3)

	// Emit PostToolUse — should only drain urgent items (P0+P1).
	cwd := t.TempDir()
	result := emitEvent(t, client, "PostToolUse", "e2e-ptu", cwd)

	// Check that urgent items were injected.
	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "CRITICAL: system down") {
		t.Error("expected CRITICAL item in PostToolUse inject")
	}
	if !strings.Contains(allInject, "HIGH: decision needed") {
		t.Error("expected HIGH item in PostToolUse inject")
	}

	// NORMAL and LOW items should NOT be injected.
	if strings.Contains(allInject, "NORMAL: new mail") {
		t.Error("PostToolUse should NOT drain NORMAL priority items")
	}
	if strings.Contains(allInject, "LOW: background job done") {
		t.Error("PostToolUse should NOT drain LOW priority items")
	}

	// Verify NORMAL and LOW items remain undelivered.
	remaining, err := client.InboxList(&rpc.InboxListArgs{
		AgentName:        "test-agent",
		IncludeDelivered: false,
	})
	if err != nil {
		t.Fatalf("InboxList: %v", err)
	}
	if remaining.Count != 2 {
		t.Errorf("expected 2 remaining items (P2+P3), got %d", remaining.Count)
	}

	// Now emit SessionStart to drain remaining items.
	result2 := emitEvent(t, client, "SessionStart", "e2e-ptu-2", cwd)
	allInject2 := strings.Join(result2.Inject, "\n")

	if !strings.Contains(allInject2, "NORMAL: new mail") {
		t.Error("expected NORMAL item in SessionStart inject (left over from PostToolUse)")
	}
	if !strings.Contains(allInject2, "LOW: background job done") {
		t.Error("expected LOW item in SessionStart inject (left over from PostToolUse)")
	}

	// All items should now be delivered.
	final, err := client.InboxList(&rpc.InboxListArgs{
		AgentName:        "test-agent",
		IncludeDelivered: false,
	})
	if err != nil {
		t.Fatalf("InboxList: %v", err)
	}
	if final.Count != 0 {
		t.Errorf("expected 0 undelivered items after full drain, got %d", final.Count)
	}
}

// TestStopE2E_InboxDrainOnStop verifies that inbox drain fires during
// Stop events, ensuring decision responses are delivered before the agent stops.
func TestStopE2E_InboxDrainOnStop(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "test-agent"})
	d.Server.SetBus(bus)

	// Simulate a decision response arriving while agent was busy.
	pushInboxItem(t, client, "test-agent", "Decision resolved: Yes, deploy", "decision", 1)

	// Emit Stop event.
	result := emitEvent(t, client, "Stop", "e2e-stop", t.TempDir())

	// InboxDrainHandler (only handler) should not block.
	if result.Block {
		t.Errorf("expected no block, got block: %q", result.Reason)
	}

	// Decision response should be in the injections.
	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "deploy") {
		t.Errorf("expected decision response in Stop inject, got: %v", result.Inject)
	}
}

// TestPreCompactE2E_InboxDrain verifies inbox drain fires on PreCompact.
func TestPreCompactE2E_InboxDrain(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "test-agent"})
	d.Server.SetBus(bus)

	pushInboxItem(t, client, "test-agent", "Build failed on main", "system", 2)

	result := emitEvent(t, client, "PreCompact", "e2e-compact", t.TempDir())

	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "Build failed") {
		t.Errorf("expected inbox item in PreCompact inject, got: %v", result.Inject)
	}
}

// TestInboxDedupE2E verifies that inbox items with the same dedup_key are
// deduplicated. NOTE: dedup_key currently uses a non-unique INDEX, so
// INSERT IGNORE only deduplicates on the primary key (id). This test
// verifies the current behavior: items with different IDs but the same
// dedup_key are stored separately. When dedup_key is upgraded to UNIQUE,
// update this test to expect count=1.
func TestInboxDedupE2E(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	// Push two items with the same dedup key.
	for i := 0; i < 2; i++ {
		resp, err := client.InboxPush(&rpc.InboxPushArgs{
			AgentName: "test-agent",
			Type:      "alert",
			Source:    "ci",
			Content:   "CI build failed",
			Priority:  2,
			DedupKey:  "ci-build-42",
		})
		if err != nil {
			t.Fatalf("InboxPush attempt %d: %v", i+1, err)
		}
		if !resp.Success {
			t.Fatalf("InboxPush attempt %d failed: %s", i+1, resp.Error)
		}
	}

	// dedup_key is a UNIQUE index (bd-56da5), so the second push is silently
	// deduplicated and only one item is stored.
	list, err := client.InboxList(&rpc.InboxListArgs{AgentName: "test-agent"})
	if err != nil {
		t.Fatalf("InboxList: %v", err)
	}
	if list.Count != 1 {
		t.Errorf("expected 1 item (dedup_key is UNIQUE), got %d", list.Count)
	}
}

// TestMultiAgentInboxIsolationE2E verifies that inbox items for one agent
// don't leak to another agent's drain.
func TestMultiAgentInboxIsolationE2E(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "agent-a"})
	d.Server.SetBus(bus)

	// Push items for both agents.
	pushInboxItem(t, client, "agent-a", "Message for agent A", "agent", 2)
	pushInboxItem(t, client, "agent-b", "Message for agent B", "agent", 2)

	// Emit SessionStart — drain handler is configured for agent-a.
	result := emitEvent(t, client, "SessionStart", "e2e-multi", t.TempDir())

	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "Message for agent A") {
		t.Error("expected agent A's message in inject")
	}
	if strings.Contains(allInject, "Message for agent B") {
		t.Error("agent B's message should NOT be in agent A's inject")
	}

	// Agent B's item should still be undelivered.
	bList, err := client.InboxList(&rpc.InboxListArgs{AgentName: "agent-b"})
	if err != nil {
		t.Fatalf("InboxList(agent-b): %v", err)
	}
	if bList.Count != 1 {
		t.Errorf("expected 1 undelivered item for agent-b, got %d", bList.Count)
	}
}

// TestExternalHandlerE2E verifies external handler registration, dispatch,
// persistence, and unregistration.
func TestExternalHandlerE2E(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	d.Server.SetBus(bus)

	// 1. Register an external handler.
	regResp, err := client.Execute(rpc.OpBusRegister, rpc.BusRegisterArgs{
		ID:       "e2e-test-handler",
		Command:  `echo '{"inject":["external handler fired"]}'`,
		Events:   []string{"SessionStart"},
		Priority: 25,
		Shell:    "sh",
		Persist:  true,
	})
	if err != nil {
		t.Fatalf("BusRegister: %v", err)
	}
	if !regResp.Success {
		t.Fatalf("BusRegister failed: %s", regResp.Error)
	}

	// 2. Emit SessionStart — external handler should fire.
	result := emitEvent(t, client, "SessionStart", "e2e-ext", t.TempDir())

	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "external handler fired") {
		t.Errorf("expected external handler injection, got: %v", result.Inject)
	}

	// 3. Verify handler in handler list.
	handlersResp, err := client.Execute(rpc.OpBusHandlers, nil)
	if err != nil {
		t.Fatalf("BusHandlers: %v", err)
	}
	var handlersResult rpc.BusHandlersResult
	if err := json.Unmarshal(handlersResp.Data, &handlersResult); err != nil {
		t.Fatalf("unmarshal handlers: %v", err)
	}
	foundExternal := false
	for _, h := range handlersResult.Handlers {
		if h.ID == "e2e-test-handler" && h.External {
			foundExternal = true
			if h.Priority != 25 {
				t.Errorf("expected priority 25, got %d", h.Priority)
			}
		}
	}
	if !foundExternal {
		t.Error("external handler not found in handler list")
	}

	// 4. Verify persisted in config table.
	ctx := context.Background()
	configVal, err := d.Store.GetConfig(ctx, eventbus.HandlerConfigPrefix+"e2e-test-handler")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if configVal == "" {
		t.Error("external handler not persisted in config table")
	}

	// 5. Unregister.
	unregResp, err := client.Execute(rpc.OpBusUnregister, rpc.BusUnregisterArgs{
		ID: "e2e-test-handler",
	})
	if err != nil {
		t.Fatalf("BusUnregister: %v", err)
	}
	if !unregResp.Success {
		t.Fatalf("BusUnregister failed: %s", unregResp.Error)
	}

	// 6. Emit again — handler should NOT fire.
	result2 := emitEvent(t, client, "SessionStart", "e2e-ext-2", t.TempDir())
	for _, inject := range result2.Inject {
		if strings.Contains(inject, "external handler fired") {
			t.Error("external handler should NOT fire after unregister")
		}
	}

	// 7. Verify config entry removed.
	configVal2, err := d.Store.GetConfig(ctx, eventbus.HandlerConfigPrefix+"e2e-test-handler")
	if err != nil {
		t.Fatalf("GetConfig after unregister: %v", err)
	}
	if configVal2 != "" {
		t.Error("external handler config should be removed after unregister")
	}
}

// TestHandlerChainOrderingE2E verifies that handlers run in priority order
// and their results are properly aggregated.
func TestHandlerChainOrderingE2E(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()

	// Register handlers at different priorities that inject numbered markers.
	for _, tc := range []struct {
		id       string
		priority int
		marker   string
	}{
		{"first-10", 10, "FIRST(10)"},
		{"second-20", 20, "SECOND(20)"},
		{"third-30", 30, "THIRD(30)"},
	} {
		tc := tc
		bus.Register(&testOrderHandler{
			id:       tc.id,
			priority: tc.priority,
			marker:   tc.marker,
		})
	}
	d.Server.SetBus(bus)

	result := emitEvent(t, client, "SessionStart", "e2e-order", t.TempDir())

	// Verify all 3 handlers fired.
	if len(result.Inject) != 3 {
		t.Fatalf("expected 3 injections, got %d: %v", len(result.Inject), result.Inject)
	}

	// Verify priority ordering (10 → 20 → 30).
	expected := []string{"FIRST(10)", "SECOND(20)", "THIRD(30)"}
	for i, want := range expected {
		if result.Inject[i] != want {
			t.Errorf("inject[%d]: expected %q, got %q (full: %v)", i, want, result.Inject[i], result.Inject)
		}
	}
}

// testOrderHandler is a simple handler that injects a marker string.
type testOrderHandler struct {
	id       string
	priority int
	marker   string
}

func (h *testOrderHandler) ID() string                { return h.id }
func (h *testOrderHandler) Handles() []eventbus.EventType { return []eventbus.EventType{eventbus.EventSessionStart} }
func (h *testOrderHandler) Priority() int              { return h.priority }
func (h *testOrderHandler) Handle(_ context.Context, _ *eventbus.Event, result *eventbus.Result) error {
	result.Inject = append(result.Inject, h.marker)
	return nil
}

// TestHandlerErrorIsolationE2E verifies that a failing handler doesn't
// prevent subsequent handlers from running.
func TestHandlerErrorIsolationE2E(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()

	// First handler (priority 5) fails.
	bus.Register(&testOrderHandler{id: "fail-5", priority: 5, marker: "FAIL"})
	bus.Register(&failHandler{id: "broken", priority: 10})
	bus.Register(&testOrderHandler{id: "survive-15", priority: 15, marker: "SURVIVED"})
	d.Server.SetBus(bus)

	result := emitEvent(t, client, "SessionStart", "e2e-error", t.TempDir())

	// Both non-failing handlers should have injected.
	if len(result.Inject) < 2 {
		t.Fatalf("expected at least 2 injections despite error handler, got %d: %v",
			len(result.Inject), result.Inject)
	}

	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "FAIL") {
		t.Error("first handler's injection missing")
	}
	if !strings.Contains(allInject, "SURVIVED") {
		t.Error("handler after error should still inject")
	}
}

// failHandler is a handler that always returns an error.
type failHandler struct {
	id       string
	priority int
}

func (h *failHandler) ID() string                { return h.id }
func (h *failHandler) Handles() []eventbus.EventType { return []eventbus.EventType{eventbus.EventSessionStart} }
func (h *failHandler) Priority() int              { return h.priority }
func (h *failHandler) Handle(_ context.Context, _ *eventbus.Event, _ *eventbus.Result) error {
	return fmt.Errorf("intentional test failure")
}
