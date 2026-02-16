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
)

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

// StopDecisionHandler creates a decision point when Claude tries to stop,
// blocking until the human responds. Priority 15 (after prime, before gate).
type StopDecisionHandler struct{}

func (h *StopDecisionHandler) ID() string          { return "stop-decision" }
func (h *StopDecisionHandler) Handles() []EventType { return []EventType{EventStop} }
func (h *StopDecisionHandler) Priority() int        { return 15 }

func (h *StopDecisionHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	// If StopLoopDetector (priority 14) detected a loop, skip blocking entirely.
	// The detector sets stop_loop_break=true in event.Raw to signal this.
	if stopLoopBreakSet(event) {
		log.Printf("stop-decision: skipping — stop loop break active")
		return nil
	}

	// Pass stop_hook_active flag through to the stop-check command so it can
	// decide how to handle re-entry (e.g., skip polling, check for agent decision).
	// We no longer skip the handler entirely on re-entry because the agent may
	// have created a decision that needs to be awaited.
	args := []string{"decision", "stop-check", "--json"}
	if len(event.Raw) > 0 {
		var raw map[string]interface{}
		if err := json.Unmarshal(event.Raw, &raw); err == nil {
			if active, ok := raw["stop_hook_active"]; ok {
				if boolVal, isBool := active.(bool); isBool && boolVal {
					args = append(args, "--reentry")
				}
			}
		}
	}

	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), args...)
	if err != nil {
		// Exit code 1 means block (human said "continue").
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			var resp stopCheckResponse
			if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr == nil {
				if resp.Decision == "block" {
					result.Block = true
					result.Reason = resp.Reason
					return nil
				}
			}
			// Couldn't parse — treat as block with raw reason.
			result.Block = true
			result.Reason = strings.TrimSpace(stdout)
			return nil
		}
		// Other errors — log and allow stop (fail-open).
		return fmt.Errorf("stop-decision: %w", err)
	}

	// Exit 0 = allow stop.
	return nil
}

// stopCheckResponse mirrors the JSON output from `bd decision stop-check`.
type stopCheckResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

// stopLoopBreakSet checks if StopLoopDetector set the loop-break flag.
func stopLoopBreakSet(event *Event) bool {
	if len(event.Raw) == 0 {
		return false
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(event.Raw, &raw); err != nil {
		return false
	}
	v, ok := raw["stop_loop_break"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// GateHandler evaluates session gates on Stop and PreToolUse hooks.
// Priority 20 (runs after context injection).
type GateHandler struct{}

func (h *GateHandler) ID() string              { return "gate" }
func (h *GateHandler) Handles() []EventType     { return []EventType{EventStop, EventPreToolUse} }
func (h *GateHandler) Priority() int            { return 20 }

func (h *GateHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	hookName := string(event.Type)
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), "gate", "session-check", "--hook", hookName, "--json")
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
type InboxDrainHandler struct{}

func (h *InboxDrainHandler) ID() string          { return "inbox-drain" }
func (h *InboxDrainHandler) Handles() []EventType { return []EventType{EventSessionStart, EventPreCompact, EventStop} }
func (h *InboxDrainHandler) Priority() int        { return 30 }

func (h *InboxDrainHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	args := []string{"inbox", "drain", "--json"}
	// On SessionStart, add --reconcile to also check the DB for missed items
	if event.Type == EventSessionStart {
		args = append(args, "--reconcile")
	}

	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), args...)
	if err != nil {
		// Inbox drain is informational — log and continue (fail-open).
		return fmt.Errorf("inbox-drain: %w", err)
	}
	if stdout != "" {
		result.Inject = append(result.Inject, stdout)
	}
	return nil
}

// PostToolUseInboxHandler drains urgent (P0/P1) inbox items after each tool use.
// Priority 30 (same level as InboxDrainHandler — runs on PostToolUse only).
//
// This enables near-real-time message injection: critical alerts and high-priority
// notifications reach the agent between tool calls, not just at session boundaries.
// The handler is intentionally lightweight — it only checks for urgent items to
// minimize latency impact on the agent's tool execution loop.
type PostToolUseInboxHandler struct{}

func (h *PostToolUseInboxHandler) ID() string          { return "post-tool-inbox" }
func (h *PostToolUseInboxHandler) Handles() []EventType { return []EventType{EventPostToolUse} }
func (h *PostToolUseInboxHandler) Priority() int        { return 30 }

func (h *PostToolUseInboxHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	stdout, _, err := runBDCommandWithEnv(ctx, event.CWD, envFromEvent(event), "inbox", "drain", "--urgent", "--json")
	if err != nil {
		// Inbox drain is informational — log and continue (fail-open).
		return fmt.Errorf("post-tool-inbox: %w", err)
	}
	if stdout != "" {
		result.Inject = append(result.Inject, stdout)
	}
	return nil
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
}

// envFromEvent builds a SubprocessEnv from an Event's fields.
func envFromEvent(event *Event) *SubprocessEnv {
	env := &SubprocessEnv{
		Actor: event.Actor,
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
		&StopLoopDetector{},        // 14 — must run before StopDecisionHandler to break loops
		&StopDecisionHandler{},     // 15
		&GateHandler{},             // 20
		&InboxDrainHandler{},       // 30 — sole delivery path (Phase 5)
		&PostToolUseInboxHandler{}, // 30 — urgent inbox drain between tool calls (bd-qufo5)
	}
	handlers = append(handlers, DefaultOjHandlers()...)
	handlers = append(handlers, DefaultMailHandlers()...)
	return handlers
}
