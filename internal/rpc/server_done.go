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
// for the specified agent, or until timeout. This is the core of "bd yield" —
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

	// Fallback: poll inbox and decision tables.
	result := s.doneWaitViaPoll(ctx, agentName, timeout, listenInbox, listenDecision)
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
		// Also subscribe to the base name variant if different from agentName.
		// Decision events are published on "decisions.<requestedBy>.<type>" where
		// requestedBy may be "sharp-seal-1" while agentName is "sharp-seal" (or
		// vice versa). Subscribing to both ensures we catch the event regardless
		// of which variant name was used. (bd-zr20k)
		baseName := eventbus.AgentBaseName(agentName)
		if baseName != agentName {
			baseSubject := eventbus.SubjectDecisionPrefix + baseName + ".>"
			baseSub, err := js.SubscribeSync(baseSubject, nats.DeliverNew())
			if err == nil {
				subs = append(subs, baseSub)
			}
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

	// Check for pre-existing events that arrived before NATS subscriptions were created.
	// Without these checks, events published in the gap between the RPC call and
	// subscription setup are lost, causing yield to hang forever. (bd-yvhxp)
	s.mu.RLock()
	store := s.storage
	s.mu.RUnlock()

	if store != nil {
		// Check inbox items first (pre-existing undelivered messages).
		if listenInbox {
			items, err := store.InboxDrain(ctx, agentName)
			if err == nil && len(items) > 0 {
				// Mark as delivered so they don't re-appear on the next bd yield call.
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

		// Check for decisions that were already responded to (race: response arrived
		// before we subscribed to NATS). Look back 5 minutes to catch responses that
		// arrived while the agent was between yield calls. (bd-yvhxp)
		// Also check the base name variant to catch decisions created by a
		// continuation session (e.g., "sharp-seal-1") when yield runs as "sharp-seal". (bd-zr20k)
		if listenDecision {
			since := time.Now().Add(-5 * time.Minute)
			dp := findRecentDecisionForAgent(ctx, store, since, agentName)
			if dp != nil {
				content := fmt.Sprintf("Decision %s resolved: %s", dp.IssueID, dp.SelectedOption)
				if dp.Rationale != "" {
					content += "\n" + dp.Rationale
				}
				return &DoneWaitResult{
					EventType: "decision",
					Content:   content,
					Source:    dp.RespondedBy,
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
					// Mark as delivered so it doesn't re-appear on next bd yield call.
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

// doneWaitViaPoll is the fallback when NATS is unavailable — polls inbox table
// and decision_points table. (bd-yvhxp: added decision polling)
func (s *Server) doneWaitViaPoll(ctx context.Context, agentName string, timeout time.Duration, listenInbox, listenDecision bool) *DoneWaitResult {
	if !listenInbox && !listenDecision {
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
	// Track when polling started so we only find decisions responded after this point.
	pollStart := time.Now()

	fmt.Fprintf(os.Stderr, "done_wait: polling inbox=%v decision=%v for %s (timeout=%s)\n",
		listenInbox, listenDecision, agentName, timeout)

	// Check immediately for pre-existing items before entering the poll loop.
	if listenInbox {
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
	}

	// Check for recently responded decisions (race: response arrived before poll started). (bd-yvhxp, bd-zr20k)
	if listenDecision {
		since := pollStart.Add(-5 * time.Minute)
		dp := findRecentDecisionForAgent(ctx, store, since, agentName)
		if dp != nil {
			content := fmt.Sprintf("Decision %s resolved: %s", dp.IssueID, dp.SelectedOption)
			if dp.Rationale != "" {
				content += "\n" + dp.Rationale
			}
			return &DoneWaitResult{
				EventType: "decision",
				Content:   content,
				Source:    dp.RespondedBy,
			}
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

			// Poll inbox.
			if listenInbox {
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
			}

			// Poll decisions. (bd-yvhxp, bd-zr20k)
			if listenDecision {
				dp := findRecentDecisionForAgent(ctx, store, pollStart, agentName)
				if dp != nil {
					content := fmt.Sprintf("Decision %s resolved: %s", dp.IssueID, dp.SelectedOption)
					if dp.Rationale != "" {
						content += "\n" + dp.Rationale
					}
					return &DoneWaitResult{
						EventType: "decision",
						Content:   content,
						Source:    dp.RespondedBy,
					}
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

// decisionLister is a narrow interface for querying recently-responded decisions.
// Avoids importing the storage package directly in this file.
type decisionLister interface {
	ListRecentlyRespondedDecisions(ctx context.Context, since time.Time, requestedBy string) ([]*types.DecisionPoint, error)
}

// findRecentDecisionForAgent checks for recently-responded decisions matching
// the agent name OR its base name variant. This handles the case where a decision
// was created by "sharp-seal-1" but yield is running as "sharp-seal" (or vice
// versa). Returns the most recent match, or nil. (bd-zr20k)
func findRecentDecisionForAgent(ctx context.Context, store decisionLister, since time.Time, agentName string) *types.DecisionPoint {
	// Try exact name first.
	responded, err := store.ListRecentlyRespondedDecisions(ctx, since, agentName)
	if err == nil && len(responded) > 0 {
		return responded[0]
	}

	// Try base name variant if different.
	baseName := eventbus.AgentBaseName(agentName)
	if baseName != agentName {
		responded, err = store.ListRecentlyRespondedDecisions(ctx, since, baseName)
		if err == nil && len(responded) > 0 {
			return responded[0]
		}
	}

	return nil
}
