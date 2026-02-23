package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// ============================================================================
// Jack Type Registration Tests (bd-luh2k)
//
// Verifies that the jack custom type is properly registered, persisted,
// and integrated with the type system (listing, validation, schema).
// ============================================================================

func TestJackTypeRegistration_InCustomTypes(t *testing.T) {
	_, _, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Register jack as custom type
	if err := store.SetConfig(ctx, "types.custom", "jack,molecule,gate"); err != nil {
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	// Verify jack appears in GetCustomTypes
	customTypes, err := store.GetCustomTypes(ctx)
	if err != nil {
		t.Fatalf("GetCustomTypes failed: %v", err)
	}

	found := false
	for _, ct := range customTypes {
		if ct == "jack" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("jack not found in custom types: %v", customTypes)
	}
}

func TestJackTypeRegistration_IsValidWithCustom(t *testing.T) {
	// TypeJack is NOT a built-in type
	if types.TypeJack.IsBuiltIn() {
		t.Error("TypeJack.IsBuiltIn() = true, want false")
	}
	if types.TypeJack.IsValid() {
		t.Error("TypeJack.IsValid() = true, want false (jack is custom, not built-in)")
	}

	// TypeJack IS valid when listed in custom types
	if !types.TypeJack.IsValidWithCustom([]string{"jack"}) {
		t.Error("TypeJack.IsValidWithCustom([jack]) = false, want true")
	}

	// TypeJack is NOT valid with an empty custom types list
	if types.TypeJack.IsValidWithCustom(nil) {
		t.Error("TypeJack.IsValidWithCustom(nil) = true, want false")
	}
	if types.TypeJack.IsValidWithCustom([]string{"molecule", "gate"}) {
		t.Error("TypeJack.IsValidWithCustom([molecule,gate]) = true, want false")
	}
}

func TestJackTypeRegistration_SchemaEnforcement(t *testing.T) {
	_, _, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Register jack type schema
	schema := types.JackSchema()
	if err := store.SetTypeSchema(ctx, "jack", schema); err != nil {
		t.Fatalf("SetTypeSchema failed: %v", err)
	}

	// Retrieve and verify schema persists
	got, err := store.GetTypeSchema(ctx, "jack")
	if err != nil {
		t.Fatalf("GetTypeSchema failed: %v", err)
	}
	if got == nil {
		t.Fatal("GetTypeSchema returned nil")
	}
	if len(got.RequiredFields) != 1 || got.RequiredFields[0] != "description" {
		t.Errorf("Schema RequiredFields = %v, want [description]", got.RequiredFields)
	}
	if len(got.RequiredLabels) != 1 || got.RequiredLabels[0] != "jack:*" {
		t.Errorf("Schema RequiredLabels = %v, want [jack:*]", got.RequiredLabels)
	}
}

func TestJackTypeRegistration_CreateAndList(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Register jack as a custom type (required for listing to include it)
	if err := store.SetConfig(ctx, "types.custom", "jack"); err != nil {
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	// Create a jack via JackOn
	jackID := createTestJack(t, client, "pod/test-daemon-abc", "Type registration test", "Remove debug logging")

	// List all issues and verify jack appears with correct type
	listResp, err := client.List(&ListArgs{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if !listResp.Success {
		t.Fatalf("List failed: %s", listResp.Error)
	}

	var issues []types.Issue
	if err := json.Unmarshal(listResp.Data, &issues); err != nil {
		t.Fatalf("Failed to unmarshal list: %v", err)
	}

	found := false
	for _, issue := range issues {
		if issue.ID == jackID {
			found = true
			if issue.IssueType != types.TypeJack {
				t.Errorf("Listed jack has type=%s, want %s", issue.IssueType, types.TypeJack)
			}
			if issue.Status != types.StatusInProgress {
				t.Errorf("Listed jack has status=%s, want %s", issue.Status, types.StatusInProgress)
			}
			break
		}
	}
	if !found {
		t.Errorf("Jack %s not found in list of %d issues", jackID, len(issues))
	}
}

func TestJackTypeRegistration_ShowPreservesType(t *testing.T) {
	_, client, _, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	// Create a jack
	jackID := createTestJack(t, client, "deploy/staging-v2", "Verify type preserved through show", "Rollback deploy")

	// Show the jack and verify type is preserved
	showResp, err := client.Show(&ShowArgs{ID: jackID})
	if err != nil {
		t.Fatalf("Show failed: %v", err)
	}
	if !showResp.Success {
		t.Fatalf("Show failed: %s", showResp.Error)
	}

	var details types.IssueDetails
	if err := json.Unmarshal(showResp.Data, &details); err != nil {
		t.Fatalf("Failed to unmarshal show: %v", err)
	}

	if details.Issue.IssueType != types.TypeJack {
		t.Errorf("Show returned type=%s, want %s", details.Issue.IssueType, types.TypeJack)
	}

	// Verify metadata is populated
	if len(details.Issue.Metadata) == 0 {
		t.Error("Show returned empty metadata, expected jack metadata")
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(details.Issue.Metadata, &meta); err != nil {
		t.Fatalf("Failed to unmarshal metadata: %v", err)
	}
	if meta["jack_target"] != "deploy/staging-v2" {
		t.Errorf("Metadata jack_target=%v, want deploy/staging-v2", meta["jack_target"])
	}
	if meta["jack_revert_plan"] != "Rollback deploy" {
		t.Errorf("Metadata jack_revert_plan=%v, want 'Rollback deploy'", meta["jack_revert_plan"])
	}
}

func TestJackTypeRegistration_TypeFilterInList(t *testing.T) {
	_, client, store, cleanup := setupTestServerWithStore(t)
	defer cleanup()

	ctx := context.Background()

	// Register jack type
	if err := store.SetConfig(ctx, "types.custom", "jack"); err != nil {
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	// Create a jack and a regular task
	jackID := createTestJack(t, client, "pod/filter-test", "Filter test jack", "Revert filter test")

	taskResp, err := client.Create(&CreateArgs{
		Title:     "Regular task for filter test",
		IssueType: string(types.TypeTask),
		Priority:  2,
	})
	if err != nil {
		t.Fatalf("Create task failed: %v", err)
	}
	if !taskResp.Success {
		t.Fatalf("Create task failed: %s", taskResp.Error)
	}

	// List filtering by type=jack should only return the jack
	listResp, err := client.List(&ListArgs{IssueType: string(types.TypeJack)})
	if err != nil {
		t.Fatalf("List with type filter failed: %v", err)
	}
	if !listResp.Success {
		t.Fatalf("List with type filter failed: %s", listResp.Error)
	}

	var issues []types.Issue
	if err := json.Unmarshal(listResp.Data, &issues); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("Expected 1 jack, got %d issues", len(issues))
	}
	if issues[0].ID != jackID {
		t.Errorf("Filtered list returned %s, want %s", issues[0].ID, jackID)
	}
	if issues[0].IssueType != types.TypeJack {
		t.Errorf("Filtered issue type=%s, want %s", issues[0].IssueType, types.TypeJack)
	}
}

func TestJackTypeRegistration_ConstantsAndLabels(t *testing.T) {
	// Verify TypeJack constant value
	if string(types.TypeJack) != "jack" {
		t.Errorf("TypeJack = %q, want %q", types.TypeJack, "jack")
	}

	// Verify jack label prefix
	expectedLabels := map[string]string{
		"jack:debug":      types.LabelJackDebug,
		"jack:hotfix":     types.LabelJackHotfix,
		"jack:failover":   types.LabelJackFailover,
		"jack:config":     types.LabelJackConfig,
		"jack:experiment": types.LabelJackExperiment,
	}
	for expected, actual := range expectedLabels {
		if actual != expected {
			t.Errorf("Label constant %q = %q, want %q", expected, actual, expected)
		}
	}
}
