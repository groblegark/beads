package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPerformAtomicExport tests the atomic export functionality (temp file + rename).
func TestPerformAtomicExport(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

	ctx := context.Background()

	// Create test issues
	issues := []*types.Issue{
		{
			ID:        "beads-test1",
			Title:     "Issue 1",
			Priority:  1,
			IssueType: types.TypeBug,
			Status:    types.StatusOpen,
		},
		{
			ID:        "beads-test2",
			Title:     "Issue 2",
			Priority:  2,
			IssueType: types.TypeTask,
			Status:    types.StatusClosed,
		},
	}

	// Call performAtomicExport
	if err := performAtomicExport(ctx, jsonlPath, issues, nil); err != nil {
		t.Fatalf("performAtomicExport failed: %v", err)
	}

	// Verify the JSONL file exists and contains the issues
	if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
		t.Fatal("JSONL file was not created")
	}

	// Verify no temp files left behind
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("failed to read temp dir: %v", err)
	}

	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", entry.Name())
		}
	}

	// Parse JSONL and verify issues
	file, err := os.Open(jsonlPath)
	if err != nil {
		t.Fatalf("failed to open JSONL: %v", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var parsedIssues []*types.Issue
	for decoder.More() {
		var iss types.Issue
		if err := decoder.Decode(&iss); err != nil {
			t.Fatalf("failed to decode issue: %v", err)
		}
		parsedIssues = append(parsedIssues, &iss)
	}

	if len(parsedIssues) != 2 {
		t.Fatalf("expected 2 issues in JSONL, got %d", len(parsedIssues))
	}

	if parsedIssues[0].ID != "beads-test1" || parsedIssues[1].ID != "beads-test2" {
		t.Error("issues not in expected order or with expected IDs")
	}
}

// TestFlushRoutedRepo_IsNoOp verifies that flushRoutedRepo is a no-op in
// dolt-native mode (the only mode). Dolt handles sync natively.
func TestFlushRoutedRepo_IsNoOp(t *testing.T) {
	tmpDir := t.TempDir()

	// Call with any path — should not crash or write anything
	flushRoutedRepo(nil, tmpDir)

	// Verify no JSONL was created
	jsonlPath := filepath.Join(tmpDir, ".beads", "issues.jsonl")
	if _, err := os.Stat(jsonlPath); !os.IsNotExist(err) {
		t.Error("flushRoutedRepo should be a no-op but created JSONL")
	}
}
