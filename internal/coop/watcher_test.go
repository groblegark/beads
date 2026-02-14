package coop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func TestWatcherStateChanges(t *testing.T) {
	events := []StateChangeEvent{
		{Type: WSTypeStateChange, Prev: StateWorking, Next: StateWaitingForInput, Seq: 100},
		{Type: WSTypeStateChange, Prev: StateWaitingForInput, Next: StatePermissionPrompt, Seq: 101,
			Prompt: &PromptContext{Type: "permission", Tool: "Bash", InputPreview: "rm -rf /"}},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify mode=state query param
		if r.URL.Query().Get("mode") != "state" {
			t.Errorf("mode = %q, want 'state'", r.URL.Query().Get("mode"))
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		for _, ev := range events {
			data, _ := json.Marshal(ev)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}

		// Send a non-state-change message (should be filtered)
		conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"pong"}`))
		time.Sleep(10 * time.Millisecond)

		// Close cleanly
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"))
	}))
	defer srv.Close()

	// Convert http://... to ws://...
	wsURL := strings.Replace(srv.URL, "http://", "http://", 1)
	w := NewWatcher(wsURL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch error: %v", err)
	}

	var received []StateChangeEvent
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				goto done
			}
			received = append(received, ev)
			if len(received) >= len(events) {
				cancel()
				// Drain remaining
				for range ch {
				}
				goto done
			}
		case <-timeout:
			t.Fatal("timed out waiting for events")
			goto done
		}
	}
done:

	if len(received) != len(events) {
		t.Fatalf("got %d events, want %d", len(received), len(events))
	}

	if received[0].Prev != StateWorking || received[0].Next != StateWaitingForInput {
		t.Errorf("event[0] = %+v", received[0])
	}
	if received[1].Prompt == nil || received[1].Prompt.Tool != "Bash" {
		t.Errorf("event[1] prompt = %+v", received[1].Prompt)
	}
}

func TestWatcherAuthToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token != "secret-token" {
			t.Errorf("token = %q, want 'secret-token'", token)
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	w := NewWatcher(srv.URL, WithToken("secret-token"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch error: %v", err)
	}

	// Just drain
	cancel()
	for range ch {
	}
}

func TestWatcherClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Hold connection open
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	w := NewWatcher(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch error: %v", err)
	}

	// Give it time to connect
	time.Sleep(100 * time.Millisecond)

	w.Close()
	cancel()

	// Channel should close
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel closed after Close()")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}

// TestWatcherReconnectsAfterServerRestart verifies that the Watcher
// automatically reconnects after the WebSocket server goes down and comes
// back up (simulating a coopmux restart). This is the core reconnection
// scenario that was causing flakiness in production.
//
// Uses a proxy approach: a stable listener stays open while we swap the
// backend between "accepting connections" and "rejecting connections" to
// simulate the server going down and coming back.
func TestWatcherReconnectsAfterServerRestart(t *testing.T) {
	var connectCount atomic.Int32
	var phase atomic.Int32 // 1=serve, 2=reject, 3=serve-again
	phase.Store(1)

	phase1Events := []StateChangeEvent{
		{Type: WSTypeStateChange, Prev: StateStarting, Next: StateWorking, Seq: 1},
	}
	phase3Events := []StateChangeEvent{
		{Type: WSTypeStateChange, Prev: StateWorking, Next: StateWaitingForInput, Seq: 50},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := phase.Load()

		// Phase 2: reject connections (simulate server down).
		if p == 2 {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		connectCount.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Send events based on phase.
		var events []StateChangeEvent
		if p == 1 {
			events = phase1Events
		} else {
			events = phase3Events
		}

		for _, ev := range events {
			data, _ := json.Marshal(ev)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}

		if p == 1 {
			// Phase 1: close connection after sending events (simulate crash).
			conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "restarting"))
			return
		}

		// Phase 3: hold connection open.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	w := NewWatcher(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ch, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch error: %v", err)
	}

	// Collect phase 1 event.
	select {
	case ev := <-ch:
		if ev.Next != StateWorking {
			t.Errorf("phase1: got Next=%q, want %q", ev.Next, StateWorking)
		}
		t.Logf("phase1: received event seq=%d (%s → %s)", ev.Seq, ev.Prev, ev.Next)
	case <-time.After(5 * time.Second):
		t.Fatal("phase1: timed out waiting for event")
	}

	// Phase 2: switch to rejecting connections (simulates mux restart window).
	phase.Store(2)
	t.Log("phase2: server now rejecting — watcher should detect disconnect and backoff")

	// Wait for watcher to attempt reconnection and fail a few times.
	time.Sleep(2 * time.Second)

	// Phase 3: server comes back, start accepting with new events.
	phase.Store(3)
	t.Log("phase3: server accepting again — watcher should reconnect")

	// Collect phase 3 event — proves reconnection worked.
	select {
	case ev := <-ch:
		if ev.Next != StateWaitingForInput {
			t.Errorf("phase3: got Next=%q, want %q", ev.Next, StateWaitingForInput)
		}
		t.Logf("phase3: received event seq=%d (%s → %s) — reconnection successful", ev.Seq, ev.Prev, ev.Next)
	case <-time.After(10 * time.Second):
		t.Fatal("phase3: timed out waiting for reconnection event")
	}

	// Verify we connected at least twice (phase 1 + phase 3).
	connections := connectCount.Load()
	if connections < 2 {
		t.Errorf("expected >= 2 WebSocket upgrades, got %d", connections)
	}
	t.Logf("total WebSocket connections: %d (including reconnect retries)", connections)

	cancel()
	for range ch {
	}
}
