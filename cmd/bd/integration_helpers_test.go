//go:build integration
// +build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// runGitCmd runs a git command in the given directory, failing the test on error.
func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2024-01-01T00:00:00", "GIT_AUTHOR_DATE=2024-01-01T00:00:00")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed in %s: %v\n%s", args, dir, err, output)
	}
}

// configureGit sets up git user config and .gitignore in a test repo.
func configureGit(t *testing.T, dir string) {
	t.Helper()
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test User")
	runGitCmd(t, dir, "config", "pull.rebase", "false")

	gitignorePath := filepath.Join(dir, ".gitignore")
	gitignoreContent := "# Test database files\n*.db\n*.db-journal\n*.db-wal\n*.db-shm\ndolt/\n"
	if err := os.WriteFile(gitignorePath, []byte(gitignoreContent), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}
}

// exportIssuesToJSONL exports all issues from the store to a JSONL file.
func exportIssuesToJSONL(ctx context.Context, store storage.Storage, jsonlPath string) error {
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return err
	}

	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
		labels, _ := store.GetLabels(ctx, issue.ID)
		issue.Labels = labels
	}

	f, err := os.Create(jsonlPath) //nolint:gosec
	if err != nil {
		return err
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return err
		}
	}

	return nil
}

// isDoltBackendUnavailable checks if bd output indicates Dolt is not available.
func isDoltBackendUnavailable(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "dolt") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "not available") || strings.Contains(lower, "unknown"))
}

// setupGitRepoForIntegration initializes a git repo with test user config.
func setupGitRepoForIntegration(t *testing.T, dir string) {
	t.Helper()
	if err := runCommandInDir(dir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(dir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(dir, "git", "config", "user.name", "Test User")
}

// importJSONLToStore imports issues from a JSONL file into the store.
func importJSONLToStore(ctx context.Context, store storage.Storage, _ string, jsonlPath string) error {
	data, err := os.ReadFile(jsonlPath) //nolint:gosec
	if err != nil {
		return err
	}

	var issues []*types.Issue
	decoder := json.NewDecoder(bytes.NewReader(data))
	for decoder.More() {
		var issue types.Issue
		if err := decoder.Decode(&issue); err != nil {
			return err
		}
		issues = append(issues, &issue)
	}

	for _, issue := range issues {
		existing, _ := store.GetIssue(ctx, issue.ID)
		if existing != nil {
			updates := map[string]interface{}{
				"status":   issue.Status,
				"priority": issue.Priority,
			}
			if err := store.UpdateIssue(ctx, issue.ID, updates, "import"); err != nil {
				return err
			}
		} else {
			if err := store.CreateIssue(ctx, issue, "import"); err != nil {
				return err
			}
		}
	}

	if err := store.SetMetadata(ctx, "last_import_time", time.Now().Format(time.RFC3339)); err != nil {
		return err
	}

	return nil
}
