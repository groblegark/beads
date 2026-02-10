// Package chat provides bidirectional Slack↔Agent messaging over NATS JetStream (bd-viux).
//
// Architecture:
//   - Slack user sends message → published to chat.session.<tag>.in
//   - Agent listens via bd chat listen (blocking NATS subscription)
//   - Agent responds via bd chat reply → published to chat.session.<tag>.out
//   - Slack bot subscribes to chat.session.*.out → posts to Slack thread
//
// Session registry maps Slack threads (channel+thread_ts) to agent session tags.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/eventbus"
)

// Session tracks a bidirectional chat session between a Slack thread and an agent.
type Session struct {
	SessionTag string    `json:"session_tag"`
	ChannelID  string    `json:"channel_id"`
	ThreadTS   string    `json:"thread_ts"`
	AgentName  string    `json:"agent_name,omitempty"`
	Status     string    `json:"status"` // "open", "closed", "listening"
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Registry maps Slack threads to agent sessions. Thread-safe.
type Registry struct {
	mu       sync.RWMutex
	byTag    map[string]*Session // session_tag → Session
	byThread map[string]string   // "channelID:threadTS" → session_tag
}

// NewRegistry creates an empty session registry.
func NewRegistry() *Registry {
	return &Registry{
		byTag:    make(map[string]*Session),
		byThread: make(map[string]string),
	}
}

func threadKey(channelID, threadTS string) string {
	return channelID + ":" + threadTS
}

// Register creates or updates a chat session.
func (r *Registry) Register(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.UpdatedAt = time.Now()
	r.byTag[s.SessionTag] = s
	if s.ChannelID != "" && s.ThreadTS != "" {
		r.byThread[threadKey(s.ChannelID, s.ThreadTS)] = s.SessionTag
	}
}

// GetByTag returns a session by its agent session tag.
func (r *Registry) GetByTag(tag string) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := r.byTag[tag]
	if s == nil {
		return nil
	}
	copy := *s
	return &copy
}

// GetByThread returns a session for a Slack thread.
func (r *Registry) GetByThread(channelID, threadTS string) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tag, ok := r.byThread[threadKey(channelID, threadTS)]
	if !ok {
		return nil
	}
	s := r.byTag[tag]
	if s == nil {
		return nil
	}
	copy := *s
	return &copy
}

// Close marks a session as closed.
func (r *Registry) Close(tag string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.byTag[tag]
	if !ok {
		return false
	}
	s.Status = "closed"
	s.UpdatedAt = time.Now()
	return true
}

// All returns all sessions (snapshot).
func (r *Registry) All() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Session, 0, len(r.byTag))
	for _, s := range r.byTag {
		copy := *s
		out = append(out, &copy)
	}
	return out
}

// RPCAdapter wraps a Registry to satisfy the rpc.ChatRegistry interface.
// This avoids a circular dependency between the chat and rpc packages.
type RPCAdapter struct {
	reg *Registry
}

// NewRPCAdapter creates an adapter that bridges chat.Registry to rpc.ChatRegistry.
func NewRPCAdapter(reg *Registry) *RPCAdapter {
	return &RPCAdapter{reg: reg}
}

// RPCSessionSnapshot is the RPC-compatible session snapshot.
// Must match rpc.ChatSessionSnapshot field names exactly.
type RPCSessionSnapshot struct {
	SessionTag string
	ChannelID  string
	ThreadTS   string
	AgentName  string
	Status     string
	CreatedAt  string // RFC3339
	UpdatedAt  string // RFC3339
}

// Register creates or updates a session.
func (a *RPCAdapter) Register(tag, channelID, threadTS, agentName string) {
	a.reg.Register(&Session{
		SessionTag: tag,
		ChannelID:  channelID,
		ThreadTS:   threadTS,
		AgentName:  agentName,
		Status:     "open",
		CreatedAt:  time.Now(),
	})
}

// Close marks a session as closed.
func (a *RPCAdapter) Close(tag string) bool {
	return a.reg.Close(tag)
}

// GetByTag returns an RPC-compatible snapshot of a session.
func (a *RPCAdapter) GetByTag(tag string) *RPCSessionSnapshot {
	s := a.reg.GetByTag(tag)
	if s == nil {
		return nil
	}
	return sessionToSnapshot(s)
}

// All returns all sessions as RPC-compatible snapshots.
func (a *RPCAdapter) All() []*RPCSessionSnapshot {
	sessions := a.reg.All()
	out := make([]*RPCSessionSnapshot, len(sessions))
	for i, s := range sessions {
		out[i] = sessionToSnapshot(s)
	}
	return out
}

func sessionToSnapshot(s *Session) *RPCSessionSnapshot {
	return &RPCSessionSnapshot{
		SessionTag: s.SessionTag,
		ChannelID:  s.ChannelID,
		ThreadTS:   s.ThreadTS,
		AgentName:  s.AgentName,
		Status:     s.Status,
		CreatedAt:  s.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  s.UpdatedAt.Format(time.RFC3339),
	}
}

// Broker handles publishing and subscribing to chat messages over NATS JetStream.
type Broker struct {
	js       nats.JetStreamContext
	registry *Registry
}

// NewBroker creates a chat broker with the given JetStream context.
func NewBroker(js nats.JetStreamContext, registry *Registry) *Broker {
	return &Broker{js: js, registry: registry}
}

// Send publishes a chat message to NATS. Direction is "in" (Slack→agent) or "out" (agent→Slack).
func (b *Broker) Send(ctx context.Context, msg *eventbus.ChatMessagePayload, direction string) error {
	if msg.SessionTag == "" {
		return fmt.Errorf("chat: session_tag is required")
	}
	if msg.Content == "" {
		return fmt.Errorf("chat: content is required")
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("chat: marshal message: %w", err)
	}

	subject := eventbus.SubjectForChatSession(msg.SessionTag, direction)
	ack, err := b.js.Publish(subject, data)
	if err != nil {
		return fmt.Errorf("chat: publish to %s: %w", subject, err)
	}
	msg.Seq = ack.Sequence
	return nil
}

// Listen subscribes to inbound chat messages for a session and blocks until
// a message arrives or the context is cancelled. This is the core primitive
// for agent-side chat: an agent runs `bd chat listen` as a background task,
// and this function returns when a Slack user sends a message.
//
// If drain is true, any queued messages since the consumer was last active
// are delivered first (oldest first).
func (b *Broker) Listen(ctx context.Context, sessionTag string) (*eventbus.ChatMessagePayload, error) {
	subject := eventbus.SubjectForChatSession(sessionTag, "in")

	sub, err := b.js.SubscribeSync(subject,
		nats.DeliverNew(),
		nats.AckExplicit(),
	)
	if err != nil {
		return nil, fmt.Errorf("chat: subscribe to %s: %w", subject, err)
	}
	defer sub.Unsubscribe()

	for {
		// Check context before blocking on NextMsg.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Block with a short timeout so we can check context periodically.
		msg, err := sub.NextMsg(1 * time.Second)
		if err != nil {
			if err == nats.ErrTimeout {
				continue // Loop back to check context.
			}
			return nil, fmt.Errorf("chat: next message: %w", err)
		}

		var payload eventbus.ChatMessagePayload
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			msg.Nak()
			return nil, fmt.Errorf("chat: unmarshal message: %w", err)
		}

		meta, _ := msg.Metadata()
		if meta != nil {
			payload.Seq = meta.Sequence.Stream
		}

		msg.Ack()
		return &payload, nil
	}
}

// ListenAll drains all pending messages for a session, then listens for new ones.
// Returns all messages received (pending + first new one) or an error.
func (b *Broker) ListenAll(ctx context.Context, sessionTag string) ([]*eventbus.ChatMessagePayload, error) {
	subject := eventbus.SubjectForChatSession(sessionTag, "in")

	sub, err := b.js.SubscribeSync(subject,
		nats.DeliverAll(),
		nats.AckExplicit(),
	)
	if err != nil {
		return nil, fmt.Errorf("chat: subscribe to %s: %w", subject, err)
	}
	defer sub.Unsubscribe()

	var messages []*eventbus.ChatMessagePayload

	for {
		if err := ctx.Err(); err != nil {
			if len(messages) > 0 {
				return messages, nil
			}
			return nil, err
		}

		msg, err := sub.NextMsg(1 * time.Second)
		if err != nil {
			if err == nats.ErrTimeout {
				if len(messages) > 0 {
					return messages, nil // Drained all pending.
				}
				continue // Keep waiting for first message.
			}
			return nil, fmt.Errorf("chat: next message: %w", err)
		}

		var payload eventbus.ChatMessagePayload
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			msg.Nak()
			continue
		}

		meta, _ := msg.Metadata()
		if meta != nil {
			payload.Seq = meta.Sequence.Stream
		}

		msg.Ack()
		messages = append(messages, &payload)
	}
}
