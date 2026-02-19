package rpc

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAPIKeyScopeRigAllowed(t *testing.T) {
	tests := []struct {
		name    string
		scope   APIKeyScope
		rig     string
		allowed bool
	}{
		{"empty rigs allows all", APIKeyScope{Rigs: nil}, "gastown", true},
		{"wildcard allows all", APIKeyScope{Rigs: []string{"*"}}, "gastown", true},
		{"specific rig match", APIKeyScope{Rigs: []string{"gastown"}}, "gastown", true},
		{"specific rig mismatch", APIKeyScope{Rigs: []string{"gastown"}}, "beads", false},
		{"empty rig always allowed", APIKeyScope{Rigs: []string{"gastown"}}, "", true},
		{"multiple rigs first match", APIKeyScope{Rigs: []string{"gastown", "beads"}}, "gastown", true},
		{"multiple rigs second match", APIKeyScope{Rigs: []string{"gastown", "beads"}}, "beads", true},
		{"multiple rigs no match", APIKeyScope{Rigs: []string{"gastown", "beads"}}, "other", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.IsRigAllowed(tt.rig); got != tt.allowed {
				t.Errorf("IsRigAllowed(%q) = %v, want %v", tt.rig, got, tt.allowed)
			}
		})
	}
}

func TestAPIKeyScopeOperationAllowed(t *testing.T) {
	tests := []struct {
		name      string
		scope     APIKeyScope
		operation string
		allowed   bool
	}{
		{"empty ops allows all", APIKeyScope{Operations: nil}, "create", true},
		{"wildcard allows all", APIKeyScope{Operations: []string{"*"}}, "create", true},
		{"specific op match", APIKeyScope{Operations: []string{"create", "update"}}, "create", true},
		{"specific op mismatch", APIKeyScope{Operations: []string{"create", "update"}}, "delete", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.IsOperationAllowed(tt.operation); got != tt.allowed {
				t.Errorf("IsOperationAllowed(%q) = %v, want %v", tt.operation, got, tt.allowed)
			}
		})
	}
}

func TestAuthPolicyValidateToken(t *testing.T) {
	policy := NewAuthPolicy()
	policy.AddKey("key-1", &APIKeyScope{Name: "gastown-agent", Rigs: []string{"gastown"}})
	policy.AddKey("key-2", &APIKeyScope{Name: "admin", Rigs: []string{"*"}})

	// Valid key
	scope := policy.ValidateToken("key-1")
	if scope == nil {
		t.Fatal("expected valid scope for key-1")
	}
	if scope.Name != "gastown-agent" {
		t.Errorf("expected name 'gastown-agent', got %q", scope.Name)
	}

	// Invalid key
	if policy.ValidateToken("bad-key") != nil {
		t.Error("expected nil scope for invalid key")
	}

	// Admin key allows any rig
	scope = policy.ValidateToken("key-2")
	if scope == nil {
		t.Fatal("expected valid scope for key-2")
	}
	if !scope.IsRigAllowed("any-rig") {
		t.Error("admin key should allow any rig")
	}
}

func TestLoadAuthPolicyFromEnv(t *testing.T) {
	// Test with no env var
	os.Unsetenv("BD_RPC_AUTH_KEYS")
	policy := LoadAuthPolicyFromEnv()
	if policy.HasKeys() {
		t.Error("expected no keys when env var not set")
	}

	// Test with valid JSON
	keys := map[string]*APIKeyScope{
		"token-abc": {Name: "gastown", Rigs: []string{"gastown"}, Operations: []string{"create", "update"}},
		"token-xyz": {Name: "admin", Rigs: []string{"*"}, Operations: []string{"*"}},
	}
	data, _ := json.Marshal(keys)
	os.Setenv("BD_RPC_AUTH_KEYS", string(data))
	defer os.Unsetenv("BD_RPC_AUTH_KEYS")

	policy = LoadAuthPolicyFromEnv()
	if !policy.HasKeys() {
		t.Fatal("expected keys to be loaded from env")
	}

	scope := policy.ValidateToken("token-abc")
	if scope == nil {
		t.Fatal("expected valid scope for token-abc")
	}
	if scope.Name != "gastown" {
		t.Errorf("expected name 'gastown', got %q", scope.Name)
	}
	if scope.IsRigAllowed("beads") {
		t.Error("gastown key should not access beads rig")
	}
	if !scope.IsRigAllowed("gastown") {
		t.Error("gastown key should access gastown rig")
	}
}

func TestExtractRigFromArgs(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		args      string
		expected  string
	}{
		{"target_rig field", "create", `{"target_rig":"gastown"}`, "gastown"},
		{"rig field", "create", `{"rig":"beads"}`, "beads"},
		{"no rig field", "list", `{"status":"open"}`, ""},
		{"empty args", "list", ``, ""},
		{"target_rig takes precedence", "create", `{"target_rig":"gastown","rig":"beads"}`, "gastown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractRigFromArgs(tt.operation, json.RawMessage(tt.args))
			if got != tt.expected {
				t.Errorf("ExtractRigFromArgs(%q, %s) = %q, want %q", tt.operation, tt.args, got, tt.expected)
			}
		})
	}
}

func TestIsReadOnlyOperation(t *testing.T) {
	readOps := []string{
		"show", "list", "count", "stats", "ready", "news", "blocked", "stale",
		"resolve_id", "get_mutations", "health", "info", "ping", "status",
		"activity", "search", "dep_list", "dep_show", "dep_tree",
		"get_labels", "show_config", "bus_status", "bus_handlers",
		"graph", "epic_status", "epic_overview", "epic_orphaned_children",
		"compact_stats", "export", "types", "dirty_count",
		"gate_list", "gate_show",
		"session_gate_check", "session_gate_list",
		"decision_get", "decision_list", "decision_list_recent",
		"mol_current", "mol_progress_stats", "mol_ready_gated",
		"config_list",
		"formula_list", "formula_get", "runbook_list", "runbook_get",
		"agent_pod_status", "agent_pod_list", "agent_roster", "agent_recent_events",
		"vcs_active_branch", "vcs_status", "vcs_branches", "vcs_log",
		"history_issue", "history_diff", "history_issue_diff",
		"batch_query_workers",
		// Prefix-based matches
		"get_worker_status", "get_config", "get_molecule_progress",
		"list_watch", "show_something",
	}
	writeOps := []string{
		"create", "update", "delete", "close", "comment_add", "move",
		"dep_add", "dep_remove", "label_add", "label_remove",
		"gate_create", "gate_close",
		"decision_create", "decision_resolve", "decision_cancel",
		"config_set", "config_unset",
		"import", "compact", "shutdown", "rename",
		"vcs_commit", "vcs_push", "vcs_pull",
	}

	for _, op := range readOps {
		if !isReadOnlyOperation(op) {
			t.Errorf("expected %q to be read-only", op)
		}
	}
	for _, op := range writeOps {
		if isReadOnlyOperation(op) {
			t.Errorf("expected %q to be write operation", op)
		}
	}
}
