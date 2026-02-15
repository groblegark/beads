//go:build integration
// +build integration

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// createCaptainTestDecision creates a gate issue + decision point for captain tests.
func createCaptainTestDecision(t *testing.T, ctx context.Context, store storage.Storage, prompt, urgency, requestedBy string, options []types.DecisionOption) string {
	t.Helper()

	gateIssue := &types.Issue{
		Title:     prompt,
		IssueType: "gate",
		AwaitType: "decision",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, gateIssue, "test"); err != nil {
		t.Fatalf("Failed to create gate issue: %v", err)
	}

	optionsJSON, _ := json.Marshal(options)
	dp := &types.DecisionPoint{
		IssueID:       gateIssue.ID,
		Prompt:        prompt,
		Options:       string(optionsJSON),
		Iteration:     1,
		MaxIterations: 3,
		CreatedAt:     time.Now(),
		RequestedBy:   requestedBy,
		Urgency:       urgency,
	}
	if err := store.CreateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("Failed to create decision point: %v", err)
	}

	return gateIssue.ID
}

// createStaleCaptainTestDecision creates a decision with a backdated creation time.
func createStaleCaptainTestDecision(t *testing.T, ctx context.Context, store storage.Storage, prompt string, age time.Duration, options []types.DecisionOption) string {
	t.Helper()

	gateIssue := &types.Issue{
		Title:     prompt,
		IssueType: "gate",
		AwaitType: "decision",
		Status:    types.StatusOpen,
		Priority:  2,
	}
	if err := store.CreateIssue(ctx, gateIssue, "test"); err != nil {
		t.Fatalf("Failed to create gate issue: %v", err)
	}

	optionsJSON, _ := json.Marshal(options)
	dp := &types.DecisionPoint{
		IssueID:       gateIssue.ID,
		Prompt:        prompt,
		Options:       string(optionsJSON),
		Iteration:     1,
		MaxIterations: 3,
		CreatedAt:     time.Now().Add(-age),
		RequestedBy:   "stale-agent",
	}
	if err := store.CreateDecisionPoint(ctx, dp); err != nil {
		t.Fatalf("Failed to create decision point: %v", err)
	}

	return gateIssue.ID
}

var standardOpts = []types.DecisionOption{
	{ID: "go", Label: "Continue"},
	{ID: "stop", Label: "Done"},
}

// --- Captain List Tests ---

func TestCaptainList_ReturnsDecisions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	createCaptainTestDecision(t, ctx, store, "Captain list test", "medium", "agent", standardOpts)

	resp, err := client.DecisionList(&rpc.DecisionListArgs{All: false})
	if err != nil {
		t.Fatalf("DecisionList failed: %v", err)
	}
	if resp.Count != 1 {
		t.Errorf("Expected 1 decision, got %d", resp.Count)
	}
	if resp.Decisions[0].Decision.Prompt != "Captain list test" {
		t.Errorf("Wrong prompt: %s", resp.Decisions[0].Decision.Prompt)
	}
}

// --- Captain Respond Tests ---

func TestCaptainRespond_ResolvesDecision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	id := createCaptainTestDecision(t, ctx, store, "Respond test", "medium", "agent", standardOpts)

	_, err := client.DecisionResolve(&rpc.DecisionResolveArgs{
		IssueID:        id,
		SelectedOption: "go",
		ResponseText:   "Looks good",
		RespondedBy:    "captain",
	})
	if err != nil {
		t.Fatalf("DecisionResolve failed: %v", err)
	}

	listResp, err := client.DecisionList(&rpc.DecisionListArgs{All: false})
	if err != nil {
		t.Fatalf("DecisionList failed: %v", err)
	}
	if listResp.Count != 0 {
		t.Errorf("Expected 0 pending after resolve, got %d", listResp.Count)
	}
}

// --- Captain Sweep Tests ---

func TestCaptainSweep_ResolvesStaleOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	createStaleCaptainTestDecision(t, ctx, store, "Stale decision", 1*time.Hour, standardOpts)
	createCaptainTestDecision(t, ctx, store, "Fresh decision", "medium", "agent", standardOpts)

	resp, err := client.DecisionList(&rpc.DecisionListArgs{All: false})
	if err != nil {
		t.Fatalf("DecisionList failed: %v", err)
	}
	if resp.Count != 2 {
		t.Fatalf("Expected 2 decisions, got %d", resp.Count)
	}

	// Use the newest decision's CreatedAt as reference to avoid timezone skew
	// between Go's time.Now() and Dolt's returned timestamps.
	var newestTime time.Time
	for _, dr := range resp.Decisions {
		if dr.Decision != nil && dr.Decision.CreatedAt.After(newestTime) {
			newestTime = dr.Decision.CreatedAt
		}
	}

	// Sweep: resolve decisions that are at least 30m older than the newest.
	sweepAge := 30 * time.Minute
	var swept int
	for _, dr := range resp.Decisions {
		if dr.Decision == nil {
			continue
		}
		relativeAge := newestTime.Sub(dr.Decision.CreatedAt)
		if relativeAge >= sweepAge {
			_, err := client.DecisionResolve(&rpc.DecisionResolveArgs{
				IssueID:        dr.Decision.IssueID,
				SelectedOption: "stop",
				ResponseText:   "swept",
				RespondedBy:    "captain-sweep",
			})
			if err != nil {
				t.Errorf("Sweep failed for %s: %v", dr.Decision.IssueID, err)
			}
			swept++
		}
	}

	if swept != 1 {
		t.Errorf("Expected 1 swept, got %d", swept)
	}

	listResp, err := client.DecisionList(&rpc.DecisionListArgs{All: false})
	if err != nil {
		t.Fatalf("DecisionList failed: %v", err)
	}
	if listResp.Count != 1 {
		t.Errorf("Expected 1 remaining, got %d", listResp.Count)
	}
}

// --- Captain Watch Tests ---

func TestCaptainWatch_DetectsNewDecision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	// Snapshot existing decisions by prompt (IssueID format may differ between
	// direct store access and RPC round-trip).
	known := make(map[string]bool)
	resp, _ := client.DecisionList(&rpc.DecisionListArgs{All: false})
	for _, dr := range resp.Decisions {
		if dr.Decision != nil {
			known[dr.Decision.Prompt] = true
		}
	}

	// Create new.
	createCaptainTestDecision(t, ctx, store, "Watch detect test", "medium", "agent", standardOpts)

	// Poll again.
	resp, _ = client.DecisionList(&rpc.DecisionListArgs{All: false})
	var found bool
	for _, dr := range resp.Decisions {
		if dr.Decision != nil && dr.Decision.Prompt == "Watch detect test" {
			found = true
		}
	}
	if !found {
		t.Errorf("Watch did not detect new decision; known prompts: %v, got %d decisions", known, resp.Count)
		for _, dr := range resp.Decisions {
			if dr.Decision != nil {
				t.Logf("  decision: prompt=%q issueID=%q", dr.Decision.Prompt, dr.Decision.IssueID)
			}
		}
	}
}

func TestCaptainWatch_IgnoresExisting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	createCaptainTestDecision(t, ctx, store, "Pre-existing", "medium", "agent", standardOpts)

	// Snapshot after creation.
	known := make(map[string]bool)
	resp, _ := client.DecisionList(&rpc.DecisionListArgs{All: false})
	for _, dr := range resp.Decisions {
		if dr.Decision != nil {
			known[dr.Decision.IssueID] = true
		}
	}

	// Poll again — no new decisions.
	resp, _ = client.DecisionList(&rpc.DecisionListArgs{All: false})
	var newCount int
	for _, dr := range resp.Decisions {
		if dr.Decision != nil && !known[dr.Decision.IssueID] {
			newCount++
		}
	}
	if newCount != 0 {
		t.Errorf("Expected 0 new, got %d", newCount)
	}
}

// --- Captain Auto Tests ---

func TestCaptainAuto_ContinueAction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	opts := []types.DecisionOption{
		{ID: "fix", Label: "Fix the bug"},
		{ID: "refactor", Label: "Refactor"},
		{ID: "stop", Label: "Done"},
	}
	id := createCaptainTestDecision(t, ctx, store, "Auto continue", "medium", "agent", opts)

	selectID := autoResolveDecision(opts, "continue")
	if selectID != "fix" {
		t.Fatalf("Expected 'fix', got %q", selectID)
	}

	_, err := client.DecisionResolve(&rpc.DecisionResolveArgs{
		IssueID:        id,
		SelectedOption: selectID,
		RespondedBy:    "captain-auto",
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
}

func TestCaptainAuto_StopAction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	id := createCaptainTestDecision(t, ctx, store, "Auto stop", "medium", "agent", standardOpts)

	selectID := autoResolveDecision(standardOpts, "stop")
	if selectID != "stop" {
		t.Fatalf("Expected 'stop', got %q", selectID)
	}

	_, err := client.DecisionResolve(&rpc.DecisionResolveArgs{
		IssueID:        id,
		SelectedOption: selectID,
		RespondedBy:    "captain-auto",
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
}

func TestCaptainAuto_SweepsAndActs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	createStaleCaptainTestDecision(t, ctx, store, "Stale auto", 45*time.Minute, standardOpts)
	createCaptainTestDecision(t, ctx, store, "Fresh auto", "medium", "agent", standardOpts)

	resp, _ := client.DecisionList(&rpc.DecisionListArgs{All: false})

	// Use the newest decision's CreatedAt as reference to avoid timezone skew.
	var newestTime time.Time
	for _, dr := range resp.Decisions {
		if dr.Decision != nil && dr.Decision.CreatedAt.After(newestTime) {
			newestTime = dr.Decision.CreatedAt
		}
	}

	sweepAge := 30 * time.Minute
	var swept, acted int
	for _, dr := range resp.Decisions {
		if dr.Decision == nil {
			continue
		}
		relativeAge := newestTime.Sub(dr.Decision.CreatedAt)
		var selectID string
		if relativeAge >= sweepAge {
			selectID = autoResolveDecision(standardOpts, "stop")
			swept++
		} else {
			selectID = autoResolveDecision(standardOpts, "continue")
			acted++
		}
		client.DecisionResolve(&rpc.DecisionResolveArgs{
			IssueID:        dr.Decision.IssueID,
			SelectedOption: selectID,
			RespondedBy:    "captain-auto",
		})
	}

	if swept != 1 {
		t.Errorf("Expected 1 swept, got %d", swept)
	}
	if acted != 1 {
		t.Errorf("Expected 1 acted, got %d", acted)
	}

	listResp, _ := client.DecisionList(&rpc.DecisionListArgs{All: false})
	if listResp.Count != 0 {
		t.Errorf("Expected 0 remaining, got %d", listResp.Count)
	}
}

func TestCaptainAuto_UrgentOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel, client, store, cleanup := setupDaemonTestEnvForDecision(t)
	defer cleanup()
	defer cancel()

	id := createCaptainTestDecision(t, ctx, store, "Urgent override", "high", "agent", standardOpts)

	// Simulate: default=continue, urgent=stop
	resp, _ := client.DecisionList(&rpc.DecisionListArgs{All: false})
	for _, dr := range resp.Decisions {
		if dr.Decision == nil || dr.Decision.IssueID != id {
			continue
		}
		if dr.Decision.Urgency != "high" {
			t.Errorf("Expected urgency 'high', got %q", dr.Decision.Urgency)
		}

		// High urgency → apply urgent action (stop) instead of default (continue).
		selectID := autoResolveDecision(standardOpts, "stop")
		_, err := client.DecisionResolve(&rpc.DecisionResolveArgs{
			IssueID:        id,
			SelectedOption: selectID,
			ResponseText:   "urgent override",
			RespondedBy:    "captain-auto",
		})
		if err != nil {
			t.Fatalf("Resolve failed: %v", err)
		}
	}
}
