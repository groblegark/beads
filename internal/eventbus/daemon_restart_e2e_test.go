//go:build integration

package eventbus_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"time"

	"github.com/google/uuid"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
	"github.com/steveyegge/beads/internal/types"
)

// These tests verify the persistence guarantees that make daemon restart safe:
// data stored in Dolt (inbox, decisions, config) survives server lifecycle
// changes and can be queried/drained by freshly registered handlers.
// (bd-p8jtw)

// TestRestartInboxPersists verifies that inbox items in Dolt persist across
// bus replacement. Push items via RPC, replace the bus with fresh handlers,
// and verify items are still drainable. This simulates the key restart
// property: Dolt state outlives in-memory bus state. (bd-p8jtw)
func TestRestartInboxPersists(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	// Phase 1: Push inbox items (stored in Dolt via RPC).
	pushInboxItem(t, client, "restart-agent", "Critical: creds expired", "alert", 0)
	pushInboxItem(t, client, "restart-agent", "Decision resolved: deploy", "decision", 1)
	pushInboxItem(t, client, "restart-agent", "Teammate mail", "mail", 2)

	list1, err := client.InboxList(&rpc.InboxListArgs{AgentName: "restart-agent"})
	if err != nil {
		t.Fatalf("InboxList: %v", err)
	}
	if list1.Count != 3 {
		t.Fatalf("expected 3 items, got %d", list1.Count)
	}

	// Phase 2: Replace bus (simulates fresh daemon with same Dolt).
	// Old handlers are gone — register fresh ones.
	bus := eventbus.New()
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "restart-agent"})
	d.Server.SetBus(bus)

	// Items should still be in Dolt and drainable by the new handler.
	result := emitEvent(t, client, "SessionStart", "e2e-restart", t.TempDir())
	allInject := strings.Join(result.Inject, "\n")
	for _, want := range []string{"creds expired", "deploy", "Teammate mail"} {
		if !strings.Contains(allInject, want) {
			t.Errorf("expected %q in drain after bus replacement, got: %s", want, allInject)
		}
	}

	// Verify items marked delivered after drain.
	afterList, err := client.InboxList(&rpc.InboxListArgs{
		AgentName:        "restart-agent",
		IncludeDelivered: false,
	})
	if err != nil {
		t.Fatalf("InboxList after drain: %v", err)
	}
	if afterList.Count != 0 {
		t.Errorf("expected 0 undelivered items, got %d", afterList.Count)
	}
}

// TestRestartDecisionPersists verifies that decisions in Dolt persist across
// bus replacement and can be queried and resolved by a fresh server. (bd-p8jtw)
func TestRestartDecisionPersists(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectClient(t, d)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	// Create a decision.
	issueID := createGateIssue(t, client, "Restart decision test")
	_, err := client.DecisionCreate(&rpc.DecisionCreateArgs{
		IssueID:     issueID,
		Prompt:      "Continue after restart?",
		Options:     rpc.StringOptions("Yes", "No"),
		RequestedBy: "restart-agent",
	})
	if err != nil {
		t.Fatalf("DecisionCreate: %v", err)
	}

	// Replace bus (simulates restart).
	d.Server.SetBus(eventbus.New())

	// Decision should still be queryable from Dolt.
	dec, err := client.DecisionGet(&rpc.DecisionGetArgs{IssueID: issueID})
	if err != nil {
		t.Fatalf("DecisionGet after bus replacement: %v", err)
	}
	if dec.Decision == nil {
		t.Fatal("expected decision to be present")
	}
	if dec.Decision.Prompt != "Continue after restart?" {
		t.Errorf("expected prompt preserved, got %q", dec.Decision.Prompt)
	}

	// Should be able to resolve.
	_, err = client.DecisionResolve(&rpc.DecisionResolveArgs{
		IssueID:        issueID,
		SelectedOption: "Yes",
		RespondedBy:    "human",
	})
	if err != nil {
		t.Fatalf("DecisionResolve: %v", err)
	}
}

// TestRestartExternalHandlerPersists verifies that persisted external handlers
// stored in the Dolt config table can be reloaded by a fresh bus. (bd-p8jtw)
func TestRestartExtHandler(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	// Phase 1: Register external handler (persisted to config table).
	bus1 := eventbus.New()
	d.Server.SetBus(bus1)

	regResp, err := client.Execute(rpc.OpBusRegister, rpc.BusRegisterArgs{
		ID:       "restart-ext-handler",
		Command:  `echo '{"inject":["persisted handler fired"]}'`,
		Events:   []string{"SessionStart"},
		Priority: 25,
		Shell:    "sh",
		Persist:  true,
	})
	if err != nil || !regResp.Success {
		t.Fatalf("BusRegister: err=%v success=%v error=%s", err, regResp.Success, regResp.Error)
	}

	// Verify persisted in config table.
	ctx := context.Background()
	configVal, err := d.Store.GetConfig(ctx, eventbus.HandlerConfigPrefix+"restart-ext-handler")
	if err != nil || configVal == "" {
		t.Fatalf("expected handler in config: err=%v val=%q", err, configVal)
	}

	// Phase 2: Replace bus (simulates restart) and reload from config.
	bus2 := eventbus.New()
	configs, err := d.Store.GetAllConfig(ctx)
	if err != nil {
		t.Fatalf("GetAllConfig: %v", err)
	}
	n := bus2.LoadPersistedHandlers(configs)
	if n != 1 {
		t.Fatalf("expected 1 persisted handler, got %d", n)
	}
	d.Server.SetBus(bus2)

	// External handler should fire on SessionStart.
	result := emitEvent(t, client, "SessionStart", "e2e-restart-ext", t.TempDir())
	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "persisted handler fired") {
		t.Errorf("expected persisted handler injection, got: %v", result.Inject)
	}
}

// TestRestartHandlerListRefresh verifies that after bus replacement, the
// handler list correctly reflects newly registered handlers. (bd-p8jtw)
func TestRestartHandlerList(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	// Phase 1: Register a handler.
	bus1 := eventbus.New()
	bus1.Register(&storeInboxDrainHandler{store: d.Store, agentName: "test-agent"})
	d.Server.SetBus(bus1)

	// Verify handler appears.
	handlersResp, err := client.Execute(rpc.OpBusHandlers, nil)
	if err != nil {
		t.Fatalf("BusHandlers: %v", err)
	}
	var result1 rpc.BusHandlersResult
	json.Unmarshal(handlersResp.Data, &result1)
	if len(result1.Handlers) == 0 {
		t.Fatal("expected at least one handler")
	}

	// Phase 2: Replace bus with empty one (simulates restart without registering handlers).
	d.Server.SetBus(eventbus.New())

	handlersResp2, err := client.Execute(rpc.OpBusHandlers, nil)
	if err != nil {
		t.Fatalf("BusHandlers after replacement: %v", err)
	}
	var result2 rpc.BusHandlersResult
	json.Unmarshal(handlersResp2.Data, &result2)
	if len(result2.Handlers) != 0 {
		t.Errorf("expected 0 handlers after bus replacement, got %d", len(result2.Handlers))
	}

	// Phase 3: Register new handlers (simulates post-restart handler setup).
	bus3 := eventbus.New()
	bus3.Register(&storeInboxDrainHandler{store: d.Store, agentName: "test-agent"})
	d.Server.SetBus(bus3)

	handlersResp3, _ := client.Execute(rpc.OpBusHandlers, nil)
	var result3 rpc.BusHandlersResult
	json.Unmarshal(handlersResp3.Data, &result3)
	if len(result3.Handlers) != 1 {
		t.Errorf("expected 1 handler after re-registration, got %d", len(result3.Handlers))
	}
}

// TestRestartInboxPushDuringDowntime verifies that items pushed directly to
// the Dolt store (simulating arrival during server downtime) are available
// when the bus drains on the next SessionStart. (bd-p8jtw)
func TestRestartDowntime(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	// Push directly to store (simulates data arriving during downtime).
	ctx := context.Background()
	err := d.Store.InboxPush(ctx, &types.InboxItem{
		ID:        uuid.New().String(),
		AgentName: "downtime-agent",
		Type:      "alert",
		Source:    "system",
		Priority:  0,
		Content:   "Pushed during downtime",
		CreatedAt: time.Now().UTC(),
		DedupKey:  "downtime-1",
	})
	if err != nil {
		t.Fatalf("InboxPush to store: %v", err)
	}

	// Wire up bus.
	bus := eventbus.New()
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "downtime-agent"})
	d.Server.SetBus(bus)

	// SessionStart drain should pick up the store-pushed item.
	result := emitEvent(t, client, "SessionStart", "e2e-downtime", t.TempDir())
	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "Pushed during downtime") {
		t.Errorf("expected downtime item in drain, got: %v", result.Inject)
	}
}
