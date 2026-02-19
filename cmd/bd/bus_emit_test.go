package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

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
