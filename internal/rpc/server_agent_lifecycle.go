package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/coop"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/types"
)

// handleAgentStop sets agent_state=stopping on the bead and optionally
// sends SIGTERM to the agent's coop sidecar. The K8s controller watches
// for the stopping state and deletes the pod.
func (s *Server) handleAgentStop(req *Request) Response {
	var args AgentStopArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid agent_stop args: %v", err),
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

	// Load the agent bead to get coop_url from notes.
	issue, err := store.GetIssue(ctx, args.AgentID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("agent %s not found: %v", args.AgentID, err),
		}
	}

	// Set agent_state to stopping.
	updates := map[string]interface{}{
		"agent_state":   string(types.StateStopping),
		"last_activity": time.Now(),
	}
	if err := store.UpdateIssue(ctx, args.AgentID, updates, req.Actor); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to update agent_state for %s: %v", args.AgentID, err),
		}
	}

	// Emit mutation event so SSE subscribers (controller) see the state change.
	if updatedIssue, _ := store.GetIssue(ctx, args.AgentID); updatedIssue != nil {
		evt := MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor}
		enrichEvent(&evt, updatedIssue)
		s.emitRichMutation(evt)
	} else {
		s.emitRichMutation(MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor})
	}

	// Emit agent lifecycle event so PresenceTracker roster reflects the stop.
	s.emitAgentEvent(eventbus.EventAgentStopped, eventbus.AgentEventPayload{
		AgentID:   args.AgentID,
		AgentName: args.AgentID,
		RigName:   issue.Rig,
		Role:      issue.RoleType,
		Reason:    "agent stop requested",
	}, s.reqActor(req))

	// Optionally send SIGTERM to the coop sidecar.
	coopSignaled := false
	if !args.Force {
		if coopURL := extractCoopURL(issue.Notes); coopURL != "" {
			if err := sendCoopSignal(ctx, coopURL, "SIGTERM"); err == nil {
				coopSignaled = true
			}
		}
	}

	result := AgentStopResult{
		AgentID:    args.AgentID,
		AgentState: string(types.StateStopping),
		CoopSignal: coopSignaled,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleAgentRestart sets agent_state to spawning on the bead.
// The controller will delete the existing pod (if any) and create a new one
// when it sees the spawning state.
func (s *Server) handleAgentRestart(req *Request) Response {
	var args AgentRestartArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid agent_restart args: %v", err),
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

	// Load the agent bead to get coop_url.
	issue, err := store.GetIssue(ctx, args.AgentID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("agent %s not found: %v", args.AgentID, err),
		}
	}

	// Best-effort SIGTERM to current coop session before restarting.
	if coopURL := extractCoopURL(issue.Notes); coopURL != "" {
		_ = sendCoopSignal(ctx, coopURL, "SIGTERM")
	}

	// Set agent_state to spawning — controller will recreate the pod.
	// Also clear pod fields so the old pod metadata doesn't linger.
	updates := map[string]interface{}{
		"agent_state":   string(types.StateSpawning),
		"pod_status":    "",
		"last_activity": time.Now(),
	}
	if err := store.UpdateIssue(ctx, args.AgentID, updates, req.Actor); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to update agent_state for %s: %v", args.AgentID, err),
		}
	}

	// Emit mutation event.
	if updatedIssue, _ := store.GetIssue(ctx, args.AgentID); updatedIssue != nil {
		evt := MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor}
		enrichEvent(&evt, updatedIssue)
		s.emitRichMutation(evt)
	} else {
		s.emitRichMutation(MutationEvent{Type: MutationUpdate, IssueID: args.AgentID, Actor: req.Actor})
	}

	// Emit agent lifecycle event so PresenceTracker roster reflects the restart.
	s.emitAgentEvent(eventbus.EventAgentStarted, eventbus.AgentEventPayload{
		AgentID:   args.AgentID,
		AgentName: args.AgentID,
		RigName:   issue.Rig,
		Role:      issue.RoleType,
		Reason:    "agent restart requested",
	}, s.reqActor(req))

	result := AgentRestartResult{
		AgentID:    args.AgentID,
		AgentState: string(types.StateSpawning),
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleAgentSignal sends a signal to an agent's coop sidecar.
func (s *Server) handleAgentSignal(req *Request) Response {
	var args AgentSignalArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid agent_signal args: %v", err),
		}
	}

	if args.AgentID == "" {
		return Response{Success: false, Error: "agent_id is required"}
	}
	if args.Signal == "" {
		return Response{Success: false, Error: "signal is required"}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	issue, err := store.GetIssue(ctx, args.AgentID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("agent %s not found: %v", args.AgentID, err),
		}
	}

	coopURL := extractCoopURL(issue.Notes)
	if coopURL == "" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("agent %s has no coop_url in notes", args.AgentID),
		}
	}

	sent := false
	if err := sendCoopSignal(ctx, coopURL, args.Signal); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to send %s to agent %s: %v", args.Signal, args.AgentID, err),
		}
	}
	sent = true

	result := AgentSignalResult{
		AgentID: args.AgentID,
		Signal:  args.Signal,
		Sent:    sent,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleCreateAgent creates a new agent bead with the appropriate labels
// and sets agent_state to spawning so the K8s controller picks it up (bd-dwzum).
func (s *Server) handleCreateAgent(req *Request) Response {
	var args CreateAgentArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid create_agent args: %v", err)}
	}
	if args.AgentType == "" {
		return Response{Success: false, Error: "agent_type is required"}
	}
	if args.ParentAgentID == "" {
		return Response{Success: false, Error: "parent_agent_id is required"}
	}
	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}
	ctx, cancel := s.reqCtx(req)
	defer cancel()

	parentIssue, err := store.GetIssue(ctx, args.ParentAgentID)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("parent agent %s not found: %v", args.ParentAgentID, err)}
	}

	labels := []string{"gt:agent", "execution_target:k8s", fmt.Sprintf("parent_agent:%s", args.ParentAgentID)}
	if args.TeamID != "" {
		labels = append(labels, fmt.Sprintf("team:%s", args.TeamID))
	}
	labels = append(labels, args.Labels...)

	title := args.Title
	if title == "" {
		title = fmt.Sprintf("Agent: %s (spawned by %s)", args.AgentType, args.ParentAgentID)
	}
	rig := args.Rig
	if rig == "" {
		rig = parentIssue.Rig
	}

	createData, err := json.Marshal(CreateArgs{
		Title: title, Description: args.Description, IssueType: "agent",
		Labels: labels, RoleType: args.AgentType, Rig: rig, Pinned: true,
	})
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to marshal create args: %v", err)}
	}
	createResp := s.handleCreate(&Request{Operation: OpCreate, Args: createData, Actor: req.Actor})
	if !createResp.Success {
		return Response{Success: false, Error: fmt.Sprintf("failed to create agent bead: %s", createResp.Error)}
	}

	var createResult struct{ ID string `json:"id"` }
	if err := json.Unmarshal(createResp.Data, &createResult); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to parse create response: %v", err)}
	}
	agentID := createResult.ID

	updates := map[string]interface{}{"agent_state": string(types.StateSpawning), "last_activity": time.Now()}
	if err := store.UpdateIssue(ctx, agentID, updates, req.Actor); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to set agent_state for %s: %v", agentID, err)}
	}

	if updatedIssue, _ := store.GetIssue(ctx, agentID); updatedIssue != nil {
		evt := MutationEvent{Type: MutationCreate, IssueID: agentID, Actor: req.Actor}
		enrichEvent(&evt, updatedIssue)
		s.emitRichMutation(evt)
	}
	s.emitAgentEvent(eventbus.EventAgentStarted, eventbus.AgentEventPayload{
		AgentID: agentID, AgentName: agentID, RigName: rig,
		Role: args.AgentType, Reason: fmt.Sprintf("spawned by %s", args.ParentAgentID),
	}, s.reqActor(req))

	respData, _ := json.Marshal(CreateAgentResult{AgentID: agentID, AgentState: string(types.StateSpawning), Rig: rig})
	return Response{Success: true, Data: respData}
}

// SpawnAgent implements eventbus.AgentSpawner by delegating to handleCreateAgent. (bd-0lrwn)
func (s *Server) SpawnAgent(ctx context.Context, agentType, rig, parentAgentID, teamID string, labels []string) (string, error) {
	args := CreateAgentArgs{
		AgentType:     agentType,
		Rig:           rig,
		ParentAgentID: parentAgentID,
		TeamID:        teamID,
		Labels:        labels,
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("marshal create_agent args: %w", err)
	}
	resp := s.handleCreateAgent(&Request{
		Operation: OpCreateAgent,
		Args:      data,
		Actor:     "daemon",
	})
	if !resp.Success {
		return "", fmt.Errorf("create agent failed: %s", resp.Error)
	}
	var result CreateAgentResult
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", fmt.Errorf("parse create_agent response: %w", err)
	}
	return result.AgentID, nil
}

// extractCoopURL parses the coop_url from a bead's notes field.
// Notes format: "coop_url: http://host:port\nother_key: value\n"
func extractCoopURL(notes string) string {
	for _, line := range strings.Split(notes, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "coop_url" {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

// sendCoopSignal sends a signal to a coop sidecar at the given URL (bd-88ua6).
// Retries up to 3 times with exponential backoff for transient network failures.
func sendCoopSignal(ctx context.Context, coopURL, signal string) error {
	client := coop.NewClient(coopURL, coop.WithTimeout(10*time.Second))

	var lastErr error
	delays := []time.Duration{0, 1 * time.Second, 2 * time.Second}
	for i, delay := range delays {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(delay):
			}
		}

		lastErr = client.Signal(ctx, signal)
		if lastErr == nil {
			return nil
		}

		// Don't retry on context cancellation.
		if ctx.Err() != nil {
			return fmt.Errorf("signal %s to %s failed (attempt %d): %w", signal, coopURL, i+1, lastErr)
		}
	}
	return fmt.Errorf("signal %s to %s failed after %d attempts: %w", signal, coopURL, len(delays), lastErr)
}

// upsertNotesField sets a key-value pair in the notes field (bd-wvrpy).
// Notes use "key: value\n" format. If the key already exists, its value is replaced.
func upsertNotesField(notes, key, value string) string {
	var result []string
	found := false
	for _, line := range strings.Split(notes, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			result = append(result, key+": "+value)
			found = true
		} else {
			result = append(result, line)
		}
	}
	if !found {
		trimmed := strings.TrimRight(strings.Join(result, "\n"), "\n")
		if trimmed == "" {
			return key + ": " + value
		}
		return trimmed + "\n" + key + ": " + value
	}
	return strings.Join(result, "\n")
}

// removeNotesField removes a key from the notes field (bd-wvrpy).
func removeNotesField(notes, key string) string {
	var result []string
	for _, line := range strings.Split(notes, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}
