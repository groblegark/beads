package rpc

import (
	"encoding/json"
	"time"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/gate"
)

// handleSessionGateMark marks a session gate as satisfied via the NATS KV backend. (bd-rabpy)
func (s *Server) handleSessionGateMark(req *Request) Response {
	if s.gateBackend == nil {
		return Response{Success: false, Error: "session gate backend not configured"}
	}

	var args SessionGateMarkArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: "invalid args: " + err.Error()}
	}
	if args.Agent == "" {
		return Response{Success: false, Error: "agent is required"}
	}
	if args.GateID == "" {
		return Response{Success: false, Error: "gate_id is required"}
	}

	ttl := time.Duration(args.TTLSecs) * time.Second
	if ttl == 0 {
		ttl = gate.DefaultTTLFor(args.GateID)
	}

	opts := gate.MarkOpts{
		Mechanism: args.Mechanism,
		Actor:     req.Actor,
		SessionID: args.SessionID,
		TTL:       ttl,
	}

	if err := s.gateBackend.Mark(args.Agent, args.GateID, opts); err != nil {
		return Response{Success: false, Error: "mark gate: " + err.Error()}
	}

	// Emit gate.satisfied event (bd-cvu4c).
	s.emitGateEvent(eventbus.EventGateSatisfied, eventbus.GateEventPayload{
		GateID:    args.GateID,
		Agent:     args.Agent,
		Mechanism: args.Mechanism,
		Actor:     req.Actor,
		SessionID: args.SessionID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, req.Actor)

	return Response{Success: true}
}

// handleSessionGateClear clears a session gate. (bd-rabpy)
func (s *Server) handleSessionGateClear(req *Request) Response {
	if s.gateBackend == nil {
		return Response{Success: false, Error: "session gate backend not configured"}
	}

	var args SessionGateClearArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: "invalid args: " + err.Error()}
	}

	if args.AllAgents && args.GateID != "" {
		// Clear a specific gate across all agents.
		if err := s.gateBackend.ClearGateAllAgents(args.GateID); err != nil {
			return Response{Success: false, Error: "clear gate all agents: " + err.Error()}
		}
		// Emit gate.cleared for broadcast clear (bd-cvu4c).
		s.emitGateEvent(eventbus.EventGateCleared, eventbus.GateEventPayload{
			GateID:    args.GateID,
			Agent:     "*",
			Mechanism: "clear_all_agents",
			Actor:     req.Actor,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}, req.Actor)
		return Response{Success: true}
	}

	if args.Agent == "" {
		return Response{Success: false, Error: "agent is required (or use all_agents=true)"}
	}

	if args.GateID == "" {
		// Clear all gates for this agent.
		if err := s.gateBackend.ClearAll(args.Agent); err != nil {
			return Response{Success: false, Error: "clear all gates: " + err.Error()}
		}
		// Emit gate.cleared for clear-all (bd-cvu4c).
		s.emitGateEvent(eventbus.EventGateCleared, eventbus.GateEventPayload{
			GateID:    "*",
			Agent:     args.Agent,
			Mechanism: "clear_all",
			Actor:     req.Actor,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}, req.Actor)
	} else {
		// Clear a specific gate for this agent.
		if err := s.gateBackend.Clear(args.Agent, args.GateID); err != nil {
			return Response{Success: false, Error: "clear gate: " + err.Error()}
		}
		// Emit gate.cleared event (bd-cvu4c).
		s.emitGateEvent(eventbus.EventGateCleared, eventbus.GateEventPayload{
			GateID:    args.GateID,
			Agent:     args.Agent,
			Mechanism: "clear",
			Actor:     req.Actor,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}, req.Actor)
	}

	return Response{Success: true}
}

// handleSessionGateCheck checks if a session gate is satisfied. (bd-rabpy)
func (s *Server) handleSessionGateCheck(req *Request) Response {
	if s.gateBackend == nil {
		return Response{Success: false, Error: "session gate backend not configured"}
	}

	var args SessionGateCheckArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: "invalid args: " + err.Error()}
	}
	if args.Agent == "" || args.GateID == "" {
		return Response{Success: false, Error: "agent and gate_id are required"}
	}

	entry := s.gateBackend.Get(args.Agent, args.GateID)
	result := SessionGateCheckResult{}
	if entry != nil {
		result.Satisfied = entry.Satisfied
		result.Mechanism = entry.Mechanism
		result.Actor = entry.Actor
		result.Timestamp = entry.Timestamp
		result.TTL = entry.TTL
	}

	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleSessionGateList lists all session gates for an agent. (bd-rabpy)
func (s *Server) handleSessionGateList(req *Request) Response {
	if s.gateBackend == nil {
		return Response{Success: false, Error: "session gate backend not configured"}
	}

	var args SessionGateListArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: "invalid args: " + err.Error()}
	}
	if args.Agent == "" {
		return Response{Success: false, Error: "agent is required"}
	}

	// Check all known gate IDs for this agent.
	knownGates := []string{
		"decision", "decision-waiting", "commit-push", "bead-update",
		"destructive-op", "sandbox-boundary", "state-checkpoint",
		"dirty-work", "mol-gate-pending", "context-injection", "stale-context",
	}

	var entries []SessionGateEntry
	for _, gateID := range knownGates {
		entry := s.gateBackend.Get(args.Agent, gateID)
		if entry != nil {
			entries = append(entries, SessionGateEntry{
				GateID:    gateID,
				Satisfied: entry.Satisfied,
				Mechanism: entry.Mechanism,
				Timestamp: entry.Timestamp,
				TTL:       entry.TTL,
			})
		}
	}

	result := SessionGateListResult{Gates: entries}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}
