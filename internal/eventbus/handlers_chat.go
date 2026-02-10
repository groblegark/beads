package eventbus

import (
	"context"
	"encoding/json"
	"log"

	"github.com/nats-io/nats.go"
)

// ChatStatusHandler publishes chat status events on SessionStart and Stop,
// enabling the Slack bot to know when agents are available for chat. (bd-viux, bd-4kvl)
//
// On SessionStart: publishes ChatStatus{Status: "available"} to chat.status.<session_id>
// On Stop: publishes ChatStatus{Status: "gone"} to chat.status.<session_id>
//
// Priority 35 (after decisions at 30, before OJ at 40) — we want session
// context and decisions to be set up before announcing chat availability.
type ChatStatusHandler struct {
	jsFn func() nats.JetStreamContext // deferred access to avoid nil at registration time
}

// NewChatStatusHandler creates a handler that publishes chat status events.
// jsFn is called lazily to get the JetStream context (may not be available at startup).
func NewChatStatusHandler(jsFn func() nats.JetStreamContext) *ChatStatusHandler {
	return &ChatStatusHandler{jsFn: jsFn}
}

func (h *ChatStatusHandler) ID() string      { return "chat-status" }
func (h *ChatStatusHandler) Handles() []EventType {
	return []EventType{EventSessionStart, EventStop}
}
func (h *ChatStatusHandler) Priority() int { return 35 }

func (h *ChatStatusHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	js := h.jsFn()
	if js == nil {
		return nil // JetStream not available, silently skip.
	}

	var status string
	switch event.Type {
	case EventSessionStart:
		status = "available"
	case EventStop:
		status = "gone"
	default:
		return nil
	}

	sessionID := event.SessionID
	if sessionID == "" {
		return nil // No session to announce.
	}

	payload := ChatStatusPayload{
		SessionTag: sessionID,
		Status:     status,
		AgentName:  event.AgentID,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("eventbus/chat-status: marshal error: %v", err)
		return nil // Non-fatal, don't block the handler chain.
	}

	subject := SubjectChatPrefix + "status." + sessionID
	if _, err := js.Publish(subject, data); err != nil {
		log.Printf("eventbus/chat-status: publish %s error: %v", subject, err)
		// Non-fatal: agent session continues even if status notification fails.
	} else {
		log.Printf("eventbus/chat-status: published %s (session=%s, status=%s)",
			subject, sessionID, status)
	}

	// On SessionStart, inject a hint about chat availability into the session context.
	if event.Type == EventSessionStart {
		result.Inject = append(result.Inject, chatAvailableHint(sessionID))
	}

	return nil
}

// chatAvailableHint returns context to inject into an agent session telling it
// about its chat session tag and how to listen for messages.
func chatAvailableHint(sessionID string) string {
	return `# Chat Session Available (bd-viux)

Your session is chat-enabled. Slack users can message you.

**Session tag:** ` + "`" + sessionID + "`" + `

## Receiving Messages
To receive Slack messages, run as a **background task**:
` + "```" + `
bd chat listen ` + sessionID + ` --timeout 30m
` + "```" + `
This blocks until a Slack user sends you a message, then returns the message as JSON on stdout.

## Replying
To send a message back to the Slack thread:
` + "```" + `
bd chat reply ` + sessionID + ` "Your response here"
` + "```" + `

## Pattern
1. Start ` + "`bd chat listen`" + ` as background task
2. Continue your work
3. When the task completes, read the message
4. Process and reply with ` + "`bd chat reply`" + `
5. Re-register the listener for the next message

Chat status events are published automatically on session start/stop.
`
}

