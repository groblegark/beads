package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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

	// Store coop_url in the notes field (bd-wvrpy).
	// If not explicitly provided, derive from pod_ip (default coop port 3000). (bd-otwxa)
	coopURL := args.CoopURL
	if coopURL == "" && args.PodIP != "" {
		coopURL = fmt.Sprintf("http://%s:3000", args.PodIP)
	}
	if coopURL != "" {
		issue, err := store.GetIssue(ctx, args.AgentID)
		if err == nil && issue != nil {
			updates["notes"] = upsertNotesField(issue.Notes, "coop_url", coopURL)
		}
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

	// Clear coop_url from notes on deregister (bd-wvrpy).
	if issue, err := store.GetIssue(ctx, args.AgentID); err == nil && issue != nil {
		updates["notes"] = removeNotesField(issue.Notes, "coop_url")
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
			CoopURL:       extractCoopURL(issue.Notes),
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
			ProjectRoot:         e.CWD, // (bd-z6958)
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
	// Also collects unclaimed in_progress beads (no assignee). (bd-oenjf)
	unclaimed := s.enrichRosterWithTasks(req, rosterEntries)

	// Enrich with git branch/repo context from CWD (bd-z6958).
	enrichRosterWithGitContext(rosterEntries)

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
		Actors:         rosterEntries,
		UnclaimedTasks: unclaimed,
		Uptime:         pt.Uptime().Round(time.Second).String(),
		Tracked:        len(pt.Roster(0)),
		Working:        working,
		Idle:           idle,
		Dead:           dead,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleAgentRecentEvents returns recent events for a specific agent from the
// presence tracker's ring buffer. (bd-9y9ba)
func (s *Server) handleAgentRecentEvents(req *Request) Response {
	if s.bus == nil {
		return Response{Error: "event bus not configured"}
	}

	pt := s.bus.Presence()
	if pt == nil {
		return Response{Error: "presence tracker not running (NATS may not be enabled)"}
	}

	var args AgentRecentEventsArgs
	if req.Args != nil {
		_ = json.Unmarshal(req.Args, &args)
	}

	if args.Actor == "" {
		return Response{Success: false, Error: "actor is required"}
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	events := pt.RecentEvents(args.Actor, limit)
	result := AgentRecentEventsResult{
		Actor:  args.Actor,
		Events: make([]AgentEvent, len(events)),
	}
	for i, e := range events {
		result.Events[i] = AgentEvent{
			Timestamp: e.Timestamp.Format(time.RFC3339),
			EventType: e.EventType,
			ToolName:  e.ToolName,
			Summary:   e.Summary,
		}
	}

	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// enrichRosterWithTasks looks up in_progress beads and matches them to roster
// actors via created_by or assignee. Also walks parent-child deps to find epics.
// Returns unclaimed in_progress beads (no assignee) for visibility. (bd-oenjf)
func (s *Server) enrichRosterWithTasks(req *Request, entries []AgentRosterEntry) []UnclaimedTask {
	store := s.storage
	if store == nil || len(entries) == 0 {
		return nil
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Query all in_progress beads.
	inProgress := types.StatusInProgress
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{Status: &inProgress})
	if err != nil {
		return nil
	}

	// Build actor → issue mapping. Prefer assignee, fall back to created_by.
	// If an actor has multiple in_progress beads, keep the most recently updated.
	// Don't rely on storage sort order — explicitly compare UpdatedAt. (bd-nqnyn)
	type taskInfo struct {
		id        string
		title     string
		updatedAt time.Time
	}
	actorTask := make(map[string]taskInfo)

	// Collect unclaimed beads — in_progress with no assignee. (bd-oenjf)
	var unclaimed []UnclaimedTask

	for _, issue := range issues {
		actor := issue.Assignee
		if actor == "" {
			actor = issue.CreatedBy
		}
		if actor == "" {
			// Truly unclaimed — no assignee and no creator attribution.
			unclaimed = append(unclaimed, UnclaimedTask{
				ID:       issue.ID,
				Title:    issue.Title,
				Priority: issue.Priority,
			})
			continue
		}
		// Also track beads with no assignee but attributed to a creator — these
		// are assignment gaps where someone created work but didn't claim it.
		if issue.Assignee == "" {
			unclaimed = append(unclaimed, UnclaimedTask{
				ID:       issue.ID,
				Title:    issue.Title,
				Priority: issue.Priority,
			})
		}
		if existing, ok := actorTask[actor]; ok {
			if !issue.UpdatedAt.After(existing.updatedAt) {
				continue
			}
		}
		actorTask[actor] = taskInfo{id: issue.ID, title: issue.Title, updatedAt: issue.UpdatedAt}
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

	return unclaimed
}

// gitInfo holds cached git branch/repo for a directory.
type gitInfo struct {
	branch string
	repo   string
}

// enrichRosterWithGitContext derives git branch and repo name from each actor's
// CWD (ProjectRoot). Uses a per-directory cache to avoid redundant git execs
// when multiple agents share the same working directory. (bd-z6958)
func enrichRosterWithGitContext(entries []AgentRosterEntry) {
	cache := make(map[string]*gitInfo)

	for i := range entries {
		cwd := entries[i].ProjectRoot
		if cwd == "" {
			continue
		}

		info, ok := cache[cwd]
		if !ok {
			info = resolveGitInfo(cwd)
			cache[cwd] = info
		}

		entries[i].Branch = info.branch
		entries[i].Repo = info.repo
	}
}

// resolveGitInfo runs git commands in dir to get the branch and repo name.
// Returns empty strings on any error (not a git repo, git not installed, etc.).
func resolveGitInfo(dir string) *gitInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	info := &gitInfo{}

	// Get current branch.
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = dir
	if out, err := branchCmd.Output(); err == nil {
		info.branch = strings.TrimSpace(string(out))
	}

	// Get repo name from remote URL or directory name.
	remoteCmd := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
	remoteCmd.Dir = dir
	if out, err := remoteCmd.Output(); err == nil {
		info.repo = repoNameFromURL(strings.TrimSpace(string(out)))
	} else {
		// No remote — use directory basename as repo name.
		info.repo = filepath.Base(dir)
	}

	return info
}

// repoNameFromURL extracts a short repo name from a git remote URL.
// Handles HTTPS (https://github.com/org/repo.git) and SSH (git@github.com:org/repo.git).
func repoNameFromURL(url string) string {
	if url == "" {
		return ""
	}

	// Strip trailing .git
	url = strings.TrimSuffix(url, ".git")

	// SSH format: git@github.com:org/repo — colon separator, no slashes before it
	// (exclude URLs with :// scheme separator)
	if idx := strings.LastIndex(url, ":"); idx > 0 && !strings.Contains(url, "://") {
		return url[idx+1:]
	}

	// HTTPS format: https://github.com/org/repo — take last two path components
	parts := strings.Split(url, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}

	return url
}
