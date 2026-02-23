//go:build integration
// +build integration

package main

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// setupJackTestEnv creates a temp dir with git repo, bd init, and auto-start daemon.
// Returns the temp dir path and environment variables.
func setupJackTestEnv(t *testing.T) (string, []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("jack CLI tests not supported on Windows")
	}

	tmpDir := createTempDirWithCleanup(t)

	// Set up git repo (required for daemon autostart)
	if err := runCommandInDir(tmpDir, "git", "init"); err != nil {
		t.Fatalf("git init failed: %v", err)
	}
	_ = runCommandInDir(tmpDir, "git", "config", "user.email", "test@example.com")
	_ = runCommandInDir(tmpDir, "git", "config", "user.name", "Test User")

	socketPath := filepath.Join(tmpDir, ".beads", "bd.sock")
	env := []string{
		"BEADS_TEST_MODE=1",
		"BEADS_AUTO_START_DAEMON=true",
		"BEADS_NO_DAEMON=0",
		"BD_SOCKET=" + socketPath,
	}

	// Init with dolt backend
	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "init", "--backend", "dolt", "--prefix", "test", "--quiet")
	if initErr != nil {
		t.Fatalf("bd init failed: %v\n%s", initErr, initOut)
	}

	// Configure jack type
	configOut, configErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "config", "set", "types.custom",
		"agent,role,rig,convoy,slot,queue,event,message,molecule,gate,merge-request,config,route,jack")
	if configErr != nil {
		t.Fatalf("config set types.custom failed: %v\n%s", configErr, configOut)
	}
	configOut, configErr = runBDExecAllowErrorWithEnv(t, tmpDir, env, "config", "set", "schema.type.jack",
		`{"required_fields":["description"],"required_labels":["jack:*"],"description":"Infrastructure modification jack"}`)
	if configErr != nil {
		t.Fatalf("config set schema.type.jack failed: %v\n%s", configErr, configOut)
	}

	// Wait for daemon to be ready
	time.Sleep(500 * time.Millisecond)

	return tmpDir, env
}

func runJackCmd(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	out, err := runBDExecAllowErrorWithEnv(t, dir, env, args...)
	if err != nil {
		t.Fatalf("bd %v failed: %v\n%s", args, err, out)
	}
	return out
}

func runJackCmdExpectError(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	out, err := runBDExecAllowErrorWithEnv(t, dir, env, args...)
	if err == nil {
		t.Fatalf("expected bd %v to fail, but it succeeded: %s", args, out)
	}
	return out
}

// extractJackID extracts the jack ID from JSON output.
func extractJackID(t *testing.T, jsonOut string) string {
	t.Helper()
	jsonStart := strings.Index(jsonOut, "{")
	if jsonStart < 0 {
		t.Fatalf("No JSON found in output: %s", jsonOut)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(jsonOut[jsonStart:]), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v\nOutput: %s", err, jsonOut)
	}
	id, ok := result["id"].(string)
	if !ok || id == "" {
		t.Fatalf("No id found in JSON output: %s", jsonOut)
	}
	return id
}

func TestJack_OnBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Create a jack with required flags
	out := runJackCmd(t, tmpDir, env, "jack", "on", "pod/test-app",
		"--reason=Debug logging for timeout investigation",
		"--revert-plan=Restore log_level to info",
		"--json")

	id := extractJackID(t, out)
	if !strings.HasPrefix(id, "test-") {
		t.Errorf("Expected ID to start with 'test-', got: %s", id)
	}

	// Parse full JSON to verify fields
	jsonStart := strings.Index(out, "{")
	var result map[string]interface{}
	json.Unmarshal([]byte(out[jsonStart:]), &result)

	if result["target"] != "pod/test-app" {
		t.Errorf("Expected target 'pod/test-app', got: %v", result["target"])
	}
	if result["status"] != "in_progress" {
		t.Errorf("Expected status 'in_progress', got: %v", result["status"])
	}
}

func TestJack_OnMissingRequired(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Missing --reason
	out := runJackCmdExpectError(t, tmpDir, env, "jack", "on", "pod/test-app",
		"--revert-plan=Restore config")
	if !strings.Contains(out, "--reason is required") {
		t.Errorf("Expected --reason required error, got: %s", out)
	}

	// Missing --revert-plan
	out = runJackCmdExpectError(t, tmpDir, env, "jack", "on", "pod/test-app",
		"--reason=Testing")
	if !strings.Contains(out, "--revert-plan is required") {
		t.Errorf("Expected --revert-plan required error, got: %s", out)
	}

	// Missing target
	out = runJackCmdExpectError(t, tmpDir, env, "jack", "on",
		"--reason=Testing",
		"--revert-plan=Undo")
	if !strings.Contains(out, "--target is required") {
		t.Errorf("Expected --target required error, got: %s", out)
	}
}

func TestJack_OnWithOptions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	out := runJackCmd(t, tmpDir, env, "jack", "on", "deployment/api",
		"--reason=Testing reconnect fix",
		"--revert-plan=kubectl rollout undo deployment/api",
		"--ttl=30m",
		"--priority=1",
		"--labels=jack:debug,team-backend",
		"--json")

	jsonStart := strings.Index(out, "{")
	var result map[string]interface{}
	json.Unmarshal([]byte(out[jsonStart:]), &result)

	if result["ttl"] != "30m0s" {
		t.Errorf("Expected TTL '30m0s', got: %v", result["ttl"])
	}
	if labels, ok := result["labels"].([]interface{}); ok {
		found := false
		for _, l := range labels {
			if l == "jack:debug" {
				found = true
			}
		}
		if !found {
			t.Errorf("Expected 'jack:debug' in labels, got: %v", labels)
		}
	}
}

func TestJack_OnAutoLabel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Create jack without explicit jack:* label — should auto-add jack:general
	out := runJackCmd(t, tmpDir, env, "jack", "on", "pod/test",
		"--reason=Test",
		"--revert-plan=Undo",
		"--json")

	jsonStart := strings.Index(out, "{")
	var result map[string]interface{}
	json.Unmarshal([]byte(out[jsonStart:]), &result)

	if labels, ok := result["labels"].([]interface{}); ok {
		found := false
		for _, l := range labels {
			if l == "jack:general" {
				found = true
			}
		}
		if !found {
			t.Errorf("Expected auto-added 'jack:general' label, got: %v", labels)
		}
	}
}

func TestJack_List(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Create two jacks
	runJackCmd(t, tmpDir, env, "jack", "on", "pod/app-1",
		"--reason=Debug 1", "--revert-plan=Undo 1", "--json")
	runJackCmd(t, tmpDir, env, "jack", "on", "pod/app-2",
		"--reason=Debug 2", "--revert-plan=Undo 2", "--json")

	// List active jacks
	out := runJackCmd(t, tmpDir, env, "jack", "list")
	if !strings.Contains(out, "pod/app-1") {
		t.Errorf("Expected pod/app-1 in list output, got: %s", out)
	}
	if !strings.Contains(out, "pod/app-2") {
		t.Errorf("Expected pod/app-2 in list output, got: %s", out)
	}

	// JSON list
	out = runJackCmd(t, tmpDir, env, "jack", "list", "--json")
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("Failed to parse JSON list: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("Expected 2 jacks in JSON list, got %d", len(items))
	}
}

func TestJack_ShowBasic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Create a jack
	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/show-test",
		"--reason=Show test reason", "--revert-plan=Undo the show test", "--json")
	id := extractJackID(t, createOut)

	// Show the jack
	out := runJackCmd(t, tmpDir, env, "jack", "show", id)
	if !strings.Contains(out, "pod/show-test") {
		t.Errorf("Expected target in show output, got: %s", out)
	}
	if !strings.Contains(out, "Show test reason") {
		t.Errorf("Expected reason in show output, got: %s", out)
	}
	if !strings.Contains(out, "Undo the show test") {
		t.Errorf("Expected revert plan in show output, got: %s", out)
	}

	// Show with JSON
	out = runJackCmd(t, tmpDir, env, "jack", "show", id, "--json")
	jsonStart := strings.Index(out, "{")
	var result map[string]interface{}
	json.Unmarshal([]byte(out[jsonStart:]), &result)
	if result["target"] != "pod/show-test" {
		t.Errorf("Expected target 'pod/show-test', got: %v", result["target"])
	}
}

func TestJack_LogChange(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Create a jack
	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/log-test",
		"--reason=Log test", "--revert-plan=Undo", "--json")
	id := extractJackID(t, createOut)

	// Log a change
	out := runJackCmd(t, tmpDir, env, "jack", "log", id,
		"--action=edit",
		"--target=config.yaml",
		"--before=log_level: info",
		"--after=log_level: debug",
		"--json")

	var logResult map[string]interface{}
	json.Unmarshal([]byte(out), &logResult)
	if logResult["action"] != "edit" {
		t.Errorf("Expected action 'edit', got: %v", logResult["action"])
	}
	if logResult["total"].(float64) != 1 {
		t.Errorf("Expected total 1, got: %v", logResult["total"])
	}

	// Log an exec change
	out = runJackCmd(t, tmpDir, env, "jack", "log", id,
		"--action=exec",
		"--cmd=kubectl rollout restart deployment/api",
		"--json")

	json.Unmarshal([]byte(out), &logResult)
	if logResult["total"].(float64) != 2 {
		t.Errorf("Expected total 2, got: %v", logResult["total"])
	}

	// Verify changes in show
	showOut := runJackCmd(t, tmpDir, env, "jack", "show", id)
	if !strings.Contains(showOut, "CHANGES (2)") {
		t.Errorf("Expected CHANGES (2) in show output, got: %s", showOut)
	}
}

func TestJack_LogInvalidAction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/test",
		"--reason=Test", "--revert-plan=Undo", "--json")
	id := extractJackID(t, createOut)

	// Invalid action
	out := runJackCmdExpectError(t, tmpDir, env, "jack", "log", id,
		"--action=invalid")
	if !strings.Contains(out, "invalid action") {
		t.Errorf("Expected 'invalid action' error, got: %s", out)
	}

	// Missing action
	out = runJackCmdExpectError(t, tmpDir, env, "jack", "log", id)
	if !strings.Contains(out, "--action is required") {
		t.Errorf("Expected '--action is required' error, got: %s", out)
	}
}

func TestJack_LogSensitiveDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/sensitive-test",
		"--reason=Sensitive test", "--revert-plan=Undo", "--json")
	id := extractJackID(t, createOut)

	// Should fail without --sensitive flag
	out := runJackCmdExpectError(t, tmpDir, env, "jack", "log", id,
		"--action=edit",
		"--target=config.yaml",
		"--before=password=oldpass123",
		"--after=password=newpass456")
	if !strings.Contains(out, "secrets") {
		t.Errorf("Expected secret detection error, got: %s", out)
	}

	// Should succeed with --sensitive flag and redact values
	out = runJackCmd(t, tmpDir, env, "jack", "log", id,
		"--action=edit",
		"--target=config.yaml",
		"--before=password=oldpass123",
		"--after=password=newpass456",
		"--sensitive",
		"--json")

	var logResult map[string]interface{}
	json.Unmarshal([]byte(out), &logResult)
	if logResult["sensitive"] != true {
		t.Errorf("Expected sensitive=true in JSON output, got: %v", logResult["sensitive"])
	}

	// Verify stored values are redacted (check via jack show --json)
	showOut := runJackCmd(t, tmpDir, env, "jack", "show", id, "--json")
	jsonStart := strings.Index(showOut, "{")
	var showResult map[string]interface{}
	json.Unmarshal([]byte(showOut[jsonStart:]), &showResult)

	changes, ok := showResult["changes"].([]interface{})
	if !ok || len(changes) == 0 {
		t.Fatalf("Expected at least 1 change, got: %v", showResult["changes"])
	}
	change := changes[0].(map[string]interface{})
	before := change["before"].(string)
	after := change["after"].(string)

	// Values should be redacted (contain [REDACTED])
	if strings.Contains(before, "oldpass123") {
		t.Errorf("Expected password to be redacted in before, got: %s", before)
	}
	if strings.Contains(after, "newpass456") {
		t.Errorf("Expected password to be redacted in after, got: %s", after)
	}
	if !strings.Contains(before, "[REDACTED]") {
		t.Errorf("Expected [REDACTED] in before value, got: %s", before)
	}
}

func TestJack_Off(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Create a jack
	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/off-test",
		"--reason=Off test", "--revert-plan=Undo", "--json")
	id := extractJackID(t, createOut)

	// Close the jack
	out := runJackCmd(t, tmpDir, env, "jack", "off", id,
		"--reason=Changes reverted", "--json")

	jsonStart := strings.Index(out, "{")
	var result map[string]interface{}
	json.Unmarshal([]byte(out[jsonStart:]), &result)
	if result["status"] != "closed" {
		t.Errorf("Expected status 'closed', got: %v", result["status"])
	}

	// Verify it appears in list --all but not default list
	listOut := runJackCmd(t, tmpDir, env, "jack", "list")
	if strings.Contains(listOut, "pod/off-test") {
		t.Errorf("Closed jack should not appear in default list")
	}

	allOut := runJackCmd(t, tmpDir, env, "jack", "list", "--all")
	if !strings.Contains(allOut, "pod/off-test") {
		t.Errorf("Closed jack should appear in --all list, got: %s", allOut)
	}
}

func TestJack_OffAlreadyClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/test",
		"--reason=Test", "--revert-plan=Undo", "--json")
	id := extractJackID(t, createOut)

	// Close it
	runJackCmd(t, tmpDir, env, "jack", "off", id, "--reason=Done")

	// Try to close again — should fail
	out := runJackCmdExpectError(t, tmpDir, env, "jack", "off", id, "--reason=Again")
	if !strings.Contains(out, "already closed") {
		t.Errorf("Expected 'already closed' error, got: %s", out)
	}
}

func TestJack_Extend(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/extend-test",
		"--reason=Extend test",
		"--revert-plan=Undo",
		"--ttl=30m",
		"--json")
	id := extractJackID(t, createOut)

	// Extend the TTL
	out := runJackCmd(t, tmpDir, env, "jack", "extend", id,
		"--ttl=2h",
		"--reason=Need more time",
		"--json")

	jsonStart := strings.Index(out, "{")
	var result map[string]interface{}
	json.Unmarshal([]byte(out[jsonStart:]), &result)
	if result["ttl"] != "2h0m0s" {
		t.Errorf("Expected TTL '2h0m0s', got: %v", result["ttl"])
	}
}

func TestJack_Check(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Create a jack with very short TTL (already expired)
	runJackCmd(t, tmpDir, env, "jack", "on", "pod/expired-test",
		"--reason=Check test",
		"--revert-plan=Undo",
		"--ttl=1ms",
		"--json")

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	// Check for expired jacks
	out := runJackCmd(t, tmpDir, env, "jack", "check", "--json")
	var result map[string]interface{}
	json.Unmarshal([]byte(out), &result)

	expired, ok := result["expired"].([]interface{})
	if !ok {
		t.Fatalf("Expected expired array, got: %v", result["expired"])
	}
	if len(expired) < 1 {
		t.Errorf("Expected at least 1 expired jack, got %d", len(expired))
	}
}

func TestJack_ShowSensitiveIndicator(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/sensitive-show",
		"--reason=Sensitive show test", "--revert-plan=Undo", "--json")
	id := extractJackID(t, createOut)

	// Log a sensitive change
	runJackCmd(t, tmpDir, env, "jack", "log", id,
		"--action=edit",
		"--target=secrets.yaml",
		"--before=api_key=sk-abc123",
		"--after=api_key=sk-def456",
		"--sensitive",
		"--json")

	// Show without --reveal should show [S] indicator
	showOut := runJackCmd(t, tmpDir, env, "jack", "show", id)
	if !strings.Contains(showOut, "[S]") {
		t.Errorf("Expected [S] sensitive indicator in show output, got: %s", showOut)
	}

	// Show with JSON should mark change as sensitive
	showJSON := runJackCmd(t, tmpDir, env, "jack", "show", id, "--json")
	jsonStart := strings.Index(showJSON, "{")
	var showResult map[string]interface{}
	json.Unmarshal([]byte(showJSON[jsonStart:]), &showResult)

	changes, ok := showResult["changes"].([]interface{})
	if !ok || len(changes) == 0 {
		t.Fatalf("Expected changes in show JSON, got: %v", showResult)
	}
	change := changes[0].(map[string]interface{})
	if change["sensitive"] != true {
		t.Errorf("Expected sensitive=true on change, got: %v", change["sensitive"])
	}
}

func TestJack_ListSensitiveCount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/sensitive-list",
		"--reason=Sensitive list test", "--revert-plan=Undo", "--json")
	id := extractJackID(t, createOut)

	// Log a sensitive change
	runJackCmd(t, tmpDir, env, "jack", "log", id,
		"--action=edit",
		"--before=token=abc123xyz",
		"--after=token=def456uvw",
		"--sensitive",
		"--json")

	// List should show sensitive count
	listOut := runJackCmd(t, tmpDir, env, "jack", "list")
	if !strings.Contains(listOut, "sensitive") {
		t.Errorf("Expected 'sensitive' count in list output, got: %s", listOut)
	}
}

func TestJack_ConcurrentDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// Create first jack on target
	runJackCmd(t, tmpDir, env, "jack", "on", "pod/concurrent-test",
		"--reason=First jack", "--revert-plan=Undo 1", "--json")

	// Second jack on same target should fail
	out := runJackCmdExpectError(t, tmpDir, env, "jack", "on", "pod/concurrent-test",
		"--reason=Second jack", "--revert-plan=Undo 2")
	if !strings.Contains(out, "already exists") {
		t.Errorf("Expected concurrent jack error, got: %s", out)
	}

	// With --allow-concurrent should succeed
	out = runJackCmd(t, tmpDir, env, "jack", "on", "pod/concurrent-test",
		"--reason=Second jack",
		"--revert-plan=Undo 2",
		"--allow-concurrent",
		"--json")
	if !strings.Contains(out, "test-") {
		t.Errorf("Expected successful creation with --allow-concurrent, got: %s", out)
	}
}

func TestJack_LogToClosedJack(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "pod/closed-log",
		"--reason=Test", "--revert-plan=Undo", "--json")
	id := extractJackID(t, createOut)

	// Close the jack
	runJackCmd(t, tmpDir, env, "jack", "off", id, "--reason=Done")

	// Try to log to closed jack — should fail
	out := runJackCmdExpectError(t, tmpDir, env, "jack", "log", id,
		"--action=edit", "--target=config.yaml")
	if !strings.Contains(out, "closed") {
		t.Errorf("Expected closed jack error, got: %s", out)
	}
}

func TestJack_FullLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	tmpDir, env := setupJackTestEnv(t)

	// 1. Create jack
	createOut := runJackCmd(t, tmpDir, env, "jack", "on", "deployment/api",
		"--reason=Adding debug logging for NATS timeouts",
		"--revert-plan=Restore config.yaml log_level to info",
		"--ttl=1h",
		"--labels=jack:debug",
		"--json")
	id := extractJackID(t, createOut)

	// 2. Log changes
	runJackCmd(t, tmpDir, env, "jack", "log", id,
		"--action=edit",
		"--target=config.yaml",
		"--before=log_level: info",
		"--after=log_level: debug",
		"--json")

	runJackCmd(t, tmpDir, env, "jack", "log", id,
		"--action=exec",
		"--cmd=kubectl rollout restart deployment/api",
		"--json")

	// 3. Extend TTL
	runJackCmd(t, tmpDir, env, "jack", "extend", id,
		"--ttl=2h", "--reason=Still investigating", "--json")

	// 4. Show jack (verify all data)
	showOut := runJackCmd(t, tmpDir, env, "jack", "show", id, "--json")
	jsonStart := strings.Index(showOut, "{")
	var showResult map[string]interface{}
	json.Unmarshal([]byte(showOut[jsonStart:]), &showResult)

	if showResult["target"] != "deployment/api" {
		t.Errorf("Expected target 'deployment/api', got: %v", showResult["target"])
	}
	if showResult["change_count"].(float64) != 2 {
		t.Errorf("Expected 2 changes, got: %v", showResult["change_count"])
	}
	if showResult["ttl"] != "2h0m0s" {
		t.Errorf("Expected updated TTL '2h0m0s', got: %v", showResult["ttl"])
	}

	// 5. Close jack
	offOut := runJackCmd(t, tmpDir, env, "jack", "off", id,
		"--reason=Root cause found: connection pool exhaustion", "--json")
	jsonStart = strings.Index(offOut, "{")
	var offResult map[string]interface{}
	json.Unmarshal([]byte(offOut[jsonStart:]), &offResult)
	if offResult["status"] != "closed" {
		t.Errorf("Expected status 'closed' after off, got: %v", offResult["status"])
	}

	// 6. Verify in list --all
	allOut := runJackCmd(t, tmpDir, env, "jack", "list", "--all")
	if !strings.Contains(allOut, "deployment/api") {
		t.Errorf("Expected closed jack in --all list")
	}
}
