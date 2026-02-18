package rpc

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/types"
)

// handleAgentPodRegister sets pod fields on an agent bead.
func (s *Server) handleAgentPodRegister(req *Request) Response {
	var args AgentPodRegisterArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid agent_pod_register args: %v", err),
		}
	}

	if args.AgentID == "" {
		return Response{Success: false, Error: "agent_id is required"}
	}
	if args.PodName == "" {
		return Response{Success: false, Error: "pod_name is required"}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Default pod_status to "running" if not specified
	podStatus := args.PodStatus
	if podStatus == "" {
		podStatus = "running"
	}

	updates := map[string]interface{}{
		"pod_name":       args.PodName,
		"pod_ip":         args.PodIP,
		"pod_node":       args.PodNode,
		"pod_status":     podStatus,
		"screen_session": args.ScreenSession,
		"last_activity":  time.Now(),
	}

	if err := store.UpdateIssue(ctx, args.AgentID, updates, req.Actor); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to register pod for agent %s: %v", args.AgentID, err),
		}
	}

	if agentIssue, _ := store.GetIssue(ctx, args.AgentID); agentIssue != nil {
		evt := MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor}
		enrichEvent(&evt, agentIssue)
		s.emitRichMutation(evt)
	} else {
		s.emitRichMutation(MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor})
	}

	// Emit OjAgentSpawned event (bd-2iae)
	s.emitOjEvent(eventbus.EventOjAgentSpawned, eventbus.OjAgentEventPayload{
		AgentName: args.AgentID,
		SessionID: args.ScreenSession,
	}, s.reqActor(req))

	// Emit agent lifecycle event so PresenceTracker roster picks up the agent.
	if agentIssue, _ := store.GetIssue(ctx, args.AgentID); agentIssue != nil {
		s.emitAgentEvent(eventbus.EventAgentStarted, eventbus.AgentEventPayload{
			AgentID:   args.AgentID,
			AgentName: args.AgentID,
			RigName:   agentIssue.Rig,
			Role:      agentIssue.RoleType,
			SessionID: args.ScreenSession,
		}, s.reqActor(req))
	}

	result := AgentPodRegisterResult{
		AgentID:   args.AgentID,
		PodName:   args.PodName,
		PodStatus: podStatus,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleAgentPodDeregister clears all pod fields on an agent bead.
func (s *Server) handleAgentPodDeregister(req *Request) Response {
	var args AgentPodDeregisterArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid agent_pod_deregister args: %v", err),
		}
	}

	if args.AgentID == "" {
		return Response{Success: false, Error: "agent_id is required"}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	updates := map[string]interface{}{
		"pod_name":       "",
		"pod_ip":         "",
		"pod_node":       "",
		"pod_status":     "",
		"screen_session": "",
		"last_activity":  time.Now(),
	}

	if err := store.UpdateIssue(ctx, args.AgentID, updates, req.Actor); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to deregister pod for agent %s: %v", args.AgentID, err),
		}
	}

	if agentIssue, _ := store.GetIssue(ctx, args.AgentID); agentIssue != nil {
		evt := MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor}
		enrichEvent(&evt, agentIssue)
		s.emitRichMutation(evt)
	} else {
		s.emitRichMutation(MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor})
	}

	result := AgentPodDeregisterResult{
		AgentID: args.AgentID,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleAgentPodStatus updates only the pod_status field on an agent bead.
func (s *Server) handleAgentPodStatus(req *Request) Response {
	var args AgentPodStatusArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid agent_pod_status args: %v", err),
		}
	}

	if args.AgentID == "" {
		return Response{Success: false, Error: "agent_id is required"}
	}
	if args.PodStatus == "" {
		return Response{Success: false, Error: "pod_status is required"}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	updates := map[string]interface{}{
		"pod_status":    args.PodStatus,
		"last_activity": time.Now(),
	}

	if err := store.UpdateIssue(ctx, args.AgentID, updates, req.Actor); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to update pod status for agent %s: %v", args.AgentID, err),
		}
	}

	if agentIssue, _ := store.GetIssue(ctx, args.AgentID); agentIssue != nil {
		evt := MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor}
		enrichEvent(&evt, agentIssue)
		s.emitRichMutation(evt)
	} else {
		s.emitRichMutation(MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor})
	}

	// Emit OJ agent lifecycle events based on pod status (bd-2iae)
	switch args.PodStatus {
	case "idle":
		s.emitOjEvent(eventbus.EventOjAgentIdle, eventbus.OjAgentEventPayload{
			AgentName: args.AgentID,
		}, s.reqActor(req))
	case "failed":
		s.emitOjEvent(eventbus.EventOjJobFailed, eventbus.OjJobEventPayload{
			JobID:  args.AgentID,
			BeadID: args.AgentID,
			Error:  "agent pod failed",
		}, s.reqActor(req))
	}

	result := AgentPodStatusResult{
		AgentID:   args.AgentID,
		PodStatus: args.PodStatus,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleAgentPodList returns agents with active pods (pod_name != ”).
func (s *Server) handleAgentPodList(req *Request) Response {
	var args AgentPodListArgs
	if req.Args != nil {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid agent_pod_list args: %v", err),
			}
		}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// List all agent beads by label
	issues, err := store.GetIssuesByLabel(ctx, "gt:agent")
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list agents: %v", err),
		}
	}

	// Filter to agents with active pods
	var agents []AgentPodInfo
	for _, issue := range issues {
		if issue.PodName == "" {
			continue
		}
		// Filter by rig if specified
		if args.Rig != "" && issue.Rig != args.Rig {
			continue
		}
		agents = append(agents, AgentPodInfo{
			AgentID:       issue.ID,
			PodName:       issue.PodName,
			PodIP:         issue.PodIP,
			PodNode:       issue.PodNode,
			PodStatus:     issue.PodStatus,
			ScreenSession: issue.ScreenSession,
			AgentState:    string(issue.AgentState),
			Rig:           issue.Rig,
			RoleType:      issue.RoleType,
		})
	}

	result := AgentPodListResult{Agents: agents}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleAgentRoster returns a live presence roster from the NATS event bus (bd-3d5m2).
// Enriched with in_progress task and parent epic context (bd-qdhxw).
func (s *Server) handleAgentRoster(req *Request) Response {
	if s.bus == nil {
		return Response{Error: "event bus not configured"}
	}

	pt := s.bus.Presence()
	if pt == nil {
		return Response{Error: "presence tracker not running (NATS may not be enabled)"}
	}

	var args AgentRosterArgs
	if req.Args != nil {
		_ = json.Unmarshal(req.Args, &args)
	}

	staleThreshold := time.Duration(0)
	if args.StaleThresholdSecs > 0 {
		staleThreshold = time.Duration(args.StaleThresholdSecs) * time.Second
	}

	entries := pt.Roster(staleThreshold)
	rosterEntries := make([]AgentRosterEntry, len(entries))
	for i, e := range entries {
		entry := AgentRosterEntry{
			Actor:               e.Actor,
			LastSeen:            e.LastSeen.Format(time.RFC3339),
			LastEvent:           e.LastEvent,
			ToolName:            e.ToolName,
			SessionID:           e.SessionID,
			IdleSecs:            e.IdleSecs,
			EventCount:          e.EventCount,
			SessionDurationSecs: e.SessionDurationSecs,
			EventsPerMin:        e.EventsPerMin,
			Reaped:              e.Reaped, // (bd-khlpu)
		}
		if e.Reaped && !e.ReapedAt.IsZero() {
			entry.ReapedAt = e.ReapedAt.Format(time.RFC3339)
		}
		rosterEntries[i] = entry
	}

	// Enrich with in_progress task and epic context (bd-qdhxw).
	s.enrichRosterWithTasks(req, rosterEntries)

	// Compute working/idle/dead summary counters (bd-4ul0v, bd-khlpu).
	var working, idle, dead int
	for _, e := range rosterEntries {
		if e.Reaped {
			dead++
		} else if e.TaskID != "" {
			working++
		} else {
			idle++
		}
	}

	result := AgentRosterResult{
		Actors:  rosterEntries,
		Uptime:  pt.Uptime().Round(time.Second).String(),
		Tracked: len(pt.Roster(0)),
		Working: working,
		Idle:    idle,
		Dead:    dead,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// enrichRosterWithTasks looks up in_progress beads and matches them to roster
// actors via created_by or assignee. Also walks parent-child deps to find epics.
func (s *Server) enrichRosterWithTasks(req *Request, entries []AgentRosterEntry) {
	store := s.storage
	if store == nil || len(entries) == 0 {
		return
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Query all in_progress beads.
	inProgress := types.StatusInProgress
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{Status: &inProgress})
	if err != nil {
		return
	}

	// Build actor → issue mapping. Prefer assignee, fall back to created_by.
	// If an actor has multiple in_progress beads, use the most recently updated.
	type taskInfo struct {
		id    string
		title string
	}
	actorTask := make(map[string]taskInfo)

	for _, issue := range issues {
		actor := issue.Assignee
		if actor == "" {
			actor = issue.CreatedBy
		}
		if actor == "" {
			continue
		}
		// If we already have one for this actor, keep the most recently updated.
		if existing, ok := actorTask[actor]; ok {
			_ = existing // keep first match (issues come sorted by updated_at desc)
			continue
		}
		actorTask[actor] = taskInfo{id: issue.ID, title: issue.Title}
	}

	// Enrich each roster entry with task info.
	for i := range entries {
		info, ok := actorTask[entries[i].Actor]
		if !ok {
			continue
		}
		entries[i].TaskID = info.id
		entries[i].TaskTitle = info.title

		// Find epic via dependency walk. Check two directions:
		// 1. Forward deps (parent-child): child depends_on parent
		// 2. Reverse deps (blocks): epic blocks child → epic.issue_id, child.depends_on_id
		epicFound := false

		// Check forward: deps where this bead depends on a parent (parent-child or blocks).
		if deps, err := store.GetDependencyRecords(ctx, info.id); err == nil {
			for _, dep := range deps {
				if dep.Type == types.DepParentChild || dep.Type == types.DepBlocks {
					if parent, err := store.GetIssue(ctx, dep.DependsOnID); err == nil && parent != nil {
						entries[i].EpicID = parent.ID
						entries[i].EpicTitle = parent.Title
						epicFound = true
					}
					break
				}
			}
		}

		// Check reverse: blocks deps where an epic blocks this bead. (bd-qdhxw)
		// GetDependentsWithMetadata(childID) returns records where depends_on_id = childID.
		if !epicFound {
			if depMeta, err := store.GetDependentsWithMetadata(ctx, info.id); err == nil {
				for _, dm := range depMeta {
					if dm.DependencyType == types.DepBlocks || dm.DependencyType == types.DepParentChild {
						entries[i].EpicID = dm.Issue.ID
						entries[i].EpicTitle = dm.Issue.Title
						break
					}
				}
			}
		}
	}
}
