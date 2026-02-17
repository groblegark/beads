package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// FallbackDecisionCreator is the interface that the RPC server implements
// to allow the StopFallbackHandler to create decisions without subprocess.
// This is wired via SetFallbackCreator during daemon startup, following the
// same pattern as InboxStore and NudgeStore. (bd-csxrl)
type FallbackDecisionCreator interface {
	// CreateFallbackStopDecision creates a daemon-generated decision point
	// when the agent fails to create one within the timeout. The decision
	// is published to Slack via the normal NATS pipeline. Returns the
	// decision's issue ID.
	CreateFallbackStopDecision(ctx context.Context, actor string, sessionContext string) (string, error)
}

// StopFallbackHandler creates a daemon-generated fallback decision when the
// agent fails to create one after being blocked by StopDecisionHandler.
//
// Flow:
//  1. StopDecisionHandler (priority 15) blocks, returning "create a decision" to agent
//  2. This handler (priority 16) sees the block and starts a background timer
//  3. After FallbackTimeout, checks if agent created a decision (via subprocess)
//  4. If no decision found, creates a fallback via FallbackDecisionCreator
//  5. The fallback decision flows through normal NATS → Slack pipeline
//
// This ensures every stop attempt eventually produces a human decision point,
// even when the agent ignores the prompt, uses wrong format, or has actor
// tag mismatches. (bd-csxrl)
type StopFallbackHandler struct {
	mu      sync.Mutex
	creator FallbackDecisionCreator

	// Configurable; defaults applied in Handle.
	FallbackTimeout time.Duration // How long to wait before creating fallback (default: 45s)

	// Track active fallback timers per session to avoid duplicates.
	active map[string]*fallbackTimer
}

type fallbackTimer struct {
	cancel context.CancelFunc
	actor  string
}

func (h *StopFallbackHandler) ID() string          { return "stop-fallback" }
func (h *StopFallbackHandler) Handles() []EventType { return []EventType{EventStop, EventDecisionCreated} }
func (h *StopFallbackHandler) Priority() int        { return 16 }

// SetFallbackCreator injects the decision creator (RPC server) at startup.
func (h *StopFallbackHandler) SetFallbackCreator(c FallbackDecisionCreator) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.creator = c
}

func (h *StopFallbackHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	// On DecisionCreated events, cancel any active fallback timer for the actor.
	// This means the agent successfully created a decision — no fallback needed.
	if event.Type == EventDecisionCreated {
		if len(event.Raw) > 0 {
			var payload DecisionEventPayload
			if err := json.Unmarshal(event.Raw, &payload); err == nil && payload.RequestedBy != "" {
				h.cancelFallback(payload.RequestedBy)
			}
		}
		return nil
	}

	// Only act on Stop events where StopDecisionHandler set block=true.
	if !result.Block {
		return nil
	}

	// Skip if loop break is active (StopLoopDetector already handled this).
	if stopLoopBreakSet(event) {
		return nil
	}

	h.mu.Lock()
	creator := h.creator
	h.mu.Unlock()

	if creator == nil {
		// Not wired — skip silently (backward compat with old daemon startup).
		return nil
	}

	actor := event.Actor
	if actor == "" {
		// No actor — can't scope the decision, skip.
		return nil
	}

	// Check if we already have an active timer for this actor.
	h.mu.Lock()
	if h.active == nil {
		h.active = make(map[string]*fallbackTimer)
	}
	if _, exists := h.active[actor]; exists {
		h.mu.Unlock()
		log.Printf("stop-fallback: timer already active for %s, skipping", actor)
		return nil
	}

	timeout := h.FallbackTimeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}

	// Start a background goroutine to create the fallback after timeout.
	timerCtx, cancel := context.WithCancel(context.Background())
	h.active[actor] = &fallbackTimer{cancel: cancel, actor: actor}
	h.mu.Unlock()

	go h.runFallback(timerCtx, actor, event.SessionID, event.CWD, timeout)

	return nil
}

// runFallback waits for the timeout, then checks if the agent created a decision.
// If not, creates a daemon-generated fallback decision.
func (h *StopFallbackHandler) runFallback(ctx context.Context, actor, sessionID, cwd string, timeout time.Duration) {
	defer h.cleanupFallback(actor)

	log.Printf("stop-fallback: started %v timer for actor %s (session %s)", timeout, actor, sessionID)

	select {
	case <-time.After(timeout):
		// Timeout expired — check if agent created a decision.
	case <-ctx.Done():
		// Canceled (agent created a decision, or daemon shutting down).
		log.Printf("stop-fallback: canceled for actor %s", actor)
		return
	}

	// Check if agent created a decision by running bd decision stop-check.
	// If it returns exit 0 (allow), the agent already created one.
	agentCreated := h.checkAgentDecision(cwd, actor)
	if agentCreated {
		log.Printf("stop-fallback: agent %s created decision before timeout, skipping fallback", actor)
		return
	}

	// Agent didn't create a decision — create the fallback.
	log.Printf("stop-fallback: agent %s did not create decision within %v, creating fallback", actor, timeout)

	h.mu.Lock()
	creator := h.creator
	h.mu.Unlock()

	if creator == nil {
		log.Printf("stop-fallback: creator not wired, cannot create fallback")
		return
	}

	sessionCtx := fmt.Sprintf("Agent %s was asked to create a session wrap-up decision but did not respond within %v. "+
		"This is an automatic fallback decision from the daemon.", actor, timeout)

	issueID, err := creator.CreateFallbackStopDecision(context.Background(), actor, sessionCtx)
	if err != nil {
		log.Printf("stop-fallback: failed to create fallback decision for %s: %v", actor, err)
		return
	}

	log.Printf("stop-fallback: created fallback decision %s for actor %s", issueID, actor)
}

// checkAgentDecision checks whether the agent created a pending decision
// by running `bd decision stop-check` as a subprocess (same as StopDecisionHandler).
// Returns true if a decision exists (subprocess exits 0 = allow stop).
func (h *StopFallbackHandler) checkAgentDecision(cwd, actor string) bool {
	env := &SubprocessEnv{Actor: actor}
	_, _, err := runBDCommandWithEnv(context.Background(), cwd, env,
		"decision", "stop-check", "--json")
	// Exit 0 = allow (decision found), Exit 1 = block (no decision).
	return err == nil
}

// cancelFallback cancels the active fallback timer for an actor.
func (h *StopFallbackHandler) cancelFallback(actor string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ft, ok := h.active[actor]; ok {
		log.Printf("stop-fallback: canceling timer for %s (agent created decision)", actor)
		ft.cancel()
		delete(h.active, actor)
	}
}

// cleanupFallback removes the timer entry after it completes (success or failure).
func (h *StopFallbackHandler) cleanupFallback(actor string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.active, actor)
}
