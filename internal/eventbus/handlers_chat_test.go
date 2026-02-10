package eventbus

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

func startTestNATSForChat(t testing.TB) (*natsserver.Server, nats.JetStreamContext, func()) {
	t.Helper()
	dir := t.TempDir()
	opts := &natsserver.Options{
		Port:               -1,
		JetStream:          true,
		JetStreamMaxMemory: 512 << 20,
		JetStreamMaxStore:  512 << 20,
		StoreDir:           dir,
		NoLog:              true,
		NoSigs:             true,
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("create test NATS server: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("test NATS server failed to start")
	}

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		t.Fatalf("connect to test NATS: %v", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatalf("get JetStream context: %v", err)
	}

	if err := EnsureStreams(js); err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatalf("create streams: %v", err)
	}

	cleanup := func() {
		nc.Drain()
		nc.Close()
		ns.Shutdown()
	}
	return ns, js, cleanup
}

func TestChatStatusHandlerSessionStart(t *testing.T) {
	_, js, cleanup := startTestNATSForChat(t)
	defer cleanup()

	handler := NewChatStatusHandler(func() nats.JetStreamContext { return js })

	if handler.ID() != "chat-status" {
		t.Errorf("ID() = %q, want %q", handler.ID(), "chat-status")
	}
	if handler.Priority() != 35 {
		t.Errorf("Priority() = %d, want 35", handler.Priority())
	}

	// Subscribe to chat.status.* to verify the handler publishes.
	sub, err := js.SubscribeSync("chat.status.>", nats.DeliverAll(), nats.AckExplicit())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Fire SessionStart event.
	event := &Event{
		Type:      EventSessionStart,
		SessionID: "test-session-123",
		AgentID:   "test-agent",
	}
	result := &Result{}

	err = handler.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	// Verify chat.status event was published.
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("no status message received: %v", err)
	}

	var payload ChatStatusPayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if payload.SessionTag != "test-session-123" {
		t.Errorf("session_tag = %q, want %q", payload.SessionTag, "test-session-123")
	}
	if payload.Status != "available" {
		t.Errorf("status = %q, want %q", payload.Status, "available")
	}
	if payload.AgentName != "test-agent" {
		t.Errorf("agent_name = %q, want %q", payload.AgentName, "test-agent")
	}

	msg.Ack()

	// Verify context injection.
	if len(result.Inject) == 0 {
		t.Fatal("expected inject context for SessionStart")
	}
	injected := result.Inject[0]
	if len(injected) == 0 {
		t.Error("injected context should not be empty")
	}
}

func TestChatStatusHandlerStop(t *testing.T) {
	_, js, cleanup := startTestNATSForChat(t)
	defer cleanup()

	handler := NewChatStatusHandler(func() nats.JetStreamContext { return js })

	sub, err := js.SubscribeSync("chat.status.>", nats.DeliverAll(), nats.AckExplicit())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	event := &Event{
		Type:      EventStop,
		SessionID: "stop-session-456",
		AgentID:   "agent-leaving",
	}
	result := &Result{}

	err = handler.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("no status message received: %v", err)
	}

	var payload ChatStatusPayload
	json.Unmarshal(msg.Data, &payload)

	if payload.SessionTag != "stop-session-456" {
		t.Errorf("session_tag = %q, want %q", payload.SessionTag, "stop-session-456")
	}
	if payload.Status != "gone" {
		t.Errorf("status = %q, want %q", payload.Status, "gone")
	}

	msg.Ack()

	// Stop should NOT inject context.
	if len(result.Inject) > 0 {
		t.Errorf("expected no inject for Stop, got %d items", len(result.Inject))
	}
}

func TestChatStatusHandlerNoJetStream(t *testing.T) {
	handler := NewChatStatusHandler(func() nats.JetStreamContext { return nil })

	event := &Event{
		Type:      EventSessionStart,
		SessionID: "no-js-session",
	}
	result := &Result{}

	// Should not error, just silently skip.
	err := handler.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// No injection when JetStream unavailable.
	if len(result.Inject) > 0 {
		t.Error("expected no inject when JetStream is unavailable")
	}
}

func TestChatStatusHandlerEmptySessionID(t *testing.T) {
	_, js, cleanup := startTestNATSForChat(t)
	defer cleanup()

	handler := NewChatStatusHandler(func() nats.JetStreamContext { return js })

	event := &Event{
		Type:      EventSessionStart,
		SessionID: "", // Empty — should be a no-op.
	}
	result := &Result{}

	err := handler.Handle(context.Background(), event, result)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(result.Inject) > 0 {
		t.Error("expected no inject for empty session_id")
	}
}

func TestChatStatusHandlerHandlesTypes(t *testing.T) {
	handler := NewChatStatusHandler(func() nats.JetStreamContext { return nil })

	handles := handler.Handles()
	if len(handles) != 2 {
		t.Fatalf("expected 2 handled event types, got %d", len(handles))
	}

	found := map[EventType]bool{}
	for _, h := range handles {
		found[h] = true
	}
	if !found[EventSessionStart] {
		t.Error("expected SessionStart in handled types")
	}
	if !found[EventStop] {
		t.Error("expected Stop in handled types")
	}
}

func TestChatStatusHandlerInjectContent(t *testing.T) {
	hint := chatAvailableHint("my-session-tag")

	if hint == "" {
		t.Fatal("expected non-empty hint")
	}
	// Verify it contains the session tag.
	if !containsStr(hint, "my-session-tag") {
		t.Error("hint should contain the session tag")
	}
	// Verify it mentions bd chat listen.
	if !containsStr(hint, "bd chat listen") {
		t.Error("hint should mention 'bd chat listen'")
	}
	// Verify it mentions bd chat reply.
	if !containsStr(hint, "bd chat reply") {
		t.Error("hint should mention 'bd chat reply'")
	}
}

func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
