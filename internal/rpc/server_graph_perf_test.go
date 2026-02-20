//go:build bench

package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil/teststore"
	"github.com/steveyegge/beads/internal/types"
)

// seedGraphData populates a store with a realistic dataset for graph query
// benchmarking. Returns the IDs of all created issues.
func seedGraphData(tb testing.TB, store storage.Storage, count int) []string {
	tb.Helper()
	ctx := context.Background()

	issueTypes := []string{"task", "bug", "feature", "epic"}
	statuses := []string{types.StatusOpen, types.StatusInProgress, types.StatusOpen, types.StatusOpen}
	agents := []string{"alice", "bob", "charlie", "diana", "eve",
		"gt-emma", "gt-frank", "gt-grace", "gt-heidi", "gt-ivan",
		"neat-crab", "cool-lion", "arch-hawk", "vast-raven", "deft-ox",
		"brave-rat", "tall-toad", "true-crab", "keen-newt", "bold-elk"}
	labels := []string{"frontend", "backend", "infra", "ci", "docs", "ux", "perf"}

	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		issue := &types.Issue{
			Title:       fmt.Sprintf("Issue %d: %s work item", i, issueTypes[i%len(issueTypes)]),
			Description: fmt.Sprintf("Description for issue %d with enough text to be realistic", i),
			IssueType:   issueTypes[i%len(issueTypes)],
			Priority:    i%5 + 0,
			Status:      statuses[i%len(statuses)],
		}
		// Assign ~60% of issues to agents
		if i%5 < 3 {
			issue.Assignee = agents[i%len(agents)]
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			tb.Fatalf("seed issue %d: %v", i, err)
		}
		ids = append(ids, issue.ID)

		// Add labels to ~40% of issues
		if i%5 < 2 {
			if err := store.AddLabel(ctx, issue.ID, labels[i%len(labels)], "benchmark"); err != nil {
				tb.Fatalf("seed label %d: %v", i, err)
			}
		}
	}

	// Create dependency chains: each issue depends on one ~10 issues earlier
	for i := 10; i < count; i += 3 {
		depIdx := i - 5 - (i % 5)
		if depIdx >= 0 && depIdx < len(ids) {
			dep := &types.Dependency{
				IssueID:     ids[i],
				DependsOnID: ids[depIdx],
				Type:        types.DepBlocks,
			}
			if err := store.AddDependency(ctx, dep, "benchmark"); err != nil {
				tb.Fatalf("seed dep %d->%d: %v", i, depIdx, err)
			}
		}
	}

	// Close ~20% of issues
	for i := 0; i < count; i += 5 {
		updates := map[string]interface{}{"status": "closed"}
		if err := store.UpdateIssue(ctx, ids[i], updates, "benchmark"); err != nil {
			tb.Fatalf("close issue %d: %v", i, err)
		}
	}

	// Mark some as agent beads (gt:agent label)
	for i := 0; i < min(20, count); i++ {
		if err := store.AddLabel(ctx, ids[i], "gt:agent", "benchmark"); err != nil {
			tb.Fatalf("seed agent label %d: %v", i, err)
		}
	}

	return ids
}

// startGraphServer creates a server with HTTP endpoint for graph benchmarking.
func startGraphServer(tb testing.TB, store storage.Storage) (addr string, cleanup func()) {
	tb.Helper()
	tmpDir, err := os.MkdirTemp("", "bd-graph-bench-*")
	if err != nil {
		tb.Fatalf("temp dir: %v", err)
	}
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	socketPath := filepath.Join(beadsDir, "bd.sock")
	dbPath := filepath.Join(beadsDir, "test.db")

	server := NewServer(socketPath, store, tmpDir, dbPath)
	server.SetHTTPAddr("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	go server.Start(ctx)

	select {
	case <-server.WaitReady():
	case <-time.After(10 * time.Second):
		cancel()
		os.RemoveAll(tmpDir)
		tb.Fatal("timeout waiting for server")
	}

	return server.HTTPServer().Addr(), func() {
		cancel()
		server.Stop()
		os.RemoveAll(tmpDir)
	}
}

// startGraphServerWithRedis creates a server with Redis cache enabled.
func startGraphServerWithRedis(tb testing.TB, store storage.Storage) (addr string, cleanup func()) {
	tb.Helper()
	tmpDir, err := os.MkdirTemp("", "bd-graph-bench-redis-*")
	if err != nil {
		tb.Fatalf("temp dir: %v", err)
	}
	beadsDir := filepath.Join(tmpDir, ".beads")
	os.MkdirAll(beadsDir, 0755)
	socketPath := filepath.Join(beadsDir, "bd.sock")
	dbPath := filepath.Join(beadsDir, "test.db")

	mr, err := miniredis.Run()
	if err != nil {
		os.RemoveAll(tmpDir)
		tb.Fatalf("miniredis: %v", err)
	}

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	server := NewServer(socketPath, store, tmpDir, dbPath)
	server.SetHTTPAddr("127.0.0.1:0")
	server.SetRedisCache(NewRedisQueryCache(redisClient, 30*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	go server.Start(ctx)

	select {
	case <-server.WaitReady():
	case <-time.After(10 * time.Second):
		cancel()
		mr.Close()
		redisClient.Close()
		os.RemoveAll(tmpDir)
		tb.Fatal("timeout waiting for server")
	}

	return server.HTTPServer().Addr(), func() {
		cancel()
		server.Stop()
		redisClient.Close()
		mr.Close()
		os.RemoveAll(tmpDir)
	}
}

// postGraph sends a graph query and returns the result (benchmark version).
func postGraph(b *testing.B, addr string, args GraphArgs) GraphResult {
	b.Helper()
	body, _ := json.Marshal(args)
	resp, err := http.Post("http://"+addr+"/bd.v1.BeadsService/Graph",
		"application/json", bytes.NewReader(body))
	if err != nil {
		b.Fatalf("POST graph: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		b.Fatalf("graph returned %d: %s", resp.StatusCode, string(bodyBytes))
	}
	var result GraphResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.Fatalf("decode graph: %v", err)
	}
	return result
}

// postGraphT sends a graph query and returns the result (test version).
func postGraphT(t *testing.T, addr string, args GraphArgs) GraphResult {
	t.Helper()
	body, _ := json.Marshal(args)
	resp, err := http.Post("http://"+addr+"/bd.v1.BeadsService/Graph",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST graph: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("graph returned %d: %s", resp.StatusCode, string(bodyBytes))
	}
	var result GraphResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	return result
}

// BenchmarkGraphQuery_200Issues benchmarks graph query with 200 issues + deps + agents.
func BenchmarkGraphQuery_200Issues(b *testing.B) {
	store := teststore.New(b)
	seedGraphData(b, store, 200)

	addr, cleanup := startGraphServer(b, store)
	defer cleanup()

	args := GraphArgs{
		IncludeAgents: true,
		IncludeDeps:   true,
		Limit:         500,
	}

	postGraph(b, addr, args) // warm up
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		postGraph(b, addr, args)
	}
}

// BenchmarkGraphQuery_500Issues benchmarks with a larger dataset.
func BenchmarkGraphQuery_500Issues(b *testing.B) {
	store := teststore.New(b)
	seedGraphData(b, store, 500)

	addr, cleanup := startGraphServer(b, store)
	defer cleanup()

	args := GraphArgs{
		IncludeAgents: true,
		IncludeDeps:   true,
		Limit:         500,
	}

	postGraph(b, addr, args) // warm up
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		postGraph(b, addr, args)
	}
}

// BenchmarkGraphQuery_WithRedisCache benchmarks graph with Redis cache to verify
// cache hits are significantly faster than direct Dolt queries (bd-bxkln).
func BenchmarkGraphQuery_WithRedisCache(b *testing.B) {
	store := teststore.New(b)
	seedGraphData(b, store, 200)

	addr, cleanup := startGraphServerWithRedis(b, store)
	defer cleanup()

	args := GraphArgs{
		IncludeAgents: true,
		IncludeDeps:   true,
		Limit:         500,
	}

	// First call populates cache
	postGraph(b, addr, args)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		postGraph(b, addr, args)
	}
}

// BenchmarkGraphQuery_CacheMissVsHit measures the performance difference
// between cache misses (Dolt query) and cache hits (Redis).
func BenchmarkGraphQuery_CacheMissVsHit(b *testing.B) {
	store := teststore.New(b)
	seedGraphData(b, store, 200)

	b.Run("no_cache", func(b *testing.B) {
		addr, cleanup := startGraphServer(b, store)
		defer cleanup()

		args := GraphArgs{
			IncludeAgents: true,
			IncludeDeps:   true,
			Limit:         500,
		}
		postGraph(b, addr, args) // warm up
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			postGraph(b, addr, args)
		}
	})

	b.Run("redis_cache_hit", func(b *testing.B) {
		addr, cleanup := startGraphServerWithRedis(b, store)
		defer cleanup()

		args := GraphArgs{
			IncludeAgents: true,
			IncludeDeps:   true,
			Limit:         500,
		}
		postGraph(b, addr, args) // populate cache
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			postGraph(b, addr, args)
		}
	})
}

// TestGraphCacheCorrectness verifies that the Redis-cached response is identical
// to the direct Dolt response for the same query (bd-bxkln).
func TestGraphCacheCorrectness(t *testing.T) {
	store := teststore.New(t)
	ctx := context.Background()

	// Seed data with agents, deps, mixed statuses
	agents := []string{"alice", "bob"}
	issueTypes := []string{"task", "bug", "feature"}
	var ids []string
	for i := 0; i < 50; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Issue %d", i),
			IssueType: issueTypes[i%len(issueTypes)],
			Priority:  i%5 + 0,
			Status:    types.StatusOpen,
		}
		if i%3 < 2 {
			issue.Assignee = agents[i%len(agents)]
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("create issue %d: %v", i, err)
		}
		ids = append(ids, issue.ID)
	}
	// Add deps
	for i := 5; i < 50; i += 3 {
		dep := &types.Dependency{
			IssueID:     ids[i],
			DependsOnID: ids[i-3],
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "test"); err != nil {
			t.Fatalf("add dep %d: %v", i, err)
		}
	}
	// Close some
	for i := 0; i < 50; i += 7 {
		store.UpdateIssue(ctx, ids[i], map[string]interface{}{"status": "closed"}, "test")
	}

	addr, cleanup := startGraphServerWithRedis(t, store)
	defer cleanup()

	args := GraphArgs{
		IncludeAgents: true,
		IncludeDeps:   true,
		Limit:         500,
	}

	// Call 1: cache miss → direct Dolt query
	result1 := postGraphT(t, addr, args)

	// Call 2: cache hit → Redis
	result2 := postGraphT(t, addr, args)

	// Verify identical node counts
	if len(result1.Nodes) != len(result2.Nodes) {
		t.Errorf("node count mismatch: direct=%d cached=%d", len(result1.Nodes), len(result2.Nodes))
	}
	if len(result1.Edges) != len(result2.Edges) {
		t.Errorf("edge count mismatch: direct=%d cached=%d", len(result1.Edges), len(result2.Edges))
	}

	// Verify stats match
	if result1.Stats.TotalOpen != result2.Stats.TotalOpen {
		t.Errorf("stats.total_open mismatch: %d vs %d", result1.Stats.TotalOpen, result2.Stats.TotalOpen)
	}
	if result1.Stats.TotalInProgress != result2.Stats.TotalInProgress {
		t.Errorf("stats.total_in_progress mismatch: %d vs %d", result1.Stats.TotalInProgress, result2.Stats.TotalInProgress)
	}

	// Verify node IDs match
	nodeIDs1 := make(map[string]bool)
	nodeIDs2 := make(map[string]bool)
	for _, n := range result1.Nodes {
		nodeIDs1[n.ID] = true
	}
	for _, n := range result2.Nodes {
		nodeIDs2[n.ID] = true
	}
	for id := range nodeIDs1 {
		if !nodeIDs2[id] {
			t.Errorf("node %s in direct result but not cached", id)
		}
	}
	for id := range nodeIDs2 {
		if !nodeIDs1[id] {
			t.Errorf("node %s in cached result but not direct", id)
		}
	}

	// Verify agent assigned_to edge counts match
	agentEdges1 := make(map[string]int)
	agentEdges2 := make(map[string]int)
	for _, e := range result1.Edges {
		if e.Type == "assigned_to" {
			agentEdges1[e.Source]++
		}
	}
	for _, e := range result2.Edges {
		if e.Type == "assigned_to" {
			agentEdges2[e.Source]++
		}
	}
	for agent, count := range agentEdges1 {
		if agentEdges2[agent] != count {
			t.Errorf("agent %s edge count mismatch: direct=%d cached=%d", agent, count, agentEdges2[agent])
		}
	}

	t.Logf("Cache correctness verified: %d nodes, %d edges, %d agent edge groups",
		len(result1.Nodes), len(result1.Edges), len(agentEdges1))
}

// TestGraphAgentEdges_AllSources verifies that agents from all sources
// (agent beads, roster, assignees) get correct assigned_to edges (bd-988ef).
func TestGraphAgentEdges_AllSources(t *testing.T) {
	store := teststore.New(t)
	ctx := context.Background()

	// Create issues assigned to "alice" and "bob"
	issue1 := &types.Issue{
		Title: "Task for alice", IssueType: "task", Priority: 2,
		Status: types.StatusOpen, Assignee: "alice",
	}
	store.CreateIssue(ctx, issue1, "test")

	issue2 := &types.Issue{
		Title: "Bug for alice", IssueType: "bug", Priority: 1,
		Status: types.StatusInProgress, Assignee: "alice",
	}
	store.CreateIssue(ctx, issue2, "test")

	// Create an agent bead with gt:agent label (Source 0)
	agentBead := &types.Issue{
		Title: "alice-agent", IssueType: "agent", Priority: 2,
		Status: types.StatusOpen,
	}
	store.CreateIssue(ctx, agentBead, "test")
	store.AddLabel(ctx, agentBead.ID, "gt:agent", "test")

	// Create issues for "bob" (Source 2 only — no agent bead, no roster)
	issue3 := &types.Issue{
		Title: "Feature for bob", IssueType: "feature", Priority: 2,
		Status: types.StatusOpen, Assignee: "bob",
	}
	store.CreateIssue(ctx, issue3, "test")

	addr, cleanup := startGraphServer(t, store)
	defer cleanup()

	result := postGraphT(t, addr, GraphArgs{
		IncludeAgents: true,
		IncludeDeps:   true,
	})

	// Verify agent nodes exist
	agentNodes := make(map[string]bool)
	for _, n := range result.Nodes {
		if n.IssueType == "agent" {
			agentNodes[n.ID] = true
			t.Logf("agent node: id=%s title=%s status=%s", n.ID, n.Title, n.Status)
		}
	}

	if !agentNodes["agent:bob"] {
		t.Error("expected agent:bob node from assignee source")
	}

	// Check assigned_to edges
	edgesByAgent := make(map[string][]string)
	for _, e := range result.Edges {
		if e.Type == "assigned_to" {
			edgesByAgent[e.Source] = append(edgesByAgent[e.Source], e.Target)
		}
	}

	// alice should have 2 edges (to issue1 and issue2)
	if len(edgesByAgent["agent:alice"]) != 2 {
		t.Errorf("expected alice 2 assigned_to edges, got %d: %v",
			len(edgesByAgent["agent:alice"]), edgesByAgent["agent:alice"])
	}

	// bob should have 1 edge (to issue3)
	if len(edgesByAgent["agent:bob"]) != 1 {
		t.Errorf("expected bob 1 assigned_to edge, got %d: %v",
			len(edgesByAgent["agent:bob"]), edgesByAgent["agent:bob"])
	}

	t.Logf("Agent edges verified: %d agent nodes, alice=%d edges, bob=%d edges",
		len(agentNodes), len(edgesByAgent["agent:alice"]), len(edgesByAgent["agent:bob"]))
}
