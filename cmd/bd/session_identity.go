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
// within the same session.
//
// Priority for key derivation:
//  1. CLAUDE_SESSION_ID — set by Claude Code, stable across all CLI calls in a session
//  2. TERM_SESSION_ID — macOS terminal tabs, stable within a tab
//  3. BD_ACTOR / BEADS_ACTOR / GT_ROLE + project-root — managed agents without
//     a terminal session; uses actor identity instead of PPID to avoid creating
//     a new session for every CLI invocation (bd-ph9r1)
//  4. PPID + TTY + project-root — fallback for interactive terminals
func deriveSessionKey(projectRoot string) string {
	// Best: Claude Code session ID — explicitly stable across CLI calls
	if claudeSession := os.Getenv("CLAUDE_SESSION_ID"); claudeSession != "" {
		raw := fmt.Sprintf("claude:%s:%s", claudeSession, projectRoot)
		return shortHash(raw)
	}

	// Use TERM_SESSION_ID if available (macOS terminal tabs — most stable)
	termSessionID := os.Getenv("TERM_SESSION_ID")
	if termSessionID != "" {
		raw := fmt.Sprintf("term:%s:%s", termSessionID, projectRoot)
		return shortHash(raw)
	}

	// Managed agents: use actor identity instead of PPID.
	// Without this, every `bd` CLI call from a K8s pod gets a unique PPID,
	// creating hundreds of orphan sessions (e.g., mayor-1 through mayor-800+).
	if actor := getStableActorIdentity(); actor != "" {
		raw := fmt.Sprintf("actor:%s:%s", actor, projectRoot)
		return shortHash(raw)
	}

	// Fallback: PPID + TTY + project root
	ppid := os.Getppid()
	tty := getTTY()
	raw := fmt.Sprintf("%d:%s:%s", ppid, tty, projectRoot)
	return shortHash(raw)
}

// getStableActorIdentity returns a stable agent identity from env vars,
// or empty string if none is set. Used to derive session keys for managed
// agents that don't have a terminal session.
func getStableActorIdentity() string {
	if v := os.Getenv("BD_ACTOR"); v != "" {
		return v
	}
	if v := os.Getenv("BEADS_ACTOR"); v != "" {
		return v
	}
	if v := os.Getenv("GT_ROLE"); v != "" {
		return v
	}
	return ""
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

	// Check for a persistent claimed name (bd config set actor <name>).
	// This survives compaction and session key changes because it's stored
	// in the database, not derived from the session key. (bd-lqw3p)
	// Only use it if no env var explicitly sets the actor identity.
	if getStableActorIdentity() == "" {
		if claimed := getClaimedActorName(client); claimed != "" {
			debug.Logf("registerSession: using claimed actor name %q (from config)", claimed)
			baseName = claimed
		}
	}

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

// getClaimedActorName queries the daemon for a persistent actor name set via
// `bd config set actor <name>`. Returns empty string if not set or on error.
// This allows a name to survive compaction and session key rotation. (bd-lqw3p)
func getClaimedActorName(client *rpc.Client) string {
	if client == nil {
		return ""
	}
	result, err := client.GetConfig(&rpc.GetConfigArgs{Key: "actor"})
	if err != nil || result == nil || result.Value == "" {
		return ""
	}
	return result.Value
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
