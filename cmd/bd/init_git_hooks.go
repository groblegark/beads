package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/ui"
)


// preCommitFrameworkPattern matches pre-commit or prek framework hooks.
// Uses same patterns as hookManagerPatterns in doctor/fix/hooks.go for consistency.
// Includes all detection patterns: pre-commit run, prek run/hook-impl, config file refs, and pre-commit env vars.
var preCommitFrameworkPattern = regexp.MustCompile(`(?i)(pre-commit\s+run|prek\s+run|prek\s+hook-impl|\.pre-commit-config|INSTALL_PYTHON|PRE_COMMIT)`)

// hooksInstalled checks if bd git hooks are installed.
// Since the pre-commit hook was removed (Dolt handles sync), we only check post-merge.
func hooksInstalled() bool {
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return false
	}
	postMerge := filepath.Join(hooksDir, "post-merge")

	// Check if post-merge hook exists
	if _, err := os.Stat(postMerge); err != nil {
		return false
	}

	// Verify it's a bd hook by checking for signature comment
	// #nosec G304 - controlled path from git directory
	postMergeContent, err := os.ReadFile(postMerge)
	if err != nil || !strings.Contains(string(postMergeContent), "bd (beads) post-merge hook") {
		return false
	}

	// Verify hook is executable
	postMergeInfo, err := os.Stat(postMerge)
	if err != nil {
		return false
	}
	if postMergeInfo.Mode().Perm()&0111 == 0 {
		return false // Not executable
	}

	return true
}

// hookInfo contains information about an existing hook
type hookInfo struct {
	name                 string
	path                 string
	exists               bool
	isBdHook             bool
	isPreCommitFramework bool // true for pre-commit or prek
	content              string
}

// detectExistingHooks scans for existing git hooks
func detectExistingHooks() []hookInfo {
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return nil
	}
	hooks := []hookInfo{
		{name: "post-merge", path: filepath.Join(hooksDir, "post-merge")},
		{name: "pre-push", path: filepath.Join(hooksDir, "pre-push")},
	}

	for i := range hooks {
		content, err := os.ReadFile(hooks[i].path)
		if err == nil {
			hooks[i].exists = true
			hooks[i].content = string(content)
			hooks[i].isBdHook = strings.Contains(hooks[i].content, "bd (beads)")
			// Only detect pre-commit/prek framework if not a bd hook
			// Use regex for consistency with DetectActiveHookManager patterns
			if !hooks[i].isBdHook {
				hooks[i].isPreCommitFramework = preCommitFrameworkPattern.MatchString(hooks[i].content)
			}
		}
	}

	return hooks
}

// promptHookAction asks user what to do with existing hooks
func promptHookAction(existingHooks []hookInfo) string {
	fmt.Printf("\n%s Found existing git hooks:\n", ui.RenderWarn("⚠"))
	for _, hook := range existingHooks {
		if hook.exists && !hook.isBdHook {
			hookType := "custom script"
			if hook.isPreCommitFramework {
				hookType = "pre-commit/prek framework"
			}
			fmt.Printf("  - %s (%s)\n", hook.name, hookType)
		}
	}

	fmt.Printf("\nHow should bd proceed?\n")
	fmt.Printf("  [1] Chain with existing hooks (recommended)\n")
	fmt.Printf("  [2] Overwrite existing hooks\n")
	fmt.Printf("  [3] Skip git hooks installation\n")
	fmt.Printf("Choice [1-3]: ")

	var response string
	_, _ = fmt.Scanln(&response)
	response = strings.TrimSpace(response)

	return response
}

// buildPostMergeHook generates the post-merge hook content
func buildPostMergeHook(chainHooks bool, existingHooks []hookInfo) string {
	if chainHooks {
		// Find existing post-merge hook (already renamed to .old by caller)
		var existingPostMerge string
		for _, hook := range existingHooks {
			if hook.name == "post-merge" && hook.exists && !hook.isBdHook {
				existingPostMerge = hook.path + ".old"
				break
			}
		}

		return `#!/bin/sh
#
# bd (beads) post-merge hook (chained)
#
# This hook chains bd functionality with your existing post-merge hook.

# Run existing hook first
if [ -x "` + existingPostMerge + `" ]; then
    "` + existingPostMerge + `" "$@"
    EXIT_CODE=$?
    if [ $EXIT_CODE -ne 0 ]; then
        exit $EXIT_CODE
    fi
fi

` + postMergeHookBody()
	}

	return `#!/bin/sh
#
# bd (beads) post-merge hook
#
# This hook imports updated issues from .beads/issues.jsonl after a
# git pull or merge, ensuring the database stays in sync with git.

` + postMergeHookBody()
}

// postMergeHookBody returns the common post-merge hook logic
func postMergeHookBody() string {
	return `# Check if bd is available
if ! command -v bd >/dev/null 2>&1; then
    echo "Warning: bd command not found, skipping post-merge import" >&2
    exit 0
fi

# Check if we're in a bd workspace
# For worktrees, .beads is in the main repository root, not the worktree
BEADS_DIR=""
if git rev-parse --git-dir >/dev/null 2>&1; then
    # Check if we're in a worktree
    if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
        # Worktree: .beads is in main repo root
        MAIN_REPO_ROOT="$(git rev-parse --git-common-dir)"
        MAIN_REPO_ROOT="$(dirname "$MAIN_REPO_ROOT")"
        if [ -d "$MAIN_REPO_ROOT/.beads" ]; then
            BEADS_DIR="$MAIN_REPO_ROOT/.beads"
        fi
    else
        # Regular repo: check current directory
        if [ -d .beads ]; then
            BEADS_DIR=".beads"
        fi
    fi
fi

if [ -z "$BEADS_DIR" ]; then
    exit 0
fi

# Skip for Dolt backend (uses its own sync mechanism, not JSONL import)
if [ -f "$BEADS_DIR/metadata.json" ]; then
    if grep -q '"backend"[[:space:]]*:[[:space:]]*"dolt"' "$BEADS_DIR/metadata.json" 2>/dev/null; then
        exit 0
    fi
fi

# Check if issues.jsonl exists and was updated
if [ ! -f "$BEADS_DIR/issues.jsonl" ]; then
    exit 0
fi

# Import updated JSONL into the database
IMPORT_OUTPUT=$(bd import 2>&1)
IMPORT_EXIT=$?

if [ $IMPORT_EXIT -ne 0 ]; then
    echo "Warning: Failed to import bd changes after merge (exit code $IMPORT_EXIT)" >&2
    if [ -n "$IMPORT_OUTPUT" ]; then
        echo "Error details:" >&2
        echo "$IMPORT_OUTPUT" | head -5 >&2
    fi
    echo "Run 'bd doctor --fix' to diagnose and repair" >&2
fi

exit 0
`
}

// mergeDriverInstalled checks if bd merge driver is configured correctly
// Note: This runs during bd init BEFORE .beads exists, so it runs git in CWD.
func mergeDriverInstalled() bool {
	// Check git config for merge driver (runs in CWD)
	cmd := exec.Command("git", "config", "merge.beads.driver")
	output, err := cmd.Output()
	if err != nil || len(output) == 0 {
		return false
	}

	// Check if using old invalid placeholders (%L/%R from versions <0.24.0)
	// Git only supports %O (base), %A (current), %B (other)
	driverConfig := strings.TrimSpace(string(output))
	if strings.Contains(driverConfig, "%L") || strings.Contains(driverConfig, "%R") {
		// Stale config with invalid placeholders - needs repair
		return false
	}

	// Check if .gitattributes has the merge driver configured
	gitattributesPath := ".gitattributes"
	content, err := os.ReadFile(gitattributesPath)
	if err != nil {
		return false
	}

	// Look for beads JSONL merge attribute (either canonical or legacy filename)
	hasCanonical := strings.Contains(string(content), ".beads/issues.jsonl") &&
		strings.Contains(string(content), "merge=beads")
	hasLegacy := strings.Contains(string(content), ".beads/beads.jsonl") &&
		strings.Contains(string(content), "merge=beads")
	return hasCanonical || hasLegacy
}

// installJJHooks installs simplified git hooks for colocated jujutsu+git repos.
// jj's model is simpler: the working copy IS always a commit, so no staging needed.
// Changes flow into the current change automatically.
func installJJHooks() error {
	hooksDir, err := git.GetGitHooksDir()
	if err != nil {
		return fmt.Errorf("get git hooks dir for jj: %w", err)
	}

	// Ensure hooks directory exists
	if err := os.MkdirAll(hooksDir, 0750); err != nil {
		return fmt.Errorf("failed to create hooks directory: %w", err)
	}

	// Detect existing hooks
	existingHooks := detectExistingHooks()

	// Check if any non-bd hooks exist
	hasExistingHooks := false
	for _, hook := range existingHooks {
		if hook.exists && !hook.isBdHook {
			hasExistingHooks = true
			break
		}
	}

	// Determine installation mode
	chainHooks := false
	if hasExistingHooks {
		choice := promptHookAction(existingHooks)
		switch choice {
		case "1", "":
			chainHooks = true
			// Chain mode - rename existing hooks to .old so they can be called
			for _, hook := range existingHooks {
				if hook.exists && !hook.isBdHook {
					oldPath := hook.path + ".old"
					if err := os.Rename(hook.path, oldPath); err != nil {
						return fmt.Errorf("failed to rename %s to .old: %w", hook.name, err)
					}
					fmt.Printf("  Renamed %s to %s\n", hook.name, filepath.Base(oldPath))
				}
			}
		case "2":
			// Overwrite mode - backup existing hooks
			for _, hook := range existingHooks {
				if hook.exists && !hook.isBdHook {
					timestamp := time.Now().Format("20060102-150405")
					backup := hook.path + ".backup-" + timestamp
					if err := os.Rename(hook.path, backup); err != nil {
						return fmt.Errorf("failed to backup %s: %w", hook.name, err)
					}
					fmt.Printf("  Backed up %s to %s\n", hook.name, filepath.Base(backup))
				}
			}
		case "3":
			fmt.Printf("Skipping git hooks installation.\n")
			return nil
		default:
			return fmt.Errorf("invalid choice: %s", choice)
		}
	}

	// post-merge hook
	postMergePath := filepath.Join(hooksDir, "post-merge")
	postMergeContent := buildPostMergeHook(chainHooks, existingHooks)

	// Write post-merge hook
	// #nosec G306 - git hooks must be executable
	if err := os.WriteFile(postMergePath, []byte(postMergeContent), 0700); err != nil {
		return fmt.Errorf("failed to write post-merge hook: %w", err)
	}

	if chainHooks {
		fmt.Printf("%s Chained bd hooks with existing hooks (jj mode)\n", ui.RenderPass("✓"))
	}

	return nil
}

// printJJAliasInstructions prints setup instructions for pure jujutsu repos.
// Since jj doesn't have native hooks yet, users need to set up aliases.
func printJJAliasInstructions() {
	fmt.Printf("\n%s Jujutsu repository detected (not colocated with git)\n\n", ui.RenderWarn("⚠"))
	fmt.Printf("Jujutsu doesn't support hooks yet. To auto-export beads on push,\n")
	fmt.Printf("add this alias to your jj config (~/.config/jj/config.toml):\n\n")
	fmt.Printf("  %s\n", ui.RenderAccent("[aliases]"))
	fmt.Printf("  %s\n", ui.RenderAccent(`push = ["util", "exec", "--", "sh", "-c", "bd export && jj git push \"$@\"", ""]`))
	fmt.Printf("\nThen use %s instead of %s\n\n", ui.RenderAccent("jj push"), ui.RenderAccent("jj git push"))
	fmt.Printf("For more details, see: https://github.com/steveyegge/beads/blob/main/docs/JUJUTSU.md\n\n")
}

// installMergeDriver configures git to use bd merge for JSONL files
// Note: This runs during bd init BEFORE .beads exists, so it runs git in CWD.
func installMergeDriver() error {
	// Configure git merge driver (runs in CWD)
	cmd := exec.Command("git", "config", "merge.beads.driver", "bd merge %A %O %A %B")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to configure git merge driver: %w\n%s", err, output)
	}

	cmd = exec.Command("git", "config", "merge.beads.name", "bd JSONL merge driver")
	if output, err := cmd.CombinedOutput(); err != nil {
		// Non-fatal, the name is just descriptive
		fmt.Fprintf(os.Stderr, "Warning: failed to set merge driver name: %v\n%s", err, output)
	}

	// Create or update .gitattributes
	gitattributesPath := ".gitattributes"

	// Read existing .gitattributes if it exists
	var existingContent string
	content, err := os.ReadFile(gitattributesPath)
	if err == nil {
		existingContent = string(content)
	}

	// Check if beads merge driver is already configured
	// Check for either pattern (issues.jsonl is canonical, beads.jsonl is legacy)
	hasBeadsMerge := (strings.Contains(existingContent, ".beads/issues.jsonl") ||
		strings.Contains(existingContent, ".beads/beads.jsonl")) &&
		strings.Contains(existingContent, "merge=beads")

	if !hasBeadsMerge {
		// Append beads merge driver configuration (issues.jsonl is canonical)
		beadsMergeAttr := "\n# Use bd merge for beads JSONL files\n.beads/issues.jsonl merge=beads\n"

		newContent := existingContent
		if !strings.HasSuffix(newContent, "\n") && len(newContent) > 0 {
			newContent += "\n"
		}
		newContent += beadsMergeAttr

		// Write updated .gitattributes (0644 is standard for .gitattributes)
		// #nosec G306 - .gitattributes needs to be readable
		if err := os.WriteFile(gitattributesPath, []byte(newContent), 0644); err != nil {
			return fmt.Errorf("failed to update .gitattributes: %w", err)
		}
	}

	return nil
}
