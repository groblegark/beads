//go:build integration

package eventbus_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
)

// connectClient connects a socket-based RPC client to the test daemon.
func connectClient(t *testing.T, d *testdaemon.Daemon) *rpc.Client {
	t.Helper()
	client, err := rpc.TryConnect(d.SocketPath)
	if err != nil {
		t.Fatalf("Failed to connect to test daemon socket: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// createGateIssue creates a gate-type issue and returns its ID.
func createGateIssue(t *testing.T, client *rpc.Client, title string) string {
	t.Helper()
	resp, err := client.Create(&rpc.CreateArgs{
		Title:     title,
		IssueType: "gate",
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("Create issue: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Create issue failed: %s", resp.Error)
	}
	var issue struct{ ID string }
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("Unmarshal issue: %v", err)
	}
	return issue.ID
}

// setupDaemonWithNudgeHandler starts a test daemon and wires an event bus with
// a DecisionNudgeHandler using the given HTTP client and coop URL resolver.
func setupDaemonWithNudgeHandler(t *testing.T, httpClient *http.Client, resolver eventbus.CoopURLResolver) *testdaemon.Daemon {
	t.Helper()
	d := testdaemon.Start(t)
	bus := eventbus.New()
	bus.Register(&eventbus.DecisionNudgeHandler{
		HTTPClient:      httpClient,
		CoopURLResolver: resolver,
	})
	d.Server.SetBus(bus)
	return d
}

// TestDecisionResponseTriggersCoopNudge verifies the full decision workflow:
// create decision → resolve decision → EventDecisionResponded emitted →
// DecisionNudgeHandler fires → POST to coop sidecar /api/v1/agent/nudge.
func TestDecisionResponseTriggersCoopNudge(t *testing.T) {
	// 1. Start a mock Coop sidecar that records nudge requests.
	var nudgeReceived atomic.Bool
	var nudgeMessage atomic.Value
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/nudge" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		nudgeMessage.Store(body["message"])
		nudgeReceived.Store(true)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"delivered":    true,
			"state_before": "idle",
		})
	}))
	defer coopServer.Close()

	// 2. Start test daemon with event bus + nudge handler.
	d := setupDaemonWithNudgeHandler(t, coopServer.Client(),
		func(_ context.Context, _, _ string) (string, error) {
			return coopServer.URL, nil
		},
	)

	// 3. Connect an RPC client and create a gate issue.
	client := connectClient(t, d)
	issueID := createGateIssue(t, client, "Deploy to production?")

	// 4. Create a decision point with RequestedBy identifying the agent.
	_, err := client.DecisionCreate(&rpc.DecisionCreateArgs{
		IssueID:     issueID,
		Prompt:      "Deploy to production?",
		Options:     rpc.StringOptions("Yes, deploy", "No, abort"),
		RequestedBy: "gt-gastown-polecat-test",
	})
	if err != nil {
		t.Fatalf("DecisionCreate: %v", err)
	}

	// 5. Resolve the decision (simulates human responding).
	_, err = client.DecisionResolve(&rpc.DecisionResolveArgs{
		IssueID:        issueID,
		SelectedOption: "Yes, deploy",
		RespondedBy:    "human-reviewer",
	})
	if err != nil {
		t.Fatalf("DecisionResolve: %v", err)
	}

	// 6. Assert: the mock Coop server received the nudge.
	//    Dispatch is synchronous through the bus, so this should be immediate.
	deadline := time.Now().Add(5 * time.Second)
	for !nudgeReceived.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Timed out waiting for nudge to be received by mock Coop server")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 7. Verify nudge message content.
	msg, ok := nudgeMessage.Load().(string)
	if !ok || msg == "" {
		t.Fatal("Expected non-empty nudge message")
	}
	t.Logf("Nudge message: %s", msg)

	// Message format: "Decision <id> resolved by <responder> (chose: <option>)"
	for _, want := range []string{issueID, "human-reviewer", "Yes, deploy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Nudge message missing %q.\n  got: %s", want, msg)
		}
	}
}

// TestDecisionResponse_NoRequestedBy_NoNudge verifies that resolving a decision
// without a RequestedBy agent does not trigger a nudge.
func TestDecisionResponse_NoRequestedBy_NoNudge(t *testing.T) {
	var nudgeCalled atomic.Bool
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nudgeCalled.Store(true)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"delivered": true})
	}))
	defer coopServer.Close()

	d := setupDaemonWithNudgeHandler(t, coopServer.Client(),
		func(_ context.Context, _, _ string) (string, error) {
			return coopServer.URL, nil
		},
	)

	client := connectClient(t, d)
	issueID := createGateIssue(t, client, "No-agent decision")

	// Create decision WITHOUT RequestedBy.
	_, err := client.DecisionCreate(&rpc.DecisionCreateArgs{
		IssueID: issueID,
		Prompt:  "Should we proceed?",
		Options: rpc.StringOptions("Yes", "No"),
	})
	if err != nil {
		t.Fatalf("DecisionCreate: %v", err)
	}

	_, err = client.DecisionResolve(&rpc.DecisionResolveArgs{
		IssueID:        issueID,
		SelectedOption: "Yes",
		RespondedBy:    "human",
	})
	if err != nil {
		t.Fatalf("DecisionResolve: %v", err)
	}

	// Give enough time for any async dispatch to complete.
	time.Sleep(200 * time.Millisecond)

	if nudgeCalled.Load() {
		t.Error("Nudge should NOT have been called when RequestedBy is empty")
	}
}

// TestDecisionResponse_CoopDown_GracefulFailure verifies that the handler
// does not return an error when the Coop sidecar is unreachable.
func TestDecisionResponse_CoopDown_GracefulFailure(t *testing.T) {
	d := setupDaemonWithNudgeHandler(t, nil,
		func(_ context.Context, _, _ string) (string, error) {
			return "http://127.0.0.1:1", nil // Unreachable port
		},
	)

	client := connectClient(t, d)
	issueID := createGateIssue(t, client, "Coop-down test")

	_, err := client.DecisionCreate(&rpc.DecisionCreateArgs{
		IssueID:     issueID,
		Prompt:      "Test with coop down?",
		Options:     rpc.StringOptions("Yes", "No"),
		RequestedBy: "test-agent",
	})
	if err != nil {
		t.Fatalf("DecisionCreate: %v", err)
	}

	// Resolve should succeed even though coop is down (handler is best-effort).
	_, err = client.DecisionResolve(&rpc.DecisionResolveArgs{
		IssueID:        issueID,
		SelectedOption: "Yes",
		RespondedBy:    "human",
	})
	if err != nil {
		t.Fatalf("DecisionResolve should not fail when coop is down: %v", err)
	}
}

// TestDecisionResponse_AgentBusy_NotDelivered verifies graceful handling when
// the Coop sidecar reports the agent is busy (delivered=false).
func TestDecisionResponse_AgentBusy_NotDelivered(t *testing.T) {
	var nudgeReceived atomic.Bool
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nudgeReceived.Store(true)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"delivered":    false,
			"state_before": "working",
			"reason":       "agent is busy",
		})
	}))
	defer coopServer.Close()

	d := setupDaemonWithNudgeHandler(t, coopServer.Client(),
		func(_ context.Context, _, _ string) (string, error) {
			return coopServer.URL, nil
		},
	)

	client := connectClient(t, d)
	issueID := createGateIssue(t, client, "Busy agent test")

	_, err := client.DecisionCreate(&rpc.DecisionCreateArgs{
		IssueID:     issueID,
		Prompt:      "Test busy agent?",
		Options:     rpc.StringOptions("Go", "Stop"),
		RequestedBy: "busy-agent",
	})
	if err != nil {
		t.Fatalf("DecisionCreate: %v", err)
	}

	_, err = client.DecisionResolve(&rpc.DecisionResolveArgs{
		IssueID:        issueID,
		SelectedOption: "Go",
		RespondedBy:    "human",
	})
	if err != nil {
		t.Fatalf("DecisionResolve should succeed even when agent is busy: %v", err)
	}

	// Verify the nudge was attempted even though delivery failed.
	deadline := time.Now().Add(5 * time.Second)
	for !nudgeReceived.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Timed out waiting for nudge attempt")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
