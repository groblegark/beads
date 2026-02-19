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
	Actor               string    `json:"actor"`
	LastSeen            time.Time `json:"last_seen"`
	FirstSeen           time.Time `json:"first_seen"`              // when this actor first appeared (bd-4ul0v)
	LastEvent           string    `json:"last_event"`              // e.g., "PreToolUse", "PostToolUse"
	ToolName            string    `json:"tool_name,omitempty"`     // last tool used (if hook event)
	SessionID           string    `json:"session_id,omitempty"`    // Claude Code session
	CWD                 string    `json:"cwd,omitempty"`           // last known working directory (bd-z6958)
	IdleSecs            float64   `json:"idle_secs"`               // seconds since last event
	EventCount          int64     `json:"event_count"`             // total events seen
	SessionDurationSecs float64   `json:"session_duration_secs"`   // seconds since first event (bd-4ul0v)
	EventsPerMin        float64   `json:"events_per_min"`          // rolling event rate (bd-4ul0v)
	Reaped              bool      `json:"reaped,omitempty"`        // true if reaper marked this actor dead (bd-khlpu)
	ReapedAt            time.Time `json:"reaped_at,omitzero"`     // when the reaper marked this actor dead (bd-khlpu)
}

// PresenceTracker subscribes to NATS JetStream and maintains a live roster
// of active actors based on hook and agent lifecycle events. (bd-3d5m2)
type PresenceTracker struct {
	mu      sync.RWMutex
	actors  map[string]*actorState
	subs    []*nats.Subscription
	started time.Time

	// Reaper goroutine state (bd-khlpu).
	reaperStop chan struct{}
	reaperDone chan struct{}
}

type actorState struct {
	firstSeen  time.Time
	lastSeen   time.Time
	lastEvent  string
	toolName   string
	sessionID  string
	cwd        string          // last known working directory from hook events (bd-z6958)
	eventCount int64
	taskIDs    map[string]bool // in_progress bead IDs for this actor (bd-tlckc)
	reaped     bool            // true if reaper marked this actor dead (bd-khlpu)
	reapedAt   time.Time       // when reaped (bd-khlpu)
}

// ReaperConfig configures the dead-agent reaper goroutine. (bd-khlpu)
type ReaperConfig struct {
	// DeadThreshold is how long an actor must be idle before being marked dead.
	// Default: 15 minutes.
	DeadThreshold time.Duration

	// EvictAfter is how long after being reaped before an actor is permanently
	// removed from the in-memory map. This prevents unbounded growth from
	// ephemeral agents (e.g. furiosa-1 through furiosa-267). Default: 30 minutes.
	// (bd-vta0i)
	EvictAfter time.Duration

	// SweepInterval is how often the reaper scans for dead actors.
	// Default: 60 seconds.
	SweepInterval time.Duration

	// OnDead is called for each actor newly marked as dead. The callback
	// receives the actor name and session ID. Use this to emit NATS events,
	// update agent_state in storage, etc. Called under NO lock — safe to
	// make blocking calls. Errors from OnDead are logged but don't stop the reaper.
	OnDead func(actor, sessionID string)
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

// Stop unsubscribes from all streams and stops the reaper goroutine.
func (pt *PresenceTracker) Stop() {
	// Stop reaper first (if running). (bd-khlpu)
	if pt.reaperStop != nil {
		close(pt.reaperStop)
		<-pt.reaperDone
		pt.reaperStop = nil
		pt.reaperDone = nil
	}

	for _, sub := range pt.subs {
		_ = sub.Unsubscribe()
	}
	pt.subs = nil
	log.Printf("presence: tracker stopped")
}

// StartReaper launches a background goroutine that periodically scans for
// actors idle longer than DeadThreshold and marks them as reaped. (bd-khlpu)
//
// Call after Start(). Safe to call with nil config (uses defaults, no callback).
// Call Stop() to shut down the reaper.
func (pt *PresenceTracker) StartReaper(cfg *ReaperConfig) {
	if cfg == nil {
		cfg = &ReaperConfig{}
	}
	if cfg.DeadThreshold == 0 {
		cfg.DeadThreshold = 15 * time.Minute
	}
	if cfg.EvictAfter == 0 {
		cfg.EvictAfter = 30 * time.Minute // (bd-vta0i)
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = 60 * time.Second
	}

	pt.reaperStop = make(chan struct{})
	pt.reaperDone = make(chan struct{})

	go pt.reapLoop(cfg)
	log.Printf("presence: reaper started (dead threshold=%s, sweep interval=%s)",
		cfg.DeadThreshold, cfg.SweepInterval)
}

// reapLoop is the reaper goroutine. Runs until reaperStop is closed.
func (pt *PresenceTracker) reapLoop(cfg *ReaperConfig) {
	defer close(pt.reaperDone)

	ticker := time.NewTicker(cfg.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pt.reaperStop:
			return
		case <-ticker.C:
			pt.sweep(cfg)
		}
	}
}

// sweep scans all actors and marks those idle > DeadThreshold as reaped.
// Actors reaped for longer than EvictAfter are permanently removed from the map
// to prevent unbounded growth from ephemeral agents. (bd-vta0i)
// Newly reaped actors trigger the OnDead callback (outside the lock).
func (pt *PresenceTracker) sweep(cfg *ReaperConfig) {
	now := time.Now()

	// Collect newly dead actors under the lock.
	type deadActor struct {
		name      string
		sessionID string
	}
	var newlyDead []deadActor
	var evicted int

	pt.mu.Lock()
	for actor, state := range pt.actors {
		if state.reaped {
			// Evict actors that have been reaped for longer than EvictAfter. (bd-vta0i)
			if !state.reapedAt.IsZero() && now.Sub(state.reapedAt) > cfg.EvictAfter {
				delete(pt.actors, actor)
				evicted++
			}
			continue
		}
		idle := now.Sub(state.lastSeen)
		if idle > cfg.DeadThreshold {
			state.reaped = true
			state.reapedAt = now
			newlyDead = append(newlyDead, deadActor{name: actor, sessionID: state.sessionID})
		}
	}
	pt.mu.Unlock()

	if evicted > 0 {
		log.Printf("presence: reaper evicted %d stale actors", evicted)
	}

	// Fire callbacks outside the lock.
	for _, dead := range newlyDead {
		log.Printf("presence: reaper marked %q as dead (idle > %s)", dead.name, cfg.DeadThreshold)
		if cfg.OnDead != nil {
			cfg.OnDead(dead.name, dead.sessionID)
		}
	}
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
		// Use firstSeen if set, otherwise fall back to tracker start time (for pre-existing actors).
		firstSeen := state.firstSeen
		if firstSeen.IsZero() {
			firstSeen = pt.started
		}
		sessionDur := now.Sub(firstSeen).Seconds()
		var eventsPerMin float64
		if sessionDur > 0 {
			eventsPerMin = float64(state.eventCount) / (sessionDur / 60.0)
		}
		entries = append(entries, PresenceEntry{
			Actor:               actor,
			LastSeen:            state.lastSeen,
			FirstSeen:           firstSeen,
			LastEvent:           state.lastEvent,
			ToolName:            state.toolName,
			SessionID:           state.sessionID,
			CWD:                 state.cwd,       // (bd-z6958)
			IdleSecs:            idle.Seconds(),
			EventCount:          state.eventCount,
			SessionDurationSecs: sessionDur,
			EventsPerMin:        eventsPerMin,
			Reaped:              state.reaped,    // (bd-khlpu)
			ReapedAt:            state.reapedAt,  // (bd-khlpu)
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

// TaskSeed represents a pre-existing in_progress bead for cold-start backfill.
type TaskSeed struct {
	IssueID string // bead ID
	Actor   string // assignee or created_by
}

// BackfillTasks pre-populates the actor->taskIDs map from existing in_progress
// beads. Call this after Start() to handle the cold-start problem: on daemon
// restart, mutation events only track future changes, so agents with existing
// in_progress beads would be incorrectly nudged until their next status change.
// (bd-o4rvv)
func (pt *PresenceTracker) BackfillTasks(seeds []TaskSeed) int {
	if len(seeds) == 0 {
		return 0
	}

	pt.mu.Lock()
	defer pt.mu.Unlock()

	count := 0
	for _, seed := range seeds {
		if seed.Actor == "" || seed.IssueID == "" {
			continue
		}
		state, ok := pt.actors[seed.Actor]
		if !ok {
			state = &actorState{taskIDs: make(map[string]bool)}
			pt.actors[seed.Actor] = state
		}
		if state.taskIDs == nil {
			state.taskIDs = make(map[string]bool)
		}
		state.taskIDs[seed.IssueID] = true
		count++
	}

	if count > 0 {
		log.Printf("presence: backfilled %d in_progress tasks across %d actors", count, len(pt.actors))
	}
	return count
}

func (pt *PresenceTracker) handleHookEvent(msg *nats.Msg) {
	var event struct {
		Actor     string `json:"actor"`
		EventType string `json:"hook_event_name"`
		ToolName  string `json:"tool_name"`
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
	}
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		return
	}
	if event.Actor == "" {
		return
	}

	now := time.Now()
	pt.mu.Lock()
	defer pt.mu.Unlock()

	state, ok := pt.actors[event.Actor]
	if !ok {
		state = &actorState{firstSeen: now}
		pt.actors[event.Actor] = state
	}
	// Resurrect reaped actors that come back to life. (bd-khlpu)
	if state.reaped {
		log.Printf("presence: actor %q resurrected (was reaped at %s)", event.Actor, state.reapedAt.Format(time.RFC3339))
		state.reaped = false
		state.reapedAt = time.Time{}
	}
	state.lastSeen = now
	state.lastEvent = event.EventType
	state.eventCount++
	if event.ToolName != "" {
		state.toolName = event.ToolName
	}
	if event.SessionID != "" {
		state.sessionID = event.SessionID
	}
	if event.CWD != "" {
		state.cwd = event.CWD // (bd-z6958)
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
		// Also track for the assignee if different from the mutation actor.
		// When a captain assigns work (captain is actor, agent is assignee),
		// both should have the task in their taskIDs. (bd-nqnyn)
		if payload.Assignee != "" && payload.Assignee != actor {
			assigneeState, ok := pt.actors[payload.Assignee]
			if !ok {
				assigneeState = &actorState{taskIDs: make(map[string]bool)}
				pt.actors[payload.Assignee] = assigneeState
			}
			if assigneeState.taskIDs == nil {
				assigneeState.taskIDs = make(map[string]bool)
			}
			assigneeState.taskIDs[payload.IssueID] = true
		}
	} else if payload.OldStatus == "in_progress" {
		delete(state.taskIDs, payload.IssueID)
		// Also remove from assignee's tracking. (bd-nqnyn)
		if payload.Assignee != "" && payload.Assignee != actor {
			if assigneeState, ok := pt.actors[payload.Assignee]; ok {
				delete(assigneeState.taskIDs, payload.IssueID)
			}
		}
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

	now := time.Now()
	pt.mu.Lock()
	defer pt.mu.Unlock()

	state, ok := pt.actors[actor]
	if !ok {
		state = &actorState{firstSeen: now}
		pt.actors[actor] = state
	}
	// Resurrect reaped actors that come back to life. (bd-khlpu)
	if state.reaped {
		log.Printf("presence: actor %q resurrected via agent event (was reaped at %s)", actor, state.reapedAt.Format(time.RFC3339))
		state.reaped = false
		state.reapedAt = time.Time{}
	}
	state.lastSeen = now
	state.lastEvent = eventType
	state.eventCount++
	if payload.SessionID != "" {
		state.sessionID = payload.SessionID
	}
}
