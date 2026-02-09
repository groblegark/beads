package eventbus

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

const (
	// StreamHookEvents is the JetStream stream for hook events.
	StreamHookEvents = "HOOK_EVENTS"

	// StreamDecisionEvents is the JetStream stream for decision events (od-k3o.15.1).
	StreamDecisionEvents = "DECISION_EVENTS"

	// StreamOjEvents is the JetStream stream for OddJobs lifecycle events (bd-4q86.4).
	StreamOjEvents = "OJ_EVENTS"

	// SubjectHookPrefix is the subject prefix for all hook events.
	SubjectHookPrefix = "hooks."

	// SubjectDecisionPrefix is the subject prefix for decision events.
	SubjectDecisionPrefix = "decisions."

	// SubjectOjPrefix is the subject prefix for OddJobs events.
	SubjectOjPrefix = "oj."

	// StreamChatEvents is the JetStream stream for chat events (bd-viux).
	StreamChatEvents = "CHAT_EVENTS"

	// SubjectChatPrefix is the subject prefix for chat events.
	SubjectChatPrefix = "chat."
)

// SubjectForEvent returns the NATS subject for a given event type.
// Hook events use "hooks.<type>"; decision events use "decisions.<type>";
// OJ events use "oj.<type>"; chat events use "chat.<type>".
func SubjectForEvent(eventType EventType) string {
	if eventType.IsDecisionEvent() {
		return SubjectDecisionPrefix + string(eventType)
	}
	if eventType.IsOjEvent() {
		return SubjectOjPrefix + string(eventType)
	}
	if eventType.IsChatEvent() {
		return SubjectChatPrefix + string(eventType)
	}
	return SubjectHookPrefix + string(eventType)
}

// SubjectForChatSession returns a NATS subject scoped to a specific chat
// session and direction. Used for targeted subscriptions.
// Example: "chat.session.abc123.in" or "chat.session.abc123.out"
func SubjectForChatSession(sessionTag, direction string) string {
	return SubjectChatPrefix + "session." + sessionTag + "." + direction
}

// EnsureStreams creates the required JetStream streams if they don't already
// exist. Called during daemon startup when NATS is enabled.
func EnsureStreams(js nats.JetStreamContext) error {
	// Hook events stream.
	if _, err := js.StreamInfo(StreamHookEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamHookEvents,
			Subjects: []string{SubjectHookPrefix + ">"},
			Storage:  nats.FileStorage,
			// Retain last 10000 messages or 100MB, whichever comes first.
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamHookEvents, err)
		}
	}

	// Decision events stream (od-k3o.15.1).
	if _, err := js.StreamInfo(StreamDecisionEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamDecisionEvents,
			Subjects: []string{SubjectDecisionPrefix + ">"},
			Storage:  nats.FileStorage,
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamDecisionEvents, err)
		}
	}

	// OddJobs lifecycle events stream (bd-4q86.4).
	if _, err := js.StreamInfo(StreamOjEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamOjEvents,
			Subjects: []string{SubjectOjPrefix + ">"},
			Storage:  nats.FileStorage,
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamOjEvents, err)
		}
	}

	// Chat events stream (bd-viux): bidirectional Slack↔Agent messages.
	if _, err := js.StreamInfo(StreamChatEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamChatEvents,
			Subjects: []string{SubjectChatPrefix + ">"},
			Storage:  nats.FileStorage,
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamChatEvents, err)
		}
	}

	return nil
}
