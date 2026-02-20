//go:build !windows

package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil/teststore"
)

// TestGraphIncludeAgents tests that include_agents adds agent nodes and assignment edges (bd-cd86r).
func TestGraphIncludeAgents(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "graph-agents-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "bd.sock")
	store := teststore.New(t)

	server := NewServer(socketPath, store, tmpDir, filepath.Join(tmpDir, "beads.db"))
	server.SetHTTPAddr("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	select {
	case <-server.WaitReady():
	case err := <-errChan:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server")
	}

	httpAddr := server.HTTPServer().Addr()

	// Create test issues with assignees
	createIssue := func(title, issueType, assignee string) string {
		body, _ := json.Marshal(map[string]interface{}{
			"title":      title,
			"issue_type": issueType,
			"priority":   2,
			"assignee":   assignee,
		})
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/Create",
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
		defer resp.Body.Close()
		var result struct {
			ID string `json:"id"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		return result.ID
	}

	id1 := createIssue("Task for alice", "task", "alice")
	id2 := createIssue("Bug for alice", "bug", "alice")
	_ = createIssue("Feature for bob", "feature", "bob")
	_ = createIssue("Unassigned task", "task", "")

	t.Run("without_include_agents", func(t *testing.T) {
		body, _ := json.Marshal(GraphArgs{
			IncludeAgents: false,
		})
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/Graph",
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
		}

		var result GraphResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		// Should have no agent nodes
		for _, node := range result.Nodes {
			if node.IssueType == "agent" {
				t.Error("expected no agent nodes when include_agents=false")
			}
		}
		// Should have no assigned_to edges
		for _, edge := range result.Edges {
			if edge.Type == "assigned_to" {
				t.Error("expected no assigned_to edges when include_agents=false")
			}
		}
	})

	t.Run("with_include_agents", func(t *testing.T) {
		body, _ := json.Marshal(GraphArgs{
			IncludeAgents: true,
		})
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/Graph",
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
		}

		var result GraphResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		// Should have agent nodes for alice and bob
		agentNodes := make(map[string]bool)
		for _, node := range result.Nodes {
			if node.IssueType == "agent" {
				agentNodes[node.Title] = true
			}
		}
		if !agentNodes["alice"] {
			t.Error("expected agent node for alice")
		}
		if !agentNodes["bob"] {
			t.Error("expected agent node for bob")
		}
		if len(agentNodes) != 2 {
			t.Errorf("expected exactly 2 agent nodes, got %d", len(agentNodes))
		}

		// Should have assigned_to edges
		assignedEdges := make(map[string][]string) // agent -> issue IDs
		for _, edge := range result.Edges {
			if edge.Type == "assigned_to" {
				assignedEdges[edge.Source] = append(assignedEdges[edge.Source], edge.Target)
			}
		}

		// alice should have 2 assignments
		aliceEdges := assignedEdges["agent:alice"]
		if len(aliceEdges) != 2 {
			t.Errorf("expected alice to have 2 assignments, got %d", len(aliceEdges))
		}
		// Verify the specific IDs
		aliceIDs := make(map[string]bool)
		for _, id := range aliceEdges {
			aliceIDs[id] = true
		}
		if !aliceIDs[id1] || !aliceIDs[id2] {
			t.Errorf("expected alice assigned to %s and %s, got %v", id1, id2, aliceEdges)
		}

		// bob should have 1 assignment
		bobEdges := assignedEdges["agent:bob"]
		if len(bobEdges) != 1 {
			t.Errorf("expected bob to have 1 assignment, got %d", len(bobEdges))
		}
	})

	if err := server.Stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
}
