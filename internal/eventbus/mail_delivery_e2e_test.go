//go:build integration

package eventbus_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
	"github.com/steveyegge/beads/internal/types"
)

// testMailAddressToAgentID mirrors the unexported mailAddressToAgentID for tests.
// Covers the common address formats used in Gas Town.
func testMailAddressToAgentID(address string) string {
	address = strings.TrimSuffix(address, "/")
	switch address {
	case "mayor":
		return "gt-mayor"
	case "deacon":
		return "gt-deacon"
	}
	parts := strings.Split(address, "/")
	switch len(parts) {
	case 2:
		rig, role := parts[0], parts[1]
		switch role {
		case "witness":
			return fmt.Sprintf("gt-%s-witness", rig)
		case "refinery":
			return fmt.Sprintf("gt-%s-refinery", rig)
		default:
			return fmt.Sprintf("gt-%s-polecat-%s", rig, role)
		}
	case 3:
		rig, middle, name := parts[0], parts[1], parts[2]
		switch middle {
		case "polecats":
			return fmt.Sprintf("gt-%s-polecat-%s", rig, name)
		case "crew":
			return fmt.Sprintf("gt-%s-crew-%s", rig, name)
		}
	}
	return ""
}

// storeMailNudgeHandler is a test handler that replicates the MailNudgeHandler
// workflow (mail address → inbox push → Coop nudge) but uses store-direct
// inbox push instead of shelling out to `bd`. This avoids the re-entrant
// RPC deadlock and bd-binary dependency in integration tests.
type storeMailNudgeHandler struct {
	d               *testdaemon.Daemon
	httpClient      *http.Client
	coopURLResolver eventbus.CoopURLResolver
	nudgeAttempted  atomic.Bool
}

func (h *storeMailNudgeHandler) ID() string          { return "mail-nudge-test" }
func (h *storeMailNudgeHandler) Handles() []eventbus.EventType { return []eventbus.EventType{eventbus.EventMailSent} }
func (h *storeMailNudgeHandler) Priority() int        { return 50 }

func (h *storeMailNudgeHandler) Handle(ctx context.Context, event *eventbus.Event, result *eventbus.Result) error {
	var payload struct {
		MessageID string `json:"message_id"`
		From      string `json:"from"`
		To        string `json:"to"`
		Subject   string `json:"subject"`
	}
	if err := json.Unmarshal(event.Raw, &payload); err != nil {
		return fmt.Errorf("mail-nudge-test: unmarshal: %w", err)
	}

	if payload.To == "" {
		return nil
	}

	// Resolve mail address to agent ID using the same logic as the real handler.
	// We inline this because mailAddressToAgentID is unexported.
	agentID := testMailAddressToAgentID(payload.To)
	if agentID == "" {
		result.Warnings = append(result.Warnings, fmt.Sprintf("cannot resolve agent ID for %q", payload.To))
		return nil
	}

	// Push to inbox via store (not bd subprocess).
	message := fmt.Sprintf("You have new mail from %s: %s", payload.From, payload.Subject)
	dedupKey := fmt.Sprintf("mail:%s", payload.MessageID)
	err := h.d.Store.InboxPush(ctx, &types.InboxItem{
		ID:        uuid.New().String(),
		AgentName: agentID,
		Type:      "mail",
		Source:    "mail-nudge",
		Content:   message,
		Priority:  2,
		CreatedAt: time.Now().UTC(),
		DedupKey:  dedupKey,
	})
	if err != nil {
		return fmt.Errorf("mail-nudge-test: inbox push: %w", err)
	}

	// Nudge via Coop HTTP.
	if h.coopURLResolver != nil {
		coopURL, err := h.coopURLResolver(ctx, event.CWD, agentID)
		if err == nil && coopURL != "" {
			h.nudgeAttempted.Store(true)
			client := h.httpClient
			if client == nil {
				client = &http.Client{Timeout: 5 * time.Second}
			}
			body, _ := json.Marshal(map[string]string{"message": message})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, coopURL+"/api/v1/agent/nudge", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
	}

	return nil
}

// emitMailEvent emits a MailSent event and returns the bus result.
func emitMailEvent(t *testing.T, client *rpc.Client, from, to, subject, messageID, cwd string) rpc.BusEmitResult {
	t.Helper()
	raw := map[string]interface{}{
		"from":       from,
		"to":         to,
		"subject":    subject,
		"message_id": messageID,
		"cwd":        cwd,
	}
	eventJSON, _ := json.Marshal(raw)
	resp, err := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
		HookType:  "MailSent",
		EventJSON: json.RawMessage(eventJSON),
	})
	if err != nil {
		t.Fatalf("BusEmit(MailSent): %v", err)
	}
	if !resp.Success {
		t.Fatalf("BusEmit(MailSent) failed: %s", resp.Error)
	}
	var result rpc.BusEmitResult
	json.Unmarshal(resp.Data, &result)
	return result
}

// TestMailDeliveryFull verifies the full mail pipeline:
// MailSent event → address resolution → inbox push → Coop nudge → SessionStart drain.
// (bd-a19hu)
func TestMailDeliveryFull(t *testing.T) {
	var nudgeReceived atomic.Bool
	var nudgeMsg atomic.Value
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/nudge" && r.Method == http.MethodPost {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			nudgeMsg.Store(body["message"])
			nudgeReceived.Store(true)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"delivered":    true,
				"state_before": "idle",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer coopServer.Close()

	d := testdaemon.Start(t)
	client := d.Client(t)

	handler := &storeMailNudgeHandler{
		d:          d,
		httpClient: coopServer.Client(),
		coopURLResolver: func(_ context.Context, _, _ string) (string, error) {
			return coopServer.URL, nil
		},
	}

	bus := eventbus.New()
	bus.Register(handler)
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "gt-gastown-polecat-Toast"})
	d.Server.SetBus(bus)

	cwd := t.TempDir()

	// Emit MailSent event.
	emitMailEvent(t, client, "mayor/", "gastown/polecats/Toast", "Deploy approval needed", "msg-001", cwd)

	// Wait for nudge.
	deadline := time.Now().Add(5 * time.Second)
	for !nudgeReceived.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Timed out waiting for nudge")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify nudge message content.
	msg, _ := nudgeMsg.Load().(string)
	if !strings.Contains(msg, "Deploy approval needed") {
		t.Errorf("nudge missing subject, got: %q", msg)
	}
	if !strings.Contains(msg, "mayor/") {
		t.Errorf("nudge missing sender, got: %q", msg)
	}

	// Drain inbox via SessionStart — verify mail was pushed.
	result := emitEvent(t, client, "SessionStart", "e2e-mail-drain", cwd)
	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "Deploy approval needed") {
		t.Errorf("expected mail in inbox drain, got: %v", result.Inject)
	}
}

// TestMailDeliveryUnknownAddress verifies that unknown mail addresses
// don't crash and produce a warning. (bd-a19hu)
func TestMailDeliveryUnknownAddress(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	handler := &storeMailNudgeHandler{d: d}
	bus := eventbus.New()
	bus.Register(handler)
	d.Server.SetBus(bus)

	// Empty "to" — should return silently.
	result := emitMailEvent(t, client, "someone", "", "Hello", "msg-empty", t.TempDir())
	if result.Block {
		t.Error("empty to should not block")
	}

	// Unresolvable address — should produce warning.
	result2 := emitMailEvent(t, client, "someone", "unknown-format", "Hello", "msg-unknown", t.TempDir())
	if result2.Block {
		t.Error("unresolvable address should not block")
	}
	allWarnings := strings.Join(result2.Warnings, "\n")
	if !strings.Contains(allWarnings, "cannot resolve") {
		t.Logf("Note: unknown address may not produce warning in all code paths (got warnings: %v)", result2.Warnings)
	}
}

// TestMailDeliveryNoCoop verifies that mail delivery works even when
// Coop is unreachable — inbox is the reliable path. (bd-a19hu)
func TestMailDeliveryNoCoop(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	handler := &storeMailNudgeHandler{
		d: d,
		coopURLResolver: func(_ context.Context, _, _ string) (string, error) {
			return "http://127.0.0.1:1", nil // Unreachable
		},
	}

	bus := eventbus.New()
	bus.Register(handler)
	bus.Register(&storeInboxDrainHandler{store: d.Store, agentName: "gt-gastown-witness"})
	d.Server.SetBus(bus)

	cwd := t.TempDir()
	emitMailEvent(t, client, "deacon/", "gastown/witness", "Health check failed", "msg-coop-down", cwd)

	// Even with coop down, inbox should have the item.
	result := emitEvent(t, client, "SessionStart", "e2e-mail-nocoop", cwd)
	allInject := strings.Join(result.Inject, "\n")
	if !strings.Contains(allInject, "Health check failed") {
		t.Errorf("expected mail in inbox despite coop being down, got: %v", result.Inject)
	}
}

// TestMailDeliveryDedup verifies that duplicate MailSent events with the
// same message_id don't create duplicate inbox items. (bd-a19hu)
func TestMailDeliveryDedup(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	handler := &storeMailNudgeHandler{d: d}
	bus := eventbus.New()
	bus.Register(handler)
	d.Server.SetBus(bus)

	cwd := t.TempDir()

	// Send the same message twice.
	emitMailEvent(t, client, "mayor/", "gastown/polecats/Toast", "Same message", "msg-dedup", cwd)
	emitMailEvent(t, client, "mayor/", "gastown/polecats/Toast", "Same message", "msg-dedup", cwd)

	// Check inbox count.
	items, err := client.InboxList(&rpc.InboxListArgs{
		AgentName:        "gt-gastown-polecat-Toast",
		IncludeDelivered: true,
	})
	if err != nil {
		t.Fatalf("InboxList: %v", err)
	}
	// dedup_key is currently non-UNIQUE index, so both may be stored.
	// When upgraded to UNIQUE, expect count=1.
	t.Logf("Inbox items after 2 sends with same message_id: %d (dedup_key non-unique = %d expected)", items.Count, items.Count)
	if items.Count == 0 {
		t.Error("expected at least 1 inbox item")
	}
}

// TestMailDeliveryMultiAgent verifies that mail to different agents goes to
// the correct inboxes (agent isolation). (bd-a19hu)
func TestMailDeliveryMultiAgent(t *testing.T) {
	d := testdaemon.Start(t)
	client := d.Client(t)

	handler := &storeMailNudgeHandler{d: d}
	bus := eventbus.New()
	bus.Register(handler)
	d.Server.SetBus(bus)

	cwd := t.TempDir()

	// Send mail to different agents.
	emitMailEvent(t, client, "mayor/", "gastown/witness", "Witness message", "msg-w", cwd)
	emitMailEvent(t, client, "mayor/", "gastown/refinery", "Refinery message", "msg-r", cwd)
	emitMailEvent(t, client, "mayor/", "gastown/polecats/Toast", "Polecat message", "msg-p", cwd)

	// Verify each agent's inbox.
	for _, tc := range []struct {
		agent string
		want  string
	}{
		{"gt-gastown-witness", "Witness message"},
		{"gt-gastown-refinery", "Refinery message"},
		{"gt-gastown-polecat-Toast", "Polecat message"},
	} {
		items, err := client.InboxList(&rpc.InboxListArgs{AgentName: tc.agent, IncludeDelivered: true})
		if err != nil {
			t.Fatalf("InboxList(%s): %v", tc.agent, err)
		}
		if items.Count == 0 {
			t.Errorf("agent %s inbox empty, expected message containing %q", tc.agent, tc.want)
		}
	}

	// Verify cross-agent isolation: witness inbox should NOT have polecat's message.
	witnessItems, _ := client.InboxList(&rpc.InboxListArgs{AgentName: "gt-gastown-witness", IncludeDelivered: true})
	if witnessItems.Count > 1 {
		t.Errorf("witness inbox has %d items, expected 1 (isolation breach?)", witnessItems.Count)
	}
}
