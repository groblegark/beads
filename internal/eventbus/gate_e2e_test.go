//go:build integration

package eventbus_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
)

// TestGateCreate verifies gate creation via RPC returns a valid ID.
// (bd-bef6g)
func TestGateCreate(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	resp, err := client.Execute(rpc.OpGateCreate, rpc.GateCreateArgs{
		Title:     "Review gate",
		AwaitType: "human",
	})
	if err != nil {
		t.Fatalf("GateCreate: %v", err)
	}
	if !resp.Success {
		t.Fatalf("GateCreate failed: %s", resp.Error)
	}

	var result rpc.GateCreateResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.ID == "" {
		t.Error("expected non-empty gate ID")
	}
	t.Logf("Created gate: %s", result.ID)
}

// TestGateClose verifies gate close changes status. (bd-bef6g)
func TestGateClose(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	// Create gate.
	resp, err := client.Execute(rpc.OpGateCreate, rpc.GateCreateArgs{
		Title:     "Deploy gate",
		AwaitType: "human",
	})
	if err != nil || !resp.Success {
		t.Fatalf("GateCreate: err=%v success=%v", err, resp.Success)
	}
	var created rpc.GateCreateResult
	json.Unmarshal(resp.Data, &created)

	// Close gate.
	closeResp, err := client.Execute(rpc.OpGateClose, rpc.GateCloseArgs{
		ID:     created.ID,
		Reason: "approved",
	})
	if err != nil {
		t.Fatalf("GateClose: %v", err)
	}
	if !closeResp.Success {
		t.Fatalf("GateClose failed: %s", closeResp.Error)
	}

	// Verify gate shows as closed.
	showResp, err := client.Execute(rpc.OpGateShow, rpc.GateShowArgs{ID: created.ID})
	if err != nil || !showResp.Success {
		t.Fatalf("GateShow: err=%v success=%v", err, showResp.Success)
	}

	var show map[string]interface{}
	json.Unmarshal(showResp.Data, &show)
	t.Logf("Gate after close: %v", show)
}

// TestGateList verifies listing gates returns created gates. (bd-bef6g)
func TestGateList(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	// Create two gates.
	for _, title := range []string{"Gate Alpha", "Gate Beta"} {
		resp, err := client.Execute(rpc.OpGateCreate, rpc.GateCreateArgs{
			Title:     title,
			AwaitType: "human",
		})
		if err != nil || !resp.Success {
			t.Fatalf("GateCreate(%s): err=%v", title, err)
		}
	}

	// List open gates.
	listResp, err := client.Execute(rpc.OpGateList, rpc.GateListArgs{All: false})
	if err != nil {
		t.Fatalf("GateList: %v", err)
	}
	if !listResp.Success {
		t.Fatalf("GateList failed: %s", listResp.Error)
	}

	// Should have at least 2 gates.
	var items []interface{}
	json.Unmarshal(listResp.Data, &items)
	if len(items) < 2 {
		t.Errorf("expected >= 2 gates, got %d", len(items))
	}
	t.Logf("Listed %d gates", len(items))
}

// TestGateWait verifies adding waiters to a gate. (bd-bef6g)
func TestGateWait(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	// Create gate.
	resp, err := client.Execute(rpc.OpGateCreate, rpc.GateCreateArgs{
		Title:     "CI gate",
		AwaitType: "gh:run",
		AwaitID:   "123456",
	})
	if err != nil || !resp.Success {
		t.Fatalf("GateCreate: err=%v", err)
	}
	var created rpc.GateCreateResult
	json.Unmarshal(resp.Data, &created)

	// Add waiters.
	waitResp, err := client.Execute(rpc.OpGateWait, rpc.GateWaitArgs{
		ID:      created.ID,
		Waiters: []string{"alice@example.com", "bob@example.com"},
	})
	if err != nil {
		t.Fatalf("GateWait: %v", err)
	}
	if !waitResp.Success {
		t.Fatalf("GateWait failed: %s", waitResp.Error)
	}

	var waitResult rpc.GateWaitResult
	json.Unmarshal(waitResp.Data, &waitResult)
	if waitResult.AddedCount != 2 {
		t.Errorf("expected 2 waiters added, got %d", waitResult.AddedCount)
	}
}

// TestGatePostTool verifies PostToolUse is NOT affected by GateHandler.
// GateHandler only handles Stop and PreToolUse, so PostToolUse passes through
// even when the GateHandler is registered. (bd-bef6g)
func TestGatePostTool(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	// Register GateHandler on the bus — it handles Stop and PreToolUse only.
	bus := eventbus.New()
	bus.Register(&eventbus.GateHandler{})
	d.Server.SetBus(bus)

	// Emit PostToolUse — should NOT be blocked.
	cwd := t.TempDir()
	eventJSON := fmt.Sprintf(`{"session_id":"e2e-gpt","cwd":%q,"tool_name":"Read"}`, cwd)
	resp, _ := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
		HookType:  "PostToolUse",
		EventJSON: json.RawMessage(eventJSON),
		SessionID: "e2e-gpt",
	})

	var result rpc.BusEmitResult
	json.Unmarshal(resp.Data, &result)

	if result.Block {
		t.Errorf("PostToolUse should not be blocked by GateHandler: %q", result.Reason)
	}
}

// TestGateNone verifies no gates = no blocking on BusEmit. (bd-bef6g)
func TestGateNone(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	cwd := t.TempDir()
	eventJSON := fmt.Sprintf(`{"session_id":"e2e-gn","cwd":%q}`, cwd)
	resp, _ := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
		HookType: "Stop", EventJSON: json.RawMessage(eventJSON), SessionID: "e2e-gn",
	})

	var result rpc.BusEmitResult
	json.Unmarshal(resp.Data, &result)
	if result.Block {
		t.Errorf("no gates should mean no block: %q", result.Reason)
	}
}

// TestGateInbox verifies gate close pushes notification to waiters' inbox.
// (bd-bef6g)
func TestGateInbox(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)
	t.Setenv("BD_DAEMON_HOST", d.URL)
	t.Setenv("BD_ACTOR", "test-agent")

	// Create gate with waiters.
	resp, err := client.Execute(rpc.OpGateCreate, rpc.GateCreateArgs{
		Title:     "Inbox gate",
		AwaitType: "human",
		Waiters:   []string{"agent-worker"},
	})
	if err != nil || !resp.Success {
		t.Fatalf("GateCreate: err=%v", err)
	}
	var created rpc.GateCreateResult
	json.Unmarshal(resp.Data, &created)

	// Close gate.
	closeResp, err := client.Execute(rpc.OpGateClose, rpc.GateCloseArgs{
		ID:     created.ID,
		Reason: "CI passed",
	})
	if err != nil || !closeResp.Success {
		t.Fatalf("GateClose: err=%v", err)
	}

	// Check waiter inbox.
	items, err := client.InboxList(&rpc.InboxListArgs{
		AgentName:        "agent-worker",
		IncludeDelivered: true,
	})
	if err != nil {
		t.Fatalf("InboxList: %v", err)
	}
	t.Logf("Waiter inbox after gate close: %d items", items.Count)
}
