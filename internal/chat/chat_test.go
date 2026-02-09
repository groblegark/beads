package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/eventbus"
)

// startTestNATS starts an embedded NATS server with JetStream for testing.
func startTestNATS(t testing.TB) (*natsserver.Server, nats.JetStreamContext, func()) {
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

// --- Registry Tests ---

func TestRegistryRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	s := &Session{
		SessionTag: "sess-abc",
		ChannelID:  "C123",
		ThreadTS:   "1234567890.123456",
		AgentName:  "test-agent",
		Status:     "open",
		CreatedAt:  time.Now(),
	}
	r.Register(s)

	got := r.GetByTag("sess-abc")
	if got == nil {
		t.Fatal("expected session, got nil")
	}
	if got.ChannelID != "C123" {
		t.Errorf("expected channel C123, got %s", got.ChannelID)
	}
	if got.AgentName != "test-agent" {
		t.Errorf("expected agent test-agent, got %s", got.AgentName)
	}
}

func TestRegistryGetByThread(t *testing.T) {
	r := NewRegistry()
	r.Register(&Session{
		SessionTag: "sess-abc",
		ChannelID:  "C123",
		ThreadTS:   "1234567890.123456",
		Status:     "open",
		CreatedAt:  time.Now(),
	})

	got := r.GetByThread("C123", "1234567890.123456")
	if got == nil {
		t.Fatal("expected session by thread lookup, got nil")
	}
	if got.SessionTag != "sess-abc" {
		t.Errorf("expected sess-abc, got %s", got.SessionTag)
	}

	// Miss case.
	got = r.GetByThread("C999", "0000000000.000000")
	if got != nil {
		t.Errorf("expected nil for unknown thread, got %+v", got)
	}
}

func TestRegistryClose(t *testing.T) {
	r := NewRegistry()
	r.Register(&Session{
		SessionTag: "sess-abc",
		Status:     "open",
		CreatedAt:  time.Now(),
	})

	if !r.Close("sess-abc") {
		t.Error("expected Close to return true for existing session")
	}
	got := r.GetByTag("sess-abc")
	if got.Status != "closed" {
		t.Errorf("expected status closed, got %s", got.Status)
	}

	if r.Close("nonexistent") {
		t.Error("expected Close to return false for nonexistent session")
	}
}

func TestRegistryAll(t *testing.T) {
	r := NewRegistry()
	r.Register(&Session{SessionTag: "a", Status: "open", CreatedAt: time.Now()})
	r.Register(&Session{SessionTag: "b", Status: "open", CreatedAt: time.Now()})
	r.Register(&Session{SessionTag: "c", Status: "closed", CreatedAt: time.Now()})

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(all))
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup

	// Concurrent writes.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tag := fmt.Sprintf("sess-%d", i)
			r.Register(&Session{
				SessionTag: tag,
				ChannelID:  fmt.Sprintf("C%d", i),
				ThreadTS:   fmt.Sprintf("ts-%d", i),
				Status:     "open",
				CreatedAt:  time.Now(),
			})
		}(i)
	}

	// Concurrent reads.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.GetByTag(fmt.Sprintf("sess-%d", i))
			r.All()
		}(i)
	}

	wg.Wait()

	all := r.All()
	if len(all) != 50 {
		t.Errorf("expected 50 sessions after concurrent writes, got %d", len(all))
	}
}

// --- Broker Tests ---

func TestBrokerSendAndListen(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	registry := NewRegistry()
	broker := NewBroker(js, registry)

	session := "test-session-1"

	// Start listener in background.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var received *eventbus.ChatMessagePayload
	var listenErr error
	done := make(chan struct{})

	go func() {
		defer close(done)
		received, listenErr = broker.Listen(ctx, session)
	}()

	// Give listener time to subscribe.
	time.Sleep(200 * time.Millisecond)

	// Send a message.
	msg := &eventbus.ChatMessagePayload{
		SessionTag: session,
		Sender:     "test-user",
		SenderID:   "U123",
		Content:    "Hello agent!",
		ChannelID:  "C123",
		ThreadTS:   "ts-abc",
	}
	if err := broker.Send(ctx, msg, "in"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Wait for listener.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not return within 5s")
	}

	if listenErr != nil {
		t.Fatalf("Listen error: %v", listenErr)
	}
	if received == nil {
		t.Fatal("Listen returned nil message")
	}
	if received.Content != "Hello agent!" {
		t.Errorf("expected 'Hello agent!', got %q", received.Content)
	}
	if received.Sender != "test-user" {
		t.Errorf("expected sender 'test-user', got %q", received.Sender)
	}
	if received.Seq == 0 {
		t.Error("expected non-zero sequence number")
	}
}

func TestBrokerSendValidation(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	broker := NewBroker(js, NewRegistry())
	ctx := context.Background()

	// Missing session tag.
	err := broker.Send(ctx, &eventbus.ChatMessagePayload{Content: "hi"}, "in")
	if err == nil {
		t.Error("expected error for missing session_tag")
	}

	// Missing content.
	err = broker.Send(ctx, &eventbus.ChatMessagePayload{SessionTag: "s"}, "in")
	if err == nil {
		t.Error("expected error for missing content")
	}
}

func TestBrokerListenContextCancellation(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	broker := NewBroker(js, NewRegistry())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := broker.Listen(ctx, "no-messages-session")
		done <- err
	}()

	// Cancel after 500ms.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected context cancelled error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Listen did not respect context cancellation")
	}
}

func TestBrokerRoundTrip(t *testing.T) {
	// Full round-trip: Slack→agent (in) then agent→Slack (out).
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	registry := NewRegistry()
	broker := NewBroker(js, registry)
	session := "roundtrip-session"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register session.
	registry.Register(&Session{
		SessionTag: session,
		ChannelID:  "C-RT",
		ThreadTS:   "ts-rt",
		AgentName:  "rt-agent",
		Status:     "open",
		CreatedAt:  time.Now(),
	})

	// 1. Agent starts listening for inbound messages.
	agentGot := make(chan *eventbus.ChatMessagePayload, 1)
	go func() {
		msg, err := broker.Listen(ctx, session)
		if err != nil {
			t.Errorf("agent listen error: %v", err)
			return
		}
		agentGot <- msg
	}()

	time.Sleep(200 * time.Millisecond)

	// 2. Slack user sends a message.
	if err := broker.Send(ctx, &eventbus.ChatMessagePayload{
		SessionTag: session,
		Sender:     "slack-user",
		Content:    "What's the status?",
	}, "in"); err != nil {
		t.Fatalf("inbound send failed: %v", err)
	}

	// 3. Agent receives and processes.
	var inbound *eventbus.ChatMessagePayload
	select {
	case inbound = <-agentGot:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not receive inbound message")
	}

	if inbound.Content != "What's the status?" {
		t.Errorf("expected inbound content 'What's the status?', got %q", inbound.Content)
	}

	// 4. Set up Slack-side listener for outbound.
	slackGot := make(chan *eventbus.ChatMessagePayload, 1)
	go func() {
		msg, err := broker.Listen(ctx, session+"_out_listener")
		if err != nil {
			// Use the direct subject for out messages.
			return
		}
		slackGot <- msg
	}()

	// 4b. Actually subscribe to the out subject directly (more realistic).
	outSubject := eventbus.SubjectForChatSession(session, "out")
	outSub, err := js.SubscribeSync(outSubject, nats.DeliverNew(), nats.AckExplicit())
	if err != nil {
		t.Fatalf("subscribe to outbound: %v", err)
	}
	defer outSub.Unsubscribe()

	// 5. Agent sends a reply.
	if err := broker.Send(ctx, &eventbus.ChatMessagePayload{
		SessionTag: session,
		Sender:     "rt-agent",
		Content:    "Everything is green.",
	}, "out"); err != nil {
		t.Fatalf("outbound send failed: %v", err)
	}

	// 6. Slack-side receives the reply.
	outMsg, err := outSub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("did not receive outbound message: %v", err)
	}

	var outPayload eventbus.ChatMessagePayload
	if err := json.Unmarshal(outMsg.Data, &outPayload); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if outPayload.Content != "Everything is green." {
		t.Errorf("expected outbound content 'Everything is green.', got %q", outPayload.Content)
	}
	if outPayload.Sender != "rt-agent" {
		t.Errorf("expected sender 'rt-agent', got %q", outPayload.Sender)
	}
	outMsg.Ack()
}

func TestBrokerMultipleQueuedMessages(t *testing.T) {
	// Simulate: 3 Slack messages arrive while agent is busy, then agent calls ListenAll.
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	broker := NewBroker(js, NewRegistry())
	session := "queue-test"
	ctx := context.Background()

	// Publish 3 messages before any listener exists.
	for i := 1; i <= 3; i++ {
		if err := broker.Send(ctx, &eventbus.ChatMessagePayload{
			SessionTag: session,
			Sender:     "user",
			Content:    fmt.Sprintf("message %d", i),
		}, "in"); err != nil {
			t.Fatalf("send message %d: %v", i, err)
		}
	}

	// Small delay to ensure messages are persisted.
	time.Sleep(200 * time.Millisecond)

	// Agent calls ListenAll — should drain all 3.
	listenCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	messages, err := broker.ListenAll(listenCtx, session)
	if err != nil {
		t.Fatalf("ListenAll error: %v", err)
	}

	if len(messages) != 3 {
		t.Fatalf("expected 3 queued messages, got %d", len(messages))
	}

	for i, msg := range messages {
		expected := fmt.Sprintf("message %d", i+1)
		if msg.Content != expected {
			t.Errorf("message %d: expected %q, got %q", i, expected, msg.Content)
		}
	}
}

func TestBrokerTimestampAutoFill(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	broker := NewBroker(js, NewRegistry())
	msg := &eventbus.ChatMessagePayload{
		SessionTag: "ts-test",
		Sender:     "user",
		Content:    "hi",
	}

	before := time.Now().UTC()
	if err := broker.Send(context.Background(), msg, "in"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	after := time.Now().UTC()

	if msg.Timestamp.Before(before) || msg.Timestamp.After(after) {
		t.Errorf("expected timestamp between %v and %v, got %v", before, after, msg.Timestamp)
	}
}

func TestBrokerSendSetsSequence(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	broker := NewBroker(js, NewRegistry())
	ctx := context.Background()

	msg1 := &eventbus.ChatMessagePayload{SessionTag: "seq-test", Sender: "u", Content: "a"}
	msg2 := &eventbus.ChatMessagePayload{SessionTag: "seq-test", Sender: "u", Content: "b"}

	if err := broker.Send(ctx, msg1, "in"); err != nil {
		t.Fatal(err)
	}
	if err := broker.Send(ctx, msg2, "in"); err != nil {
		t.Fatal(err)
	}

	if msg1.Seq == 0 || msg2.Seq == 0 {
		t.Error("expected non-zero sequence numbers")
	}
	if msg2.Seq <= msg1.Seq {
		t.Errorf("expected msg2.Seq > msg1.Seq, got %d <= %d", msg2.Seq, msg1.Seq)
	}
}

// --- Event Type Tests ---

func TestChatEventTypes(t *testing.T) {
	tests := []struct {
		et     eventbus.EventType
		isChat bool
	}{
		{eventbus.EventChatMessageIn, true},
		{eventbus.EventChatMessageOut, true},
		{eventbus.EventChatStatus, true},
		{eventbus.EventSessionStart, false},
		{eventbus.EventDecisionCreated, false},
		{eventbus.EventOjJobCreated, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.et), func(t *testing.T) {
			if got := tt.et.IsChatEvent(); got != tt.isChat {
				t.Errorf("IsChatEvent() = %v, want %v", got, tt.isChat)
			}
		})
	}
}

func TestChatSubjectRouting(t *testing.T) {
	tests := []struct {
		et      eventbus.EventType
		want    string
	}{
		{eventbus.EventChatMessageIn, "chat.ChatMessageIn"},
		{eventbus.EventChatMessageOut, "chat.ChatMessageOut"},
		{eventbus.EventChatStatus, "chat.ChatStatus"},
	}

	for _, tt := range tests {
		t.Run(string(tt.et), func(t *testing.T) {
			got := eventbus.SubjectForEvent(tt.et)
			if got != tt.want {
				t.Errorf("SubjectForEvent(%s) = %q, want %q", tt.et, got, tt.want)
			}
		})
	}
}

func TestChatSessionSubject(t *testing.T) {
	tests := []struct {
		tag, dir, want string
	}{
		{"abc123", "in", "chat.session.abc123.in"},
		{"abc123", "out", "chat.session.abc123.out"},
		{"my-session", "in", "chat.session.my-session.in"},
	}

	for _, tt := range tests {
		got := eventbus.SubjectForChatSession(tt.tag, tt.dir)
		if got != tt.want {
			t.Errorf("SubjectForChatSession(%q, %q) = %q, want %q", tt.tag, tt.dir, got, tt.want)
		}
	}
}

func TestChatPayloadSerialization(t *testing.T) {
	ts := time.Date(2026, 2, 9, 12, 0, 0, 0, time.UTC)
	msg := eventbus.ChatMessagePayload{
		SessionTag: "sess-1",
		ThreadTS:   "ts-123",
		ChannelID:  "C456",
		Sender:     "user",
		SenderID:   "U789",
		Content:    "hello",
		Timestamp:  ts,
		Seq:        42,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded eventbus.ChatMessagePayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.SessionTag != msg.SessionTag {
		t.Errorf("session_tag mismatch: %q vs %q", decoded.SessionTag, msg.SessionTag)
	}
	if decoded.Content != msg.Content {
		t.Errorf("content mismatch: %q vs %q", decoded.Content, msg.Content)
	}
	if decoded.Seq != msg.Seq {
		t.Errorf("seq mismatch: %d vs %d", decoded.Seq, msg.Seq)
	}
	if !decoded.Timestamp.Equal(msg.Timestamp) {
		t.Errorf("timestamp mismatch: %v vs %v", decoded.Timestamp, msg.Timestamp)
	}
}

func TestChatStatusPayloadSerialization(t *testing.T) {
	status := eventbus.ChatStatusPayload{
		SessionTag: "sess-1",
		Status:     "listening",
		AgentName:  "my-agent",
		ChannelID:  "C123",
		ThreadTS:   "ts-456",
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded eventbus.ChatStatusPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Status != "listening" {
		t.Errorf("expected status 'listening', got %q", decoded.Status)
	}
	if decoded.AgentName != "my-agent" {
		t.Errorf("expected agent 'my-agent', got %q", decoded.AgentName)
	}
}

// --- Stream Integration Tests ---

func TestChatStreamExists(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	info, err := js.StreamInfo(eventbus.StreamChatEvents)
	if err != nil {
		t.Fatalf("CHAT_EVENTS stream not found: %v", err)
	}
	if info.Config.Name != "CHAT_EVENTS" {
		t.Errorf("expected stream name CHAT_EVENTS, got %s", info.Config.Name)
	}
}

func TestChatEventsDoNotCrossStreams(t *testing.T) {
	// Verify chat messages don't appear in other streams.
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	broker := NewBroker(js, NewRegistry())
	ctx := context.Background()

	if err := broker.Send(ctx, &eventbus.ChatMessagePayload{
		SessionTag: "cross-test",
		Sender:     "user",
		Content:    "should only be in CHAT_EVENTS",
	}, "in"); err != nil {
		t.Fatal(err)
	}

	// Check CHAT_EVENTS has the message.
	chatInfo, _ := js.StreamInfo(eventbus.StreamChatEvents)
	if chatInfo.State.Msgs != 1 {
		t.Errorf("expected 1 message in CHAT_EVENTS, got %d", chatInfo.State.Msgs)
	}

	// Check other streams are empty.
	hookInfo, _ := js.StreamInfo(eventbus.StreamHookEvents)
	if hookInfo.State.Msgs != 0 {
		t.Errorf("expected 0 messages in HOOK_EVENTS, got %d", hookInfo.State.Msgs)
	}
	decisionInfo, _ := js.StreamInfo(eventbus.StreamDecisionEvents)
	if decisionInfo.State.Msgs != 0 {
		t.Errorf("expected 0 messages in DECISION_EVENTS, got %d", decisionInfo.State.Msgs)
	}
	ojInfo, _ := js.StreamInfo(eventbus.StreamOjEvents)
	if ojInfo.State.Msgs != 0 {
		t.Errorf("expected 0 messages in OJ_EVENTS, got %d", ojInfo.State.Msgs)
	}
}

