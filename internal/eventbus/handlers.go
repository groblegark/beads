package eventbus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// InboxStore is the minimal storage interface needed by inbox handlers
// to drain and mark items without spawning a subprocess. (bd-f33nh)
type InboxStore interface {
	InboxDrain(ctx context.Context, agentName string, maxPriority ...int) ([]*types.InboxItem, error)
	InboxMarkDelivered(ctx context.Context, agentName string, ids []string) error
}

// PrimeHandler injects bd prime workflow context on SessionStart and PreCompact.
// Priority 10 (runs first — context injection should happen before gates).
type PrimeHandler struct{}

func (h *PrimeHandler) ID() string              { return "prime" }
func (h *PrimeHandler) Handles() []EventType     { return []EventType{EventSessionStart, EventPreCompact} }
func (h *PrimeHandler) Priority() int            { return 10 }

func (h *PrimeHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), "prime")
	if err != nil {
		// bd prime exits 0 even on error (silent fail-safe).
		// Only log if the process didn't start.
		return fmt.Errorf("prime: %w", err)
	}
	if stdout != "" {
		result.Inject = append(result.Inject, stdout)
	}
	return nil
}

// GateHandler evaluates session gates on Stop and PreToolUse hooks.
// Priority 20 (runs after context injection).
type GateHandler struct{}

func (h *GateHandler) ID() string              { return "gate" }
func (h *GateHandler) Handles() []EventType     { return []EventType{EventStop, EventPreToolUse} }
func (h *GateHandler) Priority() int            { return 20 }

func (h *GateHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	hookName := string(event.Type)
	args := []string{"gate", "session-check", "--hook", hookName, "--json"}
	if event.SessionID != "" {
		args = append(args, "--session", event.SessionID)
	}
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), args...)
	if err != nil {
		// Exit code 1 means blocked. Parse the JSON to get the reason.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Parse JSON from stdout for block details.
			var resp gateCheckResponse
			if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr == nil {
				if resp.Decision == "block" {
					result.Block = true
					result.Reason = resp.Reason
				}
				for _, w := range resp.Warnings {
					result.Warnings = append(result.Warnings, w)
				}
				return nil
			}
			// Couldn't parse — treat as block with raw reason.
			result.Block = true
			result.Reason = strings.TrimSpace(stdout)
			return nil
		}
		return fmt.Errorf("gate: %w", err)
	}

	// Exit 0 = all gates satisfied. Check for warnings.
	var resp gateCheckResponse
	if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr == nil {
		for _, w := range resp.Warnings {
			result.Warnings = append(result.Warnings, w)
		}
	}
	return nil
}

// gateCheckResponse mirrors gate.CheckResponse for JSON parsing.
type gateCheckResponse struct {
	Decision string   `json:"decision"`
	Reason   string   `json:"reason,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}


// InboxDrainHandler drains inbox items on SessionStart, PreCompact, and Stop.
// Priority 30 (Phase 3: primary delivery path, replaces DecisionHandler).
//
// On Stop events, inbox drain ensures decision responses (and other inbox items)
// are injected into the agent's context. Without this, responses delivered via
// inbox while the agent was blocking in `bd decision create --wait` would never
// be surfaced, causing the agent to create duplicate decisions. (bd-rwzse)
//
// When store is set (via SetInboxStore), drains in-process — no subprocess.
// Falls back to subprocess when store is nil (backward compat). (bd-f33nh)
type InboxDrainHandler struct {
	store InboxStore
}

func (h *InboxDrainHandler) ID() string          { return "inbox-drain" }
func (h *InboxDrainHandler) Handles() []EventType { return []EventType{EventSessionStart, EventPreCompact, EventStop} }
func (h *InboxDrainHandler) Priority() int        { return 30 }

// SetInboxStore wires in direct storage access, eliminating the subprocess round-trip.
func (h *InboxDrainHandler) SetInboxStore(store InboxStore) { h.store = store }

func (h *InboxDrainHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	if h.store != nil && event.Actor != "" {
		return h.handleInProcess(ctx, event, result)
	}
	// Fallback: subprocess (for when store isn't wired or actor is unknown).
	args := []string{"inbox", "drain", "--json"}
	if event.Type == EventSessionStart {
		args = append(args, "--reconcile")
	}
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), args...)
	if err != nil {
		return fmt.Errorf("inbox-drain: %w", err)
	}
	if stdout != "" {
		result.Inject = append(result.Inject, stdout)
	}
	return nil
}

func (h *InboxDrainHandler) handleInProcess(ctx context.Context, event *Event, result *Result) error {
	items, err := h.store.InboxDrain(ctx, event.Actor)
	if err != nil {
		return fmt.Errorf("inbox-drain: %w", err)
	}
	if len(items) == 0 {
		return nil
	}

	// Mark as delivered.
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	if markErr := h.store.InboxMarkDelivered(ctx, event.Actor, ids); markErr != nil {
		log.Printf("inbox-drain: failed to mark delivered: %v", markErr)
	}

	result.Inject = append(result.Inject, formatInboxItems(items))
	return nil
}

// PostToolUseInboxHandler drains urgent (P0/P1) inbox items after each tool use.
// Priority 30 (same level as InboxDrainHandler — runs on PostToolUse only).
//
// This enables near-real-time message injection: critical alerts and high-priority
// notifications reach the agent between tool calls, not just at session boundaries.
// The handler is intentionally lightweight — it only checks for urgent items to
// minimize latency impact on the agent's tool execution loop.
//
// When store is set (via SetInboxStore), drains in-process — no subprocess.
// Falls back to subprocess when store is nil (backward compat). (bd-f33nh)
type PostToolUseInboxHandler struct {
	store InboxStore
}

func (h *PostToolUseInboxHandler) ID() string          { return "post-tool-inbox" }
func (h *PostToolUseInboxHandler) Handles() []EventType { return []EventType{EventPostToolUse} }
func (h *PostToolUseInboxHandler) Priority() int        { return 30 }

// SetInboxStore wires in direct storage access, eliminating the subprocess round-trip.
func (h *PostToolUseInboxHandler) SetInboxStore(store InboxStore) { h.store = store }

func (h *PostToolUseInboxHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	if h.store != nil && event.Actor != "" {
		return h.handleInProcess(ctx, event, result)
	}
	// Fallback: subprocess.
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), "inbox", "drain", "--urgent", "--json")
	if err != nil {
		return fmt.Errorf("post-tool-inbox: %w", err)
	}
	if stdout != "" {
		result.Inject = append(result.Inject, stdout)
	}
	return nil
}

func (h *PostToolUseInboxHandler) handleInProcess(ctx context.Context, event *Event, result *Result) error {
	items, err := h.store.InboxDrain(ctx, event.Actor, 1) // maxPriority=1 → P0+P1 only
	if err != nil {
		return fmt.Errorf("post-tool-inbox: %w", err)
	}
	if len(items) == 0 {
		return nil
	}

	// Mark as delivered.
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	if markErr := h.store.InboxMarkDelivered(ctx, event.Actor, ids); markErr != nil {
		log.Printf("post-tool-inbox: failed to mark delivered: %v", markErr)
	}

	result.Inject = append(result.Inject, formatInboxItems(items))
	return nil
}

// formatInboxItems formats drained inbox items for injection into the agent's
// context. Mirrors the formatting from cmd/bd/inbox.go runInboxDrain. (bd-f33nh)
func formatInboxItems(items []*types.InboxItem) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Inbox: %d new notification(s)\n\n", len(items)))
	for _, item := range items {
		sourceStr := ""
		if item.Source != "" {
			sourceStr = fmt.Sprintf(" (from %s)", item.Source)
		}
		sb.WriteString(fmt.Sprintf("- [%s]%s: %s\n", item.Type, sourceStr, item.Content))
	}
	return sb.String()
}

// runBDCommand executes a bd subcommand and captures stdout/stderr.
// The CWD parameter sets the working directory for the subprocess.
// Falls back to os.TempDir() if the CWD doesn't exist (e.g., remote daemon in K8s).
func runBDCommand(ctx context.Context, cwd string, args ...string) (string, string, error) {
	return runBDCommandWithEnv(ctx, cwd, nil, args...)
}

// SubprocessEnv holds environment overrides for bd subprocesses spawned by handlers.
// These override the daemon's own environment so the subprocess runs in the
// context of the calling agent, not the daemon itself. (bd-awrs6, bd-7j9ao)
type SubprocessEnv struct {
	Actor             string // BD_ACTOR — the beads agent name (e.g., "bright-hog")
	CallerSessionTag  string // TERM_SESSION_ID — scopes decisions to the caller's terminal
	ClaudeSessionID   string // CLAUDE_SESSION_ID — Claude Code session ID for gate checks
}

// envFromEvent builds a SubprocessEnv from an Event's fields.
func envFromEvent(event *Event) *SubprocessEnv {
	env := &SubprocessEnv{
		Actor:           event.Actor,
		ClaudeSessionID: event.SessionID,
	}
	// Extract caller_session_tag from Raw if present.
	if len(event.Raw) > 0 {
		var raw map[string]interface{}
		if err := json.Unmarshal(event.Raw, &raw); err == nil {
			if tag, ok := raw["caller_session_tag"].(string); ok && tag != "" {
				env.CallerSessionTag = tag
			}
		}
	}
	return env
}

// runBDCommandWithEnv runs a bd subprocess with optional environment overrides.
// When env is non-nil, BD_ACTOR and TERM_SESSION_ID are set so the subprocess
// runs in the context of the calling agent. (bd-awrs6, bd-7j9ao)
func runBDCommandWithEnv(ctx context.Context, cwd string, env *SubprocessEnv, args ...string) (string, string, error) {
	bdPath, err := findBDBinary()
	if err != nil {
		return "", "", err
	}

	cmd := exec.CommandContext(ctx, bdPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if cwd != "" {
		// Verify CWD exists; fall back to temp dir if not (remote daemon scenario)
		if info, statErr := os.Stat(cwd); statErr != nil || !info.IsDir() {
			cwd = os.TempDir()
		}
		cmd.Dir = cwd
	}

	// Pass through environment but ensure no daemon socket override
	// (subprocess should discover daemon via normal socket discovery).
	cmd.Env = os.Environ()

	// Apply environment overrides from the calling agent's context.
	if env != nil {
		if env.Actor != "" {
			cmd.Env = append(cmd.Env, "BD_ACTOR="+env.Actor)
		}
		if env.CallerSessionTag != "" {
			cmd.Env = append(cmd.Env, "TERM_SESSION_ID="+env.CallerSessionTag)
		}
		if env.ClaudeSessionID != "" {
			cmd.Env = append(cmd.Env, "CLAUDE_SESSION_ID="+env.ClaudeSessionID)
		}
	}

	err = cmd.Run()
	return strings.TrimRight(stdout.String(), "\n"), strings.TrimRight(stderr.String(), "\n"), err
}

// findBDBinary locates the bd binary.
func findBDBinary() (string, error) {
	path, err := exec.LookPath("bd")
	if err != nil {
		return "", fmt.Errorf("bd binary not found in PATH: %w", err)
	}
	return path, nil
}

// DefaultHandlers returns the standard set of event bus handlers for daemon registration.
func DefaultHandlers() []Handler {
	handlers := []Handler{
		&HealthCheckHandler{},      // 5 — early stuck detection for agents (bd-4mpv3)
		&PrimeHandler{},            // 10
		&GateHandler{},             // 20
		&AdviceHookHandler{},       // 25 — run advice hook commands at lifecycle points (beads-it2j)
		&InboxDrainHandler{},       // 30 — sole delivery path (Phase 5)
		&PostToolUseInboxHandler{}, // 30 — urgent inbox drain between tool calls (bd-qufo5)
		&BeadNudgeHandler{},        // 40 — nudge unassigned agents to claim/create beads (bd-0ttt3)
	}
	handlers = append(handlers, DefaultOjHandlers()...)
	handlers = append(handlers, DefaultMailHandlers()...)
	return handlers
}
