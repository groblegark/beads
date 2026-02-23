package rpc

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

func (s *Server) handleGateCreate(req *Request) Response {
	var args GateCreateArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate create args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	now := time.Now()

	// Create gate issue
	gate := &types.Issue{
		Title:     args.Title,
		IssueType: "gate",
		Status:    types.StatusOpen,
		Priority:  1, // Gates are typically high priority
		Assignee:  "deacon/",
		Ephemeral: true, // Gates are wisps (ephemeral)
		AwaitType: args.AwaitType,
		AwaitID:   args.AwaitID,
		Timeout:   args.Timeout,
		Waiters:   args.Waiters,
		CreatedAt: now,
		UpdatedAt: now,
	}
	gate.ContentHash = gate.ComputeContentHash()

	if err := store.CreateIssue(ctx, gate, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create gate: %v", err),
		}
	}

	// Emit mutation event
	s.emitMutationFor(MutationCreate, gate)

	return jsonOK(GateCreateResult{ID: gate.ID})
}

func (s *Server) handleGateList(req *Request) Response {
	var args GateListArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate list args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Build filter for gates
	gateType := types.IssueType("gate")
	filter := types.IssueFilter{
		IssueType: &gateType,
	}
	// By default, exclude closed gates (consistent with CLI behavior)
	if !args.All {
		filter.ExcludeStatus = []types.Status{types.StatusClosed}
	}

	gates, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list gates: %v", err),
		}
	}

	return jsonOK(gates)
}

func (s *Server) handleGateShow(req *Request) Response {
	var args GateShowArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate show args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Resolve partial ID
	gateID, err := utils.ResolvePartialID(ctx, store, args.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve gate ID: %v", err),
		}
	}

	gate, err := store.GetIssue(ctx, gateID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get gate: %v", err),
		}
	}
	if gate == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gate %s not found", gateID),
		}
	}
	if gate.IssueType != "gate" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("%s is not a gate (type: %s)", gateID, gate.IssueType),
		}
	}

	return jsonOK(gate)
}

func (s *Server) handleGateClose(req *Request) Response {
	var args GateCloseArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate close args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Resolve partial ID
	gateID, err := utils.ResolvePartialID(ctx, store, args.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve gate ID: %v", err),
		}
	}

	// Verify it's a gate
	gate, err := store.GetIssue(ctx, gateID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get gate: %v", err),
		}
	}
	if gate == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gate %s not found", gateID),
		}
	}
	if gate.IssueType != "gate" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("%s is not a gate (type: %s)", gateID, gate.IssueType),
		}
	}

	reason := args.Reason
	if reason == "" {
		reason = "Gate closed"
	}

	oldStatus := string(gate.Status)

	if err := store.CloseIssue(ctx, gateID, reason, s.reqActor(req), ""); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to close gate: %v", err),
		}
	}

	// Emit rich status change event
	evt := MutationEvent{
		Type:      MutationStatus,
		IssueID:   gateID,
		OldStatus: oldStatus,
		NewStatus: "closed",
	}
	enrichEvent(&evt, gate)
	s.emitRichMutation(evt)

	// Gate → inbox bridge (bd-xtahx.6): push notification to waiters and assignee.
	s.pushGateClosedToInbox(ctx, gate, reason)

	closedGate, _ := store.GetIssue(ctx, gateID)
	return jsonOK(closedGate)
}

func (s *Server) handleGateWait(req *Request) Response {
	var args GateWaitArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate wait args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Resolve partial ID
	gateID, err := utils.ResolvePartialID(ctx, store, args.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve gate ID: %v", err),
		}
	}

	// Get existing gate
	gate, err := store.GetIssue(ctx, gateID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get gate: %v", err),
		}
	}
	if gate == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gate %s not found", gateID),
		}
	}
	if gate.IssueType != "gate" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("%s is not a gate (type: %s)", gateID, gate.IssueType),
		}
	}
	if gate.Status == types.StatusClosed {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gate %s is already closed", gateID),
		}
	}

	// Add new waiters (avoiding duplicates)
	waiterSet := make(map[string]bool)
	for _, w := range gate.Waiters {
		waiterSet[w] = true
	}
	newWaiters := []string{}
	for _, addr := range args.Waiters {
		if !waiterSet[addr] {
			newWaiters = append(newWaiters, addr)
			waiterSet[addr] = true
		}
	}

	addedCount := len(newWaiters)

	if addedCount > 0 {
		allWaiters := append(gate.Waiters, newWaiters...)
		waitersJSON, _ := json.Marshal(allWaiters)

		// Use raw SQL to update the waiters field via UnderlyingDB
		_, err = store.UnderlyingDB().ExecContext(ctx, `UPDATE issues SET waiters = ?, updated_at = ? WHERE id = ?`,
			string(waitersJSON), time.Now(), gateID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add waiters: %v", err),
			}
		}

		// Emit mutation event
		s.emitMutationFor(MutationUpdate, gate)
	}

	return jsonOK(GateWaitResult{AddedCount: addedCount})
}
