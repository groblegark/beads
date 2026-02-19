package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/eventbus"
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
	// (within 60s), skip this event entirely. This prevents the re-fire loop
	// where the Stop hook fires faster than the agent can process the
	// checkpoint prompt and call bd decision create.
	// IMPORTANT: we return early (exit 0, no block) rather than marking the
	// gate as satisfied — marking creates permanent state that outlives the
	// debounce window and permanently disables checkpoints. (bd-ss2lc, bd-s8zi7)
	if resolvedType == "Stop" {
		if debounceStopHook(eventMeta.SessionID) {
			return outputEmitResult(&rpc.BusEmitResult{Block: false, Reason: "debounced"})
		}
	}

	// Stop-loop detection: track stop hook activations in a sliding window.
	// If the stop hook fires too many times within the window (even across
	// decision create/respond cycles), skip this event to break the loop.
	// This catches the bd-done wake loop where broadcast mail keeps waking
	// the agent, triggering stop → checkpoint → bd yield → wake → stop.
	// IMPORTANT: we return early rather than marking the gate — the loop
	// will naturally resolve as timestamps age out of the window. (hq-86ygth, bd-s8zi7)
	if resolvedType == "Stop" {
		if detectStopLoop(eventMeta.SessionID) {
			return outputEmitResult(&rpc.BusEmitResult{Block: false, Reason: "stop-loop detected"})
		}
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

	// Dispatch via daemon RPC, or fall back to local dispatch if no daemon.
	// Local dispatch creates a bus with DefaultHandlers() so hooks like
	// SessionStart and Stop still work without a daemon. (beads-feat-event_bus)
	if daemonClient == nil {
		return dispatchLocal(resolvedType, eventData, eventMeta.SessionID, hookType)
	}

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

// defaultStopRetryThreshold is the number of consecutive Stop hook blocks
// before the circuit breaker auto-satisfies the decision gate.
const defaultStopRetryThreshold = 3

// debounceStopHook checks if the Stop hook fired and blocked recently (within
// 60s). Returns true if the caller should skip this stop event entirely (exit 0,
// no block, no gate modification). This prevents the re-fire loop where the
// Stop hook fires faster than the agent can process the checkpoint prompt.
// (bd-ss2lc, bd-s8zi7)
//
// Also implements circuit breaker (bd-ywda): after N consecutive blocks without
// the agent creating a decision, returns true to skip. Unlike the previous
// implementation, this does NOT mark the gate as permanently satisfied — it only
// skips the current event. The debounce marker is transient (60s TTL) and the
// circuit breaker resets, so future stop hooks will fire normally once the
// debounce window expires.
func debounceStopHook(sessionID string) bool {
	markerPath := stopDebounceMarkerPath(sessionID)
	data, err := os.ReadFile(markerPath) //nolint:gosec // G304: path is constructed from controlled .runtime/ directory
	if err != nil {
		return false
	}

	// Parse "timestamp:retryCount" format.
	ts, retries := parseDebounceMarker(string(data))
	if ts == 0 {
		return false
	}

	elapsed := time.Since(time.Unix(ts, 0))
	if elapsed > 60*time.Second {
		_ = os.Remove(markerPath)
		return false
	}

	// Circuit breaker: after N consecutive blocks, skip this event to break
	// the loop. Do NOT mark the gate — the marker file will expire after 60s
	// and future stop hooks will fire normally. (bd-ywda, bd-s8zi7)
	threshold := stopRetryThreshold()
	if retries >= threshold {
		_ = os.Remove(markerPath) // Reset counter after circuit break
		return true
	}

	// Debounce marker exists and is recent (<60s) but under the circuit
	// breaker threshold. Skip this event so the agent has time to process
	// the checkpoint prompt from the previous block. (bd-iki79, bd-s8zi7)
	return true
}

// writeStopDebounceMarker records that the Stop hook blocked at the current time.
// Increments the retry counter for circuit breaker tracking. (bd-ywda)
func writeStopDebounceMarker(sessionID string) {
	markerPath := stopDebounceMarkerPath(sessionID)
	dir := filepath.Dir(markerPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	// Read existing marker to increment retry count.
	retries := 0
	if data, err := os.ReadFile(markerPath); err == nil { //nolint:gosec // G304: path from controlled .runtime/ directory
		_, retries = parseDebounceMarker(string(data))
	}

	// Write "timestamp:retryCount" format.
	content := fmt.Sprintf("%d:%d", time.Now().Unix(), retries+1)
	_ = os.WriteFile(markerPath, []byte(content), 0o600)
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

// parseDebounceMarker parses the debounce marker content.
// Format: "timestamp" (legacy) or "timestamp:retryCount".
// Returns (unix timestamp, retry count).
func parseDebounceMarker(data string) (int64, int) {
	data = strings.TrimSpace(data)
	if data == "" {
		return 0, 0
	}

	parts := strings.SplitN(data, ":", 2)
	ts, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0
	}

	retries := 0
	if len(parts) == 2 {
		retries, _ = strconv.Atoi(parts[1])
	}
	return ts, retries
}

// stopRetryThreshold returns the circuit breaker threshold from config or default.
// Configurable via BEADS_STOP_RETRY_THRESHOLD env var. (bd-ywda)
func stopRetryThreshold() int {
	if v := os.Getenv("BEADS_STOP_RETRY_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultStopRetryThreshold
}

// stopDebounceMarkerPath returns the path for the Stop hook debounce marker.
func stopDebounceMarkerPath(sessionID string) string {
	if sessionID == "" {
		sessionID = "_unknown"
	}
	return filepath.Join(getWorkDir(), ".runtime", "stop-debounce", sessionID)
}

// --------------------------------------------------------------------------
// Stop-loop detection: sliding-window counter that persists across decision
// create/respond cycles. Unlike the debounce marker (reset by bd decision
// create), the stop-loop log tracks ALL stop hook activations within a
// rolling window. When the count exceeds the threshold, the loop is broken
// by auto-satisfying the decision gate. (hq-86ygth)
// --------------------------------------------------------------------------

const (
	// defaultStopLoopWindow is the sliding window duration for loop detection.
	defaultStopLoopWindow = 30 * time.Minute
	// defaultStopLoopThreshold is the number of stop activations within the
	// window before the loop is broken.
	defaultStopLoopThreshold = 5
)

// detectStopLoop records a stop hook activation and returns true if the
// session is in a stop loop (activations within window >= threshold).
func detectStopLoop(sessionID string) bool {
	logPath := stopLoopLogPath(sessionID)
	dir := filepath.Dir(logPath)
	_ = os.MkdirAll(dir, 0o755)

	now := time.Now()
	cutoff := now.Add(-stopLoopWindow())

	// Read existing timestamps.
	var timestamps []int64
	if data, err := os.ReadFile(logPath); err == nil { //nolint:gosec // path from controlled .runtime/ directory
		timestamps = parseStopLoopLog(string(data))
	}

	// Prune entries outside the window.
	pruned := make([]int64, 0, len(timestamps)+1)
	for _, ts := range timestamps {
		if time.Unix(ts, 0).After(cutoff) {
			pruned = append(pruned, ts)
		}
	}

	// Append current activation.
	pruned = append(pruned, now.Unix())

	// Write back.
	writeStopLoopLog(logPath, pruned)

	// Check threshold.
	return len(pruned) >= stopLoopThreshold()
}

// stopLoopLogPath returns the path for the stop-loop activation log.
func stopLoopLogPath(sessionID string) string {
	if sessionID == "" {
		sessionID = "_unknown"
	}
	return filepath.Join(getWorkDir(), ".runtime", "stop-loop", sessionID)
}

// parseStopLoopLog parses newline-separated unix timestamps.
func parseStopLoopLog(data string) []int64 {
	var result []int64
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ts, err := strconv.ParseInt(line, 10, 64)
		if err == nil {
			result = append(result, ts)
		}
	}
	return result
}

// writeStopLoopLog writes timestamps as newline-separated unix timestamps.
func writeStopLoopLog(path string, timestamps []int64) {
	var sb strings.Builder
	for _, ts := range timestamps {
		fmt.Fprintf(&sb, "%d\n", ts)
	}
	_ = os.WriteFile(path, []byte(sb.String()), 0o600)
}

// stopLoopWindow returns the sliding window duration from config or default.
func stopLoopWindow() time.Duration {
	if v := os.Getenv("BEADS_STOP_LOOP_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultStopLoopWindow
}

// stopLoopThreshold returns the loop detection threshold from config or default.
func stopLoopThreshold() int {
	if v := os.Getenv("BEADS_STOP_LOOP_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultStopLoopThreshold
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

// dispatchLocal creates a local event bus with DefaultHandlers() and dispatches
// the event without requiring a daemon connection. This enables hooks like
// SessionStart and Stop to work when the daemon is unavailable.
// Handlers run as subprocesses (bd prime, bd gate session-check, etc.), which
// is the same mechanism the daemon uses. (beads-feat-event_bus)
func dispatchLocal(hookType string, eventData []byte, sessionID, _ string) error {
	bus := eventbus.New()
	for _, h := range eventbus.DefaultHandlers() {
		bus.Register(h)
	}

	// Build event from the raw JSON payload (same logic as server_bus.go).
	event := &eventbus.Event{
		Type:      eventbus.EventType(hookType),
		SessionID: sessionID,
		Raw:       eventData,
	}
	if len(eventData) > 0 {
		_ = json.Unmarshal(eventData, event)
		event.Type = eventbus.EventType(hookType)
	}

	// Set Actor from BD_ACTOR env var (normally set by daemon from authenticated request).
	if actor := os.Getenv("BD_ACTOR"); actor != "" {
		event.Actor = actor
	}

	// Use a generous timeout — local dispatch doesn't have the daemon's
	// request timeout constraint. For Stop hooks, allow up to 1 hour.
	timeout := 5 * time.Minute
	if hookType == "Stop" {
		timeout = time.Hour
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := bus.Dispatch(ctx, event)
	if err != nil {
		return fmt.Errorf("bus emit (local): dispatch error: %w", err)
	}

	emitResult := &rpc.BusEmitResult{
		Block:    result.Block,
		Reason:   result.Reason,
		Inject:   result.Inject,
		Warnings: result.Warnings,
	}

	// Write debounce marker after a Stop hook blocks. (bd-ss2lc)
	if hookType == "Stop" && emitResult.Block {
		writeStopDebounceMarker(sessionID)
	}

	return outputEmitResult(emitResult)
}

func init() {
	busEmitCmd.Flags().String("hook", "", "Hook event type (e.g., Stop, PreToolUse, SessionStart)")
	busEmitCmd.Flags().String("event", "", "Non-hook event type (e.g., DecisionCreated, DecisionResponded)")
	busEmitCmd.Flags().String("payload", "", "JSON payload for --event (alternative to stdin)")
	busCmd.AddCommand(busEmitCmd)
}
