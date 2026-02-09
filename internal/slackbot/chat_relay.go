package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/slack-go/slack"
	"github.com/steveyegge/beads/internal/chat"
	"github.com/steveyegge/beads/internal/eventbus"
)

// ChatRelay bridges Slack messages and agent chat sessions via NATS JetStream.
//
// Inbound (Slack → agent):
//   - handleThreadReply/handleAgentChannelMessage detect chat-eligible messages
//   - PublishInbound publishes them to chat.session.<tag>.in
//
// Outbound (agent → Slack):
//   - Subscribes to chat.session.*.out on NATS
//   - Posts received messages as Slack thread replies
type ChatRelay struct {
	natsURL   string
	natsToken string
	bot       *Bot
	registry  *chat.Registry
	conn      *nats.Conn
	js        nats.JetStreamContext
	sub       *nats.Subscription
	mu        sync.Mutex
}

// NewChatRelay creates a relay that bridges NATS chat messages to/from Slack.
func NewChatRelay(natsURL, natsToken string, bot *Bot) *ChatRelay {
	return &ChatRelay{
		natsURL:   natsURL,
		natsToken: natsToken,
		bot:       bot,
		registry:  chat.NewRegistry(),
	}
}

// Run connects to NATS, subscribes to outbound chat messages, and blocks
// until ctx is cancelled. Uses the same reconnect pattern as NATSWatcher.
func (r *ChatRelay) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := r.connect(ctx)
		if err != nil {
			log.Printf("slackbot/chat-relay: connect error: %v (retry in %v)", err, backoff)
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

		backoff = time.Second
		log.Printf("slackbot/chat-relay: connected, listening for outbound chat messages")

		// Wait for disconnect.
		select {
		case <-ctx.Done():
			r.close()
			return ctx.Err()
		case <-r.waitDisconnect():
			log.Printf("slackbot/chat-relay: disconnected, will reconnect")
			r.close()
		}
	}
}

func (r *ChatRelay) connect(ctx context.Context) error {
	connectOpts := []nats.Option{
		nats.Name("beads-slack-chat-relay"),
		nats.RetryOnFailedConnect(false),
	}
	if r.natsToken != "" {
		connectOpts = append(connectOpts, nats.Token(r.natsToken))
	}

	nc, err := nats.Connect(r.natsURL, connectOpts...)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("jetstream context: %w", err)
	}

	// Subscribe to all outbound chat messages: chat.session.*.out
	sub, err := js.Subscribe(
		eventbus.SubjectChatPrefix+"session.*.out",
		r.handleOutbound,
		nats.Durable("slack-chat-relay"),
		nats.DeliverNew(),
		nats.AckExplicit(),
		nats.ManualAck(),
	)
	if err != nil {
		nc.Close()
		return fmt.Errorf("jetstream subscribe: %w", err)
	}

	r.mu.Lock()
	r.conn = nc
	r.js = js
	r.sub = sub
	r.mu.Unlock()
	return nil
}

func (r *ChatRelay) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sub != nil {
		_ = r.sub.Unsubscribe()
		r.sub = nil
	}
	if r.conn != nil {
		r.conn.Drain()
		r.conn.Close()
		r.conn = nil
	}
	r.js = nil
}

func (r *ChatRelay) waitDisconnect() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		r.mu.Lock()
		conn := r.conn
		r.mu.Unlock()
		if conn == nil {
			close(ch)
			return
		}
		for conn.IsConnected() {
			time.Sleep(500 * time.Millisecond)
		}
		close(ch)
	}()
	return ch
}

// handleOutbound receives agent → Slack messages from NATS and posts to Slack.
func (r *ChatRelay) handleOutbound(msg *nats.Msg) {
	var payload eventbus.ChatMessagePayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		log.Printf("slackbot/chat-relay: unmarshal outbound: %v", err)
		_ = msg.Ack()
		return
	}

	log.Printf("slackbot/chat-relay: outbound from %s: %q (session=%s)",
		payload.Sender, truncate(payload.Content, 80), payload.SessionTag)

	// Look up the Slack thread for this session.
	session := r.registry.GetByTag(payload.SessionTag)
	if session == nil {
		log.Printf("slackbot/chat-relay: no session found for tag %s, using payload fields",
			payload.SessionTag)
		// Fall back to channel/thread from the payload itself.
		if payload.ChannelID == "" || payload.ThreadTS == "" {
			log.Printf("slackbot/chat-relay: no channel/thread for session %s, dropping message",
				payload.SessionTag)
			_ = msg.Ack()
			return
		}
		// Auto-register so future messages route correctly.
		r.registry.Register(&chat.Session{
			SessionTag: payload.SessionTag,
			ChannelID:  payload.ChannelID,
			ThreadTS:   payload.ThreadTS,
			AgentName:  payload.Sender,
			Status:     "open",
			CreatedAt:  time.Now(),
		})
		session = r.registry.GetByTag(payload.SessionTag)
	}

	// Post to Slack thread.
	_, _, err := r.bot.client.PostMessage(
		session.ChannelID,
		slack.MsgOptionText(payload.Content, false),
		slack.MsgOptionTS(session.ThreadTS),
	)
	if err != nil {
		log.Printf("slackbot/chat-relay: failed to post to Slack: %v", err)
	} else {
		log.Printf("slackbot/chat-relay: posted to channel=%s thread=%s",
			session.ChannelID, session.ThreadTS)
	}

	_ = msg.Ack()
}

// PublishInbound publishes a Slack user message to NATS for agent consumption.
// Called from handleThreadReply and handleAgentChannelMessage in bot.go.
func (r *ChatRelay) PublishInbound(ctx context.Context, sessionTag, sender, senderID, content, channelID, threadTS string) error {
	r.mu.Lock()
	js := r.js
	r.mu.Unlock()

	if js == nil {
		return fmt.Errorf("chat relay not connected to NATS")
	}

	payload := &eventbus.ChatMessagePayload{
		SessionTag: sessionTag,
		Sender:     sender,
		SenderID:   senderID,
		Content:    content,
		ChannelID:  channelID,
		ThreadTS:   threadTS,
		Timestamp:  time.Now().UTC(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal inbound: %w", err)
	}

	subject := eventbus.SubjectForChatSession(sessionTag, "in")
	_, err = js.Publish(subject, data)
	if err != nil {
		return fmt.Errorf("publish to %s: %w", subject, err)
	}

	log.Printf("slackbot/chat-relay: published inbound from %s to session %s", sender, sessionTag)
	return nil
}

// RegisterSession registers a Slack thread ↔ agent session mapping.
func (r *ChatRelay) RegisterSession(sessionTag, channelID, threadTS, agentName string) {
	r.registry.Register(&chat.Session{
		SessionTag: sessionTag,
		ChannelID:  channelID,
		ThreadTS:   threadTS,
		AgentName:  agentName,
		Status:     "open",
		CreatedAt:  time.Now(),
	})
	log.Printf("slackbot/chat-relay: registered session %s → %s:%s", sessionTag, channelID, threadTS)
}

// GetSessionByThread looks up a session by Slack thread.
func (r *ChatRelay) GetSessionByThread(channelID, threadTS string) *chat.Session {
	return r.registry.GetByThread(channelID, threadTS)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
