package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// mockNudgeStore implements NudgeStore for testing. (gt-2md5kf)
type mockNudgeStore struct {
	issues map[string]*types.Issue
}

func (m *mockNudgeStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if issue, ok := m.issues[id]; ok {
		return issue, nil
	}
	return nil, fmt.Errorf("issue %q not found", id)
}

func TestMailNudgeHandlerMetadata(t *testing.T) {
	h := &MailNudgeHandler{}
	if h.ID() != "mail-nudge" {
		t.Errorf("ID() = %q, want %q", h.ID(), "mail-nudge")
	}
	if h.Priority() != 50 {
		t.Errorf("Priority() = %d, want 50", h.Priority())
	}
	if len(h.Handles()) != 1 || h.Handles()[0] != EventMailSent {
		t.Errorf("Handles() = %v, want [MailSent]", h.Handles())
	}
}

func TestMailNudgeHandler_EmptyTo(t *testing.T) {
	h := &MailNudgeHandler{}
	payload := MailEventPayload{From: "mayor/", To: "", Subject: "hello"}
	raw, _ := json.Marshal(payload)
	event := &Event{Type: EventMailSent, Raw: raw}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Errorf("expected nil error for empty To, got: %v", err)
	}
}

func TestMailNudgeHandler_BadPayload(t *testing.T) {
	h := &MailNudgeHandler{}
	event := &Event{Type: EventMailSent, Raw: json.RawMessage(`{invalid`)}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err == nil {
		t.Error("expected error for invalid JSON payload")
	}
}

func TestMailAddressToAgentID(t *testing.T) {
	tests := []struct {
		address string
		want    string
	}{
		// Town-level agents
		{"mayor", "hq-mayor"},
		{"mayor/", "hq-mayor"},
		{"deacon", "hq-deacon"},
		{"deacon/", "hq-deacon"},

		// Rig-scoped: known roles
		{"gastown/witness", "gt-gastown-witness"},
		{"gastown/refinery", "gt-gastown-refinery"},

		// Rig-scoped: default to polecat
		{"gastown/Toast", "gt-gastown-polecat-Toast"},
		{"dev/worker1", "gt-dev-polecat-worker1"},

		// Three-part: explicit polecats
		{"gastown/polecats/Toast", "gt-gastown-polecat-Toast"},
		{"dev/polecats/p1", "gt-dev-polecat-p1"},

		// Three-part: crew
		{"gastown/crew/max", "gt-gastown-crew-max"},
		{"hq/crew/test", "gt-hq-crew-test"},

		// Unrecognized patterns
		{"", ""},
		{"a/b/c/d", ""},           // Too many parts
		{"gastown/unknown/foo", ""}, // Unknown middle component
	}

	for _, tt := range tests {
		got := mailAddressToAgentID(tt.address)
		if got != tt.want {
			t.Errorf("mailAddressToAgentID(%q) = %q, want %q", tt.address, got, tt.want)
		}
	}
}

func TestMailAddressToLabels(t *testing.T) {
	tests := []struct {
		address string
		want    []string
	}{
		// Town-level agents
		{"mayor", []string{"gt:agent", "role_type:mayor"}},
		{"mayor/", []string{"gt:agent", "role_type:mayor"}},
		{"deacon", []string{"gt:agent", "role_type:deacon"}},

		// Rig-scoped: known singleton roles
		{"gastown/witness", []string{"gt:agent", "rig:gastown", "role_type:witness"}},
		{"gastown/refinery", []string{"gt:agent", "rig:gastown", "role_type:refinery"}},

		// Rig-scoped: default to polecat
		{"gastown/Toast", []string{"gt:agent", "rig:gastown", "role_type:polecat"}},

		// Three-part: explicit polecats
		{"gastown/polecats/Toast", []string{"gt:agent", "rig:gastown", "role_type:polecat"}},

		// Three-part: crew
		{"gastown/crew/max", []string{"gt:agent", "rig:gastown", "role_type:crew"}},

		// Empty / invalid
		{"", nil},
		{"a/b/c/d", nil},
		{"gastown/unknown/foo", nil},
	}

	for _, tt := range tests {
		got := mailAddressToLabels(tt.address)
		if len(got) != len(tt.want) {
			t.Errorf("mailAddressToLabels(%q) = %v, want %v", tt.address, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("mailAddressToLabels(%q)[%d] = %q, want %q", tt.address, i, got[i], tt.want[i])
			}
		}
	}
}

// mockLabelNudgeStore extends mockNudgeStore with label query support for
// testing label-based mail routing. (bd-p1mt)
type mockLabelNudgeStore struct {
	mockNudgeStore
	labels map[string][]string // issueID → labels
}

func (m *mockLabelNudgeStore) GetIssuesByLabel(_ context.Context, label string) ([]*types.Issue, error) {
	var results []*types.Issue
	for id, labels := range m.labels {
		if slices.Contains(labels, label) {
			if issue, ok := m.issues[id]; ok {
				results = append(results, issue)
			}
		}
	}
	return results, nil
}

func (m *mockLabelNudgeStore) GetLabels(_ context.Context, issueID string) ([]string, error) {
	if labels, ok := m.labels[issueID]; ok {
		return labels, nil
	}
	return nil, nil
}

func TestResolveCoopURLFromStore_LabelFallback(t *testing.T) {
	// Agent bead ID doesn't match the hardcoded pattern, but labels match.
	store := &mockLabelNudgeStore{
		mockNudgeStore: mockNudgeStore{
			issues: map[string]*types.Issue{
				"custom-gastown-scout": {
					ID:    "custom-gastown-scout",
					Notes: "coop_url: http://10.0.0.7:3000",
				},
			},
		},
		labels: map[string][]string{
			"custom-gastown-scout": {"gt:agent", "rig:gastown", "role_type:witness"},
		},
	}

	// "gastown/witness" would normally resolve to "gt-gastown-witness" via
	// mailAddressToAgentID, but that bead doesn't exist. The label-based
	// fallback should find "custom-gastown-scout" by label match.
	url, err := resolveCoopURLFromStore(context.Background(), store, "gastown/witness")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "http://10.0.0.7:3000" {
		t.Errorf("got %q, want %q", url, "http://10.0.0.7:3000")
	}
}

func TestMailNudgeHandler_PostNudge_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/nudge" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["message"] == "" {
			t.Error("expected non-empty message in nudge request")
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"delivered":    true,
			"state_before": "idle",
		})
	}))
	defer server.Close()

	h := &MailNudgeHandler{httpClient: server.Client()}
	delivered, reason, err := postNudge(context.Background(), h.httpClient, server.URL, "You have new mail")
	if err != nil {
		t.Fatalf("postNudge error: %v", err)
	}
	if !delivered {
		t.Errorf("expected delivered=true, got false (reason: %s)", reason)
	}
}

func TestMailNudgeHandler_PostNudge_NotDelivered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"delivered":    false,
			"state_before": "working",
			"reason":       "agent is busy",
		})
	}))
	defer server.Close()

	h := &MailNudgeHandler{httpClient: server.Client()}
	delivered, reason, err := postNudge(context.Background(), h.httpClient, server.URL, "test")
	if err != nil {
		t.Fatalf("postNudge error: %v", err)
	}
	if delivered {
		t.Error("expected delivered=false")
	}
	if reason != "agent is busy" {
		t.Errorf("expected reason=%q, got %q", "agent is busy", reason)
	}
}

func TestMailNudgeHandler_PostNudge_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	h := &MailNudgeHandler{httpClient: server.Client()}
	_, _, err := postNudge(context.Background(), h.httpClient, server.URL, "test")
	if err == nil {
		t.Error("expected error for HTTP 500")
	}
}

func TestPostNudge_ConnectionRefused(t *testing.T) {
	_, _, err := postNudge(context.Background(), nil, "http://127.0.0.1:1", "test")
	if err == nil {
		t.Error("expected error for connection refused")
	}
}

func TestDefaultMailHandlers(t *testing.T) {
	handlers := DefaultMailHandlers()
	if len(handlers) != 2 {
		t.Fatalf("expected 2 handlers, got %d", len(handlers))
	}
	if handlers[0].ID() != "mail-nudge" {
		t.Errorf("expected handler[0] ID %q, got %q", "mail-nudge", handlers[0].ID())
	}
	if handlers[1].ID() != "decision-nudge" {
		t.Errorf("expected handler[1] ID %q, got %q", "decision-nudge", handlers[1].ID())
	}
}

// TestMailNudgeInDefaultHandlers verifies the mail nudge handler is included
// in the default handler set registered at daemon startup.
func TestMailNudgeInDefaultHandlers(t *testing.T) {
	handlers := DefaultHandlers()
	found := false
	for _, h := range handlers {
		if h.ID() == "mail-nudge" {
			found = true
			break
		}
	}
	if !found {
		t.Error("mail-nudge handler not found in DefaultHandlers()")
	}
}

func TestDecisionNudgeHandlerMetadata(t *testing.T) {
	h := &DecisionNudgeHandler{}
	if h.ID() != "decision-nudge" {
		t.Errorf("ID() = %q, want %q", h.ID(), "decision-nudge")
	}
	if h.Priority() != 50 {
		t.Errorf("Priority() = %d, want 50", h.Priority())
	}
	if len(h.Handles()) != 1 || h.Handles()[0] != EventDecisionResponded {
		t.Errorf("Handles() = %v, want [DecisionResponded]", h.Handles())
	}
}

func TestDecisionNudgeHandler_EmptyRequestedBy(t *testing.T) {
	h := &DecisionNudgeHandler{}
	payload := DecisionEventPayload{DecisionID: "test-123", RequestedBy: ""}
	raw, _ := json.Marshal(payload)
	event := &Event{Type: EventDecisionResponded, Raw: raw}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Errorf("expected nil error for empty RequestedBy, got: %v", err)
	}
}

func TestDecisionNudgeHandler_BadPayload(t *testing.T) {
	h := &DecisionNudgeHandler{}
	event := &Event{Type: EventDecisionResponded, Raw: json.RawMessage(`{invalid`)}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err == nil {
		t.Error("expected error for invalid JSON payload")
	}
}

func TestDecisionNudgeInDefaultHandlers(t *testing.T) {
	handlers := DefaultHandlers()
	found := false
	for _, h := range handlers {
		if h.ID() == "decision-nudge" {
			found = true
			break
		}
	}
	if !found {
		t.Error("decision-nudge handler not found in DefaultHandlers()")
	}
}

// TestMailEventSubjectRouting verifies MailSent events route to the mail.> subject.
func TestMailEventSubjectRouting(t *testing.T) {
	subject := SubjectForEvent(EventMailSent)
	if subject != "mail.MailSent" {
		t.Errorf("SubjectForEvent(MailSent) = %q, want %q", subject, "mail.MailSent")
	}
}

// TestMailEventIsMailEvent verifies MailSent is classified correctly.
func TestMailEventIsMailEvent(t *testing.T) {
	if !EventMailSent.IsMailEvent() {
		t.Error("expected EventMailSent.IsMailEvent() = true")
	}
	if EventSessionStart.IsMailEvent() {
		t.Error("expected EventSessionStart.IsMailEvent() = false")
	}
	if EventOjJobCreated.IsMailEvent() {
		t.Error("expected EventOjJobCreated.IsMailEvent() = false")
	}
}

// --- resolveCoopURLFromStore tests (gt-2md5kf) ---

func TestResolveCoopURLFromStore_DirectLookup(t *testing.T) {
	store := &mockNudgeStore{
		issues: map[string]*types.Issue{
			"hq-mayor": {ID: "hq-mayor", Notes: "coop_url: http://localhost:8080\nother: val"},
		},
	}
	url, err := resolveCoopURLFromStore(context.Background(), store, "hq-mayor")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "http://localhost:8080" {
		t.Errorf("got %q, want %q", url, "http://localhost:8080")
	}
}

func TestResolveCoopURLFromStore_NameConversion(t *testing.T) {
	store := &mockNudgeStore{
		issues: map[string]*types.Issue{
			"hq-mayor": {ID: "hq-mayor", Notes: "coop_url: http://10.0.0.5:3000"},
		},
	}
	// "mayor/" is the actor name, should be converted to "hq-mayor" via mailAddressToAgentID.
	url, err := resolveCoopURLFromStore(context.Background(), store, "mayor/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "http://10.0.0.5:3000" {
		t.Errorf("got %q, want %q", url, "http://10.0.0.5:3000")
	}
}

func TestResolveCoopURLFromStore_PoleactConversion(t *testing.T) {
	store := &mockNudgeStore{
		issues: map[string]*types.Issue{
			"gt-gastown-polecat-furiosa": {ID: "gt-gastown-polecat-furiosa", Notes: "coop_url: http://10.0.0.6:3000"},
		},
	}
	// "gastown/furiosa" → "gt-gastown-polecat-furiosa"
	url, err := resolveCoopURLFromStore(context.Background(), store, "gastown/furiosa")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "http://10.0.0.6:3000" {
		t.Errorf("got %q, want %q", url, "http://10.0.0.6:3000")
	}
}

func TestResolveCoopURLFromStore_NoCoop(t *testing.T) {
	store := &mockNudgeStore{
		issues: map[string]*types.Issue{
			"hq-mayor": {ID: "hq-mayor", Notes: "some_other_key: value"},
		},
	}
	_, err := resolveCoopURLFromStore(context.Background(), store, "mayor/")
	if err == nil {
		t.Error("expected error when no coop_url in notes")
	}
}

func TestResolveCoopURLFromStore_NotFound(t *testing.T) {
	store := &mockNudgeStore{issues: map[string]*types.Issue{}}
	_, err := resolveCoopURLFromStore(context.Background(), store, "unknown-agent")
	if err == nil {
		t.Error("expected error when agent bead not found")
	}
}

// --- DecisionNudgeHandler with store-based resolution (gt-2md5kf) ---

func TestDecisionNudgeHandler_WithStore(t *testing.T) {
	var nudgeReceived atomic.Bool
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nudgeReceived.Store(true)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"delivered": true, "state_before": "idle"})
	}))
	defer coopServer.Close()

	store := &mockNudgeStore{
		issues: map[string]*types.Issue{
			"hq-mayor": {ID: "hq-mayor", Notes: "coop_url: " + coopServer.URL},
		},
	}

	h := &DecisionNudgeHandler{HTTPClient: coopServer.Client()}
	h.SetNudgeStore(store)

	payload := DecisionEventPayload{
		DecisionID:  "test-dec-1",
		RequestedBy: "mayor/",
		ChosenLabel: "deploy",
		ResolvedBy:  "human",
	}
	raw, _ := json.Marshal(payload)
	event := &Event{Type: EventDecisionResponded, Raw: raw}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	if !nudgeReceived.Load() {
		t.Error("expected nudge to be received by mock Coop server")
	}
}

func TestDecisionNudgeHandler_StoreNoCoopURL(t *testing.T) {
	store := &mockNudgeStore{
		issues: map[string]*types.Issue{
			"hq-mayor": {ID: "hq-mayor", Notes: "no_coop: true"},
		},
	}

	h := &DecisionNudgeHandler{}
	h.SetNudgeStore(store)

	payload := DecisionEventPayload{
		DecisionID:  "test-dec-2",
		RequestedBy: "mayor/",
		ChosenLabel: "abort",
		ResolvedBy:  "human",
	}
	raw, _ := json.Marshal(payload)
	event := &Event{Type: EventDecisionResponded, Raw: raw}
	result := &Result{}

	// Should not error — just logs and returns nil.
	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("Handle should not error when coop_url missing: %v", err)
	}
}

func TestDecisionNudgeHandler_ExplicitResolverTakesPrecedence(t *testing.T) {
	var nudgeReceived atomic.Bool
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nudgeReceived.Store(true)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"delivered": true})
	}))
	defer coopServer.Close()

	// Even with store set, explicit resolver should take precedence.
	store := &mockNudgeStore{issues: map[string]*types.Issue{}}
	h := &DecisionNudgeHandler{
		HTTPClient: coopServer.Client(),
		CoopURLResolver: func(_ context.Context, _, _ string) (string, error) {
			return coopServer.URL, nil
		},
	}
	h.SetNudgeStore(store)

	payload := DecisionEventPayload{
		DecisionID:  "test-dec-3",
		RequestedBy: "any-agent",
		ChosenLabel: "go",
		ResolvedBy:  "human",
	}
	raw, _ := json.Marshal(payload)
	event := &Event{Type: EventDecisionResponded, Raw: raw}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("Handle error: %v", err)
	}
	if !nudgeReceived.Load() {
		t.Error("expected nudge via explicit resolver")
	}
}
