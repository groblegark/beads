package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/types"
)

// handleDoneWait blocks until an inbox message or decision response arrives
// for the specified agent, or until timeout. This is the core of "bd done" —
// it allows agents to yield their session and wake when something happens.
//
// Uses NATS JetStream for sub-second latency when available, falls back to
// polling the inbox table when NATS is unavailable. (bd-3lheb)
func (s *Server) handleDoneWait(req *Request) Response {
	var args DoneWaitArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid done_wait args: %v", err),
		}
	}

	// Default agent name from request actor.
	agentName := args.AgentName
	if agentName == "" {
		agentName = s.reqActor(req)
	}
	if agentName == "" {
		return Response{
			Success: false,
			Error:   "agent_name is required (or set BD_ACTOR)",
		}
	}

	// Default timeout: 30 minutes.
	timeoutSec := args.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 1800
	}
	// Cap at 1 hour.
	if timeoutSec > 3600 {
		timeoutSec = 3600
	}
	timeout := time.Duration(timeoutSec) * time.Second

	// MaxPollSec caps each server-side poll to stay under proxy timeouts.
	// The client retries until the overall timeout expires. (bd-wccdf)
	maxPoll := args.MaxPollSec
	if maxPoll > 0 && time.Duration(maxPoll)*time.Second < timeout {
		timeout = time.Duration(maxPoll) * time.Second
	}

	// Parse event filter.
	listenInbox := true
	listenDecision := true
	if args.On != "" {
		listenInbox = false
		listenDecision = false
		for _, part := range strings.Split(args.On, ",") {
			switch strings.TrimSpace(part) {
			case "inbox":
				listenInbox = true
			case "decision":
				listenDecision = true
			}
		}
	}

	fmt.Fprintf(os.Stderr, "done_wait: agent=%s timeout=%s inbox=%v decision=%v\n",
		agentName, timeout, listenInbox, listenDecision)

	// Use request timeout if provided (Stop hooks set 1 hour).
	ctx := context.Background()
	if req.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.TimeoutMs)*time.Millisecond)
		defer cancel()
	}

	// Try NATS first for sub-second latency.
	s.mu.RLock()
	bus := s.bus
	s.mu.RUnlock()

	if bus != nil && bus.JetStreamEnabled() {
		result, err := s.doneWaitViaNATS(ctx, bus.JetStream(), agentName, timeout, listenInbox, listenDecision)
		if err == nil {
			data, _ := json.Marshal(result)
			return Response{Success: true, Data: data}
		}
		fmt.Fprintf(os.Stderr, "done_wait: NATS failed (%v), falling back to polling\n", err)
	}

	// Fallback: poll inbox table.
	result := s.doneWaitViaPoll(ctx, agentName, timeout, listenInbox)
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// doneWaitViaNATS subscribes to JetStream subjects and blocks until an event arrives.
func (s *Server) doneWaitViaNATS(ctx context.Context, js nats.JetStreamContext, agentName string, timeout time.Duration, listenInbox, listenDecision bool) (*DoneWaitResult, error) {
	type subResult struct {
		eventType string
		content   string
		source    string
	}

	resultCh := make(chan subResult, 1)
	var subs []*nats.Subscription

	// Subscribe to inbox events for this agent.
	if listenInbox {
		// Agent-specific inbox.
		subject := "inbox.agent." + agentName
		sub, err := js.SubscribeSync(subject, nats.DeliverNew())
		if err != nil {
			return nil, fmt.Errorf("subscribe %s: %w", subject, err)
		}
		subs = append(subs, sub)

		// Also listen for broadcasts.
		broadcastSub, err := js.SubscribeSync("inbox.all", nats.DeliverNew())
		if err == nil {
			subs = append(subs, broadcastSub)
		}
	}

	// Subscribe to decision events for this agent.
	if listenDecision {
		subject := eventbus.SubjectDecisionPrefix + agentName + ".>"
		sub, err := js.SubscribeSync(subject, nats.DeliverNew())
		if err == nil {
			subs = append(subs, sub)
		}
		// Also catch global decisions.
		globalSub, err := js.SubscribeSync(eventbus.SubjectDecisionPrefix+"_global.>", nats.DeliverNew())
		if err == nil {
			subs = append(subs, globalSub)
		}
	}

	if len(subs) == 0 {
		return nil, fmt.Errorf("no subscriptions created")
	}

	defer func() {
		for _, sub := range subs {
			sub.Unsubscribe()
		}
	}()

	fmt.Fprintf(os.Stderr, "done_wait: listening on %d NATS subscriptions for %s\n", len(subs), agentName)

	// Check for pre-existing undelivered inbox items (race: item arrived before subscription).
	if listenInbox {
		s.mu.RLock()
		store := s.storage
		s.mu.RUnlock()
		if store != nil {
			items, err := store.InboxDrain(ctx, agentName)
			if err == nil && len(items) > 0 {
				// Mark as delivered so they don't re-appear on the next bd done call.
				ids := make([]string, len(items))
				for i, item := range items {
					ids[i] = item.ID
				}
				_ = store.InboxMarkDelivered(ctx, agentName, ids)
				return &DoneWaitResult{
					EventType: "inbox",
					Content:   formatInboxItems(items),
					Source:    items[0].Source,
				}, nil
			}
		}
	}

	// Poll all subscriptions in a loop.
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return &DoneWaitResult{EventType: "timeout", TimedOut: true}, nil
		}

		select {
		case <-ctx.Done():
			return &DoneWaitResult{EventType: "timeout", TimedOut: true}, nil
		case <-resultCh:
			// Won't reach here — we use sync subscriptions below.
		default:
		}

		// Try each subscription with a short timeout.
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

			// Parse the message to determine type.
			subject := msg.Subject
			stream := eventbus.StreamForSubject(subject)

			switch stream {
			case "inbox":
				var item types.InboxItem
				if err := json.Unmarshal(msg.Data, &item); err == nil {
					// Mark as delivered so it doesn't re-appear on next bd done call.
					if item.ID != "" {
						s.mu.RLock()
						st := s.storage
						s.mu.RUnlock()
						if st != nil {
							_ = st.InboxMarkDelivered(ctx, agentName, []string{item.ID})
						}
					}
					return &DoneWaitResult{
						EventType: "inbox",
						Content:   item.Content,
						Source:    item.Source,
					}, nil
				}
				return &DoneWaitResult{
					EventType: "inbox",
					Content:   string(msg.Data),
				}, nil

			case "decisions":
				var payload eventbus.DecisionEventPayload
				if err := json.Unmarshal(msg.Data, &payload); err == nil {
					content := fmt.Sprintf("Decision %s resolved: %s", payload.DecisionID, payload.ChosenLabel)
					if payload.Rationale != "" {
						content += "\n" + payload.Rationale
					}
					return &DoneWaitResult{
						EventType: "decision",
						Content:   content,
						Source:    payload.ResolvedBy,
					}, nil
				}
				return &DoneWaitResult{
					EventType: "decision",
					Content:   string(msg.Data),
				}, nil
			}
		}
	}
}

// doneWaitViaPoll is the fallback when NATS is unavailable — polls inbox table.
func (s *Server) doneWaitViaPoll(ctx context.Context, agentName string, timeout time.Duration, listenInbox bool) *DoneWaitResult {
	if !listenInbox {
		// Can't poll decisions without NATS — just wait and timeout.
		select {
		case <-time.After(timeout):
		case <-ctx.Done():
		}
		return &DoneWaitResult{EventType: "timeout", TimedOut: true}
	}

	s.mu.RLock()
	store := s.storage
	s.mu.RUnlock()

	if store == nil {
		return &DoneWaitResult{EventType: "timeout", TimedOut: true, Content: "storage not available"}
	}

	deadline := time.Now().Add(timeout)

	fmt.Fprintf(os.Stderr, "done_wait: polling inbox for %s (timeout=%s)\n", agentName, timeout)

	// Check immediately for pre-existing items before entering the poll loop.
	items, err := store.InboxDrain(ctx, agentName)
	if err == nil && len(items) > 0 {
		ids := make([]string, len(items))
		for i, item := range items {
			ids[i] = item.ID
		}
		_ = store.InboxMarkDelivered(ctx, agentName, ids)
		return &DoneWaitResult{
			EventType: "inbox",
			Content:   formatInboxItems(items),
			Source:    items[0].Source,
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return &DoneWaitResult{EventType: "timeout", TimedOut: true}
		case <-ticker.C:
			if time.Now().After(deadline) {
				return &DoneWaitResult{EventType: "timeout", TimedOut: true}
			}

			items, err := store.InboxDrain(ctx, agentName)
			if err != nil {
				continue
			}
			if len(items) > 0 {
				ids := make([]string, len(items))
				for i, item := range items {
					ids[i] = item.ID
				}
				_ = store.InboxMarkDelivered(ctx, agentName, ids)
				return &DoneWaitResult{
					EventType: "inbox",
					Content:   formatInboxItems(items),
					Source:    items[0].Source,
				}
			}
		}
	}
}

// formatInboxItems formats inbox items for display.
func formatInboxItems(items []*types.InboxItem) string {
	if len(items) == 1 {
		return items[0].Content
	}
	var parts []string
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("[%s] %s", item.Type, item.Content))
	}
	return strings.Join(parts, "\n")
}
