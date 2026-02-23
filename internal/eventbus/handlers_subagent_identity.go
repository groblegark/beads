package eventbus

import (
	"context"
	"fmt"
	"strings"
)

// AgentSpawner creates a new agent bead via the daemon's CreateAgent RPC.
// Used by SubagentIdentityHandler to create K8s agent pods for subagents. (bd-0lrwn)
type AgentSpawner interface {
	// SpawnAgent creates a new agent bead and returns the agent ID.
	// The daemon sets agent_state=spawning so the K8s controller creates a pod.
	SpawnAgent(ctx context.Context, agentType, rig, parentAgentID, teamID string, labels []string) (agentID string, err error)
}

// SubagentIdentityHandler injects a unique BD_ACTOR for subagents spawned
// by Claude Code's Task tool. When SubagentStart fires, the handler derives
// a scoped name from the parent actor and the subagent's agent_type, then
// injects a system-reminder instructing the subagent to use it. (bd-hfiav)
//
// When an AgentSpawner is set (K8s/cloud mode), the handler also creates a
// new agent bead via CreateAgent RPC so the K8s controller provisions a pod
// for the subagent. (bd-0lrwn)
//
// Without this, subagents inherit the parent's BD_ACTOR and all events,
// decisions, and audit trails are attributed to the parent instead of the
// subagent.
//
// Priority 15 — after health check (5), before gate (20).
type SubagentIdentityHandler struct {
	spawner AgentSpawner
}

// SetAgentSpawner wires in the agent spawner for K8s mode. (bd-0lrwn)
func (h *SubagentIdentityHandler) SetAgentSpawner(s AgentSpawner) { h.spawner = s }

func (h *SubagentIdentityHandler) ID() string           { return "subagent-identity" }
func (h *SubagentIdentityHandler) Handles() []EventType { return []EventType{EventSubagentStart} }
func (h *SubagentIdentityHandler) Priority() int        { return 15 }

func (h *SubagentIdentityHandler) Handle(ctx context.Context, event *Event, result *Result) error {
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

	// In K8s mode, create a new agent bead so the controller provisions a pod. (bd-0lrwn)
	if h.spawner != nil && event.AgentID != "" {
		agentID, err := h.spawner.SpawnAgent(ctx, subName, "", event.AgentID, "", nil)
		if err != nil {
			// Non-fatal — log warning but still inject identity.
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("failed to spawn K8s agent for subagent %s: %v", fullName, err))
		} else {
			// Inject the new agent bead ID so the subagent knows its K8s identity.
			result.Inject = append(result.Inject, fmt.Sprintf(
				"<system-reminder>K8s agent bead created: %s (state: spawning). "+
					"The controller will provision a pod for this subagent.</system-reminder>",
				agentID))
		}
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
