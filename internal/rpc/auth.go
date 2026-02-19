package rpc

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// AuthPolicy defines per-rig API key authorization.
// Keys can be scoped to specific rigs and operations.
type AuthPolicy struct {
	mu   sync.RWMutex
	keys map[string]*APIKeyScope // token -> scope
}

// APIKeyScope defines what a specific API key is authorized to access.
type APIKeyScope struct {
	Name       string   `json:"name"`                 // Human-readable key name (for audit)
	Rigs       []string `json:"rigs,omitempty"`        // Allowed rig names ("*" = all)
	Operations []string `json:"operations,omitempty"`  // Allowed operations ("*" = all)
}

// NewAuthPolicy creates an empty auth policy.
func NewAuthPolicy() *AuthPolicy {
	return &AuthPolicy{
		keys: make(map[string]*APIKeyScope),
	}
}

// AddKey registers an API key with its scope.
func (p *AuthPolicy) AddKey(token string, scope *APIKeyScope) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys[token] = scope
}

// HasKeys returns true if any per-rig keys are configured.
func (p *AuthPolicy) HasKeys() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.keys) > 0
}

// ValidateToken checks if a token is valid and returns its scope.
// Returns nil if the token is not recognized.
func (p *AuthPolicy) ValidateToken(token string) *APIKeyScope {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.keys[token]
}

// IsRigAllowed checks if a scope permits access to a specific rig.
// Empty rig (local/town-level operations) is always allowed.
func (s *APIKeyScope) IsRigAllowed(rig string) bool {
	if rig == "" {
		return true // Town-level operations are always allowed
	}
	if len(s.Rigs) == 0 {
		return true // No rig restrictions = all rigs allowed
	}
	for _, allowed := range s.Rigs {
		if allowed == "*" || allowed == rig {
			return true
		}
	}
	return false
}

// IsOperationAllowed checks if a scope permits a specific RPC operation.
func (s *APIKeyScope) IsOperationAllowed(operation string) bool {
	if len(s.Operations) == 0 {
		return true // No operation restrictions = all operations allowed
	}
	for _, allowed := range s.Operations {
		if allowed == "*" || allowed == operation {
			return true
		}
	}
	return false
}

// LoadAuthPolicyFromEnv loads per-rig API keys from BD_RPC_AUTH_KEYS env var.
// Format: JSON object mapping tokens to scopes.
// Example: {"key1": {"name": "gastown-agent", "rigs": ["gastown"], "operations": ["*"]}}
func LoadAuthPolicyFromEnv() *AuthPolicy {
	policy := NewAuthPolicy()

	keysJSON := os.Getenv("BD_RPC_AUTH_KEYS")
	if keysJSON == "" {
		return policy
	}

	var keys map[string]*APIKeyScope
	if err := json.Unmarshal([]byte(keysJSON), &keys); err != nil {
		return policy
	}

	for token, scope := range keys {
		policy.AddKey(token, scope)
	}

	return policy
}

// ExtractRigFromArgs extracts the target rig from RPC operation args.
// Returns empty string if no rig is specified (town-level operation).
func ExtractRigFromArgs(operation string, args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}

	// Many args have a TargetRig field
	var rigArgs struct {
		TargetRig string `json:"target_rig"`
		Rig       string `json:"rig"`
	}
	if err := json.Unmarshal(args, &rigArgs); err == nil {
		if rigArgs.TargetRig != "" {
			return rigArgs.TargetRig
		}
		if rigArgs.Rig != "" {
			return rigArgs.Rig
		}
	}

	// For operations on specific issues, extract rig from issue ID prefix
	var idArgs struct {
		ID  string `json:"id"`
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(args, &idArgs); err == nil {
		if idArgs.ID != "" {
			return extractRigFromID(idArgs.ID)
		}
		if len(idArgs.IDs) > 0 {
			return extractRigFromID(idArgs.IDs[0])
		}
	}

	return ""
}

// extractRigFromID extracts a rig hint from an issue ID.
// This is best-effort — the rig is only available if the ID prefix maps to a known rig.
// Returns empty string for unknown mappings.
func extractRigFromID(id string) string {
	// Issue IDs are prefixed: "gt-abc123", "bd-xyz", "hq-123"
	// The prefix alone doesn't tell us the rig — that requires route resolution.
	// For auth purposes, we rely on TargetRig in the args rather than ID prefix.
	_ = id
	return ""
}

// AuditEntry represents a single RPC audit log entry.
type AuditEntry struct {
	Timestamp string `json:"timestamp"`
	Operation string `json:"operation"`
	Actor     string `json:"actor"`
	KeyName   string `json:"key_name,omitempty"` // API key name (if authenticated via per-rig key)
	Rig       string `json:"rig,omitempty"`      // Target rig (if applicable)
	IssueID   string `json:"issue_id,omitempty"` // Primary issue ID (if applicable)
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// ExtractIssueIDFromArgs extracts the primary issue ID from RPC args.
func ExtractIssueIDFromArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var idArgs struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &idArgs); err == nil && idArgs.ID != "" {
		return idArgs.ID
	}
	return ""
}

// isReadOnlyOperation returns true for operations that only read data.
func isReadOnlyOperation(operation string) bool {
	switch operation {
	case "show", "list", "count", "stats", "ready", "news", "blocked", "stale",
		"resolve_id", "get_mutations", "health", "info", "ping", "status",
		"activity", "search", "dep_list", "dep_show", "dep_tree",
		"get_labels", "show_config", "bus_status", "bus_handlers",
		"slot_get", "slot_list",
		"graph", "epic_status", "epic_overview", "epic_orphaned_children",
		"compact_stats", "export", "types", "dirty_count",
		"gate_list", "gate_show",
		"session_gate_check", "session_gate_list",
		"decision_get", "decision_list", "decision_list_recent",
		"mol_current", "mol_progress_stats", "mol_ready_gated",
		"config_list",
		"sync_status",
		"formula_list", "formula_get", "runbook_list", "runbook_get",
		"agent_pod_status", "agent_pod_list", "agent_roster", "agent_recent_events",
		"vcs_active_branch", "vcs_status", "vcs_has_uncommitted",
		"vcs_branches", "vcs_current_commit", "vcs_commit_exists", "vcs_log",
		"fed_list_remotes", "fed_sync_status",
		"inbox_list",
		"session_list",
		"history_issue", "history_diff", "history_issue_diff",
		"history_conflicts", "versioned_diff",
		"sql",
		"batch_query_workers",
		"burndown", "velocity", "cycle_time":
		return true
	}
	return strings.HasPrefix(operation, "get_") ||
		strings.HasPrefix(operation, "list_") ||
		strings.HasPrefix(operation, "show_")
}
