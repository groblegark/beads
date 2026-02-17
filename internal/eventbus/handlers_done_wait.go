package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/types"
)

// DoneWaitHandler blocks on Stop events until an inbox message or decision
// response arrives, keeping the agent alive instead of letting it exit.
//
// Priority 90 (runs last — after all other Stop handlers have injected
// context, drained inbox, and nudged about beads/commits).
//
// When an event arrives during the wait period, the handler blocks the Stop
// and injects the event content as the block reason. This causes Claude Code
// to wake the agent with the event payload instead of exiting.
//
// Opt-out: set BD_DONE_DISABLED=1 to skip this handler entirely.
//
// Requires either NATS JetStream (for sub-second latency) or storage
// (for poll-based fallback). If neither is available, the handler is a no-op.
// (bd-s7wv1)
type DoneWaitHandler struct {
	store   InboxStore
	js      nats.JetStreamContext
	timeout time.Duration // default 30 minutes
}

func (h *DoneWaitHandler) ID() string          { return "done-wait" }
func (h *DoneWaitHandler) Handles() []EventType { return []EventType{EventStop} }
func (h *DoneWaitHandler) Priority() int        { return 90 }

// SetInboxStore wires in direct storage access for poll-based fallback.
func (h *DoneWaitHandler) SetInboxStore(store InboxStore) { h.store = store }

// SetJetStream wires in NATS JetStream for real-time event delivery.
func (h *DoneWaitHandler) SetJetStream(js nats.JetStreamContext) { h.js = js }

// SetTimeout overrides the default 30-minute wait timeout.
func (h *DoneWaitHandler) SetTimeout(d time.Duration) { h.timeout = d }

func (h *DoneWaitHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	// Opt-out check.
	if os.Getenv("BD_DONE_DISABLED") == "1" {
		return nil
	}

	agentName := event.Actor
	if agentName == "" {
		return nil
	}

	// If the result already has a block, another handler wants the agent to stay.
	// Don't add a second block — let the existing one take effect.
	if result.Block {
		return nil
	}

	timeout := h.timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	log.Printf("done-wait: blocking Stop for %s (timeout=%s)", agentName, timeout)

	// Try NATS first for sub-second latency.
	if h.js != nil {
		waitResult, err := h.waitViaNATS(ctx, agentName, timeout)
		if err == nil && waitResult != nil {
			if !waitResult.timedOut {
				result.Block = true
				result.Reason = h.formatBlockReason(waitResult)
				log.Printf("done-wait: woke %s via NATS (event=%s)", agentName, waitResult.eventType)
				return nil
			}
			log.Printf("done-wait: NATS wait timed out for %s", agentName)
			return nil
		}
		if err != nil {
			log.Printf("done-wait: NATS failed (%v), trying poll fallback", err)
		}
	}

	// Poll fallback: check inbox periodically.
	if h.store != nil {
		waitResult := h.waitViaPoll(ctx, agentName, timeout)
		if waitResult != nil && !waitResult.timedOut {
			result.Block = true
			result.Reason = h.formatBlockReason(waitResult)
			log.Printf("done-wait: woke %s via poll (event=%s)", agentName, waitResult.eventType)
			return nil
		}
	}

	log.Printf("done-wait: no events arrived for %s, allowing exit", agentName)
	return nil
}

// doneWaitEvent represents an event that woke the agent.
type doneWaitEvent struct {
	eventType string
	content   string
	source    string
	timedOut  bool
}

// waitViaNATS subscribes to JetStream subjects and blocks until an event arrives.
func (h *DoneWaitHandler) waitViaNATS(ctx context.Context, agentName string, timeout time.Duration) (*doneWaitEvent, error) {
	var subs []*nats.Subscription

	// Subscribe to inbox events for this agent.
	subject := "inbox.agent." + agentName
	sub, err := h.js.SubscribeSync(subject, nats.DeliverNew())
	if err != nil {
		return nil, fmt.Errorf("subscribe %s: %w", subject, err)
	}
	subs = append(subs, sub)

	// Also listen for broadcasts.
	broadcastSub, err := h.js.SubscribeSync("inbox.all", nats.DeliverNew())
	if err == nil {
		subs = append(subs, broadcastSub)
	}

	// Subscribe to decision events.
	decSub, err := h.js.SubscribeSync(SubjectDecisionPrefix+agentName+".>", nats.DeliverNew())
	if err == nil {
		subs = append(subs, decSub)
	}
	globalDecSub, err := h.js.SubscribeSync(SubjectDecisionPrefix+"_global.>", nats.DeliverNew())
	if err == nil {
		subs = append(subs, globalDecSub)
	}

	if len(subs) == 0 {
		return nil, fmt.Errorf("no subscriptions created")
	}

	defer func() {
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
	}()

	// Check for pre-existing undelivered inbox items (race: item arrived before subscription).
	if h.store != nil {
		items, err := h.store.InboxDrain(ctx, agentName)
		if err == nil && len(items) > 0 {
			// Mark as delivered so they don't re-appear.
			ids := make([]string, len(items))
			for i, item := range items {
				ids[i] = item.ID
			}
			_ = h.store.InboxMarkDelivered(ctx, agentName, ids)

			return &doneWaitEvent{
				eventType: "inbox",
				content:   formatInboxItems(items),
				source:    items[0].Source,
			}, nil
		}
	}

	// Poll subscriptions in a loop.
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return &doneWaitEvent{timedOut: true}, nil
		}

		select {
		case <-ctx.Done():
			return &doneWaitEvent{timedOut: true}, nil
		default:
		}

		timeLeft := time.Until(deadline)
		pollTimeout := 2 * time.Second
		if timeLeft < pollTimeout {
			pollTimeout = timeLeft
		}

		for _, sub := range subs {
			msg, err := sub.NextMsg(pollTimeout)
			if err != nil {
				continue
			}
			_ = msg.Ack()

			subject := msg.Subject
			stream := StreamForSubject(subject)

			switch stream {
			case "inbox":
				var item types.InboxItem
				if err := json.Unmarshal(msg.Data, &item); err == nil {
					return &doneWaitEvent{
						eventType: "inbox",
						content:   item.Content,
						source:    item.Source,
					}, nil
				}
				return &doneWaitEvent{
					eventType: "inbox",
					content:   string(msg.Data),
				}, nil

			case "decisions":
				var payload DecisionEventPayload
				if err := json.Unmarshal(msg.Data, &payload); err == nil {
					return &doneWaitEvent{
						eventType: "decision",
						content:   fmt.Sprintf("Decision %s resolved: %s", payload.DecisionID, payload.ChosenLabel),
						source:    payload.ResolvedBy,
					}, nil
				}
				return &doneWaitEvent{
					eventType: "decision",
					content:   string(msg.Data),
				}, nil
			}
		}
	}
}

// waitViaPoll polls the inbox table for new items.
func (h *DoneWaitHandler) waitViaPoll(ctx context.Context, agentName string, timeout time.Duration) *doneWaitEvent {
	deadline := time.Now().Add(timeout)

	// Immediate check for pre-existing items.
	items, err := h.store.InboxDrain(ctx, agentName)
	if err == nil && len(items) > 0 {
		ids := make([]string, len(items))
		for i, item := range items {
			ids[i] = item.ID
		}
		_ = h.store.InboxMarkDelivered(ctx, agentName, ids)

		return &doneWaitEvent{
			eventType: "inbox",
			content:   formatInboxItems(items),
			source:    items[0].Source,
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return &doneWaitEvent{timedOut: true}
		case <-ticker.C:
			if time.Now().After(deadline) {
				return &doneWaitEvent{timedOut: true}
			}

			items, err := h.store.InboxDrain(ctx, agentName)
			if err != nil {
				continue
			}
			if len(items) > 0 {
				ids := make([]string, len(items))
				for i, item := range items {
					ids[i] = item.ID
				}
				_ = h.store.InboxMarkDelivered(ctx, agentName, ids)

				return &doneWaitEvent{
					eventType: "inbox",
					content:   formatInboxItems(items),
					source:    items[0].Source,
				}
			}
		}
	}
}

// formatBlockReason builds the block reason for Claude Code.
// The reason is a JSON block decision that tells the agent what woke it up.
func (h *DoneWaitHandler) formatBlockReason(evt *doneWaitEvent) string {
	// Build a reason that the agent can act on.
	parts := []string{
		fmt.Sprintf("An event arrived while you were idle (type: %s).", evt.eventType),
	}
	if evt.source != "" {
		parts = append(parts, fmt.Sprintf("From: %s.", evt.source))
	}
	if evt.content != "" {
		parts = append(parts, fmt.Sprintf("\n%s", evt.content))
	}
	parts = append(parts, "\nYou were about to exit, but new work arrived. Process this event and continue working.")
	return strings.Join(parts, " ")
}

// formatInboxItems is defined in handlers.go — reused here.
