package runbook

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestRunbookToIssue(t *testing.T) {
	rb := &RunbookContent{
		Name:     "my-runbook",
		Format:   "hcl",
		Content:  `job "build" {}`,
		Jobs:     []string{"build"},
		Commands: []string{"plan"},
	}

	issue, labels, err := RunbookToIssue(rb, "od-")
	if err != nil {
		t.Fatalf("RunbookToIssue() error: %v", err)
	}

	if issue.ID != "od-runbook-my-runbook" {
		t.Errorf("ID = %q, want %q", issue.ID, "od-runbook-my-runbook")
	}
	if issue.Title != "my-runbook" {
		t.Errorf("Title = %q, want %q", issue.Title, "my-runbook")
	}
	if issue.IssueType != types.TypeRunbook {
		t.Errorf("IssueType = %q, want %q", issue.IssueType, types.TypeRunbook)
	}
	if !issue.IsTemplate {
		t.Error("IsTemplate = false, want true")
	}
	if len(issue.Metadata) == 0 {
		t.Error("Metadata is empty")
	}

	// Check labels
	labelSet := make(map[string]bool)
	for _, l := range labels {
		labelSet[l] = true
	}
	if !labelSet["format:hcl"] {
		t.Error("missing label format:hcl")
	}
	if !labelSet["job:build"] {
		t.Error("missing label job:build")
	}
	if !labelSet["cmd:plan"] {
		t.Error("missing label cmd:plan")
	}
}

func TestRunbookToIssueNil(t *testing.T) {
	_, _, err := RunbookToIssue(nil, "")
	if err == nil {
		t.Error("expected error for nil runbook")
	}
}

func TestRunbookToIssueEmptyName(t *testing.T) {
	rb := &RunbookContent{Format: "hcl", Content: "test"}
	_, _, err := RunbookToIssue(rb, "")
	if err == nil {
		t.Error("expected error for empty name")
	}
}

func TestIssueToRunbook(t *testing.T) {
	rb := &RunbookContent{
		Name:    "test-runbook",
		Format:  "hcl",
		Content: `command "deploy" { run = "echo deploy" }`,
	}

	issue, _, err := RunbookToIssue(rb, "od-")
	if err != nil {
		t.Fatalf("RunbookToIssue() error: %v", err)
	}

	// Roundtrip
	got, err := IssueToRunbook(issue)
	if err != nil {
		t.Fatalf("IssueToRunbook() error: %v", err)
	}

	if got.Name != rb.Name {
		t.Errorf("Name = %q, want %q", got.Name, rb.Name)
	}
	if got.Content != rb.Content {
		t.Errorf("Content mismatch")
	}
	if got.Format != rb.Format {
		t.Errorf("Format = %q, want %q", got.Format, rb.Format)
	}
	if got.Source != "bead:od-runbook-test-runbook" {
		t.Errorf("Source = %q, want bead prefix", got.Source)
	}
}

func TestIssueToRunbookWrongType(t *testing.T) {
	issue := &types.Issue{IssueType: types.TypeTask}
	_, err := IssueToRunbook(issue)
	if err == nil {
		t.Error("expected error for wrong type")
	}
}

func TestParseRunbookFile(t *testing.T) {
	content := `
command "plan" {
  args = "<name>"
  run  = { job = "plan" }
}

command "deploy" {
  run = { job = "deploy" }
}

job "plan" {
  vars = ["name"]
  step "build" {
    run = { agent = "claude" }
  }
}

job "deploy" {
  step "push" {
    run = "git push"
  }
}

worker "bug" {
  source  = { queue = "bugs" }
  handler = { job = "bug" }
}

cron "nightly" {
  schedule = "0 0 * * *"
  run      = { job = "backup" }
}

queue "bugs" {
  type = "external"
}
`
	rb := ParseRunbookFile("test", content, "hcl")

	if len(rb.Jobs) != 2 {
		t.Errorf("Jobs count = %d, want 2, got %v", len(rb.Jobs), rb.Jobs)
	}
	if len(rb.Commands) != 2 {
		t.Errorf("Commands count = %d, want 2, got %v", len(rb.Commands), rb.Commands)
	}
	if len(rb.Workers) != 1 {
		t.Errorf("Workers count = %d, want 1", len(rb.Workers))
	}
	if len(rb.Crons) != 1 {
		t.Errorf("Crons count = %d, want 1", len(rb.Crons))
	}
	if len(rb.Queues) != 1 {
		t.Errorf("Queues count = %d, want 1", len(rb.Queues))
	}
}

func TestNameToSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-runbook", "my-runbook"},
		{"My Runbook", "my-runbook"},
		{"test_runbook.hcl", "test-runbook-hcl"},
		{"UPPER", "upper"},
		{"a--b", "a-b"},
	}
	for _, tt := range tests {
		got := nameToSlug(tt.input)
		if got != tt.want {
			t.Errorf("nameToSlug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestScopeSpecificity(t *testing.T) {
	tests := []struct {
		scope string
		want  int
	}{
		{"", ScopeGlobal},
		{"global", ScopeGlobal},
		{"town:mytown", ScopeTown},
		{"rig:beads", ScopeRig},
		{"role:polecat", ScopeRole},
		{"agent:beads/polecats/garnet", ScopeAgent},
		{"unknown", ScopeGlobal},
	}
	for _, tt := range tests {
		got := ScopeSpecificity(tt.scope)
		if got != tt.want {
			t.Errorf("ScopeSpecificity(%q) = %d, want %d", tt.scope, got, tt.want)
		}
	}
}

func TestScopeSpecificityOrdering(t *testing.T) {
	// Verify that more specific scopes have higher scores
	if ScopeSpecificity("global") >= ScopeSpecificity("town:x") {
		t.Error("global should be less specific than town")
	}
	if ScopeSpecificity("town:x") >= ScopeSpecificity("rig:x") {
		t.Error("town should be less specific than rig")
	}
	if ScopeSpecificity("rig:x") >= ScopeSpecificity("role:x") {
		t.Error("rig should be less specific than role")
	}
	if ScopeSpecificity("role:x") >= ScopeSpecificity("agent:x") {
		t.Error("role should be less specific than agent")
	}
}

func TestScopeLabel(t *testing.T) {
	tests := []struct {
		scope string
		want  string
	}{
		{"", "scope:global"},
		{"global", "scope:global"},
		{"rig:beads", "scope:rig:beads"},
		{"role:polecat", "scope:role:polecat"},
		{"town:mytown", "scope:town:mytown"},
		{"agent:beads/polecats/garnet", "scope:agent:beads/polecats/garnet"},
	}
	for _, tt := range tests {
		got := ScopeLabel(tt.scope)
		if got != tt.want {
			t.Errorf("ScopeLabel(%q) = %q, want %q", tt.scope, got, tt.want)
		}
	}
}

func TestParseScopeFromLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"no labels", nil, "global"},
		{"no scope labels", []string{"format:hcl", "job:build"}, "global"},
		{"global scope", []string{"scope:global"}, "global"},
		{"rig scope", []string{"scope:rig:beads", "format:hcl"}, "rig:beads"},
		{"role scope", []string{"scope:role:polecat"}, "role:polecat"},
		{"agent scope", []string{"scope:agent:beads/polecats/garnet"}, "agent:beads/polecats/garnet"},
		{"most specific wins", []string{"scope:global", "scope:rig:beads", "scope:role:polecat"}, "role:polecat"},
		{"town scope", []string{"scope:town:mytown"}, "town:mytown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseScopeFromLabels(tt.labels)
			if got != tt.want {
				t.Errorf("ParseScopeFromLabels(%v) = %q, want %q", tt.labels, got, tt.want)
			}
		})
	}
}

func TestRunbookToIssueScopeLabel(t *testing.T) {
	rb := &RunbookContent{
		Name:    "my-runbook",
		Format:  "hcl",
		Content: `job "build" {}`,
		Jobs:    []string{"build"},
		Scope:   "rig:beads",
	}

	_, labels, err := RunbookToIssue(rb, "od-")
	if err != nil {
		t.Fatalf("RunbookToIssue() error: %v", err)
	}

	labelSet := make(map[string]bool)
	for _, l := range labels {
		labelSet[l] = true
	}

	if !labelSet["scope:rig:beads"] {
		t.Errorf("missing scope label, got labels: %v", labels)
	}
}

func TestRunbookToIssueDefaultScope(t *testing.T) {
	rb := &RunbookContent{
		Name:    "my-runbook",
		Format:  "hcl",
		Content: `job "build" {}`,
	}

	_, labels, err := RunbookToIssue(rb, "od-")
	if err != nil {
		t.Fatalf("RunbookToIssue() error: %v", err)
	}

	labelSet := make(map[string]bool)
	for _, l := range labels {
		labelSet[l] = true
	}

	if !labelSet["scope:global"] {
		t.Errorf("missing default scope label, got labels: %v", labels)
	}
}

func TestScopeRoundtrip(t *testing.T) {
	rb := &RunbookContent{
		Name:    "scoped-runbook",
		Format:  "hcl",
		Content: `job "test" {}`,
		Scope:   "role:polecat",
	}

	issue, _, err := RunbookToIssue(rb, "od-")
	if err != nil {
		t.Fatalf("RunbookToIssue() error: %v", err)
	}

	got, err := IssueToRunbook(issue)
	if err != nil {
		t.Fatalf("IssueToRunbook() error: %v", err)
	}

	if got.Scope != "role:polecat" {
		t.Errorf("Scope = %q, want %q", got.Scope, "role:polecat")
	}
}
