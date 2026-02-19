package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/gate"
	"github.com/steveyegge/beads/internal/rpc"
)

var busEmitCmd = &cobra.Command{
	Use:   "emit",
	Short: "Dispatch an event through the event bus",
	Long: `Dispatch an event through the event bus.

For Claude Code hook events, reads the hook event JSON from stdin:
  bd bus emit --hook=Stop

For non-hook events (e.g. decision events), use --event and --payload:
  bd bus emit --event=DecisionCreated --payload='{"decision_id":"x",...}'

Events are dispatched via the bd daemon RPC.

Exit codes:
  0 - Event processed, no blocking
  2 - Event blocked by a handler (gate check failed)

Examples:
  # Claude Code hook (reads stdin):
  bd bus emit --hook=Stop
  bd bus emit --hook=PreToolUse

  # Decision event (inline payload):
  bd bus emit --event=DecisionCreated --payload='{"decision_id":"od-xyz","question":"Which approach?"}'`,
	RunE: runBusEmit,
}

func runBusEmit(cmd *cobra.Command, args []string) error {
	hookType, _ := cmd.Flags().GetString("hook")
	eventType, _ := cmd.Flags().GetString("event")
	payloadFlag, _ := cmd.Flags().GetString("payload")

	// Determine the event type from either --hook or --event.
	var resolvedType string
	var eventData []byte

	switch {
	case hookType != "" && eventType != "":
		return fmt.Errorf("--hook and --event are mutually exclusive")
	case hookType != "":
		resolvedType = hookType
		// Read stdin (Claude Code sends hook event JSON on stdin).
		var err error
		eventData, err = io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
	case eventType != "":
		resolvedType = eventType
		if payloadFlag != "" {
			eventData = []byte(payloadFlag)
		}
	default:
		return fmt.Errorf("either --hook or --event is required")
	}

	// Extract session_id from the event JSON if present.
	// Fall back to TERM_SESSION_ID so handlers can track sessions
	// even when Claude Code doesn't send session_id in the hook JSON.
	var eventMeta struct {
		SessionID string `json:"session_id"`
	}
	if len(eventData) > 0 {
		_ = json.Unmarshal(eventData, &eventMeta)
	}
	if eventMeta.SessionID == "" {
		eventMeta.SessionID = os.Getenv("TERM_SESSION_ID")
	}

	// Write a session alias so direct CLI commands (like bd decision create)
	// can discover the Claude session_id from TERM_SESSION_ID. Without this,
	// gate markers written by bd decision create use an empty session ID and
	// the Stop hook gate never sees them → infinite re-fire loop. (bd-02qeb)
	if termSID := os.Getenv("TERM_SESSION_ID"); termSID != "" && eventMeta.SessionID != "" && eventMeta.SessionID != termSID {
		aliasDir := filepath.Join(".", ".runtime", "session-alias")
		if err := os.MkdirAll(aliasDir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(aliasDir, termSID), []byte(eventMeta.SessionID), 0o600)
		}
		// Write reverse mapping (CLAUDE_SESSION_ID → TERM_SESSION_ID) so
		// cross-session gate checks can be scoped to the same terminal/agent,
		// preventing one agent's decision marker from satisfying all agents. (bd-gh0ik)
		_ = gate.WriteSessionTerminal(".", eventMeta.SessionID, termSID)
	}

	// Inject caller's session tag into the event JSON so handlers can
	// scope decisions to this terminal session.
	if sessionTag := os.Getenv("TERM_SESSION_ID"); sessionTag != "" {
		var raw map[string]interface{}
		if len(eventData) > 0 {
			_ = json.Unmarshal(eventData, &raw)
		}
		if raw == nil {
			raw = map[string]interface{}{}
		}
		if _, exists := raw["caller_session_tag"]; !exists {
			raw["caller_session_tag"] = sessionTag
			eventData, _ = json.Marshal(raw)
		}
	}

	// Stop hook debounce: if the Stop hook already fired and blocked recently
	// (within 60s), mark the decision gate as satisfied so it won't re-fire.
	// This prevents the re-fire loop where the Stop hook fires faster than the
	// agent can process the checkpoint prompt and call bd decision create.
	// The marker is written after a successful block (see below). (bd-ss2lc)
	if resolvedType == "Stop" {
		debounceStopHook(eventMeta.SessionID)
	}

	// Local gate pre-check: gate markers live on the local filesystem but the
	// daemon may be remote (BD_DAEMON_HOST). Run the gate check locally first
	// and inject the result into the event JSON so the remote GateHandler can
	// skip its (doomed-to-fail) subprocess check. (bd-tpkw9)
	if hookType != "" {
		localGateResult := runLocalGateCheck(resolvedType, eventMeta.SessionID)
		if localGateResult != nil {
			var raw map[string]interface{}
			if len(eventData) > 0 {
				_ = json.Unmarshal(eventData, &raw)
			}
			if raw == nil {
				raw = map[string]interface{}{}
			}
			raw["gate_pre_checked"] = true
			raw["gate_pre_check_result"] = localGateResult
			eventData, _ = json.Marshal(raw)
		}
	}

	// Dispatch via daemon RPC
	requireDaemon("bus emit")

	// For Stop hooks, extend the request timeout so handlers
	// can complete without hitting daemon timeouts.
	if resolvedType == "Stop" {
		daemonClient.SetRequestTimeout(3600 * 1000) // 1 hour
	}

	emitArgs := &rpc.BusEmitArgs{
		HookType:  resolvedType,
		EventJSON: eventData,
		SessionID: eventMeta.SessionID,
	}

	resp, err := daemonClient.Execute(rpc.OpBusEmit, emitArgs)

	// Reset request timeout after the call.
	if resolvedType == "Stop" {
		daemonClient.SetRequestTimeout(0)
	}

	if err != nil {
		return fmt.Errorf("bus emit: daemon RPC failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("bus emit: daemon error: %s", resp.Error)
	}

	var emitResult rpc.BusEmitResult
	if err := json.Unmarshal(resp.Data, &emitResult); err != nil {
		return fmt.Errorf("parse emit result: %w", err)
	}

	// If the daemon blocked but the local gate pre-check said "allow",
	// override the block. Gate markers live locally — the remote daemon's
	// gate check can't see them, so trust the local result. (bd-tpkw9)
	if emitResult.Block && hookType != "" {
		localResult := runLocalGateCheck(resolvedType, eventMeta.SessionID)
		if localResult != nil {
			if decision, ok := localResult["decision"].(string); ok && decision == "allow" {
				emitResult.Block = false
				emitResult.Reason = ""
			}
		}
	}

	// Write debounce marker after a Stop hook blocks, so rapid re-fires
	// don't create infinite checkpoint loops. (bd-ss2lc)
	if resolvedType == "Stop" && emitResult.Block {
		writeStopDebounceMarker(eventMeta.SessionID)
	}

	return outputEmitResult(&emitResult)
}

// debounceStopHook checks if the Stop hook fired and blocked recently (within
// 60s) and marks the decision gate as satisfied to prevent re-fire loops.
// This handles the race where the Stop hook fires faster than the agent can
// process the checkpoint prompt and call bd decision create. (bd-ss2lc)
func debounceStopHook(sessionID string) {
	markerPath := stopDebounceMarkerPath(sessionID)
	data, err := os.ReadFile(markerPath) //nolint:gosec // G304: path is constructed from controlled .runtime/ directory
	if err != nil {
		return
	}
	ts, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return
	}
	elapsed := time.Since(time.Unix(ts, 0))
	if elapsed > 60*time.Second {
		_ = os.Remove(markerPath)
		return
	}

	// Recent Stop hook block — mark the decision gate as satisfied
	// so the gate check won't re-fire the checkpoint prompt.
	if sessionID != "" {
		_ = gate.MarkGate(getWorkDir(), sessionID, "decision")
	}
}

// writeStopDebounceMarker records that the Stop hook blocked at the current time.
func writeStopDebounceMarker(sessionID string) {
	markerPath := stopDebounceMarkerPath(sessionID)
	dir := filepath.Dir(markerPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(markerPath, []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0o600)
}

// clearStopDebounceMarker removes the Stop hook debounce marker for the
// current session. Called when a new decision is created so the next
// checkpoint cycle can fire normally. (bd-ss2lc)
func clearStopDebounceMarker() {
	sid := getSessionID()
	if sid == "" {
		sid = os.Getenv("TERM_SESSION_ID")
	}
	if sid == "" {
		return
	}
	_ = os.Remove(stopDebounceMarkerPath(sid))
}

// stopDebounceMarkerPath returns the path for the Stop hook debounce marker.
func stopDebounceMarkerPath(sessionID string) string {
	if sessionID == "" {
		sessionID = "_unknown"
	}
	return filepath.Join(getWorkDir(), ".runtime", "stop-debounce", sessionID)
}

// runLocalGateCheck runs the gate check locally (where markers live on the
// local filesystem) and returns the result as a map suitable for JSON injection
// into the event. Returns nil if no local check was needed or possible.
// This bridges the local-markers / remote-daemon gap. (bd-tpkw9)
func runLocalGateCheck(hookType, sessionID string) map[string]interface{} {
	ht, err := gate.ParseHookType(hookType)
	if err != nil {
		return nil
	}

	workDir := getWorkDir()
	termSID := os.Getenv("TERM_SESSION_ID")
	results, err := gate.CheckGatesForHook(workDir, sessionID, ht, sessionGateRegistry, termSID)
	if err != nil || len(results) == 0 {
		return nil
	}

	// Convert to JSON-friendly format matching gateCheckResponse.
	decision := "allow"
	var resultList []map[string]interface{}
	var warnings []string

	for _, r := range results {
		entry := map[string]interface{}{
			"gate_id":   r.GateID,
			"hook":      string(r.Hook),
			"satisfied": r.Satisfied,
			"mode":      string(r.Mode),
			"message":   r.Message,
			"hint":      r.Hint,
		}
		resultList = append(resultList, entry)

		if !r.Satisfied && r.Mode == gate.GateModeStrict {
			decision = "block"
		}
		if !r.Satisfied {
			warnings = append(warnings, fmt.Sprintf("%s: %s (hint: %s)", r.GateID, r.Message, r.Hint))
		}
	}

	result := map[string]interface{}{
		"decision": decision,
		"results":  resultList,
		"warnings": warnings,
	}

	// For Stop hook blocks, enrich the reason with the config bead prompt
	// (same logic as gateSessionCheckCmd). Without this, the daemon's
	// handlePreCheckedGate returns an empty reason and Claude Code shows
	// "No stderr output" instead of the decision prompt. (bd-gh0ik)
	if decision == "block" && hookType == "Stop" {
		reason := "decision: decision point offered before session end"
		for _, w := range warnings {
			reason = w // Use last strict-gate warning as default reason
		}
		if prompt := loadGatePromptFromConfig(); prompt != "" {
			reason = prompt
		}
		result["reason"] = reason
	}

	return result
}

// outputEmitResult writes the emit result according to the Claude Code hook
// protocol:
//   - result.Block → exit 2, stderr JSON with decision/reason
//   - result.Inject → stdout (content for Claude Code)
//   - result.Warnings → stdout as system-reminder tags
//   - otherwise → exit 0
func outputEmitResult(result *rpc.BusEmitResult) error {
	// Output injected content.
	for _, msg := range result.Inject {
		fmt.Println(msg)
	}

	// Output warnings as system-reminder tags.
	for _, w := range result.Warnings {
		fmt.Printf("<system-reminder>%s</system-reminder>\n", w)
	}

	if result.Block {
		blockJSON, _ := json.Marshal(map[string]string{
			"decision": "block",
			"reason":   result.Reason,
		})
		fmt.Fprintln(os.Stderr, string(blockJSON))
		os.Exit(2)
	}

	return nil
}

func init() {
	busEmitCmd.Flags().String("hook", "", "Hook event type (e.g., Stop, PreToolUse, SessionStart)")
	busEmitCmd.Flags().String("event", "", "Non-hook event type (e.g., DecisionCreated, DecisionResponded)")
	busEmitCmd.Flags().String("payload", "", "JSON payload for --event (alternative to stdin)")
	busCmd.AddCommand(busEmitCmd)
}
