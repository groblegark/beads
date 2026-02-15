//go:build integration

package rpc_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestAgentRestart_SetsSpawningState(t *testing.T) {
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

func TestAgentRestart_SignalsCoopBeforeRestart(t *testing.T) {
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
