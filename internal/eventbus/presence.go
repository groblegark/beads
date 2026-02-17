package eventbus

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// PresenceEntry tracks a single actor's live presence derived from NATS events.
type PresenceEntry struct {
	Actor       string    `json:"actor"`
	LastSeen    time.Time `json:"last_seen"`
	LastEvent   string    `json:"last_event"`             // e.g., "PreToolUse", "PostToolUse"
	ToolName    string    `json:"tool_name,omitempty"`     // last tool used (if hook event)
	SessionID   string    `json:"session_id,omitempty"`    // Claude Code session
	IdleSecs    float64   `json:"idle_secs"`               // seconds since last event
	EventCount  int64     `json:"event_count"`             // total events seen
}

// PresenceTracker subscribes to NATS JetStream and maintains a live roster
// of active actors based on hook and agent lifecycle events. (bd-3d5m2)
type PresenceTracker struct {
	mu      sync.RWMutex
	actors  map[string]*actorState
	subs    []*nats.Subscription
	started time.Time
}

type actorState struct {
	lastSeen   time.Time
	lastEvent  string
	toolName   string
	sessionID  string
	eventCount int64
}

// NewPresenceTracker creates a new tracker. Call Start() to begin subscribing.
func NewPresenceTracker() *PresenceTracker {
	return &PresenceTracker{
		actors: make(map[string]*actorState),
	}
}

// Start subscribes to hook and agent event streams on JetStream.
// Returns an error if subscription fails.
func (pt *PresenceTracker) Start(js nats.JetStreamContext) error {
	pt.started = time.Now()

	// Subscribe to all hook events (hooks.>) — these carry actor in the JSON.
	hookSub, err := js.Subscribe(SubjectHookPrefix+">", pt.handleHookEvent,
		nats.DeliverNew(),
		nats.AckNone(),
	)
	if err != nil {
		return err
	}

	// Subscribe to agent lifecycle events (agents.>).
	agentSub, err := js.Subscribe(SubjectAgentPrefix+">", pt.handleAgentEvent,
		nats.DeliverNew(),
		nats.AckNone(),
	)
	if err != nil {
		hookSub.Unsubscribe()
		return err
	}

	pt.subs = append(pt.subs, hookSub, agentSub)
	log.Printf("presence: tracker started — subscribing to hooks.> and agents.>")
	return nil
}

// Stop unsubscribes from all streams.
func (pt *PresenceTracker) Stop() {
	for _, sub := range pt.subs {
		_ = sub.Unsubscribe()
	}
	pt.subs = nil
	log.Printf("presence: tracker stopped")
}

// Roster returns a snapshot of all tracked actors, sorted by most recently active.
// staleThreshold controls how long since last event before an actor is excluded.
// Pass 0 to include all actors ever seen.
func (pt *PresenceTracker) Roster(staleThreshold time.Duration) []PresenceEntry {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	now := time.Now()
	entries := make([]PresenceEntry, 0, len(pt.actors))

	for actor, state := range pt.actors {
		idle := now.Sub(state.lastSeen)
		if staleThreshold > 0 && idle > staleThreshold {
			continue
		}
		entries = append(entries, PresenceEntry{
			Actor:      actor,
			LastSeen:   state.lastSeen,
			LastEvent:  state.lastEvent,
			ToolName:   state.toolName,
			SessionID:  state.sessionID,
			IdleSecs:   idle.Seconds(),
			EventCount: state.eventCount,
		})
	}

	// Sort by last seen (most recent first).
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].LastSeen.After(entries[i].LastSeen) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	return entries
}

// Uptime returns how long the tracker has been running.
func (pt *PresenceTracker) Uptime() time.Duration {
	return time.Since(pt.started)
}

func (pt *PresenceTracker) handleHookEvent(msg *nats.Msg) {
	var event struct {
		Actor     string `json:"actor"`
		EventType string `json:"hook_event_name"`
		ToolName  string `json:"tool_name"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		return
	}
	if event.Actor == "" {
		return
	}

	pt.mu.Lock()
	defer pt.mu.Unlock()

	state, ok := pt.actors[event.Actor]
	if !ok {
		state = &actorState{}
		pt.actors[event.Actor] = state
	}
	state.lastSeen = time.Now()
	state.lastEvent = event.EventType
	state.eventCount++
	if event.ToolName != "" {
		state.toolName = event.ToolName
	}
	if event.SessionID != "" {
		state.sessionID = event.SessionID
	}
}

func (pt *PresenceTracker) handleAgentEvent(msg *nats.Msg) {
	var payload AgentEventPayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		return
	}

	actor := payload.AgentName
	if actor == "" {
		actor = payload.AgentID
	}
	if actor == "" {
		return
	}

	eventType := EventTypeFromSubject(msg.Subject)

	pt.mu.Lock()
	defer pt.mu.Unlock()

	state, ok := pt.actors[actor]
	if !ok {
		state = &actorState{}
		pt.actors[actor] = state
	}
	state.lastSeen = time.Now()
	state.lastEvent = eventType
	state.eventCount++
	if payload.SessionID != "" {
		state.sessionID = payload.SessionID
	}
}
