package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/ui"
)

// setupStealthMode configures git settings for stealth operation
// Uses .git/info/exclude (per-repository) instead of global gitignore because:
// - Global gitignore doesn't support absolute paths (GitHub #704)
// - .git/info/exclude is designed for user-specific, repo-local ignores
// - Patterns are relative to repo root, so ".beads/" works correctly
func setupStealthMode(verbose bool) error {
	// Remote mode: no local git repo or .claude writes needed
	if os.Getenv("BD_DAEMON_HOST") != "" {
		return nil
	}

	// Setup per-repository git exclude file
	if err := setupGitExclude(verbose); err != nil {
		return fmt.Errorf("failed to setup git exclude: %w", err)
	}

	// Setup claude settings
	if err := setupClaudeSettings(verbose); err != nil {
		return fmt.Errorf("failed to setup claude settings: %w", err)
	}

	if verbose {
		fmt.Printf("\n%s Stealth mode configured successfully!\n\n", ui.RenderPass("✓"))
		fmt.Printf("  Git exclude: %s\n", ui.RenderAccent(".git/info/exclude configured"))
		fmt.Printf("  Claude settings: %s\n\n", ui.RenderAccent("configured with bd onboard instruction"))
		fmt.Printf("Your beads setup is now %s - other repo collaborators won't see any beads-related files.\n\n", ui.RenderAccent("invisible"))
	}

	return nil
}

// setupGitExclude configures .git/info/exclude to ignore beads and claude files
// This is the correct approach for per-repository user-specific ignores (GitHub #704).
// Unlike global gitignore, patterns here are relative to the repo root.
func setupGitExclude(verbose bool) error {
	// Find the common .git directory (handles worktrees correctly - GH#1053)
	// Use --git-common-dir to get the main repo's .git, not the worktree's .git/worktrees/<name>
	gitDir, err := exec.Command("git", "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return fmt.Errorf("not a git repository")
	}
	gitDirPath := strings.TrimSpace(string(gitDir))

	// Path to the exclude file
	excludePath := filepath.Join(gitDirPath, "info", "exclude")

	// Ensure the info directory exists
	infoDir := filepath.Join(gitDirPath, "info")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		return fmt.Errorf("failed to create git info directory: %w", err)
	}

	// Read existing exclude file if it exists
	var existingContent string
	// #nosec G304 - git config path
	if content, err := os.ReadFile(excludePath); err == nil {
		existingContent = string(content)
	}

	// Use relative patterns (these work correctly in .git/info/exclude)
	beadsPattern := ".beads/"
	claudePattern := ".claude/settings.local.json"

	hasBeads := strings.Contains(existingContent, beadsPattern)
	hasClaude := strings.Contains(existingContent, claudePattern)

	if hasBeads && hasClaude {
		if verbose {
			fmt.Printf("Git exclude already configured for stealth mode\n")
		}
		return nil
	}

	// Append missing patterns
	newContent := existingContent
	if !strings.HasSuffix(newContent, "\n") && len(newContent) > 0 {
		newContent += "\n"
	}

	if !hasBeads || !hasClaude {
		newContent += "\n# Beads stealth mode (added by bd init --stealth)\n"
	}

	if !hasBeads {
		newContent += beadsPattern + "\n"
	}
	if !hasClaude {
		newContent += claudePattern + "\n"
	}

	// Write the updated exclude file
	// #nosec G306 - config file needs 0644
	if err := os.WriteFile(excludePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to write git exclude file: %w", err)
	}

	if verbose {
		fmt.Printf("Configured git exclude for stealth mode: %s\n", excludePath)
	}

	return nil
}

// setupForkExclude configures .git/info/exclude for fork workflows (GH#742)
// Adds beads files and Claude artifacts to keep PRs to upstream clean.
// This is separate from stealth mode - fork protection is specifically about
// preventing beads/Claude files from appearing in upstream PRs.
func setupForkExclude(verbose bool) error {
	// Use --git-common-dir to get main repo's .git, not worktree's (GH#1053)
	gitDir, err := exec.Command("git", "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return fmt.Errorf("not a git repository")
	}
	gitDirPath := strings.TrimSpace(string(gitDir))
	excludePath := filepath.Join(gitDirPath, "info", "exclude")

	// Ensure info directory exists
	if err := os.MkdirAll(filepath.Join(gitDirPath, "info"), 0755); err != nil {
		return fmt.Errorf("failed to create git info directory: %w", err)
	}

	// Read existing content
	var existingContent string
	// #nosec G304 - git config path
	if content, err := os.ReadFile(excludePath); err == nil {
		existingContent = string(content)
	}

	// Patterns to add for fork protection
	patterns := []string{".beads/", "**/RECOVERY*.md", "**/SESSION*.md"}
	var toAdd []string
	for _, p := range patterns {
		// Check for exact line match (pattern alone on a line)
		// This avoids false positives like ".beads/issues.jsonl" matching ".beads/"
		if !containsExactPattern(existingContent, p) {
			toAdd = append(toAdd, p)
		}
	}

	if len(toAdd) == 0 {
		if verbose {
			fmt.Printf("%s Git exclude already configured\n", ui.RenderPass("✓"))
		}
		return nil
	}

	// Append patterns
	newContent := existingContent
	if !strings.HasSuffix(newContent, "\n") && len(newContent) > 0 {
		newContent += "\n"
	}
	newContent += "\n# Beads fork protection (bd init)\n"
	for _, p := range toAdd {
		newContent += p + "\n"
	}

	// #nosec G306 - config file needs 0644
	if err := os.WriteFile(excludePath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to write git exclude: %w", err)
	}

	if verbose {
		fmt.Printf("\n%s Added to .git/info/exclude:\n", ui.RenderPass("✓"))
		for _, p := range toAdd {
			fmt.Printf("  %s\n", p)
		}
		fmt.Println("\nNote: .git/info/exclude is local-only and won't affect upstream.")
	}
	return nil
}

// containsExactPattern checks if content contains the pattern as an exact line
// This avoids false positives like ".beads/issues.jsonl" matching ".beads/"
func containsExactPattern(content, pattern string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

// promptForkExclude asks if user wants to configure .git/info/exclude for fork workflow (GH#742)
func promptForkExclude(upstreamURL string, quiet bool) bool {
	if quiet {
		return false // Don't prompt in quiet mode
	}

	fmt.Printf("\n%s Detected fork (upstream: %s)\n\n", ui.RenderAccent("▶"), upstreamURL)
	fmt.Println("Would you like to configure .git/info/exclude to keep beads files local?")
	fmt.Println("This prevents beads from appearing in PRs to upstream.")
	fmt.Print("\n[Y/n]: ")

	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	response = strings.TrimSpace(strings.ToLower(response))

	// Default to yes (empty or "y" or "yes")
	return response == "" || response == "y" || response == "yes"
}

