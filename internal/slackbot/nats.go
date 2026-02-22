package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/eventbus"
)

// NATSWatcher watches for decision and agent lifecycle events via NATS JetStream.
// It subscribes to the DECISION_EVENTS and AGENT_EVENTS streams and calls back
// to the Bot when decisions are created/resolved or agents crash.
type NATSWatcher struct {
	natsURL      string
	natsToken    string
	bot          *Bot
	decisions    *DecisionClient
	conn         *nats.Conn
	sub          *nats.Subscription
	agentSub     *nats.Subscription
	dashboardSub *nats.Subscription // Separate consumer for dashboard refresh (bd-ije8h)
	seen         map[string]bool
	seenMu       sync.Mutex
}

// NewNATSWatcher creates a watcher that subscribes to decision events on the
// given NATS server and forwards them to the Bot. The token is used for NATS
// auth when the embedded server requires it (BD_DAEMON_TOKEN).
func NewNATSWatcher(natsURL, natsToken string, bot *Bot, decisions *DecisionClient) *NATSWatcher {
	return &NATSWatcher{
		natsURL:   natsURL,
		natsToken: natsToken,
		bot:       bot,
		decisions: decisions,
		seen:      make(map[string]bool),
	}
}

// Run connects to NATS and subscribes to decision events on the
// DECISION_EVENTS stream using a durable "slack-bot" consumer. It
// reconnects with exponential backoff on disconnect and catches up on
// missed decisions after each reconnect. Run blocks until ctx is canceled.
func (w *NATSWatcher) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := w.connect(ctx)
		if err != nil {
			log.Printf("slackbot/nats: connect error: %v (retry in %v)", err, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff = backoff * 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Reset backoff on successful connect.
		backoff = time.Second

		w.catchUpMissedDecisions(ctx)

		// Wait for context cancellation or disconnect.
		select {
		case <-ctx.Done():
			w.Close()
			return ctx.Err()
		case <-w.waitDisconnect():
			log.Printf("slackbot/nats: disconnected, will reconnect")
			w.Close()
		}
	}
}

// connect establishes the NATS connection and JetStream subscription.
// It retries the JetStream subscribe if the durable consumer is still
// bound from a previous pod (common during rolling updates).
func (w *NATSWatcher) connect(ctx context.Context) error {
	connectOpts := []nats.Option{
		nats.Name("beads-slack-bot"),
		nats.RetryOnFailedConnect(false),
	}
	if w.natsToken != "" {
		connectOpts = append(connectOpts, nats.Token(w.natsToken))
	}

	nc, err := nats.Connect(w.natsURL, connectOpts...)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("jetstream context: %w", err)
	}

	// Retry subscribe to handle "consumer already bound" during rolling updates.
	// The old pod's durable consumer binding may linger briefly after termination.
	var sub *nats.Subscription
	for attempt := 1; attempt <= 10; attempt++ {
		sub, err = js.Subscribe(
			eventbus.SubjectDecisionPrefix+">",
			w.handleMessage,
			nats.Durable("slack-bot"),
			nats.DeliverNew(),
			nats.AckExplicit(),
			nats.ManualAck(),
		)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "already bound") {
			nc.Close()
			return fmt.Errorf("jetstream subscribe: %w", err)
		}
		if attempt == 10 {
			nc.Close()
			return fmt.Errorf("jetstream subscribe: consumer still bound after %d attempts: %w", attempt, err)
		}
		log.Printf("slackbot/nats: consumer still bound (attempt %d/10), retrying in 3s", attempt)
		select {
		case <-ctx.Done():
			nc.Close()
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}

	// Subscribe to agent lifecycle events (AgentCrashed, etc.) for Slack alerts.
	var agentSub *nats.Subscription
	for attempt := 1; attempt <= 10; attempt++ {
		agentSub, err = js.Subscribe(
			eventbus.SubjectAgentPrefix+">",
			w.handleAgentMessage,
			nats.Durable("slack-bot-agents"),
			nats.DeliverNew(),
			nats.AckExplicit(),
			nats.ManualAck(),
		)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "already bound") {
			// Non-fatal: agent events are supplementary. Log and continue
			// with decision-only mode.
			log.Printf("slackbot/nats: agent events subscribe failed: %v (continuing without)", err)
			agentSub = nil
			err = nil
			break
		}
		if attempt == 10 {
			log.Printf("slackbot/nats: agent events consumer still bound after %d attempts (continuing without)", attempt)
			agentSub = nil
			err = nil
			break
		}
		log.Printf("slackbot/nats: agent events consumer still bound (attempt %d/10), retrying in 3s", attempt)
		select {
		case <-ctx.Done():
			nc.Close()
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}

	// Subscribe dashboard consumer for agent events (separate durable from crash alerts).
	var dashboardSub *nats.Subscription
	if dashboard := w.bot.GetDashboard(); dashboard != nil {
		for attempt := 1; attempt <= 10; attempt++ {
			dashboardSub, err = js.Subscribe(
				eventbus.SubjectAgentPrefix+">",
				dashboard.HandleAgentEvent,
				nats.Durable("slack-bot-dashboard"),
				nats.DeliverNew(),
				nats.AckExplicit(),
				nats.ManualAck(),
			)
			if err == nil {
				break
			}
			if !strings.Contains(err.Error(), "already bound") {
				log.Printf("slackbot/nats: dashboard events subscribe failed: %v (continuing without)", err)
				dashboardSub = nil
				err = nil
				break
			}
			if attempt == 10 {
				log.Printf("slackbot/nats: dashboard events consumer still bound after %d attempts (continuing without)", attempt)
				dashboardSub = nil
				err = nil
				break
			}
			log.Printf("slackbot/nats: dashboard events consumer still bound (attempt %d/10), retrying in 3s", attempt)
			select {
			case <-ctx.Done():
				nc.Close()
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
	}

	w.conn = nc
	w.sub = sub
	w.agentSub = agentSub
	w.dashboardSub = dashboardSub

	subs := []string{eventbus.StreamDecisionEvents}
	if agentSub != nil {
		subs = append(subs, eventbus.StreamAgentEvents)
	}
	if dashboardSub != nil {
		subs = append(subs, "dashboard")
	}
	log.Printf("slackbot/nats: connected to %s, subscribed to %s", w.natsURL, strings.Join(subs, " + "))
	return nil
}

// waitDisconnect returns a channel that closes when the NATS connection is
// no longer connected (closed or disconnected without reconnect).
func (w *NATSWatcher) waitDisconnect() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		if w.conn == nil {
			close(ch)
			return
		}
		for w.conn.IsConnected() {
			time.Sleep(500 * time.Millisecond)
		}
		close(ch)
	}()
	return ch
}

// handleMessage parses a NATS message from the DECISION_EVENTS stream and
// dispatches it to the appropriate Bot notification method.
func (w *NATSWatcher) handleMessage(msg *nats.Msg) {
	log.Printf("slackbot/nats: received message on %s (%d bytes)", msg.Subject, len(msg.Data))

	var payload eventbus.DecisionEventPayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		log.Printf("slackbot/nats: unmarshal error: %v (data: %s)", err, string(msg.Data))
		_ = msg.Ack()
		return
	}

	log.Printf("slackbot/nats: parsed event decision_id=%s question=%q", payload.DecisionID, payload.Question)

	// Determine event type from the NATS subject suffix.
	// Subjects are formatted as "decisions.<scope>.<EventType>" where scope
	// is an agent ID or "_global". Extract the last segment as the event type.
	subject := msg.Subject
	lastDot := strings.LastIndex(subject, ".")
	eventType := eventbus.EventType(subject[lastDot+1:])

	switch eventType {
	case eventbus.EventDecisionCreated:
		w.notifyNewDecision(payload)
	case eventbus.EventDecisionResponded:
		w.notifyResolvedDecision(payload)
	case eventbus.EventDecisionEscalated:
		w.notifyEscalatedDecision(payload)
	case eventbus.EventDecisionExpired:
		w.handleExpiredDecision(payload)
	default:
		log.Printf("slackbot/nats: ignoring event type %q for decision %s",
			eventType, payload.DecisionID)
	}

	_ = msg.Ack()
}

// notifyNewDecision fetches the full decision and notifies the Bot.
// It deduplicates by decision ID so repeated deliveries are harmless.
func (w *NATSWatcher) notifyNewDecision(payload eventbus.DecisionEventPayload) {
	w.seenMu.Lock()
	if w.seen[payload.DecisionID] {
		w.seenMu.Unlock()
		return
	}
	w.seen[payload.DecisionID] = true
	w.seenMu.Unlock()

	decision, err := w.fetchWithRetry(payload.DecisionID, 3, 500*time.Millisecond)
	if err != nil {
		log.Printf("slackbot/nats: fetch decision %s failed: %v", payload.DecisionID, err)
		return
	}

	// Skip if already resolved by the time we fetched it.
	if decision.Resolved {
		log.Printf("slackbot/nats: decision %s already resolved, skipping", payload.DecisionID)
		return
	}

	log.Printf("slackbot/nats: notifying new decision %s question=%q options=%d",
		decision.ID, decision.Question, len(decision.Options))
	if err := w.bot.NotifyNewDecision(decision); err != nil {
		log.Printf("slackbot/nats: notify decision %s failed: %v", payload.DecisionID, err)
	} else {
		log.Printf("slackbot/nats: notify decision %s posted to Slack", payload.DecisionID)
	}
}

// notifyResolvedDecision fetches the full decision and notifies the Bot
// of resolution. It deduplicates with a "resolved:" prefix key.
func (w *NATSWatcher) notifyResolvedDecision(payload eventbus.DecisionEventPayload) {
	key := "resolved:" + payload.DecisionID
	w.seenMu.Lock()
	if w.seen[key] {
		w.seenMu.Unlock()
		return
	}
	w.seen[key] = true
	w.seenMu.Unlock()

	decision, err := w.fetchWithRetry(payload.DecisionID, 3, 500*time.Millisecond)
	if err != nil {
		log.Printf("slackbot/nats: fetch resolved decision %s failed: %v",
			payload.DecisionID, err)
		return
	}

	// Skip if it was resolved via Slack to avoid double-updating.
	// ResolvedBy format is "slack:U07K1RLQAKF" (with user ID suffix).
	if strings.HasPrefix(decision.ResolvedBy, "slack:") {
		log.Printf("slackbot/nats: decision %s resolved via Slack (%s), skipping update",
			payload.DecisionID, decision.ResolvedBy)
		return
	}

	log.Printf("slackbot/nats: notifying resolution for decision %s (resolvedBy=%s, chosenIndex=%d)",
		decision.ID, decision.ResolvedBy, decision.ChosenIndex)
	if err := w.bot.NotifyResolution(decision); err != nil {
		log.Printf("slackbot/nats: notify resolution for decision %s failed: %v",
			payload.DecisionID, err)
	}
}

// notifyEscalatedDecision fetches the full decision and highlights it in Slack.
// It deduplicates with an "escalated:" prefix key.
func (w *NATSWatcher) notifyEscalatedDecision(payload eventbus.DecisionEventPayload) {
	key := "escalated:" + payload.DecisionID
	w.seenMu.Lock()
	if w.seen[key] {
		w.seenMu.Unlock()
		return
	}
	w.seen[key] = true
	w.seenMu.Unlock()

	decision, err := w.fetchWithRetry(payload.DecisionID, 3, 500*time.Millisecond)
	if err != nil {
		log.Printf("slackbot/nats: fetch escalated decision %s failed: %v",
			payload.DecisionID, err)
		return
	}

	if decision.Resolved {
		return
	}

	w.bot.NotifyEscalation(decision)
}

// handleExpiredDecision dismisses the decision message in Slack.
func (w *NATSWatcher) handleExpiredDecision(payload eventbus.DecisionEventPayload) {
	w.bot.DismissDecisionByID(payload.DecisionID)
}

// catchUpMissedDecisions fetches all pending decisions and notifies the Bot
// for any that haven't been seen yet. Called on initial connect and reconnect.
// Only catches up decisions created within the last hour to avoid flooding
// Slack with old decisions from cloned databases.
// Rate-limits notifications to ~1 per second to respect Slack API limits.
func (w *NATSWatcher) catchUpMissedDecisions(ctx context.Context) {
	pending, err := w.decisions.ListPending(ctx)
	if err != nil {
		log.Printf("slackbot/nats: catch-up list pending failed: %v", err)
		return
	}

	cutoff := time.Now().Add(-1 * time.Hour)
	notified, skippedOld := 0, 0

	for i := range pending {
		if ctx.Err() != nil {
			return
		}

		// Mark all pending as seen so NATS events for old decisions are ignored.
		w.seenMu.Lock()
		already := w.seen[pending[i].ID]
		w.seen[pending[i].ID] = true
		w.seenMu.Unlock()
		if already {
			continue
		}

		// Skip already-resolved decisions (shouldn't appear in ListPending,
		// but guards against race conditions).
		if pending[i].Resolved {
			continue
		}

		// Skip decisions older than 1 hour — they're from before this bot instance.
		// This prevents flooding Slack when the DB is cloned from prod with
		// hundreds of pending decisions from other agents.
		if !pending[i].RequestedAt.IsZero() && pending[i].RequestedAt.Before(cutoff) {
			skippedOld++
			continue
		}

		d := pending[i]
		w.bot.NotifyNewDecision(&d)
		notified++

		// Rate limit: ~1 notification per second to respect Slack API limits.
		time.Sleep(1100 * time.Millisecond)
	}

	if notified > 0 || skippedOld > 0 {
		log.Printf("slackbot/nats: catch-up complete: notified %d, skipped %d old (>1h) decisions",
			notified, skippedOld)
	}
}

// fetchWithRetry fetches a decision by ID, retrying on transient errors.
func (w *NATSWatcher) fetchWithRetry(decisionID string, attempts int, backoff time.Duration) (*Decision, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		d, err := w.decisions.GetDecision(context.Background(), decisionID)
		if err == nil {
			return d, nil
		}
		lastErr = err
		if i < attempts-1 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", attempts, lastErr)
}

// handleAgentMessage parses a NATS message from the AGENT_EVENTS stream and
// posts Slack alerts for agent crashes.
func (w *NATSWatcher) handleAgentMessage(msg *nats.Msg) {
	// Extract event type from subject: "agents.<EventType>"
	subject := msg.Subject
	lastDot := strings.LastIndex(subject, ".")
	if lastDot < 0 {
		_ = msg.Ack()
		return
	}
	eventType := eventbus.EventType(subject[lastDot+1:])

	if eventType != eventbus.EventAgentCrashed {
		_ = msg.Ack()
		return
	}

	var payload eventbus.AgentEventPayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		log.Printf("slackbot/nats: agent event unmarshal error: %v", err)
		_ = msg.Ack()
		return
	}

	// Deduplicate by agent+event.
	key := "agent-crashed:" + payload.AgentID
	w.seenMu.Lock()
	if w.seen[key] {
		w.seenMu.Unlock()
		_ = msg.Ack()
		return
	}
	w.seen[key] = true
	w.seenMu.Unlock()

	log.Printf("slackbot/nats: agent crashed: %s (reason: %s)", payload.AgentID, payload.Reason)

	if err := w.bot.NotifyAgentCrash(payload.AgentID, payload.AgentName, payload.Reason); err != nil {
		log.Printf("slackbot/nats: notify agent crash failed: %v", err)
	}

	_ = msg.Ack()
}

// Close drains the subscriptions and closes the NATS connection.
func (w *NATSWatcher) Close() {
	if w.dashboardSub != nil {
		_ = w.dashboardSub.Drain()
		w.dashboardSub = nil
	}
	if w.agentSub != nil {
		_ = w.agentSub.Drain()
		w.agentSub = nil
	}
	if w.sub != nil {
		_ = w.sub.Drain()
		w.sub = nil
	}
	if w.conn != nil {
		w.conn.Close()
		w.conn = nil
	}
}
