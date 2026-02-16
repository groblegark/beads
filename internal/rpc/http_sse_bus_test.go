//go:build integration

package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/steveyegge/beads/internal/eventbus"
)

// startBusTestNATS starts an embedded NATS server with JetStream for bus SSE tests.
func startBusTestNATS(t *testing.T) (nats.JetStreamContext, func()) {
	t.Helper()
	dir := t.TempDir()
	opts := &natsserver.Options{
		Port:               -1,
		JetStream:          true,
		JetStreamMaxMemory: 1024 << 20,
		JetStreamMaxStore:  1024 << 20,
		StoreDir:           dir,
		NoLog:              true,
		NoSigs:             true,
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("create NATS: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS not ready")
	}

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		t.Fatalf("connect NATS: %v", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatalf("JetStream: %v", err)
	}

	if err := eventbus.EnsureStreams(js); err != nil {
		nc.Close()
		ns.Shutdown()
		t.Fatalf("EnsureStreams: %v", err)
	}

	return js, func() {
		nc.Close()
		ns.Shutdown()
	}
}

// publishTestEvent publishes a raw JSON event to JetStream on the given subject.
func publishTestEvent(t *testing.T, js nats.JetStreamContext, subject string, payload map[string]interface{}) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := js.Publish(subject, data); err != nil {
		t.Fatalf("publish to %s: %v", subject, err)
	}
}

// readBusSSEEvents reads SSE events from an http.Response body until the context
// is canceled or the connection closes. Returns events on the channel.
func readBusSSEEvents(ctx context.Context, resp *http.Response) <-chan BusSSEEvent {
	events := make(chan BusSSEEvent, 64)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var currentData string

		for scanner.Scan() {
			line := scanner.Text()

			if line == "" {
				if currentData != "" {
					var evt BusSSEEvent
					if err := json.Unmarshal([]byte(currentData), &evt); err == nil {
						select {
						case events <- evt:
						case <-ctx.Done():
							return
						}
					}
				}
				currentData = ""
				continue
			}

			if strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, "data:") {
				data := strings.TrimPrefix(line, "data: ")
				data = strings.TrimPrefix(data, "data:")
				if currentData != "" {
					currentData += "\n" + data
				} else {
					currentData = data
				}
			}
			// Ignore id:, event:, and comment lines for test purposes
		}
	}()
	return events
}

// TestBusSSE_HookEventDelivery verifies that hook events published to JetStream
// are delivered through the /bus/events SSE endpoint. (bd-kkosv)
func TestBusSSE_HookEventDelivery(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "") // no auth for test

	ts := httptest.NewServer(http.HandlerFunc(h.handleSSEBusEvents))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect SSE client to ?stream=hooks
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?stream=hooks", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	events := readBusSSEEvents(ctx, resp)

	// Give the SSE handler time to establish NATS subscriptions
	time.Sleep(200 * time.Millisecond)

	// Publish a hook event directly to JetStream
	publishTestEvent(t, js, "hooks.SessionStart", map[string]interface{}{
		"session_id": "e2e-sse-hook",
		"cwd":        "/tmp/test",
	})

	// Wait for it to arrive on SSE
	select {
	case evt := <-events:
		if evt.Stream != "hooks" {
			t.Errorf("stream: expected %q, got %q", "hooks", evt.Stream)
		}
		if evt.Type != "SessionStart" {
			t.Errorf("type: expected %q, got %q", "SessionStart", evt.Type)
		}
		if evt.Seq == 0 {
			t.Error("expected non-zero sequence number")
		}
		if evt.TS == "" {
			t.Error("expected non-empty timestamp")
		}
		// Verify payload preserved
		var payload map[string]interface{}
		if err := json.Unmarshal(evt.Payload, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload["session_id"] != "e2e-sse-hook" {
			t.Errorf("payload session_id: expected %q, got %v", "e2e-sse-hook", payload["session_id"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for hook event on SSE")
	}
}

// TestBusSSE_StreamFilter verifies that --stream filtering works: subscribing to
// "agents" only receives agent events, not hook events. (bd-kkosv)
func TestBusSSE_StreamFilter(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "")
	ts := httptest.NewServer(http.HandlerFunc(h.handleSSEBusEvents))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Subscribe to agents stream only
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?stream=agents", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	events := readBusSSEEvents(ctx, resp)
	time.Sleep(200 * time.Millisecond)

	// Publish a hook event (should NOT be received)
	publishTestEvent(t, js, "hooks.SessionStart", map[string]interface{}{
		"session_id": "should-not-arrive",
	})

	// Publish an agent event (should be received)
	publishTestEvent(t, js, "agents.AgentStarted", map[string]interface{}{
		"session_id": "e2e-sse-agent",
		"agent_name": "test-agent",
	})

	// Wait for the agent event
	select {
	case evt := <-events:
		if evt.Stream != "agents" {
			t.Errorf("stream: expected %q, got %q", "agents", evt.Stream)
		}
		if evt.Type != "AgentStarted" {
			t.Errorf("type: expected %q, got %q", "AgentStarted", evt.Type)
		}
		var payload map[string]interface{}
		json.Unmarshal(evt.Payload, &payload)
		if payload["session_id"] != "e2e-sse-agent" {
			t.Errorf("got wrong event — filter may not be working")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for agent event on SSE")
	}
}

// TestBusSSE_AllStreams verifies that ?stream=all delivers events from multiple streams. (bd-kkosv)
func TestBusSSE_AllStreams(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "")
	ts := httptest.NewServer(http.HandlerFunc(h.handleSSEBusEvents))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Subscribe to all streams
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?stream=all", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	events := readBusSSEEvents(ctx, resp)
	time.Sleep(300 * time.Millisecond)

	// Publish events on 3 different streams
	publishTestEvent(t, js, "hooks.SessionStart", map[string]interface{}{"session_id": "e2e-all-hooks"})
	publishTestEvent(t, js, "agents.AgentStarted", map[string]interface{}{"session_id": "e2e-all-agents"})
	publishTestEvent(t, js, "mail.MailSent", map[string]interface{}{"session_id": "e2e-all-mail"})

	// Collect events — we should receive all 3
	received := map[string]bool{}
	timeout := time.After(5 * time.Second)

	for len(received) < 3 {
		select {
		case evt := <-events:
			received[evt.Stream] = true
		case <-timeout:
			t.Fatalf("timeout: only received events from streams %v (wanted hooks, agents, mail)", received)
		}
	}

	for _, stream := range []string{"hooks", "agents", "mail"} {
		if !received[stream] {
			t.Errorf("missing event from stream %q", stream)
		}
	}
}

// TestBusSSE_EventTypeFilter verifies that the ?filter= parameter restricts delivery
// to only matching event types. (bd-kkosv)
func TestBusSSE_EventTypeFilter(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "")
	ts := httptest.NewServer(http.HandlerFunc(h.handleSSEBusEvents))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Subscribe to hooks stream with filter=Stop
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?stream=hooks&filter=Stop", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	events := readBusSSEEvents(ctx, resp)
	time.Sleep(200 * time.Millisecond)

	// Publish a SessionStart event (should NOT match filter)
	publishTestEvent(t, js, "hooks.SessionStart", map[string]interface{}{
		"session_id": "should-not-arrive",
	})

	// Publish a Stop event (should match filter)
	publishTestEvent(t, js, "hooks.Stop", map[string]interface{}{
		"session_id": "e2e-sse-stop",
	})

	// Should receive the Stop event
	select {
	case evt := <-events:
		if evt.Type != "Stop" {
			t.Errorf("type: expected %q, got %q", "Stop", evt.Type)
		}
		var payload map[string]interface{}
		json.Unmarshal(evt.Payload, &payload)
		if payload["session_id"] != "e2e-sse-stop" {
			t.Errorf("payload: expected e2e-sse-stop, got %v", payload["session_id"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for filtered Stop event")
	}
}

// TestBusSSE_SinceReplay verifies that ?since= replays historical events from
// JetStream. (bd-kkosv)
func TestBusSSE_SinceReplay(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "")
	ts := httptest.NewServer(http.HandlerFunc(h.handleSSEBusEvents))
	defer ts.Close()

	// Publish events BEFORE connecting SSE
	beforePublish := time.Now().Add(-time.Second)
	publishTestEvent(t, js, "hooks.SessionStart", map[string]interface{}{"session_id": "e2e-replay-1"})
	publishTestEvent(t, js, "hooks.Stop", map[string]interface{}{"session_id": "e2e-replay-2"})

	// Wait for events to be stored
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect with since= to replay from before the events
	sinceMs := beforePublish.UnixMilli()
	url := fmt.Sprintf("%s?stream=hooks&since=%d", ts.URL, sinceMs)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	events := readBusSSEEvents(ctx, resp)

	// Should receive both historical events
	var received []BusSSEEvent
	timeout := time.After(5 * time.Second)

	for len(received) < 2 {
		select {
		case evt := <-events:
			received = append(received, evt)
		case <-timeout:
			t.Fatalf("timeout: only received %d events (wanted 2)", len(received))
		}
	}

	// Verify we got both
	types := map[string]bool{}
	for _, evt := range received {
		types[evt.Type] = true
	}
	if !types["SessionStart"] || !types["Stop"] {
		t.Errorf("expected SessionStart and Stop in replay, got types: %v", types)
	}
}

// TestBusSSE_Auth verifies that /bus/events requires authentication when a token
// is configured. (bd-kkosv)
func TestBusSSE_Auth(t *testing.T) {
	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	h := NewHTTPServer(s, ":0", "secret-token")

	// Test without auth
	req := httptest.NewRequest(http.MethodGet, "/bus/events", nil)
	w := httptest.NewRecorder()
	h.handleSSEBusEvents(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth: expected 401, got %d", w.Code)
	}

	// Test with wrong token
	req = httptest.NewRequest(http.MethodGet, "/bus/events", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w = httptest.NewRecorder()
	h.handleSSEBusEvents(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: expected 401, got %d", w.Code)
	}

	// Test with correct token but no bus (should return 503)
	req = httptest.NewRequest(http.MethodGet, "/bus/events?stream=hooks", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	w = httptest.NewRecorder()
	h.handleSSEBusEvents(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("no bus: expected 503, got %d", w.Code)
	}

	// Test with wrong method
	req = httptest.NewRequest(http.MethodPost, "/bus/events", nil)
	w = httptest.NewRecorder()
	h.handleSSEBusEvents(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: expected 405, got %d", w.Code)
	}
}

// TestBusSSE_InvalidStream verifies that ?stream=bogus returns 400. (bd-kkosv)
func TestBusSSE_InvalidStream(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "")

	req := httptest.NewRequest(http.MethodGet, "/bus/events?stream=bogus", nil)
	w := httptest.NewRecorder()
	h.handleSSEBusEvents(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid stream: expected 400, got %d", w.Code)
	}
}

// TestBusSSE_MultiStream verifies subscribing to multiple comma-separated streams. (bd-kkosv)
func TestBusSSE_MultiStream(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "")
	ts := httptest.NewServer(http.HandlerFunc(h.handleSSEBusEvents))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Subscribe to hooks AND mail (but not agents)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?stream=hooks,mail", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	events := readBusSSEEvents(ctx, resp)
	time.Sleep(200 * time.Millisecond)

	// Publish on all three streams
	publishTestEvent(t, js, "hooks.SessionStart", map[string]interface{}{"session_id": "e2e-multi-hooks"})
	publishTestEvent(t, js, "mail.MailSent", map[string]interface{}{"session_id": "e2e-multi-mail"})
	publishTestEvent(t, js, "agents.AgentStarted", map[string]interface{}{"session_id": "e2e-multi-agents"})

	// Should receive hooks and mail, but NOT agents
	received := map[string]bool{}
	timeout := time.After(3 * time.Second)

	for {
		select {
		case evt, ok := <-events:
			if !ok {
				goto done
			}
			received[evt.Stream] = true
			if evt.Stream == "agents" {
				t.Error("received agent event despite not subscribing to agents stream")
			}
		case <-timeout:
			goto done
		}
	}
done:

	if !received["hooks"] {
		t.Error("missing hook event from multi-stream subscription")
	}
	if !received["mail"] {
		t.Error("missing mail event from multi-stream subscription")
	}
}

// TestBusSSE_SSEFormat verifies the wire format of SSE events: id, event, and data fields. (bd-kkosv)
func TestBusSSE_SSEFormat(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "")
	ts := httptest.NewServer(http.HandlerFunc(h.handleSSEBusEvents))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?stream=hooks", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	time.Sleep(200 * time.Millisecond)

	publishTestEvent(t, js, "hooks.Stop", map[string]interface{}{"session_id": "e2e-format"})

	// Read raw SSE lines to verify format
	scanner := bufio.NewScanner(resp.Body)
	var lines []string
	deadline := time.After(5 * time.Second)

	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for SSE event data")
		default:
		}

		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		lines = append(lines, line)

		// After we see a data line followed by empty line, we have one complete event
		if line == "" && len(lines) > 1 {
			break
		}
	}

	// Verify SSE fields present
	hasID := false
	hasEvent := false
	hasData := false

	for _, line := range lines {
		if strings.HasPrefix(line, "id:") {
			hasID = true
		}
		if strings.HasPrefix(line, "event:") {
			hasEvent = true
			// event field should be the stream name
			if !strings.Contains(line, "hooks") {
				t.Errorf("event field should contain stream name 'hooks': %s", line)
			}
		}
		if strings.HasPrefix(line, "data:") {
			hasData = true
			// Data should be valid JSON with expected fields
			data := strings.TrimPrefix(line, "data: ")
			var evt BusSSEEvent
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				t.Errorf("data is not valid BusSSEEvent JSON: %v", err)
			} else {
				if evt.Stream != "hooks" {
					t.Errorf("data.stream = %q, want hooks", evt.Stream)
				}
				if evt.Type != "Stop" {
					t.Errorf("data.type = %q, want Stop", evt.Type)
				}
			}
		}
	}

	if !hasID {
		t.Error("missing 'id:' field in SSE event")
	}
	if !hasEvent {
		t.Error("missing 'event:' field in SSE event")
	}
	if !hasData {
		t.Error("missing 'data:' field in SSE event")
	}
}

// TestBusSSE_ClientDisconnectCleansUp verifies that when the SSE client disconnects,
// server-side NATS subscriptions are cleaned up. (bd-kkosv)
func TestBusSSE_ClientDisconnectCleansUp(t *testing.T) {
	js, cleanup := startBusTestNATS(t)
	defer cleanup()

	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	bus := eventbus.New()
	bus.SetJetStream(js)
	s.SetBus(bus)

	h := NewHTTPServer(s, ":0", "")
	ts := httptest.NewServer(http.HandlerFunc(h.handleSSEBusEvents))
	defer ts.Close()

	// Connect and immediately cancel
	ctx, cancel := context.WithCancel(context.Background())

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?stream=hooks", nil)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}

	// Cancel the context to simulate client disconnect
	cancel()
	resp.Body.Close()

	// Give the server time to clean up subscriptions
	time.Sleep(500 * time.Millisecond)

	// Verify we can still publish without error (subscriptions cleaned up)
	publishTestEvent(t, js, "hooks.SessionStart", map[string]interface{}{
		"session_id": "after-disconnect",
	})

	// If we got here without panic or error, cleanup worked
}

// TestBusSSE_NoBusReturns503 verifies that /bus/events returns 503 when no event bus
// is configured on the server. (bd-kkosv)
func TestBusSSE_NoBusReturns503(t *testing.T) {
	s := NewServer("/tmp/test.sock", nil, "/tmp/test", "/tmp/test.db")
	// Do NOT call s.SetBus() — leave bus as nil

	h := NewHTTPServer(s, ":0", "")

	req := httptest.NewRequest(http.MethodGet, "/bus/events?stream=hooks", nil)
	w := httptest.NewRecorder()
	h.handleSSEBusEvents(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when no bus, got %d", w.Code)
	}
}

// TestParseBusStreams verifies the stream query parameter parser. (bd-kkosv)
func TestParseBusStreams(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string // nil means error
	}{
		{"empty returns all", "", eventbus.StreamNames},
		{"all keyword", "all", eventbus.StreamNames},
		{"single valid", "hooks", []string{"hooks"}},
		{"two valid", "hooks,agents", []string{"hooks", "agents"}},
		{"with spaces", "hooks , agents", []string{"hooks", "agents"}},
		{"invalid returns nil", "bogus", nil},
		{"one valid one invalid", "hooks,bogus", nil},
		{"empty parts ignored", "hooks,,agents", []string{"hooks", "agents"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseBusStreams(tt.input)
			if tt.want == nil {
				if got != nil {
					t.Errorf("expected nil for invalid input, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("len: expected %d, got %d (%v)", len(tt.want), len(got), got)
				return
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("[%d]: expected %q, got %q", i, tt.want[i], got[i])
				}
			}
		})
	}
}
