package rpc

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// ============================================================================
// Jack RPC Handler Tests (bd-nz7kb)
// ============================================================================

// helper: create a jack via JackOn and return the jack ID.
func createTestJack(t *testing.T, client *Client, target, reason, revertPlan string) string {
	t.Helper()
	args := &JackOnArgs{
		Target:     target,
		Reason:     reason,
		RevertPlan: revertPlan,
		TTL:        "1h",
		Priority:   2,
	}
	resp, err := client.JackOn(args)
	if err != nil {
		t.Fatalf("JackOn failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn failed: %s", resp.Error)
	}
	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal jack issue: %v", err)
	}
	if issue.ID == "" {
		t.Fatal("JackOn returned empty ID")
	}
	return issue.ID
}

// --- JackOn tests ---

func TestJackOn_Basic(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	args := &JackOnArgs{
		Target:     "pod/bd-daemon-abc",
		Reason:     "Adding debug logging",
		RevertPlan: "Restore config.yaml log_level to info",
		TTL:        "2h",
		Priority:   1,
		Labels:     []string{"jack:debug"},
	}
	resp, err := client.JackOn(args)
	if err != nil {
		t.Fatalf("JackOn failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn failed: %s", resp.Error)
	}

	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify issue fields
	if issue.IssueType != types.TypeJack {
		t.Errorf("Expected type=%s, got %s", types.TypeJack, issue.IssueType)
	}
	if issue.Status != types.StatusInProgress {
		t.Errorf("Expected status=in_progress, got %s", issue.Status)
	}
	if issue.Priority != 1 {
		t.Errorf("Expected priority=1, got %d", issue.Priority)
	}
	if issue.Description != "Adding debug logging" {
		t.Errorf("Expected description=%q, got %q", "Adding debug logging", issue.Description)
	}

	// Verify metadata
	var metadata map[string]interface{}
	if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
		t.Fatalf("Failed to unmarshal metadata: %v", err)
	}
	if metadata["jack_target"] != "pod/bd-daemon-abc" {
		t.Errorf("Expected jack_target=%q, got %v", "pod/bd-daemon-abc", metadata["jack_target"])
	}
	if metadata["jack_revert_plan"] != "Restore config.yaml log_level to info" {
		t.Errorf("Expected jack_revert_plan=%q, got %v", "Restore config.yaml log_level to info", metadata["jack_revert_plan"])
	}
	if metadata["jack_ttl"] != "2h0m0s" {
		t.Errorf("Expected jack_ttl=%q, got %v", "2h0m0s", metadata["jack_ttl"])
	}

	// Verify expires_at is set and in the future
	expiresAtStr, ok := metadata["jack_expires_at"].(string)
	if !ok || expiresAtStr == "" {
		t.Fatal("jack_expires_at not set in metadata")
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		t.Fatalf("Failed to parse jack_expires_at: %v", err)
	}
	if expiresAt.Before(time.Now()) {
		t.Error("jack_expires_at is in the past")
	}

	// Verify changes array is empty
	changes, ok := metadata["jack_changes"].([]interface{})
	if !ok {
		t.Fatal("jack_changes not set in metadata")
	}
	if len(changes) != 0 {
		t.Errorf("Expected 0 changes, got %d", len(changes))
	}
}

func TestJackOn_MissingTarget(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Reason:     "test",
		RevertPlan: "test",
	})
	if err != nil {
		t.Fatalf("JackOn request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for missing target")
	}
	if resp.Error == "" {
		t.Error("Expected error message")
	}
}

func TestJackOn_MissingReason(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		RevertPlan: "test",
	})
	if err != nil {
		t.Fatalf("JackOn request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for missing reason")
	}
}

func TestJackOn_MissingRevertPlan(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target: "pod/test",
		Reason: "test",
	})
	if err != nil {
		t.Fatalf("JackOn request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for missing revert_plan")
	}
}

func TestJackOn_InvalidTTL(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		Reason:     "test",
		RevertPlan: "test",
		TTL:        "not-a-duration",
	})
	if err != nil {
		t.Fatalf("JackOn request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for invalid TTL")
	}
}

func TestJackOn_InvalidPriority(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		Reason:     "test",
		RevertPlan: "test",
		Priority:   5,
	})
	if err != nil {
		t.Fatalf("JackOn request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for priority=5")
	}
}

func TestJackOn_AutoAddsJackLabel(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		Reason:     "test",
		RevertPlan: "test",
	})
	if err != nil {
		t.Fatalf("JackOn failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn failed: %s", resp.Error)
	}

	var issue types.Issue
	json.Unmarshal(resp.Data, &issue)

	// Labels should include jack:general since none specified
	found := false
	for _, l := range issue.Labels {
		if l == "jack:general" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected jack:general label, got %v", issue.Labels)
	}
}

func TestJackOn_DefaultTTL(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		Reason:     "test",
		RevertPlan: "test",
		// No TTL specified — should default to 1h
	})
	if err != nil {
		t.Fatalf("JackOn failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn failed: %s", resp.Error)
	}

	var issue types.Issue
	json.Unmarshal(resp.Data, &issue)

	var metadata map[string]interface{}
	json.Unmarshal(issue.Metadata, &metadata)

	if metadata["jack_ttl"] != "1h0m0s" {
		t.Errorf("Expected default TTL=1h0m0s, got %v", metadata["jack_ttl"])
	}
}

// --- JackOff tests ---

func TestJackOff_Basic(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test reason", "revert plan")

	resp, err := client.JackOff(&JackOffArgs{
		ID:     jackID,
		Reason: "Debug complete, reverted config",
	})
	if err != nil {
		t.Fatalf("JackOff failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOff failed: %s", resp.Error)
	}

	var issue types.Issue
	json.Unmarshal(resp.Data, &issue)

	if issue.Status != types.StatusClosed {
		t.Errorf("Expected status=closed, got %s", issue.Status)
	}

	// Verify metadata updated
	var metadata map[string]interface{}
	json.Unmarshal(issue.Metadata, &metadata)

	if metadata["jack_reverted"] != true {
		t.Error("Expected jack_reverted=true")
	}
	if metadata["jack_closed_reason"] != "Debug complete, reverted config" {
		t.Errorf("Expected closed reason in metadata, got %v", metadata["jack_closed_reason"])
	}
}

func TestJackOff_MissingReason(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	resp, err := client.JackOff(&JackOffArgs{
		ID: jackID,
	})
	if err != nil {
		t.Fatalf("JackOff request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for missing reason")
	}
}

func TestJackOff_NotAJack(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create a regular task
	taskID := createTestIssueForDecision(t, client, "Regular task")

	resp, err := client.JackOff(&JackOffArgs{
		ID:     taskID,
		Reason: "test",
	})
	if err != nil {
		t.Fatalf("JackOff request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for non-jack issue")
	}
}

func TestJackOff_AlreadyClosed(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	// Close it
	client.JackOff(&JackOffArgs{ID: jackID, Reason: "done"})

	// Try to close again
	resp, err := client.JackOff(&JackOffArgs{ID: jackID, Reason: "done again"})
	if err != nil {
		t.Fatalf("JackOff request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for already closed jack")
	}
}

// --- JackLog tests ---

func TestJackLog_Basic(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: "config.yaml",
		Before: "log_level: info",
		After:  "log_level: debug",
	})
	if err != nil {
		t.Fatalf("JackLog failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackLog failed: %s", resp.Error)
	}

	var result map[string]interface{}
	json.Unmarshal(resp.Data, &result)

	if result["action"] != "edit" {
		t.Errorf("Expected action=edit, got %v", result["action"])
	}
	if result["target"] != "config.yaml" {
		t.Errorf("Expected target=config.yaml, got %v", result["target"])
	}
	// total should be 1 (first change)
	if total, ok := result["total"].(float64); !ok || total != 1 {
		t.Errorf("Expected total=1, got %v", result["total"])
	}
}

func TestJackLog_InvalidAction(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "destroy", // invalid
	})
	if err != nil {
		t.Fatalf("JackLog request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for invalid action")
	}
}

func TestJackLog_AllValidActions(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	actions := []string{"edit", "exec", "patch", "delete", "create"}
	for i, action := range actions {
		resp, err := client.JackLog(&JackLogArgs{
			ID:     jackID,
			Action: action,
			Target: "resource-" + action,
		})
		if err != nil {
			t.Fatalf("JackLog(%s) failed: %v", action, err)
		}
		if !resp.Success {
			t.Fatalf("JackLog(%s) failed: %s", action, resp.Error)
		}

		var result map[string]interface{}
		json.Unmarshal(resp.Data, &result)
		if total, ok := result["total"].(float64); !ok || int(total) != i+1 {
			t.Errorf("After action %s, expected total=%d, got %v", action, i+1, result["total"])
		}
	}
}

func TestJackLog_DefaultTarget(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "deployment/bd-daemon", "test", "revert")

	// Log without specifying target — should default to jack's target
	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "exec",
		Cmd:    "kubectl rollout restart",
	})
	if err != nil {
		t.Fatalf("JackLog failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackLog failed: %s", resp.Error)
	}

	var result map[string]interface{}
	json.Unmarshal(resp.Data, &result)

	if result["target"] != "deployment/bd-daemon" {
		t.Errorf("Expected target=deployment/bd-daemon (from jack), got %v", result["target"])
	}
}

func TestJackLog_ClosedJack(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")
	client.JackOff(&JackOffArgs{ID: jackID, Reason: "done"})

	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
	})
	if err != nil {
		t.Fatalf("JackLog request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for closed jack")
	}
}

// --- JackExtend tests ---

func TestJackExtend_Basic(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	resp, err := client.JackExtend(&JackExtendArgs{
		ID:     jackID,
		TTL:    "2h",
		Reason: "Need more time",
	})
	if err != nil {
		t.Fatalf("JackExtend failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackExtend failed: %s", resp.Error)
	}

	var result map[string]interface{}
	json.Unmarshal(resp.Data, &result)

	if result["ttl"] != "2h0m0s" {
		t.Errorf("Expected ttl=2h0m0s, got %v", result["ttl"])
	}

	// Verify new expires_at is in the future
	expiresStr, ok := result["expires_at"].(string)
	if !ok || expiresStr == "" {
		t.Fatal("expires_at not in response")
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		t.Fatalf("Failed to parse expires_at: %v", err)
	}
	// Should expire roughly 2h from now
	expected := time.Now().Add(2 * time.Hour)
	if expiresAt.Before(expected.Add(-time.Minute)) || expiresAt.After(expected.Add(time.Minute)) {
		t.Errorf("Expected expires_at ~2h from now, got %v", expiresAt)
	}
}

func TestJackExtend_InvalidTTL(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	resp, err := client.JackExtend(&JackExtendArgs{
		ID:  jackID,
		TTL: "not-a-duration",
	})
	if err != nil {
		t.Fatalf("JackExtend request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for invalid TTL")
	}
}

func TestJackExtend_ClosedJack(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")
	client.JackOff(&JackOffArgs{ID: jackID, Reason: "done"})

	resp, err := client.JackExtend(&JackExtendArgs{
		ID:  jackID,
		TTL: "1h",
	})
	if err != nil {
		t.Fatalf("JackExtend request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for closed jack")
	}
}

func TestJackExtend_MissingTTL(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	resp, err := client.JackExtend(&JackExtendArgs{
		ID: jackID,
	})
	if err != nil {
		t.Fatalf("JackExtend request failed: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure for missing TTL")
	}
}

// --- JackCheck tests ---

func TestJackCheck_NoJacks(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	resp, err := client.JackCheck(&JackCheckArgs{})
	if err != nil {
		t.Fatalf("JackCheck failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackCheck failed: %s", resp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(resp.Data, &result)

	if result.Active != 0 {
		t.Errorf("Expected active=0, got %d", result.Active)
	}
	if len(result.Expired) != 0 {
		t.Errorf("Expected 0 expired, got %d", len(result.Expired))
	}
}

func TestJackCheck_ActiveJack(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	createTestJack(t, client, "pod/test", "test", "revert")

	resp, err := client.JackCheck(&JackCheckArgs{})
	if err != nil {
		t.Fatalf("JackCheck failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackCheck failed: %s", resp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(resp.Data, &result)

	if result.Active != 1 {
		t.Errorf("Expected active=1, got %d", result.Active)
	}
	if len(result.Expired) != 0 {
		t.Errorf("Expected 0 expired, got %d", len(result.Expired))
	}
}

func TestJackCheck_ExpiredJack(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create a jack with very short TTL (already expired by the time we check)
	args := &JackOnArgs{
		Target:     "pod/test",
		Reason:     "test",
		RevertPlan: "revert",
		TTL:        "1ms",
	}
	resp, err := client.JackOn(args)
	if err != nil {
		t.Fatalf("JackOn failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn failed: %s", resp.Error)
	}
	_ = store // used in setup

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	checkResp, err := client.JackCheck(&JackCheckArgs{})
	if err != nil {
		t.Fatalf("JackCheck failed: %v", err)
	}
	if !checkResp.Success {
		t.Fatalf("JackCheck failed: %s", checkResp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(checkResp.Data, &result)

	if len(result.Expired) != 1 {
		t.Errorf("Expected 1 expired jack, got %d", len(result.Expired))
	}
	if result.Escalated != 0 {
		t.Errorf("Expected 0 escalated (no --auto-escalate), got %d", result.Escalated)
	}
}

func TestJackCheck_AutoEscalate(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create an expired jack
	client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		Reason:     "test",
		RevertPlan: "revert",
		TTL:        "1ms",
	})
	time.Sleep(10 * time.Millisecond)

	resp, err := client.JackCheck(&JackCheckArgs{AutoEscalate: true})
	if err != nil {
		t.Fatalf("JackCheck failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackCheck failed: %s", resp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(resp.Data, &result)

	if result.Escalated != 1 {
		t.Errorf("Expected 1 escalated, got %d", result.Escalated)
	}
}

// --- Metadata validation tests ---

func TestJackMetadata_TitleTruncation(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	longReason := ""
	for i := 0; i < 250; i++ {
		longReason += "x"
	}

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		Reason:     longReason,
		RevertPlan: "revert",
	})
	if err != nil {
		t.Fatalf("JackOn failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn failed: %s", resp.Error)
	}

	var issue types.Issue
	json.Unmarshal(resp.Data, &issue)

	if len(issue.Title) > 200 {
		t.Errorf("Expected title truncated to 200 chars, got %d", len(issue.Title))
	}
}

func TestJackLog_MultipleChanges(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	// Log 5 changes
	for i := 0; i < 5; i++ {
		resp, err := client.JackLog(&JackLogArgs{
			ID:     jackID,
			Action: "edit",
			Target: "file-" + string(rune('a'+i)),
			Before: "old",
			After:  "new",
		})
		if err != nil {
			t.Fatalf("JackLog(%d) failed: %v", i, err)
		}
		if !resp.Success {
			t.Fatalf("JackLog(%d) failed: %s", i, resp.Error)
		}
	}

	// Verify total count
	showResp, err := client.Show(&ShowArgs{ID: jackID})
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}

	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var metadata map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &metadata)

	changes, ok := metadata["jack_changes"].([]interface{})
	if !ok {
		t.Fatal("jack_changes not found in metadata")
	}
	if len(changes) != 5 {
		t.Errorf("Expected 5 changes, got %d", len(changes))
	}
}

func TestJackOn_WithBlocks(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create an issue to block
	blockedID := createTestIssueForDecision(t, client, "Blocked task")

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		Reason:     "testing fix",
		RevertPlan: "rollback",
		Blocks:     blockedID,
	})
	if err != nil {
		t.Fatalf("JackOn failed: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn failed: %s", resp.Error)
	}

	var issue types.Issue
	json.Unmarshal(resp.Data, &issue)

	// Verify the jack was created
	if issue.ID == "" {
		t.Fatal("Expected non-empty jack ID")
	}

	// Verify the dependency exists via Show
	showResp, err := client.Show(&ShowArgs{ID: blockedID})
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	found := false
	for _, dep := range details.Dependencies {
		if dep.ID == issue.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected dependency from blocked task to jack, not found in %d deps", len(details.Dependencies))
	}
}
