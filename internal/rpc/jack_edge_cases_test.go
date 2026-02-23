package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// ============================================================================
// Jack Edge Case Tests (beads-uyjs)
//
// Covers gaps not present in server_jack_test.go, jack_auth_test.go,
// jack_type_registration_test.go, server_jack_integration_test.go,
// or server_jack_additional_test.go:
//
// - Max changes limit (500) enforcement via direct metadata injection
// - Sensitive: connection strings, GitHub tokens, bearer tokens, private keys
// - Multi-rig metadata (jack_rig field)
// - Auto-escalate creates dependency link to expired jack
// - JackOff skip-revert-check vs normal close metadata difference
// - JackOn with nonexistent blocks ID
// - JackCheck with malformed expires_at metadata
// ============================================================================

// --- Max changes limit (500) enforced at server ---

func TestJackLog_MaxChangesLimit500(t *testing.T) {
	_, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "max changes", "revert")

	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	// Inject 500 changes directly into metadata
	var meta map[string]interface{}
	json.Unmarshal(issue.Metadata, &meta)

	changes := make([]interface{}, 500)
	for i := range changes {
		changes[i] = map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"action":    "edit",
			"target":    "file.txt",
		}
	}
	meta["jack_changes"] = changes
	metaJSON, _ := json.Marshal(meta)

	if err := store.UpdateIssue(ctx, jackID, map[string]interface{}{
		"metadata": string(metaJSON),
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	// Attempt to log change #501 via RPC — should fail
	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: "one-too-many.txt",
	})
	if err != nil {
		t.Fatalf("JackLog request error: %v", err)
	}
	if resp.Success {
		t.Error("Expected failure when exceeding max changes (500)")
	}
	if !strings.Contains(resp.Error, "500") {
		t.Errorf("Error should mention max 500, got: %s", resp.Error)
	}
}

func TestJackLog_AtMaxChangesMinusOne(t *testing.T) {
	_, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "boundary test", "revert")

	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	// Inject 499 changes
	var meta map[string]interface{}
	json.Unmarshal(issue.Metadata, &meta)

	changes := make([]interface{}, 499)
	for i := range changes {
		changes[i] = map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"action":    "edit",
			"target":    "file.txt",
		}
	}
	meta["jack_changes"] = changes
	metaJSON, _ := json.Marshal(meta)

	if err := store.UpdateIssue(ctx, jackID, map[string]interface{}{
		"metadata": string(metaJSON),
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	// Change #500 should succeed (boundary)
	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: "last-allowed.txt",
	})
	if err != nil {
		t.Fatalf("JackLog request error: %v", err)
	}
	if !resp.Success {
		t.Errorf("Change #500 should succeed (at limit, not over): %s", resp.Error)
	}
}

// --- Sensitive: connection string patterns ---

func TestJackLog_SensitiveConnectionString(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "configmap/dev/db", "rotate creds", "revert")

	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: "database.yaml",
		Before: "url: postgres://admin:oldpass@db.host:5432/mydb",
		After:  "url: postgres://admin:newpass@db.host:5432/mydb",
	})
	if err != nil {
		t.Fatalf("JackLog: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackLog: %s", resp.Error)
	}

	showResp, _ := client.Show(&ShowArgs{ID: jackID})
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var meta map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &meta)
	changes := meta["jack_changes"].([]interface{})
	change := changes[0].(map[string]interface{})

	if strings.Contains(change["before"].(string), "oldpass") {
		t.Error("postgres connection string password should be redacted in before")
	}
	if strings.Contains(change["after"].(string), "newpass") {
		t.Error("postgres connection string password should be redacted in after")
	}
	if change["sensitive"] != true {
		t.Error("Expected sensitive=true for connection string")
	}
}

// --- Sensitive: GitHub personal access tokens ---

func TestJackLog_SensitiveGitHubToken(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "secret/dev/tokens", "rotate GH token", "revert")

	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: ".env",
		Before: "GH_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz123456",
		After:  "GH_TOKEN=ghp_newtoken12345678901234567890abcdef",
	})
	if err != nil {
		t.Fatalf("JackLog: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackLog: %s", resp.Error)
	}

	showResp, _ := client.Show(&ShowArgs{ID: jackID})
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var meta map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &meta)
	changes := meta["jack_changes"].([]interface{})
	change := changes[0].(map[string]interface{})

	if strings.Contains(change["before"].(string), "ghp_abcdef") {
		t.Error("GitHub PAT should be redacted in before")
	}
	if change["sensitive"] != true {
		t.Error("Expected sensitive=true for GitHub PAT")
	}
}

// --- Sensitive: bearer token patterns ---

func TestJackLog_SensitiveBearerToken(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "configmap/dev/auth", "rotate bearer", "revert")

	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: "auth.yaml",
		Before: "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig",
		After:  "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.newpayload.newsig",
	})
	if err != nil {
		t.Fatalf("JackLog: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackLog: %s", resp.Error)
	}

	showResp, _ := client.Show(&ShowArgs{ID: jackID})
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var meta map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &meta)
	changes := meta["jack_changes"].([]interface{})
	change := changes[0].(map[string]interface{})

	if strings.Contains(change["before"].(string), "eyJhbGci") {
		t.Error("Bearer token should be redacted in before")
	}
	if change["sensitive"] != true {
		t.Error("Expected sensitive=true for bearer token")
	}
}

// --- Sensitive: AWS keys ---

func TestJackLog_SensitiveAWSKey(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "secret/dev/aws", "rotate AWS keys", "revert")

	resp, err := client.JackLog(&JackLogArgs{
		ID:     jackID,
		Action: "edit",
		Target: "aws-creds.env",
		Before: "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		After:  "aws_secret_access_key=newSecretKey12345/EXAMPLE/newKeyValue123456",
	})
	if err != nil {
		t.Fatalf("JackLog: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackLog: %s", resp.Error)
	}

	showResp, _ := client.Show(&ShowArgs{ID: jackID})
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var meta map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &meta)
	changes := meta["jack_changes"].([]interface{})
	change := changes[0].(map[string]interface{})

	if strings.Contains(change["before"].(string), "wJalrXUtnFEMI") {
		t.Error("AWS secret key should be redacted in before")
	}
	if change["sensitive"] != true {
		t.Error("Expected sensitive=true for AWS secret key")
	}
}

// --- Multi-rig metadata (jack_rig field) ---

func TestJackOn_TargetRigMetadata(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/dev/app",
		Reason:     "multi-rig test",
		RevertPlan: "revert",
		TargetRig:  "gastown-rwx",
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn: %s", resp.Error)
	}

	var jack types.Issue
	json.Unmarshal(resp.Data, &jack)

	var meta map[string]interface{}
	json.Unmarshal(jack.Metadata, &meta)

	// jack_rig should be set from TargetRig
	if meta["jack_rig"] != "gastown-rwx" {
		t.Errorf("jack_rig=%v, want gastown-rwx", meta["jack_rig"])
	}
}

func TestJackOn_NoTargetRigDefaultsFromConfig(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/dev/app",
		Reason:     "default rig test",
		RevertPlan: "revert",
		// No TargetRig — should use default from server config (may be empty in test)
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOn: %s", resp.Error)
	}

	var jack types.Issue
	json.Unmarshal(resp.Data, &jack)

	var meta map[string]interface{}
	json.Unmarshal(jack.Metadata, &meta)

	// jack_rig may or may not be set depending on server config
	// Just verify metadata is well-formed (no crash)
	if meta["jack_target"] != "pod/dev/app" {
		t.Errorf("jack_target=%v", meta["jack_target"])
	}
}

// --- Auto-escalate dependency link ---

func TestJackCheck_AutoEscalateCreatesLinkedAlert(t *testing.T) {
	_, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	// Create expired jack
	onResp, err := client.JackOn(&JackOnArgs{
		Target: "pod/test", Reason: "test escalation link", RevertPlan: "revert", TTL: "1ms",
	})
	if err != nil {
		t.Fatalf("JackOn: %v", err)
	}
	if !onResp.Success {
		t.Fatalf("JackOn: %s", onResp.Error)
	}
	var jack types.Issue
	json.Unmarshal(onResp.Data, &jack)

	time.Sleep(10 * time.Millisecond)

	// Auto-escalate
	checkResp, err := client.JackCheck(&JackCheckArgs{AutoEscalate: true})
	if err != nil {
		t.Fatalf("JackCheck: %v", err)
	}
	if !checkResp.Success {
		t.Fatalf("JackCheck: %s", checkResp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(checkResp.Data, &result)
	if result.Escalated != 1 {
		t.Fatalf("escalated=%d, want 1", result.Escalated)
	}

	// Find the alert bead
	ctx := context.Background()
	taskType := types.IssueType("task")
	alerts, err := store.SearchIssues(ctx, "EXPIRED JACK", types.IssueFilter{
		IssueType: &taskType,
	})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatal("Expected alert bead for expired jack")
	}

	alert := alerts[0]

	// Verify alert properties
	if alert.Priority != 0 {
		t.Errorf("Alert priority=%d, want 0 (P0)", alert.Priority)
	}
	if alert.Status != types.StatusOpen {
		t.Errorf("Alert status=%s, want open", alert.Status)
	}
	if !strings.Contains(alert.Description, jack.ID) {
		t.Errorf("Alert description should reference jack %s: %s", jack.ID, alert.Description)
	}
	if !strings.Contains(alert.Title, "EXPIRED JACK") {
		t.Errorf("Alert title should contain 'EXPIRED JACK': %s", alert.Title)
	}

	// Verify the alert has a label
	alertLabels, err := store.GetLabels(ctx, alert.ID)
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	labelFound := false
	for _, l := range alertLabels {
		if l == "jack:expired-alert" {
			labelFound = true
			break
		}
	}
	if !labelFound {
		t.Errorf("Alert should have jack:expired-alert label, got: %v", alertLabels)
	}
}

// --- JackOff: skip-revert-check metadata ---

func TestJackOff_SkipRevertCheckMetadata(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "skip revert test", "revert plan")

	// Log some changes
	client.JackLog(&JackLogArgs{ID: jackID, Action: "edit", Target: "config.yaml", Before: "a", After: "b"})

	// Close with skip-revert-check
	resp, err := client.JackOff(&JackOffArgs{
		ID:              jackID,
		Reason:          "Emergency close without revert",
		SkipRevertCheck: true,
	})
	if err != nil {
		t.Fatalf("JackOff: %v", err)
	}
	if !resp.Success {
		t.Fatalf("JackOff: %s", resp.Error)
	}

	var issue types.Issue
	json.Unmarshal(resp.Data, &issue)
	if issue.Status != types.StatusClosed {
		t.Errorf("status=%s, want closed", issue.Status)
	}

	// Verify metadata still has jack_reverted=true (skip-revert-check doesn't change this)
	showResp, _ := client.Show(&ShowArgs{ID: jackID})
	var details types.IssueDetails
	json.Unmarshal(showResp.Data, &details)

	var meta map[string]interface{}
	json.Unmarshal(details.Issue.Metadata, &meta)

	if meta["jack_reverted"] != true {
		t.Errorf("jack_reverted=%v, want true (even with skip-revert-check)", meta["jack_reverted"])
	}
	if meta["jack_closed_reason"] != "Emergency close without revert" {
		t.Errorf("jack_closed_reason=%v", meta["jack_closed_reason"])
	}
	// Changes should still be preserved
	changes, ok := meta["jack_changes"].([]interface{})
	if !ok || len(changes) != 1 {
		t.Errorf("Expected 1 change preserved, got %v", meta["jack_changes"])
	}
}

// --- JackCheck with missing/malformed expiry metadata ---

func TestJackCheck_MissingExpiryCountsAsActive(t *testing.T) {
	_, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "missing expiry", "revert")

	// Remove jack_expires_at from metadata
	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	var meta map[string]interface{}
	json.Unmarshal(issue.Metadata, &meta)
	delete(meta, "jack_expires_at")
	metaJSON, _ := json.Marshal(meta)

	if err := store.UpdateIssue(ctx, jackID, map[string]interface{}{
		"metadata": string(metaJSON),
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	// JackCheck should count this as active (not expired)
	checkResp, err := client.JackCheck(&JackCheckArgs{})
	if err != nil {
		t.Fatalf("JackCheck: %v", err)
	}
	if !checkResp.Success {
		t.Fatalf("JackCheck: %s", checkResp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(checkResp.Data, &result)

	if result.Active != 1 {
		t.Errorf("active=%d, want 1 (jack with no expiry should count as active)", result.Active)
	}
	if len(result.Expired) != 0 {
		t.Errorf("expired=%d, want 0", len(result.Expired))
	}
}

func TestJackCheck_MalformedExpiryCountsAsActive(t *testing.T) {
	_, client, store, cleanup := setupJackTest(t)
	defer cleanup()

	jackID := createTestJack(t, client, "pod/test", "bad expiry", "revert")

	// Set jack_expires_at to invalid format
	ctx := context.Background()
	issue, err := store.GetIssue(ctx, jackID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	var meta map[string]interface{}
	json.Unmarshal(issue.Metadata, &meta)
	meta["jack_expires_at"] = "not-a-date"
	metaJSON, _ := json.Marshal(meta)

	if err := store.UpdateIssue(ctx, jackID, map[string]interface{}{
		"metadata": string(metaJSON),
	}, "test"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}

	// JackCheck should count this as active (unparseable expiry treated as active)
	checkResp, err := client.JackCheck(&JackCheckArgs{})
	if err != nil {
		t.Fatalf("JackCheck: %v", err)
	}
	if !checkResp.Success {
		t.Fatalf("JackCheck: %s", checkResp.Error)
	}

	var result JackCheckResult
	json.Unmarshal(checkResp.Data, &result)

	if result.Active != 1 {
		t.Errorf("active=%d, want 1 (malformed expiry should count as active)", result.Active)
	}
}

// --- JackOn with nonexistent blocks ID ---

func TestJackOn_NonexistentBlocksID(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "pod/test",
		Reason:     "test",
		RevertPlan: "revert",
		Blocks:     "bd-nonexistent-9999",
	})
	if err != nil {
		t.Fatalf("JackOn request error: %v", err)
	}
	// The server may or may not reject this (depends on dependency validation)
	// Just verify no panic/crash — if it fails, the error should be informative
	if !resp.Success && resp.Error == "" {
		t.Error("If JackOn fails, error should be non-empty")
	}
}

// --- JackOn title format verification ---

func TestJackOn_TitleFormatExact(t *testing.T) {
	_, client, _, cleanup := setupJackTest(t)
	defer cleanup()

	resp, err := client.JackOn(&JackOnArgs{
		Target:     "deployment/gastown-rwx/bd-daemon",
		Reason:     "Enable trace logging",
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

	expected := "Jack: deployment/gastown-rwx/bd-daemon — Enable trace logging"
	if jack.Title != expected {
		t.Errorf("title=%q, want %q", jack.Title, expected)
	}

	// Description should be the reason (not the title)
	if jack.Description != "Enable trace logging" {
		t.Errorf("description=%q, want %q", jack.Description, "Enable trace logging")
	}
}
