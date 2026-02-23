package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// ============================================================================
// Jack RPC Integration Tests (bd-cjykn)
//
// Full lifecycle flows exercising multiple handlers in sequence,
// verifying state transitions, metadata consistency, and error catalog.
// ============================================================================

// setupJackTest creates a test server with the jack custom type registered.
func setupJackTest(t *testing.T) (*Server, *Client, storage.Storage, func()) {
	t.Helper()
	srv, client, store, cleanup := setupTestServerWithStore(t)
	ctx := context.Background()
	if err := store.SetConfig(ctx, "types.custom", "jack"); err != nil {
		cleanup()
		t.Fatalf("Failed to register jack type: %v", err)
	}
	return srv, client, store, cleanup
}

// --- Full Lifecycle: on → log → extend → off ---

func TestJackLifecycle_OnLogExtendOff(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	// 1. Create jack
	onResp, err := client.JackOn(&JackOnArgs{
		Target:     "deployment/gastown-rwx/bd-daemon",
		Reason:     "Enable debug logging to diagnose slow queries",
		RevertPlan: "Set log_level back to info in config.yaml",
		TTL:        "30m",
		Priority:   2,
		Labels:     []string{"jack:debug"},
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !onResp.Success {
		t.Fatalf("JackOn failed: %s", onResp.Error)
	}

	var jack types.Issue
	json.Unmarshal(onResp.Data, &jack)
	jackID := jack.ID

	if jack.IssueType != types.TypeJack {
		t.Errorf("type=%s, want jack", jack.IssueType)
	}
	if jack.Status != types.StatusInProgress {
		t.Errorf("status=%s, want in_progress", jack.Status)
	}

	// 2. Log changes
	changes := []struct {
		action, target, before, after, cmd string
	}{
		{"edit", "config.yaml", "log_level: info", "log_level: debug", ""},
		{"exec", "deployment/gastown-rwx/bd-daemon", "", "", "kubectl rollout restart deployment/bd-daemon -n gastown-rwx"},
		{"edit", "config.yaml", "query_timeout: 30s", "query_timeout: 60s", ""},
	}
	for i, c := range changes {
		logResp, err := client.JackLog(&JackLogArgs{
			ID:     jackID,
			Action: c.action,
			Target: c.target,
			Before: c.before,
			After:  c.after,
			Cmd:    c.cmd,
		})
		if err != nil {
			t.Fatalf("JackLog[%d]: %v", i, err)
		}
		if !logResp.Success {
			t.Fatalf("JackLog[%d]: %s", i, logResp.Error)
		}

		var result map[string]interface{}
		json.Unmarshal(logResp.Data, &result)
		if total := int(result["total"].(float64)); total != i+1 {
			t.Errorf("JackLog[%d] total=%d, want %d", i, total, i+1)
		}
	}

	// 3. Extend TTL
	extendResp, err := client.JackExtend(&JackExtendArgs{
		ID:     jackID,
		TTL:    "2h",
		Reason: "Still investigating slow queries, need more time",
	})
	if err != nil {
		t.Fatalf("JackExtend: %v", err)
	}
	if !extendResp.Success {
		t.Fatalf("JackExtend: %s", extendResp.Error)
	}

	var extResult map[string]interface{}
	json.Unmarshal(extendResp.Data, &extResult)
	if extResult["ttl"] != "2h0m0s" {
		t.Errorf("extended ttl=%v, want 2h0m0s", extResult["ttl"])
	}

	// Verify new expiry is ~2h out
	expiresStr := extResult["expires_at"].(string)
	expiresAt, _ := time.Parse(time.RFC3339, expiresStr)
	expectedExpiry := time.Now().Add(2 * time.Hour)
	if expiresAt.Before(expectedExpiry.Add(-time.Minute)) || expiresAt.After(expectedExpiry.Add(time.Minute)) {
		t.Errorf("expires_at=%v, expected ~%v", expiresAt, expectedExpiry)
	}

	// 4. Close jack
	offResp, err := client.JackOff(&JackOffArgs{
		ID:     jackID,
		Reason: "Reverted config changes, root cause identified as missing index",
	})
	if err != nil {
		t.Fatalf("JackOff: %v", err)
	}
	if !offResp.Success {
		t.Fatalf("JackOff: %s", offResp.Error)
	}

	var closed types.Issue
	json.Unmarshal(offResp.Data, &closed)
	if closed.Status != types.StatusClosed {
		t.Errorf("final status=%s, want closed", closed.Status)
	}

	// 5. Verify final metadata state
	showResp, err := client.Show(&ShowArgs{ID: jackID})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var meta map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &meta)

	// All 3 changes should be recorded
	changesArr, ok := meta["jack_changes"].([]interface{})
	if !ok {
		t.Fatal("jack_changes missing from metadata")
	}
	if len(changesArr) != 3 {
		t.Errorf("jack_changes count=%d, want 3", len(changesArr))
	}

	// Revert flag should be set
	if meta["jack_reverted"] != true {
		t.Error("jack_reverted not true after JackOff")
	}

	// Closed reason in metadata
	if meta["jack_closed_reason"] != "Reverted config changes, root cause identified as missing index" {
		t.Errorf("jack_closed_reason=%v", meta["jack_closed_reason"])
	}

	// Closed timestamp should be present
	closedAt, ok := meta["jack_closed_at"].(string)
	if !ok || closedAt == "" {
		t.Error("jack_closed_at missing from metadata")
	}
}

// --- Lifecycle: on → expire → check → auto-escalate ---

func TestJackLifecycle_OnExpireEscalate(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	// Create jack with 1ms TTL
	onResp, err := client.JackOn(&JackOnArgs{
		Target:     "statefulset/gastown/dolt",
		Reason:     "Failover testing",
		RevertPlan: "Promote replica back to primary",
		TTL:        "1ms",
		Priority:   1,
		Labels:     []string{"jack:failover"},
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !onResp.Success {
		t.Fatalf("JackOn: %s", onResp.Error)
	}

	var jack types.Issue
	json.Unmarshal(onResp.Data, &jack)

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	// Check without escalation first
	checkResp, err := client.JackCheck(&JackCheckArgs{})
	if err != nil {
		t.Fatalf("JackCheck: %v", err)
	}
	if !checkResp.Success {
		t.Fatalf("JackCheck: %s", checkResp.Error)
	}

	var checkResult JackCheckResult
	json.Unmarshal(checkResp.Data, &checkResult)
	if len(checkResult.Expired) != 1 {
		t.Fatalf("expired=%d, want 1", len(checkResult.Expired))
	}
	if checkResult.Escalated != 0 {
		t.Errorf("escalated=%d before auto-escalate, want 0", checkResult.Escalated)
	}

	// Now check with auto-escalation
	escalateResp, err := client.JackCheck(&JackCheckArgs{AutoEscalate: true})
	if err != nil {
		t.Fatalf("JackCheck(auto-escalate): %v", err)
	}
	if !escalateResp.Success {
		t.Fatalf("JackCheck(auto-escalate): %s", escalateResp.Error)
	}

	var escalateResult JackCheckResult
	json.Unmarshal(escalateResp.Data, &escalateResult)
	if escalateResult.Escalated != 1 {
		t.Errorf("escalated=%d, want 1", escalateResult.Escalated)
	}

	// Verify the escalation created a P0 alert bead
	listResp, err := client.List(&ListArgs{IssueType: "task", Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !listResp.Success {
		t.Fatalf("List: %s", listResp.Error)
	}

	var issues []*types.Issue
	json.Unmarshal(listResp.Data, &issues)

	var alertFound bool
	for _, issue := range issues {
		if strings.Contains(issue.Title, "EXPIRED JACK") && issue.Priority == 0 {
			alertFound = true
			if issue.Status != types.StatusOpen {
				t.Errorf("alert status=%s, want open", issue.Status)
			}
			break
		}
	}
	if !alertFound {
		t.Error("expected P0 alert bead for expired jack, not found")
	}
}

// --- Multiple jacks: active + expired separation ---

func TestJackCheck_MixedActiveAndExpired(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	// Create 2 active jacks (long TTL)
	createTestJack(t, client, "pod/dev/app-1", "active jack 1", "revert 1")
	createTestJack(t, client, "pod/dev/app-2", "active jack 2", "revert 2")

	// Create 2 expired jacks (1ms TTL)
	client.JackOn(&JackOnArgs{
		Target: "pod/dev/app-3", Reason: "expired 1", RevertPlan: "revert 3", TTL: "1ms",
	})
	client.JackOn(&JackOnArgs{
		Target: "pod/dev/app-4", Reason: "expired 2", RevertPlan: "revert 4", TTL: "1ms",
	})

	time.Sleep(10 * time.Millisecond)

	resp, err := client.JackCheck(&JackCheckArgs{})
	if err != nil {
		t.Fatalf("JackCheck: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackCheck: %s", resp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(resp.Data, &result)

	if result.Active != 2 {
		t.Errorf("active=%d, want 2", result.Active)
	}
	if len(result.Expired) != 2 {
		t.Errorf("expired=%d, want 2", len(result.Expired))
	}
}

// --- Error handling: operations on closed jacks ---
//
// Note: the RPC client returns (resp, err) where both are non-nil when the
// server reports an error. We check the error string for expected messages.

func TestJackClosed_CannotLog(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")
	client.JackOff(&JackOffArgs{ID: jackID, Reason: "done"})

	_, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: "config.yaml",
	})
	if err == nil {
		t.Fatal("expected error logging to closed jack")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error=%q, want mention of 'closed'", err.Error())
	}
}

func TestJackClosed_CannotExtend(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")
	client.JackOff(&JackOffArgs{ID: jackID, Reason: "done"})

	_, err := client.JackExtend(&JackExtendArgs{
		ID:  jackID,
		TTL: "1h",
	})
	if err == nil {
		t.Fatal("expected error extending closed jack")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error=%q, want mention of 'closed'", err.Error())
	}
}

func TestJackClosed_CannotCloseAgain(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")
	client.JackOff(&JackOffArgs{ID: jackID, Reason: "done"})

	_, err := client.JackOff(&JackOffArgs{
		ID:     jackID,
		Reason: "try again",
	})
	if err == nil {
		t.Fatal("expected error closing already-closed jack")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error=%q, want mention of 'closed'", err.Error())
	}
}

// --- Error handling: type mismatch ---

func TestJackOff_RejectsNonJack(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	taskID := createTestIssueForDecision(t, client, "Regular task")

	_, err := client.JackOff(&JackOffArgs{ID: taskID, Reason: "test"})
	if err == nil {
		t.Fatal("expected error for non-jack")
	}
	if !strings.Contains(err.Error(), "not a jack") {
		t.Errorf("error=%q, want 'not a jack'", err.Error())
	}
}

func TestJackExtend_RejectsNonJack(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	taskID := createTestIssueForDecision(t, client, "Regular task")

	_, err := client.JackExtend(&JackExtendArgs{ID: taskID, TTL: "1h"})
	if err == nil {
		t.Fatal("expected error for non-jack")
	}
	if !strings.Contains(err.Error(), "not a jack") {
		t.Errorf("error=%q, want 'not a jack'", err.Error())
	}
}

func TestJackLog_RejectsNonJack(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	taskID := createTestIssueForDecision(t, client, "Regular task")

	_, err := client.JackLog(&JackLogArgs{ID: taskID, Action: "edit"})
	if err == nil {
		t.Fatal("expected error for non-jack")
	}
	if !strings.Contains(err.Error(), "not a jack") {
		t.Errorf("error=%q, want 'not a jack'", err.Error())
	}
}

// --- Error handling: missing required fields ---

func TestJackOn_AllFieldsMissing(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	_, err := client.JackOn(&JackOnArgs{})
	if err == nil {
		t.Fatal("expected error for empty args")
	}
}

func TestJackOff_MissingID(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	_, err := client.JackOff(&JackOffArgs{Reason: "test"})
	if err == nil {
		t.Fatal("expected error for missing ID")
	}
}

func TestJackExtend_MissingID(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	_, err := client.JackExtend(&JackExtendArgs{TTL: "1h"})
	if err == nil {
		t.Fatal("expected error for missing ID")
	}
}

func TestJackLog_MissingID(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	_, err := client.JackLog(&JackLogArgs{Action: "edit"})
	if err == nil {
		t.Fatal("expected error for missing ID")
	}
}

func TestJackLog_MissingAction(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	_, err := client.JackLog(&JackLogArgs{ID: jackID})
	if err == nil {
		t.Fatal("expected error for missing action")
	}
}

// --- Jack with --blocks dependency ---

func TestJackLifecycle_WithBlocksDependency(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	// Create an issue that the jack will block
	blockedID := createTestIssueForDecision(t, client, "Deploy API v2")

	// Create jack that blocks the deploy
	onResp, err := client.JackOn(&JackOnArgs{
		Target:     "deployment/gastown-rwx/api",
		Reason:     "Patching critical bug before deploy",
		RevertPlan: "git revert the patch commit",
		Blocks:     blockedID,
		Labels:     []string{"jack:hotfix"},
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !onResp.Success {
		t.Fatalf("JackOn: %s", onResp.Error)
	}

	var jack types.Issue
	json.Unmarshal(onResp.Data, &jack)

	// Verify the blocked issue shows the dependency
	showResp, err := client.Show(&ShowArgs{ID: blockedID})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	depFound := false
	for _, dep := range details.Dependencies {
		if dep.ID == jack.ID {
			depFound = true
			break
		}
	}
	if !depFound {
		t.Error("blocked issue should have dependency on jack")
	}

	// Close the jack
	offResp, err := client.JackOff(&JackOffArgs{
		ID:     jack.ID,
		Reason: "Patch applied and verified",
	})
	if err != nil {
		t.Fatalf("JackOff: %v", err)
	}
	if !offResp.Success {
		t.Fatalf("JackOff: %s", offResp.Error)
	}
}

// --- Authorization model integration ---

func TestJackAuth_DebugAutoApproved(t *testing.T) {
	result := AuthorizeJackTarget("pod/dev/my-app", []string{"jack:debug"})
	if !result.Allowed {
		t.Errorf("jack:debug should be auto-approved, got Allowed=%v RequiresGate=%v", result.Allowed, result.RequiresGate)
	}
}

func TestJackAuth_ProductionRequiresGate(t *testing.T) {
	result := AuthorizeJackTarget("pod/production/my-app", []string{"jack:hotfix"})
	if !result.RequiresGate {
		t.Errorf("production ns should require gate, got Allowed=%v RequiresGate=%v", result.Allowed, result.RequiresGate)
	}
}

func TestJackAuth_StatefulSetRequiresGate(t *testing.T) {
	result := AuthorizeJackTarget("statefulset/dev/dolt", []string{"jack:failover"})
	if !result.RequiresGate {
		t.Errorf("statefulset should require gate, got Allowed=%v RequiresGate=%v", result.Allowed, result.RequiresGate)
	}
}

func TestJackAuth_ExternalRequiresGate(t *testing.T) {
	result := AuthorizeJackTarget("external://aws/rds/mydb-prod", []string{"jack:config"})
	if !result.RequiresGate {
		t.Errorf("external target should require gate, got Allowed=%v RequiresGate=%v", result.Allowed, result.RequiresGate)
	}
}

func TestJackAuth_DebugOverridesProductionGate(t *testing.T) {
	result := AuthorizeJackTarget("pod/production/critical-app", []string{"jack:debug"})
	if !result.Allowed {
		t.Errorf("jack:debug should override production gate, got Allowed=%v RequiresGate=%v", result.Allowed, result.RequiresGate)
	}
}

func TestJackAuth_NonProdAutoApproved(t *testing.T) {
	result := AuthorizeJackTarget("pod/dev/my-app", []string{"jack:hotfix"})
	if !result.Allowed {
		t.Errorf("dev ns should be auto-approved, got Allowed=%v RequiresGate=%v", result.Allowed, result.RequiresGate)
	}
}

func TestJackAuth_SecretRequiresGate(t *testing.T) {
	result := AuthorizeJackTarget("secret/dev/db-creds", []string{"jack:config"})
	if !result.RequiresGate {
		t.Errorf("secret should require gate, got Allowed=%v RequiresGate=%v", result.Allowed, result.RequiresGate)
	}
}

func TestJackAuth_InvalidTarget(t *testing.T) {
	result := AuthorizeJackTarget("", nil)
	if result.Allowed || result.RequiresGate {
		t.Errorf("empty target should be denied, got Allowed=%v RequiresGate=%v", result.Allowed, result.RequiresGate)
	}
}

// --- Multiple extensions ---

func TestJackExtend_MultipleExtensions(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "test", "revert")

	// Extend 3 times, each with a different TTL
	ttls := []string{"30m", "1h", "4h"}
	for _, ttl := range ttls {
		resp, err := client.JackExtend(&JackExtendArgs{
			ID:     jackID,
			TTL:    ttl,
			Reason: "extending to " + ttl,
		})
		if err != nil {
			t.Fatalf("JackExtend(%s): %v", ttl, err)
		}
		if !resp.Success {
			t.Fatalf("JackExtend(%s): %s", ttl, resp.Error)
		}
	}

	// Verify final metadata shows the last TTL
	showResp, err := client.Show(&ShowArgs{ID: jackID})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var meta map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &meta)

	if meta["jack_ttl"] != "4h0m0s" {
		t.Errorf("final ttl=%v, want 4h0m0s", meta["jack_ttl"])
	}
}

// --- Skip revert check ---

func TestJackOff_SkipRevertCheck(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "debugging session", "revert plan")

	// Log some changes
	client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: "config.yaml",
		Before: "old",
		After:  "new",
	})

	// Close with skip-revert-check
	resp, err := client.JackOff(&JackOffArgs{
		ID:              jackID,
		Reason:          "Emergency close, will revert manually later",
		SkipRevertCheck: true,
	})
	if err != nil {
		t.Fatalf("JackOff: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOff with skip-revert-check: %s", resp.Error)
	}

	var closed types.Issue
	json.Unmarshal(resp.Data, &closed)
	if closed.Status != types.StatusClosed {
		t.Errorf("status=%s, want closed", closed.Status)
	}
}

// --- Nonexistent jack ID ---

func TestJackOff_NonexistentJack(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	_, err := client.JackOff(&JackOffArgs{
		ID:     "bd-nonexistent",
		Reason: "test",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent jack")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error=%q, want 'not found'", err.Error())
	}
}

func TestJackExtend_NonexistentJack(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	_, err := client.JackExtend(&JackExtendArgs{
		ID:  "bd-nonexistent",
		TTL: "1h",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent jack")
	}
}

func TestJackLog_NonexistentJack(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	_, err := client.JackLog(&JackLogArgs{
		ID:     "bd-nonexistent",
		Action: "edit",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent jack")
	}
}

// --- Labels persisted correctly ---

func TestJackOn_LabelsVisible(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/dev/app",
		Reason:     "test",
		RevertPlan: "revert",
		Labels:     []string{"jack:debug", "jack:hotfix"},
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn: %s", resp.Error)
	}

	var jack types.Issue
	json.Unmarshal(resp.Data, &jack)

	// Verify labels
	labelSet := make(map[string]bool)
	for _, l := range jack.Labels {
		labelSet[l] = true
	}
	if !labelSet["jack:debug"] {
		t.Error("missing jack:debug label")
	}
	if !labelSet["jack:hotfix"] {
		t.Error("missing jack:hotfix label")
	}
}

// --- Verify check returns closed jacks as expired, not active ---

func TestJackCheck_ClosedJacksExcluded(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	// Create and close a jack
	jackID := createTestJack(t, client, "pod/test", "test", "revert")
	client.JackOff(&JackOffArgs{ID: jackID, Reason: "done"})

	// Create an active jack
	createTestJack(t, client, "pod/test2", "active", "revert")

	resp, err := client.JackCheck(&JackCheckArgs{})
	if err != nil {
		t.Fatalf("JackCheck: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackCheck: %s", resp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(resp.Data, &result)

	// Only the active jack should appear (closed ones should be excluded by status filter)
	if result.Active != 1 {
		t.Errorf("active=%d, want 1 (closed jack should be excluded)", result.Active)
	}
	if len(result.Expired) != 0 {
		t.Errorf("expired=%d, want 0", len(result.Expired))
	}
}

// --- JackOn title format ---

func TestJackOn_TitleFormat(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/dev/my-app",
		Reason:     "Enable verbose logging",
		RevertPlan: "revert",
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn: %s", resp.Error)
	}

	var jack types.Issue
	json.Unmarshal(resp.Data, &jack)

	expected := "Jack: pod/dev/my-app — Enable verbose logging"
	if jack.Title != expected {
		t.Errorf("title=%q, want %q", jack.Title, expected)
	}
}

// --- Metadata round-trip: verify jack_target preserved through lifecycle ---

func TestJackMetadata_TargetPreservedThroughLifecycle(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	target := "deployment/gastown-rwx/bd-daemon"

	onResp, err := client.JackOn(&JackOnArgs{
		Target:     target,
		Reason:     "test",
		RevertPlan: "revert",
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !onResp.Success {
		t.Fatalf("JackOn: %s", onResp.Error)
	}

	var jack types.Issue
	json.Unmarshal(onResp.Data, &jack)
	jackID := jack.ID

	// Log a change
	client.JackLog(&JackLogArgs{ID: jackID, Action: "edit", Target: "config.yaml"})

	// Extend
	client.JackExtend(&JackExtendArgs{ID: jackID, TTL: "2h"})

	// Close
	client.JackOff(&JackOffArgs{ID: jackID, Reason: "done"})

	// Verify jack_target is still the original value
	showResp, err := client.Show(&ShowArgs{ID: jackID})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var meta map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &meta)

	if meta["jack_target"] != target {
		t.Errorf("jack_target=%v, want %q", meta["jack_target"], target)
	}
}
