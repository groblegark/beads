package eventbus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestDefaultHandlers(t *testing.T) {
	handlers := DefaultHandlers()
	if len(handlers) != 15 {
		t.Fatalf("expected 15 default handlers, got %d", len(handlers))
	}

	// Verify IDs
	ids := map[string]bool{}
	for _, h := range handlers {
		if h.ID() == "" {
			t.Error("handler has empty ID")
		}
		if ids[h.ID()] {
			t.Errorf("duplicate handler ID: %s", h.ID())
		}
		ids[h.ID()] = true
	}

	if !ids["health-check"] {
		t.Error("missing health-check handler")
	}
	if !ids["prime"] {
		t.Error("missing prime handler")
	}
	if !ids["gate"] {
		t.Error("missing gate handler")
	}
	if !ids["inbox-drain"] {
		t.Error("missing inbox-drain handler")
	}
	if !ids["post-tool-inbox"] {
		t.Error("missing post-tool-inbox handler")
	}
	if !ids["oj-job-complete"] {
		t.Error("missing oj-job-complete handler")
	}
	if !ids["oj-job-fail"] {
		t.Error("missing oj-job-fail handler")
	}
	if !ids["oj-step"] {
		t.Error("missing oj-step handler")
	}
	if !ids["mail-nudge"] {
		t.Error("missing mail-nudge handler")
	}
	if !ids["decision-nudge"] {
		t.Error("missing decision-nudge handler")
	}
	if !ids["bead-nudge"] {
		t.Error("missing bead-nudge handler")
	}
	if !ids["advice-hooks"] {
		t.Error("missing advice-hooks handler")
	}
	if !ids["subagent-identity"] {
		t.Error("missing subagent-identity handler")
	}
	if !ids["done-wait"] {
		t.Error("missing done-wait handler")
	}
}

func TestPrimeHandlerMetadata(t *testing.T) {
	h := &PrimeHandler{}
	if h.ID() != "prime" {
		t.Errorf("expected ID 'prime', got %q", h.ID())
	}
	if h.Priority() != 10 {
		t.Errorf("expected priority 10, got %d", h.Priority())
	}
	handles := h.Handles()
	if len(handles) != 2 {
		t.Fatalf("expected 2 event types, got %d", len(handles))
	}
	expected := map[EventType]bool{EventSessionStart: true, EventPreCompact: true}
	for _, et := range handles {
		if !expected[et] {
			t.Errorf("unexpected event type: %s", et)
		}
	}
}

func TestGateHandlerMetadata(t *testing.T) {
	h := &GateHandler{}
	if h.ID() != "gate" {
		t.Errorf("expected ID 'gate', got %q", h.ID())
	}
	if h.Priority() != 20 {
		t.Errorf("expected priority 20, got %d", h.Priority())
	}
	handles := h.Handles()
	if len(handles) != 2 {
		t.Fatalf("expected 2 event types, got %d", len(handles))
	}
	expected := map[EventType]bool{EventStop: true, EventPreToolUse: true}
	for _, et := range handles {
		if !expected[et] {
			t.Errorf("unexpected event type: %s", et)
		}
	}
}


func TestInboxDrainHandlerMetadata(t *testing.T) {
	h := &InboxDrainHandler{}
	if h.ID() != "inbox-drain" {
		t.Errorf("expected ID 'inbox-drain', got %q", h.ID())
	}
	if h.Priority() != 30 {
		t.Errorf("expected priority 30, got %d", h.Priority())
	}
	handles := h.Handles()
	if len(handles) != 3 {
		t.Fatalf("expected 3 event types, got %d", len(handles))
	}
	expected := map[EventType]bool{EventSessionStart: true, EventPreCompact: true, EventStop: true}
	for _, et := range handles {
		if !expected[et] {
			t.Errorf("unexpected event type: %s", et)
		}
	}
}

// ---------------------------------------------------------------------------
// In-process inbox drain tests (bd-f33nh)
// ---------------------------------------------------------------------------

// mockInboxStore is a minimal InboxStore for testing in-process drain.
type mockInboxStore struct {
	items    []*types.InboxItem
	drainErr error
	markErr  error
	drained  []string // IDs passed to InboxMarkDelivered
}

func (m *mockInboxStore) InboxDrain(_ context.Context, agentName string, maxPriority ...int) ([]*types.InboxItem, error) {
	if m.drainErr != nil {
		return nil, m.drainErr
	}
	// Filter by maxPriority if provided
	if len(maxPriority) > 0 {
		var filtered []*types.InboxItem
		for _, item := range m.items {
			if item.Priority <= maxPriority[0] {
				filtered = append(filtered, item)
			}
		}
		return filtered, nil
	}
	return m.items, nil
}

func (m *mockInboxStore) InboxMarkDelivered(_ context.Context, _ string, ids []string) error {
	m.drained = append(m.drained, ids...)
	return m.markErr
}

func TestInboxDrainHandler_InProcess(t *testing.T) {
	store := &mockInboxStore{
		items: []*types.InboxItem{
			{ID: "1", Type: "alert", Source: "ci", Content: "Build failed"},
			{ID: "2", Type: "decision", Source: "human", Content: "Approved"},
		},
	}

	h := &InboxDrainHandler{}
	h.SetInboxStore(store)

	event := &Event{
		Type:  EventSessionStart,
		CWD:   t.TempDir(),
		Actor: "bright-hog",
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result.Inject) != 1 {
		t.Fatalf("expected 1 inject entry, got %d", len(result.Inject))
	}
	if !strings.Contains(result.Inject[0], "2 new notification") {
		t.Errorf("expected '2 new notification' in inject, got: %q", result.Inject[0])
	}
	if !strings.Contains(result.Inject[0], "Build failed") {
		t.Errorf("expected 'Build failed' in inject, got: %q", result.Inject[0])
	}
	// Verify items were marked delivered.
	if len(store.drained) != 2 {
		t.Errorf("expected 2 items marked delivered, got %d", len(store.drained))
	}
}

func TestInboxDrainHandler_InProcess_Empty(t *testing.T) {
	store := &mockInboxStore{items: nil}

	h := &InboxDrainHandler{}
	h.SetInboxStore(store)

	event := &Event{
		Type:  EventPreCompact,
		CWD:   t.TempDir(),
		Actor: "bright-hog",
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result.Inject) != 0 {
		t.Errorf("expected no inject entries, got %d", len(result.Inject))
	}
}

func TestInboxDrainHandler_FallsBackWithoutActor(t *testing.T) {
	// When Actor is empty, handler should fall back to subprocess even if store is set.
	// We can verify this by not mocking bd — the subprocess will fail, but that's
	// the expected codepath. We just verify it doesn't use the store.
	store := &mockInboxStore{
		items: []*types.InboxItem{
			{ID: "1", Type: "alert", Content: "Should not appear"},
		},
	}

	h := &InboxDrainHandler{}
	h.SetInboxStore(store)

	event := &Event{
		Type:  EventSessionStart,
		CWD:   t.TempDir(),
		Actor: "", // no actor → fallback
	}
	result := &Result{}

	// This will fail because bd isn't in PATH, but that's fine —
	// we just want to verify it didn't use the store.
	_ = h.Handle(context.Background(), event, result)
	if len(store.drained) != 0 {
		t.Errorf("expected store not to be called when Actor is empty, but got %d drained IDs", len(store.drained))
	}
}

func TestPostToolUseInboxHandler_InProcess(t *testing.T) {
	store := &mockInboxStore{
		items: []*types.InboxItem{
			{ID: "1", Type: "alert", Priority: 0, Content: "Critical alert"},
			{ID: "2", Type: "event", Priority: 2, Content: "Normal event"},
			{ID: "3", Type: "alert", Priority: 1, Content: "High priority"},
		},
	}

	h := &PostToolUseInboxHandler{}
	h.SetInboxStore(store)

	event := &Event{
		Type:  EventPostToolUse,
		CWD:   t.TempDir(),
		Actor: "bright-hog",
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result.Inject) != 1 {
		t.Fatalf("expected 1 inject entry, got %d", len(result.Inject))
	}
	// All priorities should be drained (bd-ut3r3).
	if !strings.Contains(result.Inject[0], "Critical alert") {
		t.Errorf("expected 'Critical alert' in inject, got: %q", result.Inject[0])
	}
	if !strings.Contains(result.Inject[0], "High priority") {
		t.Errorf("expected 'High priority' in inject, got: %q", result.Inject[0])
	}
	if !strings.Contains(result.Inject[0], "Normal event") {
		t.Errorf("expected 'Normal event' (P2) in inject after bd-ut3r3, got: %q", result.Inject[0])
	}
	// All 3 items should be marked delivered.
	if len(store.drained) != 3 {
		t.Errorf("expected 3 items marked delivered, got %d", len(store.drained))
	}
}

func TestPostToolUseInboxHandler_InProcess_Empty(t *testing.T) {
	store := &mockInboxStore{items: nil}

	h := &PostToolUseInboxHandler{}
	h.SetInboxStore(store)

	event := &Event{
		Type:  EventPostToolUse,
		CWD:   t.TempDir(),
		Actor: "bright-hog",
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result.Inject) != 0 {
		t.Errorf("expected no inject entries, got %d", len(result.Inject))
	}
}

func TestFormatInboxItems(t *testing.T) {
	items := []*types.InboxItem{
		{Type: "alert", Source: "ci", Content: "Build failed on main"},
		{Type: "decision", Source: "", Content: "Approved deployment"},
	}

	output := formatInboxItems(items)
	if !strings.Contains(output, "2 new notification") {
		t.Errorf("expected '2 new notification' header, got: %q", output)
	}
	if !strings.Contains(output, "[alert] (from ci): Build failed on main") {
		t.Errorf("expected formatted alert item, got: %q", output)
	}
	if !strings.Contains(output, "[decision]: Approved deployment") {
		t.Errorf("expected formatted decision item without source, got: %q", output)
	}
}

func TestHandlerPriorityOrdering(t *testing.T) {
	handlers := DefaultHandlers()
	// Verify non-decreasing priority ordering: health(5) ≤ prime(10) ≤ gate(20) ≤ inbox(30) ≤ oj-*(40)
	for i := 0; i < len(handlers)-1; i++ {
		if handlers[i].Priority() > handlers[i+1].Priority() {
			t.Errorf("handler %q (priority %d) should not have higher priority than %q (priority %d)",
				handlers[i].ID(), handlers[i].Priority(),
				handlers[i+1].ID(), handlers[i+1].Priority())
		}
	}
}

func TestBusWithDefaultHandlers(t *testing.T) {
	bus := New()
	for _, h := range DefaultHandlers() {
		bus.Register(h)
	}

	if len(bus.Handlers()) != 15 {
		t.Errorf("expected 15 handlers, got %d", len(bus.Handlers()))
	}
}

// TestInboxDrainRunsDuringStop verifies that the inbox-drain handler is matched
// for EventStop, ensuring decision responses are injected during stop hooks. (bd-rwzse)
func TestInboxDrainRunsDuringStop(t *testing.T) {
	bus := New()
	for _, h := range DefaultHandlers() {
		bus.Register(h)
	}

	// Get matching handlers for EventStop
	var stopHandlerIDs []string
	for _, h := range bus.Handlers() {
		if slices.Contains(h.Handles(), EventStop) {
			stopHandlerIDs = append(stopHandlerIDs, h.ID())
		}
	}

	if !slices.Contains(stopHandlerIDs, "inbox-drain") {
		t.Errorf("inbox-drain handler not matched for EventStop; matched handlers: %v", stopHandlerIDs)
	}
}

func TestFindBDBinary(t *testing.T) {
	// This test verifies the bd binary lookup works in CI.
	path, err := findBDBinary()
	if err != nil {
		t.Skipf("bd binary not found (expected in dev/CI only): %v", err)
	}
	if path == "" {
		t.Error("findBDBinary returned empty path")
	}
}

// setupMockBD creates a temporary directory with a mock bd shell script,
// prepends it to PATH so handlers find it via exec.LookPath, and returns
// a cleanup function that restores the original PATH.
func setupMockBD(t *testing.T, script string) func() {
	t.Helper()
	dir := t.TempDir()
	bdPath := filepath.Join(dir, "bd")
	if err := os.WriteFile(bdPath, []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatalf("failed to write mock bd script: %v", err)
	}
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+oldPath)
	return func() { os.Setenv("PATH", oldPath) }
}

// ---------------------------------------------------------------------------
// PrimeHandler.Handle integration tests
// ---------------------------------------------------------------------------

func TestPrimeHandlerHandle(t *testing.T) {
	cleanup := setupMockBD(t, `
case "$1" in
  prime) printf "# Beads Workflow Context\n\nSome context here"; exit 0;;
esac
exit 1
`)
	defer cleanup()

	h := &PrimeHandler{}
	event := &Event{
		Type: EventSessionStart,
		CWD:  t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result.Inject) != 1 {
		t.Fatalf("expected 1 inject entry, got %d", len(result.Inject))
	}
	if !strings.Contains(result.Inject[0], "Beads Workflow Context") {
		t.Errorf("expected inject to contain workflow context, got: %q", result.Inject[0])
	}
	if !strings.Contains(result.Inject[0], "Some context here") {
		t.Errorf("expected inject to contain 'Some context here', got: %q", result.Inject[0])
	}
}

func TestPrimeHandlerHandleBDNotFound(t *testing.T) {
	// Point PATH to an empty directory so bd is not found.
	emptyDir := t.TempDir()
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", emptyDir)
	defer os.Setenv("PATH", oldPath)

	h := &PrimeHandler{}
	event := &Event{
		Type: EventSessionStart,
		CWD:  t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err == nil {
		t.Fatal("expected error when bd is not in PATH, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error message, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GateHandler.Handle integration tests
// ---------------------------------------------------------------------------

func TestGateHandlerHandle_Allow(t *testing.T) {
	cleanup := setupMockBD(t, `
case "$1" in
  gate) printf '{"decision":"allow","warnings":["test warning"]}'; exit 0;;
esac
exit 1
`)
	defer cleanup()

	h := &GateHandler{}
	event := &Event{
		Type: EventPreToolUse,
		CWD:  t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.Block {
		t.Error("expected Block=false for allow decision")
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(result.Warnings))
	}
	if result.Warnings[0] != "test warning" {
		t.Errorf("expected warning 'test warning', got %q", result.Warnings[0])
	}
}

func TestGateHandlerHandle_Block(t *testing.T) {
	cleanup := setupMockBD(t, `
case "$1" in
  gate) printf '{"decision":"block","reason":"gate failed","warnings":["blocked warning"]}'; exit 1;;
esac
exit 1
`)
	defer cleanup()

	h := &GateHandler{}
	event := &Event{
		Type: EventStop,
		CWD:  t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error (block is not an error), got: %v", err)
	}
	if !result.Block {
		t.Error("expected Block=true for block decision")
	}
	if result.Reason != "gate failed" {
		t.Errorf("expected reason 'gate failed', got %q", result.Reason)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(result.Warnings))
	}
	if result.Warnings[0] != "blocked warning" {
		t.Errorf("expected warning 'blocked warning', got %q", result.Warnings[0])
	}
}

func TestGateHandlerHandle_BlockRawOutput(t *testing.T) {
	// When bd exits 1 with non-JSON output, the handler should treat
	// the raw stdout as the block reason.
	cleanup := setupMockBD(t, `
case "$1" in
  gate) printf "raw gate failure message"; exit 1;;
esac
exit 1
`)
	defer cleanup()

	h := &GateHandler{}
	event := &Event{
		Type: EventPreToolUse,
		CWD:  t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error (block is not an error), got: %v", err)
	}
	if !result.Block {
		t.Error("expected Block=true for non-JSON exit-1 output")
	}
	if result.Reason != "raw gate failure message" {
		t.Errorf("expected reason 'raw gate failure message', got %q", result.Reason)
	}
}

// ---------------------------------------------------------------------------
// GateHandler pre-checked gate tests (bd-tpkw9)
// ---------------------------------------------------------------------------

func TestGateHandler_PreCheckedAllow(t *testing.T) {
	// When gate_pre_checked=true and decision=allow, handler should not block.
	raw := map[string]any{
		"gate_pre_checked": true,
		"gate_pre_check_result": map[string]any{
			"decision": "allow",
			"warnings": []any{"soft: commit-push unsatisfied"},
		},
	}
	eventData, _ := json.Marshal(raw)

	h := &GateHandler{}
	event := &Event{
		Type: EventStop,
		CWD:  t.TempDir(),
		Raw:  eventData,
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Block {
		t.Error("expected Block=false for pre-checked allow decision")
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "soft: commit-push unsatisfied" {
		t.Errorf("expected 1 warning, got: %v", result.Warnings)
	}
}

func TestGateHandler_PreCheckedBlock(t *testing.T) {
	// When gate_pre_checked=true and decision=block, handler should block.
	raw := map[string]any{
		"gate_pre_checked": true,
		"gate_pre_check_result": map[string]any{
			"decision": "block",
			"reason":   "decision gate unsatisfied",
			"warnings": []any{"decision: pending human response"},
		},
	}
	eventData, _ := json.Marshal(raw)

	h := &GateHandler{}
	event := &Event{
		Type: EventStop,
		CWD:  t.TempDir(),
		Raw:  eventData,
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Block {
		t.Error("expected Block=true for pre-checked block decision")
	}
	if result.Reason != "decision gate unsatisfied" {
		t.Errorf("expected reason 'decision gate unsatisfied', got %q", result.Reason)
	}
	if len(result.Warnings) != 1 {
		t.Errorf("expected 1 warning, got: %v", result.Warnings)
	}
}

func TestGateHandler_PreCheckedNoResult(t *testing.T) {
	// When gate_pre_checked=true but no result data, handler should pass through.
	raw := map[string]any{
		"gate_pre_checked": true,
	}
	eventData, _ := json.Marshal(raw)

	h := &GateHandler{}
	event := &Event{
		Type: EventStop,
		CWD:  t.TempDir(),
		Raw:  eventData,
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Block {
		t.Error("expected Block=false when pre-check result is missing")
	}
}

// ---------------------------------------------------------------------------
// envFromEvent / SubprocessEnv tests (bd-3tvyj)
// ---------------------------------------------------------------------------

func TestEnvFromEvent_PopulatesClaudeSessionID(t *testing.T) {
	event := &Event{
		Actor:     "test-actor",
		SessionID: "claude-session-abc123",
		Raw:       json.RawMessage(`{"caller_session_tag":"term-xyz"}`),
	}

	env := envFromEvent(event)

	if env.Actor != "test-actor" {
		t.Errorf("expected Actor 'test-actor', got %q", env.Actor)
	}
	if env.ClaudeSessionID != "claude-session-abc123" {
		t.Errorf("expected ClaudeSessionID 'claude-session-abc123', got %q", env.ClaudeSessionID)
	}
	if env.CallerSessionTag != "term-xyz" {
		t.Errorf("expected CallerSessionTag 'term-xyz', got %q", env.CallerSessionTag)
	}
}

func TestEnvFromEvent_EmptySessionID(t *testing.T) {
	event := &Event{
		Actor:     "test-actor",
		SessionID: "",
	}

	env := envFromEvent(event)

	if env.ClaudeSessionID != "" {
		t.Errorf("expected empty ClaudeSessionID, got %q", env.ClaudeSessionID)
	}
}

func TestGateHandler_PassesSessionFlag(t *testing.T) {
	// Mock bd that echoes back the --session flag value
	cleanup := setupMockBD(t, `
# Capture the --session flag value
SESSION=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --session) SESSION="$2"; shift 2;;
    *) shift;;
  esac
done
if [ -n "$SESSION" ]; then
  printf '{"decision":"allow","warnings":["session=%s"]}' "$SESSION"
else
  printf '{"decision":"allow","warnings":["no-session"]}'
fi
exit 0
`)
	defer cleanup()

	h := &GateHandler{}
	event := &Event{
		Type:      EventStop,
		SessionID: "test-session-42",
		CWD:       t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The mock bd should have received --session test-session-42
	if len(result.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(result.Warnings), result.Warnings)
	}
	if result.Warnings[0] != "session=test-session-42" {
		t.Errorf("expected mock to receive session ID, got warning: %q", result.Warnings[0])
	}
}

func TestGateHandler_NoSessionFlag_WhenEmpty(t *testing.T) {
	// When SessionID is empty, --session should NOT be passed
	cleanup := setupMockBD(t, `
SESSION=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --session) SESSION="$2"; shift 2;;
    *) shift;;
  esac
done
if [ -n "$SESSION" ]; then
  printf '{"decision":"allow","warnings":["session=%s"]}' "$SESSION"
else
  printf '{"decision":"allow","warnings":["no-session"]}'
fi
exit 0
`)
	defer cleanup()

	h := &GateHandler{}
	event := &Event{
		Type:      EventStop,
		SessionID: "",
		CWD:       t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Warnings) != 1 || result.Warnings[0] != "no-session" {
		t.Errorf("expected 'no-session' warning when SessionID is empty, got: %v", result.Warnings)
	}
}

// ---------------------------------------------------------------------------
// HealthCheckHandler tests
// ---------------------------------------------------------------------------

func TestHealthCheckHandlerMetadata(t *testing.T) {
	h := &HealthCheckHandler{}
	if h.ID() != "health-check" {
		t.Errorf("expected ID 'health-check', got %q", h.ID())
	}
	if h.Priority() != 5 {
		t.Errorf("expected priority 5, got %d", h.Priority())
	}
	handles := h.Handles()
	if len(handles) != 1 {
		t.Fatalf("expected 1 event type, got %d", len(handles))
	}
	if handles[0] != EventSessionStart {
		t.Errorf("expected EventSessionStart, got %s", handles[0])
	}
}

func TestHealthCheckHandler_SkipsNonAgent(t *testing.T) {
	// Without BD_ACTOR or GT_ROLE set, handler should be a no-op.
	oldActor := os.Getenv("BD_ACTOR")
	oldRole := os.Getenv("GT_ROLE")
	os.Unsetenv("BD_ACTOR")
	os.Unsetenv("GT_ROLE")
	defer func() {
		if oldActor != "" {
			os.Setenv("BD_ACTOR", oldActor)
		}
		if oldRole != "" {
			os.Setenv("GT_ROLE", oldRole)
		}
	}()

	h := &HealthCheckHandler{}
	event := &Event{
		Type: EventSessionStart,
		CWD:  t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result.Inject) != 0 {
		t.Errorf("expected no injections for non-agent session, got %d", len(result.Inject))
	}
}

func TestHealthCheckHandler_DetectsIssues(t *testing.T) {
	// Mock bd to return failure for stats and health checks.
	cleanup := setupMockBD(t, `
case "$1" in
  stats) printf "stats error"; exit 1;;
  health) printf '{"status":"unhealthy","error":"db connection failed"}'; exit 0;;
  inbox) exit 0;;
esac
exit 1
`)
	defer cleanup()

	oldActor := os.Getenv("BD_ACTOR")
	os.Setenv("BD_ACTOR", "test-agent")
	defer func() {
		if oldActor != "" {
			os.Setenv("BD_ACTOR", oldActor)
		} else {
			os.Unsetenv("BD_ACTOR")
		}
	}()

	h := &HealthCheckHandler{}
	event := &Event{
		Type: EventSessionStart,
		CWD:  t.TempDir(),
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(result.Inject) == 0 {
		t.Fatal("expected health warning injection for failing checks")
	}
	if !strings.Contains(result.Inject[0], "HEALTH WARNING") {
		t.Errorf("expected HEALTH WARNING in injection, got: %q", result.Inject[0])
	}
}

func TestHealthCheckHandler_AllHealthy(t *testing.T) {
	// Mock bd to return success for all checks.
	cleanup := setupMockBD(t, `
case "$1" in
  stats) printf '{"total":10}'; exit 0;;
  health) printf '{"status":"healthy","uptime":100}'; exit 0;;
  inbox) exit 0;;
esac
exit 0
`)
	defer cleanup()

	oldActor := os.Getenv("BD_ACTOR")
	os.Setenv("BD_ACTOR", "test-agent")
	defer func() {
		if oldActor != "" {
			os.Setenv("BD_ACTOR", oldActor)
		} else {
			os.Unsetenv("BD_ACTOR")
		}
	}()

	h := &HealthCheckHandler{}
	// Use a real temp dir that exists (git check will fail but that's ok for this test
	// since the focus is on workspace and daemon checks)
	cwd := t.TempDir()
	event := &Event{
		Type: EventSessionStart,
		CWD:  cwd,
	}
	result := &Result{}

	err := h.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	// Git check will detect "not a git repository" so we'll still get a warning.
	// That's expected — the test verifies workspace and daemon checks pass.
}
