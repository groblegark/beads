package slackbot

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/eventbus"
)

// startTestNATSForChat starts an embedded NATS server with JetStream for chat relay testing.
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

func TestChatRelayPublishInbound(t *testing.T) {
	ns, js, cleanup := startTestNATSForChat(t)
	defer cleanup()

	mockAPI := newMockSlackAPI()
	bot := newBotForTest(mockAPI, nil, "C-TEST")

	relay := NewChatRelay(ns.ClientURL(), "", bot)

	// Manually set JS context (skip full connect/Run lifecycle).
	nc, _ := nats.Connect(ns.ClientURL())
	defer nc.Close()
	relayJS, _ := nc.JetStream()
	relay.mu.Lock()
	relay.conn = nc
	relay.js = relayJS
	relay.mu.Unlock()

	// Subscribe to inbound subject to verify message arrives.
	session := "test-inbound-session"
	subject := eventbus.SubjectForChatSession(session, "in")
	sub, err := js.SubscribeSync(subject, nats.DeliverAll(), nats.AckExplicit())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Publish inbound.
	ctx := context.Background()
	err = relay.PublishInbound(ctx, session, "TestUser", "U123", "Hello agent!", "C-TEST", "ts-thread")
	if err != nil {
		t.Fatalf("PublishInbound: %v", err)
	}

	// Verify message on NATS.
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("no message received: %v", err)
	}

	var payload eventbus.ChatMessagePayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if payload.SessionTag != session {
		t.Errorf("session_tag = %q, want %q", payload.SessionTag, session)
	}
	if payload.Sender != "TestUser" {
		t.Errorf("sender = %q, want %q", payload.Sender, "TestUser")
	}
	if payload.Content != "Hello agent!" {
		t.Errorf("content = %q, want %q", payload.Content, "Hello agent!")
	}
	if payload.ChannelID != "C-TEST" {
		t.Errorf("channel_id = %q, want %q", payload.ChannelID, "C-TEST")
	}
	msg.Ack()
}

func TestChatRelayOutbound(t *testing.T) {
	ns, _, cleanup := startTestNATSForChat(t)
	defer cleanup()

	mockAPI := newMockSlackAPI()
	bot := newBotForTest(mockAPI, nil, "C-TEST")

	relay := NewChatRelay(ns.ClientURL(), "", bot)
	bot.SetChatRelay(relay)

	// Register a session so outbound messages can find the Slack thread.
	relay.RegisterSession("outbound-session", "C-OUT", "ts-out-thread", "test-agent")

	// Start the relay in background.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		relay.Run(ctx)
	}()

	// Wait for relay to connect.
	time.Sleep(500 * time.Millisecond)

	// Publish an outbound message (agent → Slack).
	nc, _ := nats.Connect(ns.ClientURL())
	defer nc.Close()
	js, _ := nc.JetStream()

	outPayload := eventbus.ChatMessagePayload{
		SessionTag: "outbound-session",
		Sender:     "test-agent",
		Content:    "Task completed!",
		Timestamp:  time.Now().UTC(),
	}
	data, _ := json.Marshal(outPayload)
	subject := eventbus.SubjectForChatSession("outbound-session", "out")
	_, err := js.Publish(subject, data)
	if err != nil {
		t.Fatalf("publish outbound: %v", err)
	}

	// Wait for relay to process and post to Slack.
	time.Sleep(1 * time.Second)

	mockAPI.mu.Lock()
	posted := len(mockAPI.PostedMessages)
	var channelID string
	if posted > 0 {
		channelID = mockAPI.PostedMessages[0].ChannelID
	}
	mockAPI.mu.Unlock()

	if posted == 0 {
		t.Fatal("expected at least 1 Slack message posted, got 0")
	}
	if channelID != "C-OUT" {
		t.Errorf("posted to channel %q, want %q", channelID, "C-OUT")
	}

	cancel()
}

func TestChatRelaySessionRegistry(t *testing.T) {
	relay := NewChatRelay("nats://unused", "", nil)

	relay.RegisterSession("sess-1", "C-A", "ts-a", "agent-1")
	relay.RegisterSession("sess-2", "C-B", "ts-b", "agent-2")

	// Lookup by thread.
	s := relay.GetSessionByThread("C-A", "ts-a")
	if s == nil {
		t.Fatal("expected session for C-A:ts-a")
	}
	if s.SessionTag != "sess-1" {
		t.Errorf("tag = %q, want %q", s.SessionTag, "sess-1")
	}

	// Miss.
	s = relay.GetSessionByThread("C-X", "ts-x")
	if s != nil {
		t.Errorf("expected nil for unknown thread, got %+v", s)
	}
}

func TestChatRelayPublishInboundNotConnected(t *testing.T) {
	relay := NewChatRelay("nats://unused", "", nil)

	err := relay.PublishInbound(context.Background(), "sess", "user", "U1", "hi", "C1", "ts1")
	if err == nil {
		t.Error("expected error when not connected")
	}
}

func TestChatRelayOutboundNoSession(t *testing.T) {
	// Outbound message with no registered session but with channel/thread in payload
	// should auto-register and still post.
	ns, _, cleanup := startTestNATSForChat(t)
	defer cleanup()

	mockAPI := newMockSlackAPI()
	bot := newBotForTest(mockAPI, nil, "C-TEST")

	relay := NewChatRelay(ns.ClientURL(), "", bot)
	bot.SetChatRelay(relay)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go relay.Run(ctx)
	time.Sleep(500 * time.Millisecond)

	// Publish outbound with channel/thread in payload (no pre-registered session).
	nc, _ := nats.Connect(ns.ClientURL())
	defer nc.Close()
	js, _ := nc.JetStream()

	outPayload := eventbus.ChatMessagePayload{
		SessionTag: "auto-register-session",
		Sender:     "agent",
		Content:    "I'm here!",
		ChannelID:  "C-AUTO",
		ThreadTS:   "ts-auto",
		Timestamp:  time.Now().UTC(),
	}
	data, _ := json.Marshal(outPayload)
	subject := eventbus.SubjectForChatSession("auto-register-session", "out")
	js.Publish(subject, data)

	time.Sleep(1 * time.Second)

	mockAPI.mu.Lock()
	posted := len(mockAPI.PostedMessages)
	var channelID string
	if posted > 0 {
		channelID = mockAPI.PostedMessages[0].ChannelID
	}
	mockAPI.mu.Unlock()

	if posted == 0 {
		t.Fatal("expected Slack message posted via auto-registration")
	}
	if channelID != "C-AUTO" {
		t.Errorf("posted to %q, want %q", channelID, "C-AUTO")
	}

	// Verify session was auto-registered.
	s := relay.GetSessionByThread("C-AUTO", "ts-auto")
	if s == nil {
		t.Error("expected session to be auto-registered")
	}

	cancel()
}

func TestTruncateChat(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly ten!", 12, "exactly ten!"},
		{"this is a long string that should be truncated", 20, "this is a long st..."},
		{"abc", 3, "abc"},
		{"abcd", 3, "..."},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}
