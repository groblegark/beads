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
	taskIDs    map[string]bool // in_progress bead IDs for this actor (bd-tlckc)
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

	// Subscribe to mutation status events (mutations.MutationStatus) for task tracking. (bd-tlckc)
	mutSub, err := js.Subscribe(SubjectMutationPrefix+string(EventMutationStatus), pt.handleMutationEvent,
		nats.DeliverNew(),
		nats.AckNone(),
	)
	if err != nil {
		// Non-fatal: presence works without task tracking, just log it.
		log.Printf("presence: mutation subscription failed (task tracking disabled): %v", err)
	}

	pt.subs = append(pt.subs, hookSub, agentSub)
	if mutSub != nil {
		pt.subs = append(pt.subs, mutSub)
	}
	log.Printf("presence: tracker started — subscribing to hooks.>, agents.>, mutations.MutationStatus")
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

// HasTask returns true if the given actor has at least one in_progress bead
// tracked via mutation events. This is an O(1) in-memory check. (bd-tlckc)
func (pt *PresenceTracker) HasTask(actor string) bool {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	state, ok := pt.actors[actor]
	if !ok {
		return false
	}
	return len(state.taskIDs) > 0
}

func (pt *PresenceTracker) handleMutationEvent(msg *nats.Msg) {
	var payload MutationEventPayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		return
	}

	// We only care about status changes.
	if payload.Type != "status" {
		return
	}

	actor := payload.Actor
	if actor == "" {
		actor = payload.Assignee
	}
	if actor == "" {
		return
	}

	pt.mu.Lock()
	defer pt.mu.Unlock()

	state, ok := pt.actors[actor]
	if !ok {
		state = &actorState{taskIDs: make(map[string]bool)}
		pt.actors[actor] = state
	}
	if state.taskIDs == nil {
		state.taskIDs = make(map[string]bool)
	}

	// Track transitions into and out of in_progress.
	if payload.NewStatus == "in_progress" {
		state.taskIDs[payload.IssueID] = true
	} else if payload.OldStatus == "in_progress" {
		delete(state.taskIDs, payload.IssueID)
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
