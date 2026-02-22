package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
)

// ---------------------------------------------------------------------------
// outputEmitResult tests
// ---------------------------------------------------------------------------

func TestOutputEmitResult_InjectOnly(t *testing.T) {
	// Capture stdout.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	result := &rpc.BusEmitResult{
		Inject: []string{"line1", "line2"},
	}
	if err := outputEmitResult(result); err != nil {
		os.Stdout = oldStdout
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if got != "line1\nline2\n" {
		t.Errorf("stdout = %q, want %q", got, "line1\nline2\n")
	}
}

func TestOutputEmitResult_WarningsOnly(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	result := &rpc.BusEmitResult{
		Warnings: []string{"warn1", "warn2"},
	}
	if err := outputEmitResult(result); err != nil {
		os.Stdout = oldStdout
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "<system-reminder>warn1</system-reminder>\n<system-reminder>warn2</system-reminder>\n"
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestOutputEmitResult_InjectAndWarnings(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	result := &rpc.BusEmitResult{
		Inject:   []string{"injected"},
		Warnings: []string{"warning"},
	}
	if err := outputEmitResult(result); err != nil {
		os.Stdout = oldStdout
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	want := "injected\n<system-reminder>warning</system-reminder>\n"
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestOutputEmitResult_Empty(t *testing.T) {
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	result := &rpc.BusEmitResult{}
	if err := outputEmitResult(result); err != nil {
		os.Stdout = oldStdout
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if got != "" {
		t.Errorf("stdout = %q, want empty", got)
	}
}

func TestOutputEmitResult_BlockWritesStderr(t *testing.T) {
	// os.Exit(2) cannot be caught in-process. Run the block path in a subprocess.
	if os.Getenv("TEST_BLOCK_OUTPUT") == "1" {
		outputEmitResult(&rpc.BusEmitResult{Block: true, Reason: "gate failed"})
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestOutputEmitResult_BlockWritesStderr$")
	cmd.Env = append(os.Environ(), "TEST_BLOCK_OUTPUT=1")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Expect a non-zero exit.
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", exitErr.ExitCode())
	}

	// Verify stderr contains the expected JSON.
	var parsed map[string]string
	// stderr may contain other output; find the JSON line.
	for _, line := range strings.Split(stderr.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") {
			if json.Unmarshal([]byte(line), &parsed) == nil {
				break
			}
		}
	}
	if parsed["decision"] != "block" {
		t.Errorf("decision = %q, want %q", parsed["decision"], "block")
	}
	if parsed["reason"] != "gate failed" {
		t.Errorf("reason = %q, want %q", parsed["reason"], "gate failed")
	}
}

// ---------------------------------------------------------------------------
// runBusEmit tests
// ---------------------------------------------------------------------------

// newBusEmitCmd creates a minimal cobra command wired to runBusEmit, with the
// --hook flag registered. This avoids depending on the full command tree.
func newBusEmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "emit",
		RunE: runBusEmit,
	}
	cmd.Flags().String("hook", "", "Hook event type")
	return cmd
}

func TestRunBusEmit_MissingHookFlag(t *testing.T) {
	// Save and restore the global daemonClient.
	oldClient := daemonClient
	daemonClient = nil
	defer func() { daemonClient = oldClient }()

	cmd := newBusEmitCmd()
	// Do not set --hook; the flag defaults to "".
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing --hook flag, got nil")
	}
	if !strings.Contains(err.Error(), "either --hook or --event is required") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "either --hook or --event is required")
	}
}

// ---------------------------------------------------------------------------
// dispatchLocal tests (local fallback when daemon is unavailable)
// ---------------------------------------------------------------------------

func TestDispatchLocal_Passthrough(t *testing.T) {
	// When dispatchLocal runs without any handlers matching an unknown event type,
	// it should return nil (no block, no error) and output nothing.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	// Use a non-hook event type that no default handler handles.
	err = dispatchLocal("UnknownTestEvent", []byte(`{}`), "test-session", "")
	if err != nil {
		os.Stdout = oldStdout
		t.Fatalf("unexpected error: %v", err)
	}

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "" {
		t.Errorf("expected no output, got %q", buf.String())
	}
}

func newFullBusEmitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "emit",
		RunE: runBusEmit,
	}
	cmd.Flags().String("hook", "", "Hook event type")
	cmd.Flags().String("event", "", "Non-hook event type")
	cmd.Flags().String("payload", "", "JSON payload for --event")
	return cmd
}

func TestRunBusEmit_LocalFallback_NoDaemon(t *testing.T) {
	// Verify that runBusEmit falls back to local dispatch when daemonClient is nil.
	oldClient := daemonClient
	daemonClient = nil
	defer func() { daemonClient = oldClient }()

	cmd := newFullBusEmitCmd()
	cmd.SetArgs([]string{"--event=TestLocalFallback", "--payload={}"})

	// Should not error — local dispatch handles the event gracefully.
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("expected no error for local fallback, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stop-loop detection tests (hq-86ygth)
// ---------------------------------------------------------------------------

func TestParseStopLoopLog_Empty(t *testing.T) {
	result := parseStopLoopLog("")
	if len(result) != 0 {
		t.Errorf("expected empty, got %v", result)
	}
}

func TestParseStopLoopLog_ValidTimestamps(t *testing.T) {
	input := "1708300000\n1708300060\n1708300120\n"
	result := parseStopLoopLog(input)
	if len(result) != 3 {
		t.Fatalf("expected 3 timestamps, got %d", len(result))
	}
	if result[0] != 1708300000 || result[1] != 1708300060 || result[2] != 1708300120 {
		t.Errorf("unexpected timestamps: %v", result)
	}
}

func TestParseStopLoopLog_InvalidLines(t *testing.T) {
	input := "1708300000\nbadline\n1708300060\n"
	result := parseStopLoopLog(input)
	if len(result) != 2 {
		t.Fatalf("expected 2 valid timestamps, got %d", len(result))
	}
}

// chdirTemp changes to a temp dir and returns a cleanup function.
func chdirTemp(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })
}

func TestDetectStopLoop_BelowThreshold(t *testing.T) {
	chdirTemp(t)
	t.Setenv("BEADS_STOP_LOOP_THRESHOLD", "5")
	t.Setenv("BEADS_STOP_LOOP_WINDOW", "30m")

	sessionID := "test-session-below"

	// First 4 activations should not trigger loop detection (threshold=5).
	for i := 0; i < 4; i++ {
		if detectStopLoop(sessionID) {
			t.Errorf("activation %d should not trigger loop detection", i+1)
		}
	}
}

func TestDetectStopLoop_AtThreshold(t *testing.T) {
	chdirTemp(t)
	t.Setenv("BEADS_STOP_LOOP_THRESHOLD", "3")
	t.Setenv("BEADS_STOP_LOOP_WINDOW", "30m")

	sessionID := "test-session-threshold"

	// First 2 should not trigger.
	for i := 0; i < 2; i++ {
		if detectStopLoop(sessionID) {
			t.Errorf("activation %d should not trigger loop detection", i+1)
		}
	}

	// 3rd activation should trigger (threshold=3).
	if !detectStopLoop(sessionID) {
		t.Error("activation 3 should trigger loop detection at threshold=3")
	}
}

func TestDetectStopLoop_WindowExpiry(t *testing.T) {
	chdirTemp(t)
	t.Setenv("BEADS_STOP_LOOP_THRESHOLD", "3")
	t.Setenv("BEADS_STOP_LOOP_WINDOW", "30m")

	sessionID := "test-session-expiry"

	// Write old timestamps (>30 min ago) directly to the log file.
	wd, _ := os.Getwd()
	logPath := filepath.Join(wd, ".runtime", "stop-loop", sessionID)
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	oldTime := time.Now().Add(-45 * time.Minute).Unix()
	content := fmt.Sprintf("%d\n%d\n", oldTime, oldTime+60)
	_ = os.WriteFile(logPath, []byte(content), 0o600)

	// New activation should start fresh (old entries pruned).
	if detectStopLoop(sessionID) {
		t.Error("should not trigger — old entries outside window should be pruned")
	}
}

func TestDetectStopLoop_SessionIsolation(t *testing.T) {
	chdirTemp(t)
	t.Setenv("BEADS_STOP_LOOP_THRESHOLD", "2")
	t.Setenv("BEADS_STOP_LOOP_WINDOW", "30m")

	// Fill session A to just below threshold.
	detectStopLoop("session-a")

	// Session B should not be affected by session A.
	if detectStopLoop("session-b") {
		t.Error("session-b should not be affected by session-a activations")
	}
}

func TestDetectStopLoop_PersistsAcrossDecisionCreate(t *testing.T) {
	// Verify that the stop-loop log is NOT cleared by clearStopDebounceMarker.
	// This is the key behavior: debounce markers reset per decision cycle,
	// but the stop-loop log persists across the full window.
	chdirTemp(t)
	t.Setenv("BEADS_STOP_LOOP_THRESHOLD", "3")
	t.Setenv("BEADS_STOP_LOOP_WINDOW", "30m")

	sessionID := "test-persist"

	// Simulate 2 activations.
	detectStopLoop(sessionID)
	detectStopLoop(sessionID)

	// Simulate what bd decision create does — clear the debounce marker.
	// The stop-loop log should NOT be affected.
	debounceMarker := stopDebounceMarkerPath(sessionID)
	_ = os.MkdirAll(filepath.Dir(debounceMarker), 0o755)
	_ = os.WriteFile(debounceMarker, []byte("123:1"), 0o600)
	_ = os.Remove(debounceMarker)

	// 3rd activation should still trigger (log wasn't cleared).
	if !detectStopLoop(sessionID) {
		t.Error("stop-loop log should persist across debounce marker clears")
	}
}

func TestClearStopLoopLog_ResetsCounter(t *testing.T) {
	// Verify that clearStopLoopLog removes the activation log,
	// allowing future stop hooks to fire normally. This is the fix for
	// bd-iki79: productive sessions should not be penalized by the
	// circuit breaker when the human is engaging with checkpoints.
	chdirTemp(t)
	t.Setenv("BEADS_STOP_LOOP_THRESHOLD", "3")
	t.Setenv("BEADS_STOP_LOOP_WINDOW", "30m")

	sessionID := "test-clear-loop"

	// Simulate 2 activations (just below threshold).
	detectStopLoop(sessionID)
	detectStopLoop(sessionID)

	// Simulate a successful decision respond cycle — clear the log.
	clearStopLoopLog(sessionID)

	// After clearing, the counter should be reset. 2 more activations
	// should NOT trigger (we're starting fresh, need 3 to hit threshold).
	if detectStopLoop(sessionID) {
		t.Error("1st activation after clear should not trigger loop detection")
	}
	if detectStopLoop(sessionID) {
		t.Error("2nd activation after clear should not trigger loop detection")
	}
	// 3rd should trigger again (fresh count of 3).
	if !detectStopLoop(sessionID) {
		t.Error("3rd activation after clear should trigger loop detection")
	}
}

func TestClearStopLoopLog_EmptySessionID(t *testing.T) {
	// clearStopLoopLog with empty session ID should be a no-op (not panic).
	clearStopLoopLog("")
}

func TestStopLoopThreshold_Default(t *testing.T) {
	t.Setenv("BEADS_STOP_LOOP_THRESHOLD", "")
	if got := stopLoopThreshold(); got != defaultStopLoopThreshold {
		t.Errorf("default threshold = %d, want %d", got, defaultStopLoopThreshold)
	}
}

func TestStopLoopThreshold_Custom(t *testing.T) {
	t.Setenv("BEADS_STOP_LOOP_THRESHOLD", "10")
	if got := stopLoopThreshold(); got != 10 {
		t.Errorf("custom threshold = %d, want 10", got)
	}
}

func TestStopLoopWindow_Default(t *testing.T) {
	t.Setenv("BEADS_STOP_LOOP_WINDOW", "")
	if got := stopLoopWindow(); got != defaultStopLoopWindow {
		t.Errorf("default window = %v, want %v", got, defaultStopLoopWindow)
	}
}

func TestStopLoopWindow_Custom(t *testing.T) {
	t.Setenv("BEADS_STOP_LOOP_WINDOW", "1h")
	if got := stopLoopWindow(); got != time.Hour {
		t.Errorf("custom window = %v, want 1h", got)
	}
}

// ---------------------------------------------------------------------------
// sanitizeJSONControlChars tests (bd-rcsec)
// ---------------------------------------------------------------------------

func TestSanitizeJSONControlChars_ValidJSON(t *testing.T) {
	// Already-valid JSON should pass through unmarshaled (the function
	// is only called when json.Unmarshal fails, but it shouldn't break
	// valid JSON).
	input := `{"key":"value","num":42}`
	got := string(sanitizeJSONControlChars([]byte(input)))
	// Should still be valid JSON.
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(got), &raw); err != nil {
		t.Errorf("sanitized output is not valid JSON: %v (got: %q)", err, got)
	}
}

func TestSanitizeJSONControlChars_NewlineInString(t *testing.T) {
	// Literal newline inside a JSON string value — this is the main bug.
	input := "{\"message\":\"line1\nline2\"}"
	got := string(sanitizeJSONControlChars([]byte(input)))
	want := `{"message":"line1\nline2"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Verify the output is now valid JSON.
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(got), &raw); err != nil {
		t.Errorf("sanitized output is not valid JSON: %v", err)
	}
	if raw["message"] != "line1\nline2" {
		t.Errorf("message = %q, want %q", raw["message"], "line1\nline2")
	}
}

func TestSanitizeJSONControlChars_TabInString(t *testing.T) {
	input := "{\"data\":\"col1\tcol2\"}"
	got := string(sanitizeJSONControlChars([]byte(input)))
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(got), &raw); err != nil {
		t.Errorf("sanitized output is not valid JSON: %v", err)
	}
	if raw["data"] != "col1\tcol2" {
		t.Errorf("data = %q, want %q", raw["data"], "col1\tcol2")
	}
}

func TestSanitizeJSONControlChars_CRLFInString(t *testing.T) {
	input := "{\"msg\":\"hello\r\nworld\"}"
	got := string(sanitizeJSONControlChars([]byte(input)))
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(got), &raw); err != nil {
		t.Errorf("sanitized output is not valid JSON: %v", err)
	}
	if raw["msg"] != "hello\r\nworld" {
		t.Errorf("msg = %q, want %q", raw["msg"], "hello\r\nworld")
	}
}

func TestSanitizeJSONControlChars_PreservesEscapedNewlines(t *testing.T) {
	// Already-escaped \n should not be double-escaped.
	input := `{"msg":"already\\nescaped"}`
	got := string(sanitizeJSONControlChars([]byte(input)))
	if got != input {
		t.Errorf("got %q, want %q (should not modify already-escaped sequences)", got, input)
	}
}

func TestSanitizeJSONControlChars_NewlinesOutsideStrings(t *testing.T) {
	// Newlines between JSON tokens (pretty-printed JSON) should be preserved.
	input := "{\n  \"key\": \"value\"\n}"
	got := string(sanitizeJSONControlChars([]byte(input)))
	// The newlines outside strings should be kept as-is.
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(got), &raw); err != nil {
		t.Errorf("sanitized output is not valid JSON: %v", err)
	}
	if raw["key"] != "value" {
		t.Errorf("key = %q, want %q", raw["key"], "value")
	}
}

func TestSanitizeJSONControlChars_MultipleNewlinesInString(t *testing.T) {
	// Multiple newlines in a single string value.
	input := "{\"text\":\"a\nb\nc\nd\"}"
	got := string(sanitizeJSONControlChars([]byte(input)))
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(got), &raw); err != nil {
		t.Errorf("sanitized output is not valid JSON: %v", err)
	}
	if raw["text"] != "a\nb\nc\nd" {
		t.Errorf("text = %q, want %q", raw["text"], "a\nb\nc\nd")
	}
}

func TestSanitizeJSONControlChars_EmptyInput(t *testing.T) {
	got := sanitizeJSONControlChars(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}
	got = sanitizeJSONControlChars([]byte{})
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestSanitizeJSONControlChars_EscapedQuote(t *testing.T) {
	// Escaped quotes inside strings should not confuse the string tracker.
	input := `{"msg":"he said \"hello\nworld\""}`
	got := string(sanitizeJSONControlChars([]byte(input)))
	// The literal \n between hello and world should be escaped.
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(got), &raw); err != nil {
		t.Errorf("sanitized output is not valid JSON: %v", err)
	}
}
