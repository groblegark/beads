package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"gopkg.in/yaml.v3"
)

// readFromGitRef reads file content from a git ref (branch or commit) in the beads repo.
// Returns the raw bytes from git show <ref>:<path>.
func readFromGitRef(filePath, gitRef string) ([]byte, error) {
	gitPath := filepath.ToSlash(filePath)
	var cmd *exec.Cmd
	if rc, err := beads.GetRepoContext(); err == nil {
		cmd = rc.GitCmd(context.Background(), "show", fmt.Sprintf("%s:%s", gitRef, gitPath))
	} else {
		cmd = exec.Command("git", "show", fmt.Sprintf("%s:%s", gitRef, gitPath)) // #nosec G204 - git command with safe args
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read from git: %w", err)
	}
	return output, nil
}

// checkAndAutoImport checks if the database is empty but git has issues.
// If so, it automatically imports them and returns true.
func checkAndAutoImport(ctx context.Context, store storage.Storage) bool {
	stats, err := store.GetStatistics(ctx)
	if err != nil || stats.TotalIssues > 0 {
		return false
	}

	issueCount, jsonlPath, gitRef := checkGitForIssues()
	if issueCount == 0 {
		return false
	}

	if !jsonOutput {
		fmt.Fprintf(os.Stderr, "Found 0 issues in database but %d in git. Importing...\n", issueCount)
	}

	if err := importFromGit(ctx, dbPath, store, jsonlPath, gitRef); err != nil {
		if !jsonOutput {
			fmt.Fprintf(os.Stderr, "Warning: auto-import failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "Try manually: git show %s:%s | bd import -i /dev/stdin\n", gitRef, jsonlPath)
		}
		return false
	}

	if !jsonOutput {
		fmt.Fprintf(os.Stderr, "Successfully imported %d issues from git.\n\n", issueCount)
	}

	return true
}

// checkGitForIssues checks if git has issues in .beads/beads.jsonl or issues.jsonl
// Reads from HEAD. Returns (issue_count, relative_jsonl_path, git_ref).
func checkGitForIssues() (int, string, string) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return 0, "", ""
	}

	gitRoot := git.GetRepoRoot()
	if gitRoot == "" {
		return 0, "", ""
	}

	resolvedBeadsDir, err := filepath.EvalSymlinks(beadsDir)
	if err != nil {
		return 0, "", ""
	}
	beadsDir = resolvedBeadsDir
	resolvedGitRoot, err := filepath.EvalSymlinks(gitRoot)
	if err != nil {
		return 0, "", ""
	}
	gitRoot = resolvedGitRoot

	beadsDir = filepath.Clean(beadsDir)
	gitRoot = filepath.Clean(gitRoot)

	relBeads, err := filepath.Rel(gitRoot, beadsDir)
	if err != nil {
		return 0, "", ""
	}

	if strings.HasPrefix(relBeads, "..") {
		return 0, "", ""
	}

	gitRef := "HEAD"

	candidates := []string{
		filepath.Join(relBeads, "issues.jsonl"),
		filepath.Join(relBeads, "beads.jsonl"),
	}

	for _, relPath := range candidates {
		output, err := readFromGitRef(relPath, gitRef)
		if err == nil && len(output) > 0 {
			lines := bytes.Count(output, []byte("\n"))
			if lines > 0 {
				return lines, relPath, gitRef
			}
		}
	}

	return 0, "", ""
}

// localConfig represents the subset of config.yaml for auto-import and no-db detection.
type localConfig struct {
	NoDb bool `yaml:"no-db"`
}

// isNoDbModeConfigured checks if no-db: true is set in config.yaml.
func isNoDbModeConfigured(beadsDir string) bool {
	configPath := filepath.Join(beadsDir, "config.yaml")
	data, err := os.ReadFile(configPath) // #nosec G304 - config file path from beadsDir
	if err != nil {
		return false
	}

	var cfg localConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return false
	}

	return cfg.NoDb
}

// getLocalSyncBranch is a no-op stub retained for compatibility.
func getLocalSyncBranch(_ string) string {
	return ""
}

// importFromJSONLData imports issues from raw JSONL bytes.
func importFromJSONLData(ctx context.Context, dbFilePath string, store storage.Storage, jsonlData []byte) (int, error) {
	scanner := bufio.NewScanner(bytes.NewReader(jsonlData))
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	var issues []*types.Issue

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return 0, fmt.Errorf("failed to parse issue: %w", err)
		}
		issue.SetDefaults()
		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("failed to scan JSONL: %w", err)
	}

	if len(issues) > 0 {
		configuredPrefix, err := store.GetConfig(ctx, "issue_prefix")
		if err == nil && strings.TrimSpace(configuredPrefix) == "" {
			firstPrefix := utils.ExtractIssuePrefix(issues[0].ID)
			if firstPrefix != "" {
				if err := store.SetConfig(ctx, "issue_prefix", firstPrefix); err != nil {
					return 0, fmt.Errorf("failed to set issue_prefix from imported issues: %w", err)
				}
			}
		}
	}

	opts := ImportOptions{
		DryRun:               false,
		SkipUpdate:           false,
		SkipPrefixValidation: true,
	}

	_, err := importIssuesCore(ctx, dbFilePath, store, issues, opts)
	if err != nil {
		return 0, err
	}

	return len(issues), nil
}

// importFromLocalJSONL imports issues from a local JSONL file on disk.
func importFromLocalJSONL(ctx context.Context, dbFilePath string, store storage.Storage, localPath string) (int, error) {
	// #nosec G304 -- path provided by bd init command
	jsonlData, err := os.ReadFile(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read local JSONL file: %w", err)
	}
	return importFromJSONLData(ctx, dbFilePath, store, jsonlData)
}

// importFromGit imports issues from git at the specified ref.
func importFromGit(ctx context.Context, dbFilePath string, store storage.Storage, jsonlPath, gitRef string) error {
	jsonlData, err := readFromGitRef(jsonlPath, gitRef)
	if err != nil {
		return err
	}
	_, err = importFromJSONLData(ctx, dbFilePath, store, jsonlData)
	return err
}
