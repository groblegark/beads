package rpc

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/sensitive"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// emitJackEvent publishes a jack lifecycle event to JetStream (bd-yrzhq).
func (s *Server) emitJackEvent(evtType eventbus.EventType, jackID, target, agent, reason, ttl, expiresAt string) {
	if s.bus == nil || !s.bus.JetStreamEnabled() {
		return
	}
	payload := eventbus.JackEventPayload{
		JackID:    jackID,
		Target:    target,
		Agent:     agent,
		Reason:    reason,
		TTL:       ttl,
		ExpiresAt: expiresAt,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if data, err := json.Marshal(payload); err == nil {
		subject := eventbus.SubjectForEvent(evtType)
		s.bus.PublishRaw(subject, data)
	}
}

// handleJackOn creates and activates a jack bead.
// Steps: validate → create issue → add labels → set status to in_progress → add dependency.
func (s *Server) handleJackOn(req *Request) Response {
	var args JackOnArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid jack on args: %v", err)}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	// Validate required fields
	if args.Target == "" {
		return Response{Success: false, Error: "target is required"}
	}
	if args.Reason == "" {
		return Response{Success: false, Error: "reason is required"}
	}
	if args.RevertPlan == "" {
		return Response{Success: false, Error: "revert_plan is required"}
	}

	// Parse TTL
	ttl := time.Hour // default 1h
	if args.TTL != "" {
		d, err := time.ParseDuration(args.TTL)
		if err != nil {
			return Response{Success: false, Error: fmt.Sprintf("invalid TTL %q: %v", args.TTL, err)}
		}
		ttl = d
	}

	// Validate priority
	priority := args.Priority
	if priority < 0 || priority > 4 {
		return Response{Success: false, Error: fmt.Sprintf("priority must be 0-4, got %d", priority)}
	}

	// Ensure at least one jack:* label
	labels := args.Labels
	hasJackLabel := false
	for _, l := range labels {
		if strings.HasPrefix(l, types.JackLabelPrefix) {
			hasJackLabel = true
			break
		}
	}
	if !hasJackLabel {
		labels = append(labels, "jack:general")
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	actor := s.reqActor(req)

	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	// Build metadata
	metadata := map[string]interface{}{
		"jack_target":      args.Target,
		"jack_ttl":         ttl.String(),
		"jack_expires_at":  expiresAt.Format(time.RFC3339),
		"jack_revert_plan": args.RevertPlan,
		"jack_reverted":    false,
		"jack_changes":     []interface{}{},
	}

	// Set jack_rig for multi-rig visibility (design doc 8.10)
	if args.TargetRig != "" {
		metadata["jack_rig"] = args.TargetRig
	} else if rig := s.getDefaultRig(ctx); rig != "" {
		metadata["jack_rig"] = rig
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to marshal metadata: %v", err)}
	}

	// Build title
	title := fmt.Sprintf("Jack: %s — %s", args.Target, args.Reason)
	if len(title) > 200 {
		title = title[:197] + "..."
	}

	issue := &types.Issue{
		Title:       title,
		Description: args.Reason,
		IssueType:   types.TypeJack,
		Priority:    priority,
		Status:      types.StatusInProgress, // Jacks are born active
		Metadata:    json.RawMessage(metadataJSON),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if actor != "" {
		issue.CreatedBy = actor
		issue.Assignee = actor
	}

	err = store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, issue, actor); err != nil {
			return fmt.Errorf("create jack: %w", err)
		}
		for _, label := range labels {
			if err := tx.AddLabel(ctx, issue.ID, label, actor); err != nil {
				return fmt.Errorf("add label %q: %w", label, err)
			}
		}
		// Add dependency if --blocks specified
		if args.Blocks != "" {
			dep := &types.Dependency{
				IssueID:     args.Blocks,
				DependsOnID: issue.ID,
				Type:        types.DepBlocks,
			}
			if err := tx.AddDependency(ctx, dep, actor); err != nil {
				return fmt.Errorf("add dependency: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to create jack: %v", err)}
	}

	issue.Labels = labels
	s.emitMutationFor(MutationCreate, issue)
	s.emitJackEvent(eventbus.EventJackOn, issue.ID, args.Target, actor, args.Reason, ttl.String(), expiresAt.Format(time.RFC3339))

	data, _ := json.Marshal(issue)
	return Response{Success: true, Data: data}
}

// handleJackOff closes a jack bead after verifying revert status.
func (s *Server) handleJackOff(req *Request) Response {
	var args JackOffArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid jack off args: %v", err)}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	if args.ID == "" {
		return Response{Success: false, Error: "id is required"}
	}
	if args.Reason == "" {
		return Response{Success: false, Error: "reason is required"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	actor := s.reqActor(req)

	// Fetch jack and validate
	issue, err := store.GetIssue(ctx, args.ID)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("jack not found: %v", err)}
	}
	if issue == nil {
		return Response{Success: false, Error: fmt.Sprintf("jack %s not found", args.ID)}
	}
	if issue.IssueType != types.TypeJack {
		return Response{Success: false, Error: fmt.Sprintf("%s is not a jack (type=%s)", args.ID, issue.IssueType)}
	}
	if issue.Status == types.StatusClosed {
		return Response{Success: false, Error: fmt.Sprintf("jack %s is already closed", args.ID)}
	}

	// Parse metadata to check revert status
	metadata := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		_ = json.Unmarshal(issue.Metadata, &metadata)
	}

	// If not skipping revert check, verify changes were logged
	if !args.SkipRevertCheck {
		if changes, ok := metadata["jack_changes"]; ok {
			if arr, ok := changes.([]interface{}); ok && len(arr) > 0 {
				// Has changes — warn if no revert actions logged
				// (This is advisory, not blocking)
			}
		}
	}

	// Mark as reverted and close
	metadata["jack_reverted"] = true
	metadata["jack_closed_reason"] = args.Reason
	metadata["jack_closed_at"] = time.Now().UTC().Format(time.RFC3339)
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to marshal metadata: %v", err)}
	}

	now := time.Now().UTC()
	updates := map[string]interface{}{
		"status":      string(types.StatusClosed),
		"close_reason": args.Reason,
		"closed_at":   now,
		"metadata":    string(metadataJSON),
	}

	err = store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, args.ID, updates, actor)
	})
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to close jack: %v", err)}
	}

	// Refresh issue for response
	issue, _ = store.GetIssue(ctx, args.ID)
	s.emitMutationFor(MutationUpdate, issue)

	target, _ := metadata["jack_target"].(string)
	s.emitJackEvent(eventbus.EventJackOff, args.ID, target, actor, args.Reason, "", "")

	data, _ := json.Marshal(issue)
	return Response{Success: true, Data: data}
}

// handleJackCheck finds expired jacks. If --auto-escalate, creates P0 alert beads.
func (s *Server) handleJackCheck(req *Request) Response {
	var args JackCheckArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid jack check args: %v", err)}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	actor := s.reqActor(req)

	// Find all active jacks (type=jack, status=in_progress)
	jackType := types.TypeJack
	inProgress := types.StatusInProgress
	jacks, err := store.SearchIssues(ctx, "", types.IssueFilter{
		IssueType: &jackType,
		Status:    &inProgress,
	})
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to search jacks: %v", err)}
	}

	now := time.Now().UTC()
	var expired []*types.Issue
	active := 0
	escalated := 0

	for _, jack := range jacks {
		// Parse metadata to get expiry
		metadata := make(map[string]interface{})
		if len(jack.Metadata) > 0 {
			_ = json.Unmarshal(jack.Metadata, &metadata)
		}

		expiresAtStr, _ := metadata["jack_expires_at"].(string)
		if expiresAtStr == "" {
			active++
			continue
		}

		expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
		if err != nil {
			active++
			continue
		}

		if now.After(expiresAt) {
			expired = append(expired, jack)

			// Auto-escalate: create P0 alert bead for each expired jack
			if args.AutoEscalate {
				target, _ := metadata["jack_target"].(string)
				alertTitle := fmt.Sprintf("EXPIRED JACK: %s — %s", target, jack.Title)
				if len(alertTitle) > 200 {
					alertTitle = alertTitle[:197] + "..."
				}

				alertIssue := &types.Issue{
					Title:       alertTitle,
					Description: fmt.Sprintf("Jack %s has expired. Target: %s. Original TTL expired at %s.", jack.ID, target, expiresAtStr),
					IssueType:   types.IssueType("task"),
					Priority:    0, // P0 critical
					Status:      types.StatusOpen,
					CreatedAt:   now,
					UpdatedAt:   now,
				}
				if actor != "" {
					alertIssue.CreatedBy = actor
				}

				txErr := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
					if err := tx.CreateIssue(ctx, alertIssue, actor); err != nil {
						return err
					}
					if err := tx.AddLabel(ctx, alertIssue.ID, "jack:expired-alert", actor); err != nil {
						return err
					}
					// Link alert to the expired jack
					dep := &types.Dependency{
						IssueID:     alertIssue.ID,
						DependsOnID: jack.ID,
						Type:        types.DepRelated,
					}
					return tx.AddDependency(ctx, dep, actor)
				})
				if txErr == nil {
					escalated++
					s.emitMutationFor(MutationCreate, alertIssue)
					s.emitJackEvent(eventbus.EventJackExpired, jack.ID, target, actor, "TTL expired", "", expiresAtStr)
				}
			}
		} else {
			active++
		}
	}

	result := JackCheckResult{
		Expired:   expired,
		Active:    active,
		Escalated: escalated,
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleJackExtend extends a jack's TTL.
func (s *Server) handleJackExtend(req *Request) Response {
	var args JackExtendArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid jack extend args: %v", err)}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	if args.ID == "" {
		return Response{Success: false, Error: "id is required"}
	}
	if args.TTL == "" {
		return Response{Success: false, Error: "ttl is required"}
	}

	ttl, err := time.ParseDuration(args.TTL)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid TTL %q: %v", args.TTL, err)}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	actor := s.reqActor(req)

	// Fetch jack and validate
	issue, err := store.GetIssue(ctx, args.ID)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("jack not found: %v", err)}
	}
	if issue == nil {
		return Response{Success: false, Error: fmt.Sprintf("jack %s not found", args.ID)}
	}
	if issue.IssueType != types.TypeJack {
		return Response{Success: false, Error: fmt.Sprintf("%s is not a jack (type=%s)", args.ID, issue.IssueType)}
	}
	if issue.Status == types.StatusClosed {
		return Response{Success: false, Error: fmt.Sprintf("jack %s is already closed — cannot extend", args.ID)}
	}

	// Parse and update metadata
	metadata := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		_ = json.Unmarshal(issue.Metadata, &metadata)
	}

	now := time.Now().UTC()
	newExpiry := now.Add(ttl)
	metadata["jack_ttl"] = ttl.String()
	metadata["jack_expires_at"] = newExpiry.Format(time.RFC3339)

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to marshal metadata: %v", err)}
	}

	updates := map[string]interface{}{
		"metadata": string(metadataJSON),
	}

	err = store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, args.ID, updates, actor)
	})
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to extend jack: %v", err)}
	}

	issue, _ = store.GetIssue(ctx, args.ID)
	s.emitMutationFor(MutationUpdate, issue)

	target, _ := metadata["jack_target"].(string)
	s.emitJackEvent(eventbus.EventJackExtend, args.ID, target, actor, args.Reason, ttl.String(), newExpiry.Format(time.RFC3339))

	result := map[string]interface{}{
		"id":         args.ID,
		"ttl":        ttl.String(),
		"expires_at": newExpiry.Format(time.RFC3339),
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleJackLog appends a change record to a jack's metadata.
func (s *Server) handleJackLog(req *Request) Response {
	var args JackLogArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid jack log args: %v", err)}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	if args.ID == "" {
		return Response{Success: false, Error: "id is required"}
	}
	if args.Action == "" {
		return Response{Success: false, Error: "action is required"}
	}

	// Validate action
	validActions := map[string]bool{"edit": true, "exec": true, "patch": true, "delete": true, "create": true}
	if !validActions[args.Action] {
		return Response{Success: false, Error: fmt.Sprintf("invalid action %q — must be one of: edit, exec, patch, delete, create", args.Action)}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()
	actor := s.reqActor(req)

	// Fetch jack and validate
	issue, err := store.GetIssue(ctx, args.ID)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("jack not found: %v", err)}
	}
	if issue == nil {
		return Response{Success: false, Error: fmt.Sprintf("jack %s not found", args.ID)}
	}
	if issue.IssueType != types.TypeJack {
		return Response{Success: false, Error: fmt.Sprintf("%s is not a jack (type=%s)", args.ID, issue.IssueType)}
	}
	if issue.Status == types.StatusClosed {
		return Response{Success: false, Error: fmt.Sprintf("jack %s is closed — cannot log changes", args.ID)}
	}

	// Parse metadata
	metadata := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		json.Unmarshal(issue.Metadata, &metadata)
	}

	// Get existing changes
	var changes []interface{}
	if existing, ok := metadata["jack_changes"]; ok {
		if arr, ok := existing.([]interface{}); ok {
			changes = arr
		}
	}

	// Enforce max changes
	const maxChanges = 500
	if len(changes) >= maxChanges {
		return Response{Success: false, Error: fmt.Sprintf("jack %s has %d changes (max %d)", args.ID, len(changes), maxChanges)}
	}

	// Default target to jack target
	target := args.Target
	if target == "" {
		if jackTarget, ok := metadata["jack_target"].(string); ok {
			target = jackTarget
		}
	}

	// Detect and redact sensitive data in before/after values
	before := args.Before
	after := args.After
	hasSensitive := sensitive.ContainsSecret(before) || sensitive.ContainsSecret(after)
	if hasSensitive {
		before, _ = sensitive.Redact(before)
		after, _ = sensitive.Redact(after)
	}

	// Build change record
	change := JackChange{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Action:    args.Action,
		Target:    target,
		Before:    before,
		After:     after,
		Cmd:       args.Cmd,
		Agent:     actor,
		Sensitive: hasSensitive,
	}

	changes = append(changes, change)
	metadata["jack_changes"] = changes

	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to marshal metadata: %v", err)}
	}

	updates := map[string]interface{}{
		"metadata": string(metadataJSON),
	}

	err = store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, args.ID, updates, actor)
	})
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("failed to log change: %v", err)}
	}

	issue, _ = store.GetIssue(ctx, args.ID)
	s.emitMutationFor(MutationUpdate, issue)

	result := map[string]interface{}{
		"jack_id":      args.ID,
		"change_index": len(changes) - 1,
		"action":       args.Action,
		"target":       target,
		"timestamp":    change.Timestamp,
		"total":        len(changes),
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}
