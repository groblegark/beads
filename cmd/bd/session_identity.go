package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/rpc"
)

// sessionAssignedName caches the daemon-assigned session name for this process.
// Set once during daemon connection, used by getActor() thereafter.
var sessionAssignedName string

// deriveSessionKey computes a session key from available signals.
// The key must be unique per Claude Code session and stable across commands
// within the same session. Uses (PPID, TTY, project-root).
func deriveSessionKey(projectRoot string) string {
	// Use TERM_SESSION_ID if available (macOS terminal tabs — most stable)
	termSessionID := os.Getenv("TERM_SESSION_ID")
	if termSessionID != "" {
		raw := fmt.Sprintf("term:%s:%s", termSessionID, projectRoot)
		return shortHash(raw)
	}

	// Fallback: PPID + TTY + project root
	ppid := os.Getppid()
	tty := getTTY()
	raw := fmt.Sprintf("%d:%s:%s", ppid, tty, projectRoot)
	return shortHash(raw)
}

// getTTY returns the terminal device path, or "notty" if unavailable.
func getTTY() string {
	// Check if stdin is a terminal
	if fi, err := os.Stdin.Stat(); err == nil {
		if fi.Mode()&os.ModeCharDevice != 0 {
			// On Unix, /dev/tty is the controlling terminal
			if ttyPath, err := os.Readlink("/dev/fd/0"); err == nil {
				return ttyPath
			}
		}
	}
	return "notty"
}

// shortHash returns the first 16 hex chars of a SHA-256 hash.
func shortHash(input string) string {
	h := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", h[:8])
}

// registerSession attempts to register this session with the daemon and get
// a unique name. Called once during daemon connection setup.
// Returns the assigned name, or empty string if registration failed/skipped.
func registerSession(client *rpc.Client, projectRoot string) string {
	if client == nil {
		return ""
	}

	sessionKey := deriveSessionKey(projectRoot)
	baseName := getBaseActorName(sessionKey)

	debug.Logf("registerSession: sessionKey=%q baseName=%q projectRoot=%q", sessionKey, baseName, projectRoot)
	result, err := client.SessionRegister(&rpc.SessionRegisterArgs{
		SessionKey:  sessionKey,
		BaseName:    baseName,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		// Registration failed — fall through to normal actor resolution.
		// This is expected when connecting to older daemons that don't support this RPC.
		debug.Logf("registerSession: RPC failed: %v", err)
		return ""
	}

	debug.Logf("registerSession: assigned=%q isNew=%v", result.AssignedName, result.IsNew)
	return result.AssignedName
}

// getBaseActorName returns the base actor name before session numbering.
// This is the name that gets suffixed with -1, -2, etc. for multiple sessions.
//
// Priority:
//  1. BD_ACTOR env var (explicit agent identity)
//  2. BEADS_ACTOR env var (MCP/integration alias)
//  3. GT_ROLE env var (gastown-managed agents)
//  4. Generated agent name from session key (e.g., "swift-fox")
//
// Human names (git user.name, $USER) are deliberately NOT used as agent identities.
// Agents get fun, memorable names that persist for the session lifetime.
func getBaseActorName(sessionKey string) string {
	if bdActor := os.Getenv("BD_ACTOR"); bdActor != "" {
		return bdActor
	}
	if beadsActor := os.Getenv("BEADS_ACTOR"); beadsActor != "" {
		return beadsActor
	}
	if gtRole := os.Getenv("GT_ROLE"); gtRole != "" {
		return gtRole
	}

	// Generate a fun agent name from the session key.
	// Same session always gets the same base name.
	return rpc.GenerateAgentName(sessionKey)
}

// getGitUserName reads git config user.name from the .gitconfig file directly
// to avoid importing os/exec (which getActorWithGit already handles).
func getGitUserName() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Clean(filepath.Join(home, ".gitconfig")))
	if err != nil {
		return ""
	}
	// Simple parser: look for "name = " in [user] section
	inUserSection := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "[user]" {
			inUserSection = true
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			inUserSection = false
			continue
		}
		if inUserSection && strings.HasPrefix(trimmed, "name") {
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}
