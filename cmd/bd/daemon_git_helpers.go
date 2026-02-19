package main

// daemon_git_helpers.go restores git helper functions that daemon_sync.go,
// daemon_sync_branch.go, daemon.go, daemon_start.go, migrate_sync.go, and
// sync.go depend on. These were deleted in the sync_*.go cleanup (beads-gfz6)
// but are still needed by daemon sync and the sync.go stub's flush-only path.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
)

// gitHasUpstream checks if the current branch has an upstream configured.
func gitHasUpstream() bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}
	ctx := context.Background()
	branchCmd := rc.GitCmd(ctx, "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return false
	}
	branch := strings.TrimSpace(string(branchOutput))
	return gitBranchHasUpstream(branch)
}

// gitBranchHasUpstream checks if a specific branch has an upstream configured.
func gitBranchHasUpstream(branch string) bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}
	ctx := context.Background()
	remoteCmd := rc.GitCmd(ctx, "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) //nolint:gosec // args are safe git config keys
	mergeCmd := rc.GitCmd(ctx, "config", "--get", fmt.Sprintf("branch.%s.merge", branch))   //nolint:gosec // args are safe git config keys
	return remoteCmd.Run() == nil && mergeCmd.Run() == nil
}

// gitHasChanges checks if the specified file has uncommitted changes.
func gitHasChanges(ctx context.Context, filePath string) (bool, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false, fmt.Errorf("getting repo context: %w", err)
	}
	cmd := rc.GitCmd(ctx, "status", "--porcelain", filePath)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// gitHasBeadsChanges checks if any tracked files in .beads/ have uncommitted changes.
func gitHasBeadsChanges(ctx context.Context) (bool, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false, fmt.Errorf("getting repo context: %w", err)
	}
	cmd := rc.GitCmd(ctx, "status", "--porcelain", rc.BeadsDir)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// buildCommitArgs returns git commit args for use with RepoContext.GitCmd().
func buildCommitArgs(message string, extraArgs ...string) []string {
	args := []string{"commit"}
	if author := config.GetString("git.author"); author != "" {
		args = append(args, "--author", author)
	}
	if config.GetBool("git.no-gpg-sign") {
		args = append(args, "--no-gpg-sign")
	}
	args = append(args, "-m", message)
	args = append(args, extraArgs...)
	return args
}

// gitCommit commits the specified file in the beads repository.
func gitCommit(ctx context.Context, filePath string, message string) error {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}
	relPath, err := filepath.Rel(rc.RepoRoot, filePath)
	if err != nil {
		relPath = filePath
	}
	addCmd := rc.GitCmd(ctx, "add", relPath)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}
	commitArgs := buildCommitArgs(message, "--", relPath)
	commitCmd := rc.GitCmd(ctx, commitArgs...)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}
	return nil
}

// gitCommitBeadsDir stages and commits only sync-related files in .beads/.
func gitCommitBeadsDir(ctx context.Context, message string) error {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}
	syncFiles := []string{
		filepath.Join(rc.BeadsDir, "issues.jsonl"),
		filepath.Join(rc.BeadsDir, "events.jsonl"),
		filepath.Join(rc.BeadsDir, "deletions.jsonl"),
		filepath.Join(rc.BeadsDir, "interactions.jsonl"),
		filepath.Join(rc.BeadsDir, "metadata.json"),
	}
	var filesToAdd []string
	for _, f := range syncFiles {
		if _, err := os.Stat(f); err == nil {
			relPath, err := filepath.Rel(rc.RepoRoot, f)
			if err != nil {
				relPath = f
			}
			filesToAdd = append(filesToAdd, relPath)
		}
	}
	if len(filesToAdd) == 0 {
		return fmt.Errorf("no sync files found to commit")
	}
	addArgs := append([]string{"add"}, filesToAdd...)
	addCmd := rc.GitCmd(ctx, addArgs...)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}
	if message == "" {
		message = fmt.Sprintf("bd sync: %s", time.Now().Format("2006-01-02 15:04:05"))
	}
	relBeadsDir, err := filepath.Rel(rc.RepoRoot, rc.BeadsDir)
	if err != nil {
		relBeadsDir = rc.BeadsDir
	}
	commitArgs := buildCommitArgs(message, "--", relBeadsDir)
	commitCmd := rc.GitCmd(ctx, commitArgs...)
	output, err := commitCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit failed: %w\n%s", err, output)
	}
	return nil
}

// hasGitRemote checks if a git remote exists in the beads repository.
func hasGitRemote(ctx context.Context) bool {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return false
	}
	cmd := rc.GitCmd(ctx, "remote")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(output))) > 0
}

// gitPull pulls from the current branch's upstream.
func gitPull(ctx context.Context, configuredRemote string) error {
	if !hasGitRemote(ctx) {
		return nil
	}
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}
	branchCmd := rc.GitCmd(ctx, "symbolic-ref", "--short", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOutput))
	remote := configuredRemote
	if remote == "" {
		remoteCmd := rc.GitCmd(ctx, "config", "--get", fmt.Sprintf("branch.%s.remote", branch)) //nolint:gosec // args are safe git config keys
		remoteOutput, err := remoteCmd.Output()
		if err != nil {
			remote = "origin"
		} else {
			remote = strings.TrimSpace(string(remoteOutput))
		}
	}
	cmd := rc.GitCmd(ctx, "pull", remote, branch) //nolint:gosec // remote and branch from git config, not user input
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git pull failed: %w\n%s", err, output)
	}
	return nil
}

// gitPush pushes to the current branch's upstream.
func gitPush(ctx context.Context, configuredRemote string) error {
	if !hasGitRemote(ctx) {
		return nil
	}
	rc, err := beads.GetRepoContext()
	if err != nil {
		return fmt.Errorf("getting repo context: %w", err)
	}
	if configuredRemote != "" {
		branchCmd := rc.GitCmd(ctx, "symbolic-ref", "--short", "HEAD")
		branchOutput, err := branchCmd.Output()
		if err != nil {
			return fmt.Errorf("failed to get current branch: %w", err)
		}
		branch := strings.TrimSpace(string(branchOutput))
		cmd := rc.GitCmd(ctx, "push", configuredRemote, branch) //nolint:gosec // remote and branch from git config, not user input
		output, err := cmd.CombinedOutput()
		if err != nil {
			outputStr := string(output)
			if isPushPermissionDenied(outputStr) {
				showPushPermissionGuidance()
			}
			return fmt.Errorf("git push failed: %w\n%s", err, output)
		}
		return nil
	}
	cmd := rc.GitCmd(ctx, "push")
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		if isPushPermissionDenied(outputStr) {
			showPushPermissionGuidance()
		}
		return fmt.Errorf("git push failed: %w\n%s", err, output)
	}
	return nil
}

// isPushPermissionDenied checks if git push output indicates a permission error.
func isPushPermissionDenied(output string) bool {
	patterns := []string{
		"permission denied", "403", "not allowed to push",
		"could not read from remote", "remote: permission to",
		"fatal: unable to access", "authentication failed",
		"the requested url returned error: 403",
		"permission to", "denied to",
	}
	lower := strings.ToLower(output)
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// showPushPermissionGuidance prints contributor setup guidance.
func showPushPermissionGuidance() {
	fmt.Fprintln(os.Stderr, "\nPush access denied.")
	fmt.Fprintln(os.Stderr, "\nYou don't have push access to this repository.")
	fmt.Fprintln(os.Stderr, "\nIf you're contributing to someone else's repo:")
	fmt.Fprintln(os.Stderr, "  git config beads.role contributor")
	fmt.Fprintln(os.Stderr, "  bd init --contributor")
}

// getCurrentBranch returns the current git branch name.
func getCurrentBranch(ctx context.Context) (string, error) {
	rc, err := beads.GetRepoContext()
	if err != nil {
		// Fall back to exec
		cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--short", "HEAD")
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to get current branch: %w", err)
		}
		return strings.TrimSpace(string(output)), nil
	}
	cmd := rc.GitCmd(ctx, "symbolic-ref", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}
