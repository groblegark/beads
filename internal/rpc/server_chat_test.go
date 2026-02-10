package rpc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/chat"
	"github.com/steveyegge/beads/internal/eventbus"
)

// startTestNATSForRPC starts an embedded NATS server with JetStream for RPC chat testing.
func startTestNATSForRPC(t testing.TB) (*natsserver.Server, nats.JetStreamContext, func()) {
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

	if err := eventbus.EnsureStreams(js); err != nil {
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

func TestHandleChatSendNoBus(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()
	_ = server // bus is nil by default

	args := ChatSendArgs{
		SessionTag: "test-session",
		Content:    "hello",
		Sender:     "user",
		Direction:  "in",
	}
	_, err := client.Execute(OpChatSend, args)
	if err == nil {
		t.Fatal("expected error with no bus")
	}
	if !strings.Contains(err.Error(), "NATS") {
		t.Errorf("expected NATS-related error, got: %v", err)
	}
}

func TestHandleChatSendValidation(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()
	_ = server

	tests := []struct {
		name string
		args ChatSendArgs
		want string
	}{
		{"missing session_tag", ChatSendArgs{Content: "hi", Direction: "in"}, "session_tag"},
		{"missing content", ChatSendArgs{SessionTag: "s", Direction: "in"}, "content"},
		{"missing direction", ChatSendArgs{SessionTag: "s", Content: "hi"}, "direction"},
		{"invalid direction", ChatSendArgs{SessionTag: "s", Content: "hi", Direction: "bad"}, "direction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.Execute(OpChatSend, tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q should contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestHandleChatSendWithBus(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	_, js, natsCleanup := startTestNATSForRPC(t)
	defer natsCleanup()

	bus := eventbus.New()
	bus.SetJetStream(js)
	server.SetBus(bus)

	args := ChatSendArgs{
		SessionTag: "test-send-session",
		Content:    "Hello from RPC!",
		Sender:     "test-user",
		Direction:  "in",
	}

	resp, err := client.Execute(OpChatSend, args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	var result ChatSendResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Seq == 0 {
		t.Error("expected non-zero sequence number")
	}
	if result.Subject != "chat.session.test-send-session.in" {
		t.Errorf("subject = %q, want %q", result.Subject, "chat.session.test-send-session.in")
	}
}

func TestHandleChatSendOutbound(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	_, js, natsCleanup := startTestNATSForRPC(t)
	defer natsCleanup()

	bus := eventbus.New()
	bus.SetJetStream(js)
	server.SetBus(bus)

	args := ChatSendArgs{
		SessionTag: "out-session",
		Content:    "Agent reply!",
		Sender:     "agent-1",
		Direction:  "out",
	}

	resp, err := client.Execute(OpChatSend, args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	var result ChatSendResult
	json.Unmarshal(resp.Data, &result)
	if result.Subject != "chat.session.out-session.out" {
		t.Errorf("subject = %q, want %q", result.Subject, "chat.session.out-session.out")
	}
}

func TestHandleChatListenNoBus(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()
	_ = server

	args := ChatListenArgs{SessionTag: "test-session"}
	_, err := client.Execute(OpChatListen, args)
	if err == nil {
		t.Fatal("expected error with no bus")
	}
}

func TestHandleChatListenTimeout(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	_, js, natsCleanup := startTestNATSForRPC(t)
	defer natsCleanup()

	bus := eventbus.New()
	bus.SetJetStream(js)
	server.SetBus(bus)

	args := ChatListenArgs{
		SessionTag: "empty-session",
		TimeoutMs:  2000, // 2 seconds — short timeout for test.
	}

	start := time.Now()
	_, err := client.Execute(OpChatListen, args)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
	if elapsed < 1*time.Second {
		t.Errorf("expected at least 1s wait, got %v", elapsed)
	}
}

func TestHandleChatListenReceiveMessage(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	_, js, natsCleanup := startTestNATSForRPC(t)
	defer natsCleanup()

	bus := eventbus.New()
	bus.SetJetStream(js)
	server.SetBus(bus)

	// Publish a message before subscribing (will need drain mode).
	msg := &eventbus.ChatMessagePayload{
		SessionTag: "listen-test",
		Sender:     "slack-user",
		Content:    "Are you there?",
		Timestamp:  time.Now().UTC(),
	}
	data, _ := json.Marshal(msg)
	subject := eventbus.SubjectForChatSession("listen-test", "in")
	if _, err := js.Publish(subject, data); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Listen with drain to pick up the pre-published message.
	args := ChatListenArgs{
		SessionTag: "listen-test",
		TimeoutMs:  5000,
		Drain:      true,
	}

	resp, err := client.Execute(OpChatListen, args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	var result ChatListenResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("expected at least 1 message")
	}
	if result.Messages[0].Content != "Are you there?" {
		t.Errorf("content = %q, want %q", result.Messages[0].Content, "Are you there?")
	}
	if result.Messages[0].Sender != "slack-user" {
		t.Errorf("sender = %q, want %q", result.Messages[0].Sender, "slack-user")
	}
	if result.Messages[0].SessionTag != "listen-test" {
		t.Errorf("session_tag = %q, want %q", result.Messages[0].SessionTag, "listen-test")
	}
}

func TestHandleChatStatusNoRegistry(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	_, err := client.Execute(OpChatStatus, nil)
	if err == nil {
		t.Fatal("expected error with no chat registry")
	}
}

func TestHandleChatStatusListAll(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	reg := chat.NewRegistry()
	adapter := chat.NewRPCAdapter(reg)
	server.SetChatRegistry(adapter)

	// Register some sessions.
	reg.Register(&chat.Session{
		SessionTag: "sess-1",
		ChannelID:  "C1",
		ThreadTS:   "ts1",
		AgentName:  "agent-1",
		Status:     "open",
		CreatedAt:  time.Now(),
	})
	reg.Register(&chat.Session{
		SessionTag: "sess-2",
		ChannelID:  "C2",
		ThreadTS:   "ts2",
		AgentName:  "agent-2",
		Status:     "open",
		CreatedAt:  time.Now(),
	})

	resp, err := client.Execute(OpChatStatus, ChatStatusArgs{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	var result ChatStatusResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(result.Sessions))
	}
}

func TestHandleChatStatusByTag(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	reg := chat.NewRegistry()
	adapter := chat.NewRPCAdapter(reg)
	server.SetChatRegistry(adapter)

	reg.Register(&chat.Session{
		SessionTag: "target-sess",
		ChannelID:  "C-TARGET",
		ThreadTS:   "ts-target",
		AgentName:  "agent-target",
		Status:     "open",
		CreatedAt:  time.Now(),
	})

	resp, err := client.Execute(OpChatStatus, ChatStatusArgs{SessionTag: "target-sess"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	var result ChatStatusResult
	json.Unmarshal(resp.Data, &result)
	if len(result.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(result.Sessions))
	}
	if result.Sessions[0].SessionTag != "target-sess" {
		t.Errorf("session_tag = %q, want %q", result.Sessions[0].SessionTag, "target-sess")
	}
	if result.Sessions[0].ChannelID != "C-TARGET" {
		t.Errorf("channel_id = %q, want %q", result.Sessions[0].ChannelID, "C-TARGET")
	}
}

func TestHandleChatStatusNotFound(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	reg := chat.NewRegistry()
	adapter := chat.NewRPCAdapter(reg)
	server.SetChatRegistry(adapter)

	_, err := client.Execute(OpChatStatus, ChatStatusArgs{SessionTag: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestHandleChatSessionCreate(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	reg := chat.NewRegistry()
	adapter := chat.NewRPCAdapter(reg)
	server.SetChatRegistry(adapter)

	args := ChatSessionArgs{
		Action:     "create",
		SessionTag: "new-sess",
		ChannelID:  "C-NEW",
		ThreadTS:   "ts-new",
		AgentName:  "agent-new",
	}

	resp, err := client.Execute(OpChatSession, args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	var result ChatSessionResult
	json.Unmarshal(resp.Data, &result)
	if result.SessionTag != "new-sess" {
		t.Errorf("session_tag = %q, want %q", result.SessionTag, "new-sess")
	}
	if result.Status != "open" {
		t.Errorf("status = %q, want %q", result.Status, "open")
	}
	if !result.Created {
		t.Error("expected created=true for new session")
	}

	// Verify session exists in registry.
	s := reg.GetByTag("new-sess")
	if s == nil {
		t.Fatal("session not found in registry after create")
	}
	if s.ChannelID != "C-NEW" {
		t.Errorf("registry channel_id = %q, want %q", s.ChannelID, "C-NEW")
	}
}

func TestHandleChatSessionCreateDuplicate(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	reg := chat.NewRegistry()
	adapter := chat.NewRPCAdapter(reg)
	server.SetChatRegistry(adapter)

	// Pre-register.
	reg.Register(&chat.Session{
		SessionTag: "dup-sess",
		ChannelID:  "C-OLD",
		Status:     "open",
		CreatedAt:  time.Now(),
	})

	args := ChatSessionArgs{
		Action:     "create",
		SessionTag: "dup-sess",
		ChannelID:  "C-UPDATED",
		ThreadTS:   "ts-updated",
	}

	resp, err := client.Execute(OpChatSession, args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	var result ChatSessionResult
	json.Unmarshal(resp.Data, &result)
	if result.Created {
		t.Error("expected created=false for existing session")
	}
}

func TestHandleChatSessionClose(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	reg := chat.NewRegistry()
	adapter := chat.NewRPCAdapter(reg)
	server.SetChatRegistry(adapter)

	reg.Register(&chat.Session{
		SessionTag: "close-sess",
		Status:     "open",
		CreatedAt:  time.Now(),
	})

	args := ChatSessionArgs{
		Action:     "close",
		SessionTag: "close-sess",
	}

	resp, err := client.Execute(OpChatSession, args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	var result ChatSessionResult
	json.Unmarshal(resp.Data, &result)
	if result.Status != "closed" {
		t.Errorf("status = %q, want %q", result.Status, "closed")
	}

	// Verify in registry.
	s := reg.GetByTag("close-sess")
	if s == nil {
		t.Fatal("session not found")
	}
	if s.Status != "closed" {
		t.Errorf("registry status = %q, want %q", s.Status, "closed")
	}
}

func TestHandleChatSessionCloseNotFound(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	reg := chat.NewRegistry()
	adapter := chat.NewRPCAdapter(reg)
	server.SetChatRegistry(adapter)

	args := ChatSessionArgs{
		Action:     "close",
		SessionTag: "nonexistent",
	}

	_, err := client.Execute(OpChatSession, args)
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestHandleChatSessionInvalidAction(t *testing.T) {
	server, client, cleanup := setupTestServer(t)
	defer cleanup()

	reg := chat.NewRegistry()
	adapter := chat.NewRPCAdapter(reg)
	server.SetChatRegistry(adapter)

	args := ChatSessionArgs{
		Action:     "invalid",
		SessionTag: "test",
	}

	_, err := client.Execute(OpChatSession, args)
	if err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestHandleChatSessionNoRegistry(t *testing.T) {
	_, client, cleanup := setupTestServer(t)
	defer cleanup()

	args := ChatSessionArgs{
		Action:     "create",
		SessionTag: "test",
	}

	_, err := client.Execute(OpChatSession, args)
	if err == nil {
		t.Fatal("expected error with no registry")
	}
}
