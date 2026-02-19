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

	// StreamAgentEvents is the JetStream stream for agent lifecycle events (bd-e6vh).
	StreamAgentEvents = "AGENT_EVENTS"

	// SubjectAgentPrefix is the subject prefix for agent lifecycle events.
	SubjectAgentPrefix = "agents."

	// StreamMailEvents is the JetStream stream for mail events (bd-h59f).
	StreamMailEvents = "MAIL_EVENTS"

	// SubjectMailPrefix is the subject prefix for mail events.
	SubjectMailPrefix = "mail."

	// StreamMutationEvents is the JetStream stream for bead mutation events (bd-laz4).
	StreamMutationEvents = "MUTATION_EVENTS"

	// SubjectMutationPrefix is the subject prefix for mutation events.
	SubjectMutationPrefix = "mutations."

	// StreamConfigEvents is the JetStream stream for config/formula change events (bd-hkgu).
	StreamConfigEvents = "CONFIG_EVENTS"

	// SubjectConfigPrefix is the subject prefix for config/formula events.
	SubjectConfigPrefix = "config."

	// StreamInboxEvents is the JetStream stream for inbox notification events (bd-xtahx).
	StreamInboxEvents = "INBOX_EVENTS"

	// SubjectInboxPrefix is the subject prefix for inbox events.
	SubjectInboxPrefix = "inbox."
)

// StreamNames lists all known stream short names (used by CLI --stream flag and SSE ?stream= param).
var StreamNames = []string{"hooks", "decisions", "oj", "agents", "mail", "mutations", "config", "inbox"}

// streamToPrefix maps short stream names to NATS subject prefixes.
var streamToPrefix = map[string]string{
	"hooks":     SubjectHookPrefix,
	"decisions": SubjectDecisionPrefix,
	"oj":        SubjectOjPrefix,
	"agents":    SubjectAgentPrefix,
	"mail":      SubjectMailPrefix,
	"mutations": SubjectMutationPrefix,
	"config":    SubjectConfigPrefix,
	"inbox":     SubjectInboxPrefix,
}

// prefixToStream maps subject prefixes (without trailing dot) to short stream names.
var prefixToStream = map[string]string{
	"hooks":     "hooks",
	"decisions": "decisions",
	"oj":        "oj",
	"agents":    "agents",
	"mail":      "mail",
	"mutations": "mutations",
	"config":    "config",
	"inbox":     "inbox",
}

// StreamForSubject returns the short stream name for a NATS subject.
// e.g., "hooks.SessionStart" → "hooks", "decisions._global.DecisionCreated" → "decisions".
func StreamForSubject(subject string) string {
	for i := 0; i < len(subject); i++ {
		if subject[i] == '.' {
			if name, ok := prefixToStream[subject[:i]]; ok {
				return name
			}
			break
		}
	}
	return ""
}

// EventTypeFromSubject extracts the event type from a NATS subject.
// e.g., "hooks.SessionStart" → "SessionStart",
// "decisions._global.DecisionCreated" → "DecisionCreated" (last segment).
func EventTypeFromSubject(subject string) string {
	for i := len(subject) - 1; i >= 0; i-- {
		if subject[i] == '.' {
			return subject[i+1:]
		}
	}
	return subject
}

// SubjectPrefixForStream returns the NATS subject prefix for a short stream name.
// e.g., "hooks" → "hooks.", "mutations" → "mutations.".
// Returns "" if the stream name is unknown.
func SubjectPrefixForStream(name string) string {
	return streamToPrefix[name]
}

// StreamNameForJetStream returns the JetStream stream constant for a short name.
// e.g., "hooks" → "HOOK_EVENTS". Returns "" if unknown.
func StreamNameForJetStream(name string) string {
	switch name {
	case "hooks":
		return StreamHookEvents
	case "decisions":
		return StreamDecisionEvents
	case "oj":
		return StreamOjEvents
	case "agents":
		return StreamAgentEvents
	case "mail":
		return StreamMailEvents
	case "mutations":
		return StreamMutationEvents
	case "config":
		return StreamConfigEvents
	case "inbox":
		return StreamInboxEvents
	}
	return ""
}

// SubjectForEvent returns the NATS subject for a given event type.
// Hook events use "hooks.<type>"; decision events use "decisions.<type>";
// OJ events use "oj.<type>"; mutation events use "mutations.<type>".
//
// For decision events without a requestedBy scope, use SubjectForDecisionEvent
// instead to include agent-scoped routing.
func SubjectForEvent(eventType EventType) string {
	if eventType.IsDecisionEvent() {
		return SubjectDecisionPrefix + string(eventType)
	}
	if eventType.IsOjEvent() {
		return SubjectOjPrefix + string(eventType)
	}
	if eventType.IsAgentEvent() {
		return SubjectAgentPrefix + string(eventType)
	}
	if eventType.IsMailEvent() {
		return SubjectMailPrefix + string(eventType)
	}
	if eventType.IsMutationEvent() {
		return SubjectMutationPrefix + string(eventType)
	}
	if eventType.IsConfigEvent() {
		return SubjectConfigPrefix + string(eventType)
	}
	return SubjectHookPrefix + string(eventType)
}

// SubjectForDecisionEvent returns a scoped NATS subject for a decision event.
// When requestedBy is non-empty, the subject is "decisions.<requestedBy>.<EventType>"
// so that agents can subscribe to only their own decisions via "decisions.<id>.>".
// When requestedBy is empty, falls back to "decisions._global.<EventType>".
// Subscribers using the wildcard "decisions.>" still receive all decision events.
func SubjectForDecisionEvent(eventType EventType, requestedBy string) string {
	scope := "_global"
	if requestedBy != "" {
		scope = requestedBy
	}
	return SubjectDecisionPrefix + scope + "." + string(eventType)
}

// SubjectForHookEvent returns an agent-scoped NATS subject for a hook event.
// When actor is non-empty, the subject is "hooks.<actor>.<EventType>" so that
// consumers can subscribe to a single agent's events via "hooks.<actor>.>".
// When actor is empty, falls back to "hooks._global.<EventType>".
// Subscribers using the wildcard "hooks.>" still receive all hook events. (bd-fwylb)
func SubjectForHookEvent(eventType EventType, actor string) string {
	scope := "_global"
	if actor != "" {
		scope = actor
	}
	return SubjectHookPrefix + scope + "." + string(eventType)
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

	// Agent lifecycle events stream (bd-e6vh).
	if _, err := js.StreamInfo(StreamAgentEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamAgentEvents,
			Subjects: []string{SubjectAgentPrefix + ">"},
			Storage:  nats.FileStorage,
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamAgentEvents, err)
		}
	}

	// Mail events stream (bd-h59f).
	if _, err := js.StreamInfo(StreamMailEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamMailEvents,
			Subjects: []string{SubjectMailPrefix + ">"},
			Storage:  nats.FileStorage,
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamMailEvents, err)
		}
	}

	// Mutation events stream (bd-laz4).
	if _, err := js.StreamInfo(StreamMutationEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamMutationEvents,
			Subjects: []string{SubjectMutationPrefix + ">"},
			Storage:  nats.FileStorage,
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamMutationEvents, err)
		}
	}

	// Config/formula events stream (bd-hkgu).
	if _, err := js.StreamInfo(StreamConfigEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamConfigEvents,
			Subjects: []string{SubjectConfigPrefix + ">"},
			Storage:  nats.FileStorage,
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamConfigEvents, err)
		}
	}

	// Inbox events stream (bd-xtahx).
	if _, err := js.StreamInfo(StreamInboxEvents); err != nil {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     StreamInboxEvents,
			Subjects: []string{SubjectInboxPrefix + ">"},
			Storage:  nats.FileStorage,
			MaxMsgs:  10000,
			MaxBytes: 100 << 20,
		})
		if err != nil {
			return fmt.Errorf("create %s stream: %w", StreamInboxEvents, err)
		}
	}

	return nil
}

// AgentBaseName strips a trailing "-N" continuation suffix from an agent name.
// Continuation sessions append a numeric suffix (e.g., "sharp-seal-1") when
// the original session runs out of context. This helper normalizes back to the
// base name for NATS subject matching and decision lookup. (bd-6vtx4)
//
//	"sharp-seal-1" → "sharp-seal"
//	"sharp-seal"   → "sharp-seal"
//	"stout-fish-23" → "stout-fish"
func AgentBaseName(name string) string {
	idx := len(name) - 1
	// Walk backwards past digits.
	for idx >= 0 && name[idx] >= '0' && name[idx] <= '9' {
		idx--
	}
	// If we walked past at least one digit and hit a hyphen, strip the suffix.
	if idx >= 0 && idx < len(name)-1 && name[idx] == '-' {
		return name[:idx]
	}
	return name
}
