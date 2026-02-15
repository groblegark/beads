package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/coop"
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

// sendCoopSignal sends a signal to a coop sidecar at the given URL.
func sendCoopSignal(ctx context.Context, coopURL, signal string) error {
	client := coop.NewClient(coopURL, coop.WithTimeout(5*time.Second))
	return client.Signal(ctx, signal)
}
