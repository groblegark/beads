package eventbus

import (
	"context"
	"fmt"
	"strings"
)

// SubagentIdentityHandler injects a unique BD_ACTOR for subagents spawned
// by Claude Code's Task tool. When SubagentStart fires, the handler derives
// a scoped name from the parent actor and the subagent's agent_type, then
// injects a system-reminder instructing the subagent to use it. (bd-hfiav)
//
// Without this, subagents inherit the parent's BD_ACTOR and all events,
// decisions, and audit trails are attributed to the parent instead of the
// subagent.
//
// Priority 15 — after health check (5), before gate (20).
type SubagentIdentityHandler struct{}

func (h *SubagentIdentityHandler) ID() string           { return "subagent-identity" }
func (h *SubagentIdentityHandler) Handles() []EventType { return []EventType{EventSubagentStart} }
func (h *SubagentIdentityHandler) Priority() int        { return 15 }

func (h *SubagentIdentityHandler) Handle(_ context.Context, event *Event, result *Result) error {
	// Derive subagent name from parent actor + agent_type.
	parentActor := event.Actor
	agentType := event.AgentType

	if agentType == "" {
		// No agent_type — can't derive a name. Skip silently.
		return nil
	}

	// Clean the agent_type for use as an identity component.
	// Claude Code may pass types like "general-purpose", "Explore", etc.
	subName := strings.ToLower(strings.TrimSpace(agentType))
	subName = strings.ReplaceAll(subName, " ", "-")

	var fullName string
	if parentActor != "" {
		fullName = fmt.Sprintf("%s/%s", parentActor, subName)
	} else {
		fullName = subName
	}

	// Inject identity reminder into the subagent's context.
	reminder := fmt.Sprintf(
		"<system-reminder>You are subagent %q. "+
			"When running bd commands, use: export BD_ACTOR=%q"+
			"</system-reminder>",
		fullName, fullName,
	)
	result.Inject = append(result.Inject, reminder)

	return nil
}
