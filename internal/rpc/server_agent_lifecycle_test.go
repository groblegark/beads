//go:build integration

package rpc_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
	"github.com/steveyegge/beads/internal/types"
)

// createAgentBead creates an agent bead with optional notes for coop_url.
func createAgentBead(t *testing.T, client *rpc.Client, agentID, coopURL string) {
	t.Helper()

	notes := ""
	if coopURL != "" {
		notes = "coop_url: " + coopURL
	}

	_, err := client.Create(&rpc.CreateArgs{
		ID:        agentID,
		Title:     "Agent: " + agentID,
		IssueType: "agent",
		Labels:    []string{"gt:agent"},
		Notes:     notes,
	})
	if err != nil {
		t.Fatalf("create agent bead %s: %v", agentID, err)
	}

	// Set agent_state to running.
	state := string(types.StateRunning)
	_, err = client.Update(&rpc.UpdateArgs{
		ID:         agentID,
		AgentState: &state,
	})
	if err != nil {
		t.Fatalf("set agent_state for %s: %v", agentID, err)
	}
}

func connectTestClient(t *testing.T, d *testdaemon.Daemon) *rpc.Client {
	t.Helper()
	client, err := rpc.TryConnect(d.SocketPath)
	if err != nil {
		t.Fatalf("connect to daemon: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func showAgentState(t *testing.T, client *rpc.Client, agentID string) types.AgentState {
	t.Helper()
	resp, err := client.Show(&rpc.ShowArgs{ID: agentID})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !resp.Success {
		t.Fatalf("Show failed: %s", resp.Error)
	}
	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("unmarshal issue: %v", err)
	}
	return issue.AgentState
}

func TestAgentStop_SetsStoppingState(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-deacon", "")

	result, err := client.AgentStop(&rpc.AgentStopArgs{
		AgentID: "hq-deacon",
		Reason:  "test stop",
	})
	if err != nil {
		t.Fatalf("AgentStop: %v", err)
	}
	if result.AgentState != "stopping" {
		t.Errorf("AgentState = %q, want %q", result.AgentState, "stopping")
	}
	if result.CoopSignal {
		t.Error("CoopSignal should be false when no coop_url")
	}

	// Verify the bead was updated.
	state := showAgentState(t, client, "hq-deacon")
	if state != types.StateStopping {
		t.Errorf("bead agent_state = %q, want %q", state, types.StateStopping)
	}
}

func TestAgentStop_WithCoopSignal(t *testing.T) {
	var signalReceived atomic.Bool
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/signal" && r.Method == http.MethodPost {
			var body struct {
				Signal string `json:"signal"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Signal == "SIGTERM" {
				signalReceived.Store(true)
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-deacon", coopServer.URL)

	result, err := client.AgentStop(&rpc.AgentStopArgs{
		AgentID: "hq-deacon",
	})
	if err != nil {
		t.Fatalf("AgentStop: %v", err)
	}
	if !result.CoopSignal {
		t.Error("CoopSignal should be true when coop_url is set and reachable")
	}
	if !signalReceived.Load() {
		t.Error("coop server did not receive SIGTERM")
	}
}

func TestAgentStop_ForceSkipsCoop(t *testing.T) {
	var signalReceived atomic.Bool
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/signal" {
			signalReceived.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-deacon", coopServer.URL)

	result, err := client.AgentStop(&rpc.AgentStopArgs{
		AgentID: "hq-deacon",
		Force:   true,
	})
	if err != nil {
		t.Fatalf("AgentStop: %v", err)
	}
	if result.CoopSignal {
		t.Error("CoopSignal should be false with Force=true")
	}
	if signalReceived.Load() {
		t.Error("coop server should not receive signal with Force=true")
	}
}

func TestAgentStop_NotFound(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	_, err := client.AgentStop(&rpc.AgentStopArgs{
		AgentID: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestRestart_Spawning(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-deacon", "")

	result, err := client.AgentRestart(&rpc.AgentRestartArgs{
		AgentID: "hq-deacon",
		Reason:  "test restart",
	})
	if err != nil {
		t.Fatalf("AgentRestart: %v", err)
	}
	if result.AgentState != "spawning" {
		t.Errorf("AgentState = %q, want %q", result.AgentState, "spawning")
	}

	// Verify the bead was updated.
	state := showAgentState(t, client, "hq-deacon")
	if state != types.StateSpawning {
		t.Errorf("bead agent_state = %q, want %q", state, types.StateSpawning)
	}
}

func TestRestart_CoopSignal(t *testing.T) {
	var signalReceived atomic.Bool
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/signal" {
			signalReceived.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-deacon", coopServer.URL)

	_, err := client.AgentRestart(&rpc.AgentRestartArgs{
		AgentID: "hq-deacon",
	})
	if err != nil {
		t.Fatalf("AgentRestart: %v", err)
	}
	if !signalReceived.Load() {
		t.Error("coop should receive SIGTERM before restart")
	}
}

func TestAgentSignal_SendsToAgent(t *testing.T) {
	var receivedSignal string
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/signal" && r.Method == http.MethodPost {
			var body struct {
				Signal string `json:"signal"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			receivedSignal = body.Signal
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-deacon", coopServer.URL)

	result, err := client.AgentSignal(&rpc.AgentSignalArgs{
		AgentID: "hq-deacon",
		Signal:  "SIGINT",
	})
	if err != nil {
		t.Fatalf("AgentSignal: %v", err)
	}
	if !result.Sent {
		t.Error("Sent should be true")
	}
	if receivedSignal != "SIGINT" {
		t.Errorf("received signal = %q, want %q", receivedSignal, "SIGINT")
	}
}

func TestAgentSignal_NoCoopURL(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-deacon", "")

	_, err := client.AgentSignal(&rpc.AgentSignalArgs{
		AgentID: "hq-deacon",
		Signal:  "SIGTERM",
	})
	if err == nil {
		t.Fatal("expected error when agent has no coop_url")
	}
}

func TestAgentSignal_MissingFields(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	// Missing agent_id.
	_, err := client.AgentSignal(&rpc.AgentSignalArgs{
		Signal: "SIGTERM",
	})
	if err == nil {
		t.Error("expected error for missing agent_id")
	}

	// Missing signal.
	createAgentBead(t, client, "hq-deacon", "http://localhost:9999")
	_, err = client.AgentSignal(&rpc.AgentSignalArgs{
		AgentID: "hq-deacon",
	})
	if err == nil {
		t.Error("expected error for missing signal")
	}
}

func TestAgentStop_MissingAgentID(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	_, err := client.AgentStop(&rpc.AgentStopArgs{})
	if err == nil {
		t.Fatal("expected error for missing agent_id")
	}
}

// showIssue retrieves the full issue for inspection.
func showIssue(t *testing.T, client *rpc.Client, id string) types.Issue {
	t.Helper()
	resp, err := client.Show(&rpc.ShowArgs{ID: id})
	if err != nil {
		t.Fatalf("Show %s: %v", id, err)
	}
	if !resp.Success {
		t.Fatalf("Show %s failed: %s", id, resp.Error)
	}
	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("unmarshal issue %s: %v", id, err)
	}
	return issue
}

// --- Pod register/deregister coop_url tests (bd-x820i) ---

func TestPodReg_CoopURL(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-cooptest", "")

	_, err := client.AgentPodRegister(&rpc.AgentPodRegisterArgs{
		AgentID: "hq-cooptest",
		PodName: "cooptest-pod",
		PodIP:   "10.0.1.5",
		CoopURL: "http://10.0.1.5:4000",
	})
	if err != nil {
		t.Fatalf("AgentPodRegister: %v", err)
	}

	issue := showIssue(t, client, "hq-cooptest")
	if !strings.Contains(issue.Notes, "coop_url: http://10.0.1.5:4000") {
		t.Errorf("notes should contain explicit coop_url, got: %q", issue.Notes)
	}
}

func TestPodReg_DeriveFromIP(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-derivetest", "")

	// Register without CoopURL but with PodIP — should derive http://<PodIP>:3000.
	_, err := client.AgentPodRegister(&rpc.AgentPodRegisterArgs{
		AgentID: "hq-derivetest",
		PodName: "derivetest-pod",
		PodIP:   "10.0.2.99",
	})
	if err != nil {
		t.Fatalf("AgentPodRegister: %v", err)
	}

	issue := showIssue(t, client, "hq-derivetest")
	if !strings.Contains(issue.Notes, "coop_url: http://10.0.2.99:3000") {
		t.Errorf("notes should contain derived coop_url http://10.0.2.99:3000, got: %q", issue.Notes)
	}
}

func TestPodDereg_ClearsCoop(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-cleartest", "")

	// Register with coop_url.
	_, err := client.AgentPodRegister(&rpc.AgentPodRegisterArgs{
		AgentID: "hq-cleartest",
		PodName: "cleartest-pod",
		CoopURL: "http://10.0.1.5:3000",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Deregister should clear coop_url from notes.
	_, err = client.AgentPodDeregister(&rpc.AgentPodDeregisterArgs{
		AgentID: "hq-cleartest",
	})
	if err != nil {
		t.Fatalf("deregister: %v", err)
	}

	issue := showIssue(t, client, "hq-cleartest")
	if strings.Contains(issue.Notes, "coop_url") {
		t.Errorf("notes should not contain coop_url after deregister, got: %q", issue.Notes)
	}
}

// --- Retry logic tests (bd-x820i) ---

func TestStop_CoopRetry(t *testing.T) {
	var attempts atomic.Int32
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			// First 2 attempts fail with 500.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Third attempt succeeds.
		w.WriteHeader(http.StatusOK)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-retrytest", coopServer.URL)

	result, err := client.AgentStop(&rpc.AgentStopArgs{
		AgentID: "hq-retrytest",
	})
	if err != nil {
		t.Fatalf("AgentStop: %v", err)
	}

	if result.AgentState != "stopping" {
		t.Errorf("AgentState = %q, want %q", result.AgentState, "stopping")
	}
	// Verify multiple attempts were made (retry logic).
	if got := attempts.Load(); got < 2 {
		t.Errorf("expected at least 2 coop attempts (retry), got %d", got)
	}
}

func TestSignal_CoopRetry(t *testing.T) {
	var attempts atomic.Int32
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 1 {
			// First attempt fails.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Second attempt succeeds.
		w.WriteHeader(http.StatusOK)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-sigretry", coopServer.URL)

	result, err := client.AgentSignal(&rpc.AgentSignalArgs{
		AgentID: "hq-sigretry",
		Signal:  "SIGUSR1",
	})
	if err != nil {
		t.Fatalf("AgentSignal: %v", err)
	}
	if !result.Sent {
		t.Error("expected Sent=true after retry succeeds")
	}
	if got := attempts.Load(); got < 2 {
		t.Errorf("expected at least 2 attempts (retry), got %d", got)
	}
}

func TestSignal_AllRetryFail(t *testing.T) {
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always fail.
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-allfail", coopServer.URL)

	_, err := client.AgentSignal(&rpc.AgentSignalArgs{
		AgentID: "hq-allfail",
		Signal:  "SIGTERM",
	})
	if err == nil {
		t.Fatal("expected error after all retries fail")
	}
}

// --- Restart clears pod fields (bd-x820i) ---

func TestRestart_ClearsPod(t *testing.T) {
	// Start a mock coop that responds instantly so restart's best-effort
	// SIGTERM doesn't hang on a non-routable IP.
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-restartclear", "")

	// Register a pod with explicit coop_url pointing to our mock server.
	_, err := client.AgentPodRegister(&rpc.AgentPodRegisterArgs{
		AgentID:   "hq-restartclear",
		PodName:   "restartclear-pod",
		PodIP:     "10.0.3.1",
		PodStatus: "running",
		CoopURL:   coopServer.URL,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	issue := showIssue(t, client, "hq-restartclear")
	if issue.PodStatus != "running" {
		t.Fatalf("PodStatus before restart = %q, want %q", issue.PodStatus, "running")
	}

	// Restart should clear pod_status and set state to spawning.
	result, err := client.AgentRestart(&rpc.AgentRestartArgs{
		AgentID: "hq-restartclear",
		Reason:  "test restart",
	})
	if err != nil {
		t.Fatalf("AgentRestart: %v", err)
	}
	if result.AgentState != "spawning" {
		t.Errorf("AgentState = %q, want %q", result.AgentState, "spawning")
	}

	issue = showIssue(t, client, "hq-restartclear")
	if issue.PodStatus != "" {
		t.Errorf("PodStatus after restart = %q, want empty", issue.PodStatus)
	}
}

func TestRestart_NotFound(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	_, err := client.AgentRestart(&rpc.AgentRestartArgs{
		AgentID: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestRestart_NoAgentID(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	_, err := client.AgentRestart(&rpc.AgentRestartArgs{})
	if err == nil {
		t.Fatal("expected error for missing agent_id")
	}
}

// --- Stop behavior with missing/unreachable coop (bd-x820i) ---

func TestStop_NoCoop(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	createAgentBead(t, client, "hq-nocoop", "")

	result, err := client.AgentStop(&rpc.AgentStopArgs{
		AgentID: "hq-nocoop",
	})
	if err != nil {
		t.Fatalf("AgentStop: %v", err)
	}
	if result.CoopSignal {
		t.Error("CoopSignal should be false when no coop_url")
	}
	if result.AgentState != "stopping" {
		t.Errorf("AgentState = %q, want %q", result.AgentState, "stopping")
	}
}

func TestStop_CoopDown(t *testing.T) {
	d := testdaemon.Start(t)
	client := connectTestClient(t, d)

	// Use a localhost port that's not listening — gets "connection refused" quickly.
	createAgentBead(t, client, "hq-unreachable", "http://127.0.0.1:1")

	result, err := client.AgentStop(&rpc.AgentStopArgs{
		AgentID: "hq-unreachable",
	})
	if err != nil {
		t.Fatalf("AgentStop: %v", err)
	}
	if result.AgentState != "stopping" {
		t.Errorf("AgentState = %q, want %q", result.AgentState, "stopping")
	}
	if result.CoopSignal {
		t.Error("CoopSignal should be false when coop server unreachable")
	}
}
