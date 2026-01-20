package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPolecatRoutingFromPolecatDirectory tests that routing works correctly
// when a polecat runs from its designated directory structure (e.g., rig/polecats/name).
// This is a key scenario for autonomous agents running in their own workspace.
func TestPolecatRoutingFromPolecatDirectory(t *testing.T) {
	ctx := context.Background()

	// Create temp directory structure simulating a Gas Town with polecat:
	// tmpDir/
	//   mayor/
	//     town.json    <- town root marker
	//   .beads/
	//     beads.db     <- town database
	//     routes.jsonl <- routing config
	//   myrig/
	//     polecats/
	//       capable/   <- polecat workspace
	//     .beads/
	//       beads.db   <- rig database with polecat agent

	tmpDir := t.TempDir()

	// Create mayor/town.json to mark town root
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("Failed to create mayor dir: %v", err)
	}
	townJSON := filepath.Join(mayorDir, "town.json")
	if err := os.WriteFile(townJSON, []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to create town.json: %v", err)
	}

	// Create town .beads directory
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	// Create rig .beads directory
	rigBeadsDir := filepath.Join(tmpDir, "myrig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	// Create polecat workspace directory
	polecatDir := filepath.Join(tmpDir, "myrig", "polecats", "capable")
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatalf("Failed to create polecat dir: %v", err)
	}

	// Initialize databases
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create a polecat agent bead in the rig database
	polecatBead := &types.Issue{
		ID:        "gt-myrig-polecat-capable",
		Title:     "Agent: gt-myrig-polecat-capable",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		RoleType:  "polecat",
		Rig:       "myrig",
	}
	if err := rigStore.CreateIssue(ctx, polecatBead, "test"); err != nil {
		t.Fatalf("Failed to create polecat bead: %v", err)
	}
	if err := rigStore.AddLabel(ctx, polecatBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Create routes.jsonl in town .beads directory
	routesContent := `{"prefix":"gt-","path":"myrig"}`
	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	// Change to polecat directory (simulating polecat running from its workspace)
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	if err := os.Chdir(polecatDir); err != nil {
		t.Fatalf("Failed to change to polecat directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// Test routing resolves the polecat agent from the rig database
	result, err := resolveAndGetIssueWithRouting(ctx, townStore, "gt-myrig-polecat-capable")
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting failed: %v", err)
	}
	if result == nil || result.Issue == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil")
	}
	defer result.Close()

	if result.Issue.ID != "gt-myrig-polecat-capable" {
		t.Errorf("Expected issue ID %q, got %q", "gt-myrig-polecat-capable", result.Issue.ID)
	}

	if !result.Routed {
		t.Error("Expected result.Routed to be true for cross-repo polecat lookup")
	}

	t.Logf("Successfully resolved polecat agent %s from polecat directory via routing", result.Issue.ID)
}

// TestPolecatRoutingWithRedirect tests that routing works correctly
// when polecat directories use redirect files for shared beads databases.
func TestPolecatRoutingWithRedirect(t *testing.T) {
	ctx := context.Background()

	// Create temp directory structure with redirect:
	// tmpDir/
	//   mayor/
	//     town.json
	//   .beads/
	//     beads.db
	//     routes.jsonl
	//   myrig/
	//     polecats/
	//       capable/
	//         .beads/
	//           redirect  <- points to shared beads
	//     .beads/
	//       beads.db     <- actual rig database

	tmpDir := t.TempDir()

	// Create mayor/town.json
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("Failed to create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to create town.json: %v", err)
	}

	// Create town .beads directory
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	// Create rig .beads directory (shared database)
	rigBeadsDir := filepath.Join(tmpDir, "myrig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	// Create polecat workspace with redirect
	polecatBeadsDir := filepath.Join(tmpDir, "myrig", "polecats", "capable", ".beads")
	if err := os.MkdirAll(polecatBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create polecat beads dir: %v", err)
	}

	// Write redirect file pointing to rig's .beads
	redirectContent := "../../.beads" // Relative path from polecat/.beads to rig/.beads
	if err := os.WriteFile(filepath.Join(polecatBeadsDir, "redirect"), []byte(redirectContent), 0644); err != nil {
		t.Fatalf("Failed to write redirect file: %v", err)
	}

	// Initialize databases
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create a polecat agent bead in the rig database
	polecatBead := &types.Issue{
		ID:        "gt-myrig-polecat-redirect",
		Title:     "Agent: gt-myrig-polecat-redirect",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		RoleType:  "polecat",
		Rig:       "myrig",
	}
	if err := rigStore.CreateIssue(ctx, polecatBead, "test"); err != nil {
		t.Fatalf("Failed to create polecat bead: %v", err)
	}
	if err := rigStore.AddLabel(ctx, polecatBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Create routes.jsonl
	routesContent := `{"prefix":"gt-","path":"myrig"}`
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	// Change to town directory
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// Test routing works for polecat with redirect
	result, err := resolveAndGetIssueWithRouting(ctx, townStore, "gt-myrig-polecat-redirect")
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting failed: %v", err)
	}
	if result == nil || result.Issue == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil")
	}
	defer result.Close()

	if result.Issue.ID != "gt-myrig-polecat-redirect" {
		t.Errorf("Expected issue ID %q, got %q", "gt-myrig-polecat-redirect", result.Issue.ID)
	}

	t.Logf("Successfully resolved polecat with redirect via routing")
}

// TestPolecatCrossRigRouting tests that polecats can access issues from other rigs
// via the routing system.
func TestPolecatCrossRigRouting(t *testing.T) {
	ctx := context.Background()

	// Create temp directory structure with two rigs:
	// tmpDir/
	//   mayor/
	//     town.json
	//   .beads/
	//     routes.jsonl
	//   rig-alpha/
	//     polecats/
	//       worker/
	//     .beads/
	//       beads.db  <- alpha database
	//   rig-beta/
	//     .beads/
	//       beads.db  <- beta database (with target issue)

	tmpDir := t.TempDir()

	// Create town structure
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("Failed to create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to create town.json: %v", err)
	}

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	// Create rig-alpha structure (where polecat runs)
	alphaBeadsDir := filepath.Join(tmpDir, "rig-alpha", ".beads")
	if err := os.MkdirAll(alphaBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create alpha beads dir: %v", err)
	}
	polecatDir := filepath.Join(tmpDir, "rig-alpha", "polecats", "worker")
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatalf("Failed to create polecat dir: %v", err)
	}

	// Create rig-beta structure (where target issue lives)
	betaBeadsDir := filepath.Join(tmpDir, "rig-beta", ".beads")
	if err := os.MkdirAll(betaBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create beta beads dir: %v", err)
	}

	// Initialize databases
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	_ = newTestStoreWithPrefix(t, townDBPath, "hq")

	alphaDBPath := filepath.Join(alphaBeadsDir, "beads.db")
	alphaStore := newTestStoreWithPrefix(t, alphaDBPath, "aa")

	betaDBPath := filepath.Join(betaBeadsDir, "beads.db")
	betaStore := newTestStoreWithPrefix(t, betaDBPath, "bb")

	// Create a polecat agent in alpha rig
	polecatBead := &types.Issue{
		ID:        "aa-alpha-polecat-worker",
		Title:     "Agent: aa-alpha-polecat-worker",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		RoleType:  "polecat",
		Rig:       "rig-alpha",
	}
	if err := alphaStore.CreateIssue(ctx, polecatBead, "test"); err != nil {
		t.Fatalf("Failed to create polecat bead: %v", err)
	}
	if err := alphaStore.AddLabel(ctx, polecatBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Create a target issue in beta rig that polecat needs to access
	targetBead := &types.Issue{
		ID:        "bb-beta-task-123",
		Title:     "Task in Beta Rig",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
	}
	if err := betaStore.CreateIssue(ctx, targetBead, "test"); err != nil {
		t.Fatalf("Failed to create target bead: %v", err)
	}

	// Create routes.jsonl with both rig routes
	routesContent := `{"prefix":"aa-","path":"rig-alpha"}
{"prefix":"bb-","path":"rig-beta"}`
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state (polecat is in alpha rig)
	oldDbPath := dbPath
	dbPath = alphaDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	// Change to polecat directory in alpha rig
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	if err := os.Chdir(polecatDir); err != nil {
		t.Fatalf("Failed to change to polecat directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// Test that polecat in alpha can resolve issue in beta via routing
	result, err := resolveAndGetIssueWithRouting(ctx, alphaStore, "bb-beta-task-123")
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting failed: %v", err)
	}
	if result == nil || result.Issue == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil")
	}
	defer result.Close()

	if result.Issue.ID != "bb-beta-task-123" {
		t.Errorf("Expected issue ID %q, got %q", "bb-beta-task-123", result.Issue.ID)
	}

	if !result.Routed {
		t.Error("Expected result.Routed to be true for cross-rig lookup")
	}

	t.Logf("Successfully resolved cross-rig issue from polecat directory")
}

// TestPolecatRoutingMultiplePolecats tests routing when multiple polecats exist
// in the same rig.
func TestPolecatRoutingMultiplePolecats(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()

	// Create town structure
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("Failed to create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to create town.json: %v", err)
	}

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	// Create rig with multiple polecats
	rigBeadsDir := filepath.Join(tmpDir, "therig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	polecat1Dir := filepath.Join(tmpDir, "therig", "polecats", "alpha")
	if err := os.MkdirAll(polecat1Dir, 0755); err != nil {
		t.Fatalf("Failed to create polecat1 dir: %v", err)
	}
	polecat2Dir := filepath.Join(tmpDir, "therig", "polecats", "beta")
	if err := os.MkdirAll(polecat2Dir, 0755); err != nil {
		t.Fatalf("Failed to create polecat2 dir: %v", err)
	}

	// Initialize databases
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create multiple polecat agents
	polecat1 := &types.Issue{
		ID:        "gt-therig-polecat-alpha",
		Title:     "Agent: Alpha Polecat",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		RoleType:  "polecat",
		Rig:       "therig",
	}
	polecat2 := &types.Issue{
		ID:        "gt-therig-polecat-beta",
		Title:     "Agent: Beta Polecat",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		RoleType:  "polecat",
		Rig:       "therig",
	}

	for _, p := range []*types.Issue{polecat1, polecat2} {
		if err := rigStore.CreateIssue(ctx, p, "test"); err != nil {
			t.Fatalf("Failed to create polecat bead %s: %v", p.ID, err)
		}
		if err := rigStore.AddLabel(ctx, p.ID, "gt:agent", "test"); err != nil {
			t.Fatalf("Failed to add gt:agent label to %s: %v", p.ID, err)
		}
	}

	// Create routes.jsonl
	routesContent := `{"prefix":"gt-","path":"therig"}`
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// Test that both polecats can be resolved independently
	for _, expectedID := range []string{"gt-therig-polecat-alpha", "gt-therig-polecat-beta"} {
		result, err := resolveAndGetIssueWithRouting(ctx, townStore, expectedID)
		if err != nil {
			t.Fatalf("resolveAndGetIssueWithRouting failed for %s: %v", expectedID, err)
		}
		if result == nil || result.Issue == nil {
			t.Fatalf("resolveAndGetIssueWithRouting returned nil for %s", expectedID)
		}

		if result.Issue.ID != expectedID {
			t.Errorf("Expected issue ID %q, got %q", expectedID, result.Issue.ID)
		}

		result.Close()
	}

	t.Logf("Successfully resolved multiple polecats via routing")
}

// TestPolecatRoutingPartialIDResolution tests that partial ID resolution works
// for polecat agents across routed storage.
func TestPolecatRoutingPartialIDResolution(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()

	// Create town structure
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("Failed to create mayor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to create town.json: %v", err)
	}

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create town beads dir: %v", err)
	}

	rigBeadsDir := filepath.Join(tmpDir, "myrig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("Failed to create rig beads dir: %v", err)
	}

	// Initialize databases
	townDBPath := filepath.Join(townBeadsDir, "beads.db")
	townStore := newTestStoreWithPrefix(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "beads.db")
	rigStore := newTestStoreWithPrefix(t, rigDBPath, "gt")

	// Create a polecat agent with unique ID
	polecatBead := &types.Issue{
		ID:        "gt-myrig-polecat-unique123",
		Title:     "Agent: Unique Polecat",
		IssueType: types.TypeTask,
		Status:    types.StatusOpen,
		RoleType:  "polecat",
		Rig:       "myrig",
	}
	if err := rigStore.CreateIssue(ctx, polecatBead, "test"); err != nil {
		t.Fatalf("Failed to create polecat bead: %v", err)
	}
	if err := rigStore.AddLabel(ctx, polecatBead.ID, "gt:agent", "test"); err != nil {
		t.Fatalf("Failed to add gt:agent label: %v", err)
	}

	// Create routes.jsonl
	routesContent := `{"prefix":"gt-","path":"myrig"}`
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routesContent), 0644); err != nil {
		t.Fatalf("Failed to write routes.jsonl: %v", err)
	}

	// Set up global state
	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// Test partial ID resolution (using prefix of the full ID)
	// The prefix "gt-myrig-polecat-unique" should resolve to the full ID
	result, err := resolveAndGetIssueWithRouting(ctx, townStore, "gt-myrig-polecat-unique")
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting failed: %v", err)
	}
	if result == nil || result.Issue == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil")
	}
	defer result.Close()

	if result.Issue.ID != "gt-myrig-polecat-unique123" {
		t.Errorf("Expected resolved ID %q, got %q", "gt-myrig-polecat-unique123", result.Issue.ID)
	}

	if result.ResolvedID != "gt-myrig-polecat-unique123" {
		t.Errorf("Expected ResolvedID %q, got %q", "gt-myrig-polecat-unique123", result.ResolvedID)
	}

	t.Logf("Successfully resolved polecat with partial ID via routing")
}
