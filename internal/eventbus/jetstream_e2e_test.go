//go:build integration

package eventbus_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/testutil/testdaemon"
)

// startEmbeddedNATS starts a test NATS server with JetStream, returning
// the JetStream context and a cleanup function.
func startEmbeddedNATS(t *testing.T) (nats.JetStreamContext, func()) {
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

	// Create required streams.
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

// TestJSHookEventPublish verifies that hook events emitted via daemon RPC
// are published to the HOOK_EVENTS JetStream stream. (bd-pfv4d)
func TestJSHookEventPublish(t *testing.T) {
	js, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.SetJetStream(js)
	d.Server.SetBus(bus)

	// Subscribe before emitting.
	sub, err := js.SubscribeSync(eventbus.SubjectHookPrefix+">", nats.DeliverAll())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Emit SessionStart via RPC.
	emitEvent(t, client, "SessionStart", "e2e-js-hook", t.TempDir())

	// Read from JetStream.
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("expected JetStream message: %v", err)
	}

	expectedSubject := eventbus.SubjectForEvent(eventbus.EventSessionStart)
	if msg.Subject != expectedSubject {
		t.Errorf("subject: expected %q, got %q", expectedSubject, msg.Subject)
	}

	// Verify payload contains session_id.
	if !strings.Contains(string(msg.Data), "e2e-js-hook") {
		t.Errorf("payload missing session_id, got: %s", string(msg.Data))
	}
}


// TestJSEventOrdering verifies that events are published in sequence order
// within a stream. (bd-pfv4d)
func TestJSEventOrdering(t *testing.T) {
	js, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.SetJetStream(js)
	d.Server.SetBus(bus)

	sub, err := js.SubscribeSync(eventbus.SubjectHookPrefix+">", nats.DeliverAll())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Emit 3 events in sequence.
	for i := 0; i < 3; i++ {
		emitEvent(t, client, "SessionStart", "e2e-js-order", t.TempDir())
	}

	// Read all 3 and verify monotonically increasing sequence numbers.
	var prevSeq uint64
	for i := 0; i < 3; i++ {
		msg, err := sub.NextMsg(5 * time.Second)
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		meta, err := msg.Metadata()
		if err != nil {
			t.Fatalf("metadata %d: %v", i, err)
		}
		if meta.Sequence.Stream <= prevSeq {
			t.Errorf("message %d: sequence %d not > previous %d", i, meta.Sequence.Stream, prevSeq)
		}
		prevSeq = meta.Sequence.Stream
	}
}

// TestJSStreamReplay verifies that a new subscriber can replay historical
// events from the stream (DeliverAll). (bd-pfv4d)
func TestJSStreamReplay(t *testing.T) {
	js, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.SetJetStream(js)
	d.Server.SetBus(bus)

	// Emit events BEFORE subscribing.
	emitEvent(t, client, "SessionStart", "e2e-js-replay-1", t.TempDir())
	emitEvent(t, client, "SessionStart", "e2e-js-replay-2", t.TempDir())

	// Subscribe AFTER events — with DeliverAll, should see historical events.
	sub, err := js.SubscribeSync(eventbus.SubjectHookPrefix+">", nats.DeliverAll())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Should receive both historical events.
	msg1, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("first historical event: %v", err)
	}
	if !strings.Contains(string(msg1.Data), "e2e-js-replay") {
		t.Errorf("first event missing session_id prefix, got: %s", string(msg1.Data))
	}

	msg2, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("second historical event: %v", err)
	}
	if !strings.Contains(string(msg2.Data), "e2e-js-replay") {
		t.Errorf("second event missing session_id prefix, got: %s", string(msg2.Data))
	}
}

// TestJSPayloadFidelity verifies that the raw JSON payload from Claude Code
// is preserved through the JetStream publish cycle. (bd-pfv4d)
func TestJSPayloadFidelity(t *testing.T) {
	js, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.SetJetStream(js)
	d.Server.SetBus(bus)

	sub, err := js.SubscribeSync(eventbus.SubjectHookPrefix+">", nats.DeliverAll())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Emit with custom fields in the JSON payload.
	cwd := t.TempDir()
	raw := map[string]interface{}{
		"session_id":  "e2e-fidelity",
		"cwd":         cwd,
		"tool_name":   "Read",
		"custom_field": "preserved_value",
	}
	eventJSON, _ := json.Marshal(raw)
	resp, err := client.Execute(rpc.OpBusEmit, rpc.BusEmitArgs{
		HookType:  "PreToolUse",
		EventJSON: json.RawMessage(eventJSON),
		SessionID: "e2e-fidelity",
	})
	if err != nil || !resp.Success {
		t.Fatalf("BusEmit: err=%v success=%v", err, resp.Success)
	}

	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("expected message: %v", err)
	}

	// Verify the raw JSON was preserved (not re-marshaled via Event struct).
	var received map[string]interface{}
	if err := json.Unmarshal(msg.Data, &received); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if received["custom_field"] != "preserved_value" {
		t.Errorf("custom_field not preserved: %v", received)
	}
	if received["session_id"] != "e2e-fidelity" {
		t.Errorf("session_id not preserved: %v", received)
	}
}

// TestJSEnsureStreamsIdempotent verifies that calling EnsureStreams multiple
// times doesn't error (idempotent creation). (bd-pfv4d)
func TestJSEnsureStreamsIdempotent(t *testing.T) {
	js, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	// EnsureStreams was already called in startEmbeddedNATS.
	// Call it again — should succeed.
	if err := eventbus.EnsureStreams(js); err != nil {
		t.Fatalf("second EnsureStreams: %v", err)
	}

	// And a third time for good measure.
	if err := eventbus.EnsureStreams(js); err != nil {
		t.Fatalf("third EnsureStreams: %v", err)
	}

	// Verify streams exist.
	streams := []string{
		eventbus.StreamHookEvents,
		eventbus.StreamDecisionEvents,
		eventbus.StreamAgentEvents,
		eventbus.StreamOjEvents,
	}
	for _, name := range streams {
		info, err := js.StreamInfo(name)
		if err != nil {
			t.Errorf("stream %s not found: %v", name, err)
			continue
		}
		if info.Config.Name != name {
			t.Errorf("stream name mismatch: expected %q, got %q", name, info.Config.Name)
		}
	}
}

// TestJSMultiStreamRouting verifies that different event types are routed
// to their correct streams. (bd-pfv4d)
func TestJSMultiStreamRouting(t *testing.T) {
	js, cleanup := startEmbeddedNATS(t)
	defer cleanup()

	d := testdaemon.Start(t)
	client := d.Client(t)

	bus := eventbus.New()
	bus.SetJetStream(js)
	d.Server.SetBus(bus)

	// Subscribe to multiple streams.
	hookSub, _ := js.SubscribeSync(eventbus.SubjectHookPrefix+">", nats.DeliverAll())
	defer hookSub.Unsubscribe()

	// Emit a hook event (SessionStart → HOOK_EVENTS).
	emitEvent(t, client, "SessionStart", "e2e-route", t.TempDir())

	// Verify it arrived on the hook stream.
	msg, err := hookSub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("hook event: %v", err)
	}
	if !strings.HasPrefix(msg.Subject, eventbus.SubjectHookPrefix) {
		t.Errorf("hook event wrong subject prefix: %s", msg.Subject)
	}
}
