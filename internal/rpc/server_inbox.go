package rpc

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/beads/internal/types"
)

func (s *Server) handleInboxPush(req *Request) Response {
	var args InboxPushArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid inbox push args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	if args.AgentName == "" {
		return Response{
			Success: false,
			Error:   "agent_name is required",
		}
	}
	if args.Type == "" {
		return Response{
			Success: false,
			Error:   "type is required",
		}
	}
	if args.Content == "" {
		return Response{
			Success: false,
			Error:   "content is required",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	now := time.Now().UTC()

	// Generate ID and dedup key if not provided
	id := uuid.New().String()
	dedupKey := args.DedupKey
	if dedupKey == "" {
		dedupKey = fmt.Sprintf("%s:%s:%d", args.Type, args.Source, now.UnixMilli())
	}

	// Set default priority
	priority := args.Priority
	if priority == 0 && args.Type != "alert" {
		priority = 2 // normal
	}

	// Parse TTL into expiry
	var expiresAt *time.Time
	if args.TTL != "" {
		d, err := time.ParseDuration(args.TTL)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid TTL %q: %v", args.TTL, err),
			}
		}
		t := now.Add(d)
		expiresAt = &t
	}

	item := &types.InboxItem{
		ID:        id,
		AgentName: args.AgentName,
		Rig:       args.Rig,
		Type:      args.Type,
		Source:    args.Source,
		Content:   args.Content,
		Priority:  priority,
		CreatedAt: now,
		ExpiresAt: expiresAt,
		DedupKey:  dedupKey,
	}

	if err := store.InboxPush(ctx, item); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("inbox push failed: %v", err),
		}
	}

	// Publish to JetStream INBOX_EVENTS stream for real-time delivery (bd-xtahx.3).
	// Coop's durable consumer subscribes to these subjects.
	data, _ := json.Marshal(item)
	if s.bus != nil && s.bus.JetStreamEnabled() {
		subject := "inbox.agent." + args.AgentName
		if args.AgentName == "all" {
			subject = "inbox.all"
		}
		s.bus.PublishRaw(subject, data)
	}

	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleInboxList(req *Request) Response {
	var args InboxListArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid inbox list args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	if args.AgentName == "" {
		return Response{
			Success: false,
			Error:   "agent_name is required",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	items, err := store.InboxList(ctx, args.AgentName, args.IncludeDelivered)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("inbox list failed: %v", err),
		}
	}

	if items == nil {
		items = []*types.InboxItem{}
	}

	resp := InboxListResponse{
		Items: items,
		Count: len(items),
	}
	data, _ := json.Marshal(resp)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleInboxDrain(req *Request) Response {
	var args InboxDrainArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid inbox drain args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	if args.AgentName == "" {
		return Response{
			Success: false,
			Error:   "agent_name is required",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	var items []*types.InboxItem
	var err error
	if args.MaxPriority > 0 {
		items, err = store.InboxDrain(ctx, args.AgentName, args.MaxPriority)
	} else {
		items, err = store.InboxDrain(ctx, args.AgentName)
	}
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("inbox drain failed: %v", err),
		}
	}

	if items == nil {
		items = []*types.InboxItem{}
	}

	resp := InboxListResponse{
		Items: items,
		Count: len(items),
	}
	data, _ := json.Marshal(resp)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleInboxMarkDelivered(req *Request) Response {
	var args InboxMarkDeliveredArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid inbox mark delivered args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	if len(args.IDs) == 0 {
		return Response{
			Success: true,
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	agentName := args.AgentName
	if agentName == "" {
		agentName = s.reqActor(req)
	}

	if err := store.InboxMarkDelivered(ctx, agentName, args.IDs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("inbox mark delivered failed: %v", err),
		}
	}

	return Response{
		Success: true,
	}
}
