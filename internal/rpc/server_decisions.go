package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/decision"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/gate"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func (s *Server) handleDecisionCreate(req *Request) Response {
	var args DecisionCreateArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid decision create args: %v", err),
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
	actor := s.reqActor(req)

	// Auto-populate RequestedBy from the request actor when not explicitly set.
	// This ensures every decision has agent attribution for Slack display,
	// inbox routing, and nudge-on-resolve targeting. (bd-3bols)
	if args.RequestedBy == "" && actor != "" && actor != "daemon" {
		args.RequestedBy = actor
	}

	// Dedup guard: auto-supersede pending decisions from the same agent. (bd-ni0br)
	// When an agent creates a new checkpoint decision while an old one is still
	// pending, cancel the old one to prevent decision storms (4-8 stale decisions
	// piling up faster than humans can respond).
	// Configurable via decision.settings.dedup-window (default 5m, 0 to disable).
	if args.RequestedBy != "" && config.GetDecisionDedupWindow() > 0 {
		if pending, err := store.ListPendingDecisions(ctx); err == nil {
			now := time.Now()
			for _, old := range pending {
				if old.RequestedBy != args.RequestedBy {
					continue
				}
				// Auto-supersede: cancel the stale decision.
				old.RespondedAt = &now
				old.RespondedBy = "system:superseded"
				old.SelectedOption = "_superseded"
				old.ResponseText = fmt.Sprintf("Superseded by new decision from %s", args.RequestedBy)
				if err := store.UpdateDecisionPoint(ctx, old); err != nil {
					fmt.Fprintf(os.Stderr, "decision-dedup: update %s: %v\n", old.IssueID, err)
					continue
				}
				if err := store.CloseIssue(ctx, old.IssueID,
					fmt.Sprintf("Superseded by new decision from %s", args.RequestedBy),
					"system:superseded", ""); err != nil {
					fmt.Fprintf(os.Stderr, "decision-dedup: close %s: %v\n", old.IssueID, err)
				}
				s.emitDecisionEvent(eventbus.EventDecisionExpired, eventbus.DecisionEventPayload{
					DecisionID:  old.IssueID,
					Question:    old.Prompt,
					Urgency:     old.Urgency,
					RequestedBy: old.RequestedBy,
				})
				s.emitMutation(MutationUpdate, old.IssueID, "", "")
				fmt.Fprintf(os.Stderr, "decision-dedup: superseded %s from %s (age %s)\n",
					old.IssueID, old.RequestedBy, now.Sub(old.CreatedAt).Round(time.Second))
			}
		}
	}

	var issue *types.Issue

	// Validate urgency if provided.
	urgency := strings.ToLower(args.Urgency)
	if urgency == "" {
		urgency = "medium"
	}
	switch urgency {
	case "high", "medium", "low":
		// Valid
	default:
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid urgency '%s': must be high, medium, or low", args.Urgency),
		}
	}

	// Convert options to JSON
	optionsJSON, err := json.Marshal(args.Options)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal options: %v", err),
		}
	}

	// Set defaults
	maxIterations := args.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 3
	}

	now := time.Now()
	dp := &types.DecisionPoint{
		Prompt:        args.Prompt,
		Context:       args.Context,
		Options:       string(optionsJSON),
		DefaultOption: args.DefaultOption,
		MaxIterations: maxIterations,
		Iteration:     1,
		RequestedBy:   args.RequestedBy,
		Urgency:       urgency,
		PriorID:       args.Predecessor,
		ParentBeadID:  args.Parent,
		CreatedAt:     now,
	}

	// Use a single transaction for gate issue + decision point creation to ensure
	// atomicity. Previously these were separate transactions, so under Dolt overload
	// the gate issue could be created but the decision point lost
	// (beads-epc-fix_dolt_overload_export_hash_scan).
	if err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		if args.IssueID != "" {
			// Validate existing issue
			existingIssue, err := tx.GetIssue(ctx, args.IssueID)
			if err != nil {
				return fmt.Errorf("failed to get issue: %w", err)
			}
			if existingIssue == nil {
				return fmt.Errorf("issue %s not found", args.IssueID)
			}
			issue = existingIssue
		} else {
			// Create a new gate issue for the decision (gt-w3u2o9)
			gateIssue := &types.Issue{
				Title:       fmt.Sprintf("[DECISION] %s", args.Prompt),
				Description: fmt.Sprintf("Decision ID: pending\nQuestion: %s", args.Prompt),
				Status:      "open",
				Priority:    2,
				IssueType:   "gate",
				AwaitType:   "decision",
				CreatedBy:   actor,
				Labels:      []string{"gt:decision", "decision:pending", "urgency:" + urgency},
			}
			if err := tx.CreateIssue(ctx, gateIssue, actor); err != nil {
				return fmt.Errorf("failed to create gate issue: %w", err)
			}
			issue = gateIssue
			args.IssueID = gateIssue.ID
		}

		dp.IssueID = args.IssueID

		if err := tx.CreateDecisionPoint(ctx, dp); err != nil {
			return fmt.Errorf("failed to create decision point: %w", err)
		}

		// Add parent-child dependency if parent specified.
		if args.Parent != "" {
			dep := &types.Dependency{
				IssueID:     args.IssueID,
				DependsOnID: args.Parent,
				Type:        types.DepParentChild,
				CreatedAt:   now,
			}
			if err := tx.AddDependency(ctx, dep, actor); err != nil {
				// Non-fatal within transaction: log but don't fail the whole create.
				fmt.Fprintf(os.Stderr, "warning: failed to add parent dependency: %v\n", err)
			}
		}

		// Add blocks dependency if specified.
		if args.Blocks != "" {
			dep := &types.Dependency{
				IssueID:     args.Blocks,
				DependsOnID: args.IssueID,
				Type:        types.DepBlocks,
				CreatedAt:   now,
			}
			if err := tx.AddDependency(ctx, dep, actor); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to add blocks dependency: %v\n", err)
			}
		}

		return nil
	}); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("decision create: %v", err),
		}
	}

	// Emit mutation event so the daemon event-driven sync picks it up.
	s.emitMutationForActor(MutationCreate, issue, actor)

	// Mark the decision gate in the NATS backend so the in-process gate check
	// (handleInProcess) sees the satisfaction. Without this, the CLI writes a
	// file marker that the daemon's NATS-only check path never sees — causing
	// false-positive Stop hook blocks for already-resolved decisions. (bd-1x0qp)
	//
	// Use args.RequestedBy (the agent creating the decision) rather than the
	// RPC actor — the Stop hook fires under the requesting agent's name, so
	// the gate must be keyed on that agent's base name.
	if s.gateBackend != nil && args.RequestedBy != "" {
		agentName := eventbus.AgentBaseName(args.RequestedBy)
		if agentName != "" {
			_ = s.gateBackend.Mark(agentName, "decision", gate.MarkOpts{
				Mechanism: "decision_create",
				Actor:     agentName,
				TTL:       gate.DefaultTTLFor("decision"),
			})
		}
	}

	// Emit decision event to NATS JetStream so the Slack bot (and other
	// consumers) are notified immediately.  Previously the CLI client was
	// responsible for sending BusEmit after DecisionCreate, but that is
	// fragile and doesn't work for decisions created via HTTP API.
	s.emitDecisionEvent(eventbus.EventDecisionCreated, eventbus.DecisionEventPayload{
		DecisionID:  args.IssueID,
		Question:    args.Prompt,
		Urgency:     urgency,
		RequestedBy: args.RequestedBy,
		Options:     len(args.Options),
	}, actor)

	// Emit OjAgentEscalated when a decision is created by an agent (bd-2iae)
	if args.RequestedBy != "" {
		s.emitOjEvent(eventbus.EventOjAgentEscalated, eventbus.OjAgentEventPayload{
			AgentName: args.RequestedBy,
			BeadID:    args.Parent,
			Reason:    args.Prompt,
		}, s.reqActor(req))
	}

	// Return the decision with its associated issue
	resp := DecisionResponse{
		Decision: dp,
		Issue:    issue,
	}

	return jsonOK(resp)
}

func (s *Server) handleDecisionGet(req *Request) Response {
	var args DecisionGetArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid decision get args: %v", err),
		}
	}

	fmt.Fprintf(os.Stderr, "handleDecisionGet: issue_id=%s actor=%q cwd=%q\n", args.IssueID, req.Actor, req.Cwd)

	store := s.storage
	if store == nil {
		fmt.Fprintf(os.Stderr, "handleDecisionGet: storage is nil!\n")
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	dp, err := store.GetDecisionPoint(ctx, args.IssueID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "handleDecisionGet: GetDecisionPoint(%s) error: %v\n", args.IssueID, err)
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get decision point: %v", err),
		}
	}
	if dp == nil {
		fmt.Fprintf(os.Stderr, "handleDecisionGet: GetDecisionPoint(%s) returned nil (no rows)\n", args.IssueID)
		return Response{
			Success: false,
			Error:   fmt.Sprintf("no decision point for issue %s", args.IssueID),
		}
	}
	fmt.Fprintf(os.Stderr, "handleDecisionGet: found issue_id=%s prompt=%q\n", args.IssueID, dp.Prompt)

	// Get associated issue
	issue, _ := store.GetIssue(ctx, args.IssueID)

	resp := DecisionResponse{
		Decision: dp,
		Issue:    issue,
	}

	return jsonOK(resp)
}

func (s *Server) handleDecisionResolve(req *Request) Response {
	var args DecisionResolveArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid decision resolve args: %v", err),
		}
	}

	fmt.Fprintf(os.Stderr, "handleDecisionResolve: issue_id=%s option=%q by=%q actor=%q\n",
		args.IssueID, args.SelectedOption, args.RespondedBy, req.Actor)

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	// Get existing decision point
	dp, err := store.GetDecisionPoint(ctx, args.IssueID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "handleDecisionResolve: GetDecisionPoint(%s) error: %v\n", args.IssueID, err)
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get decision point: %v", err),
		}
	}
	if dp == nil {
		fmt.Fprintf(os.Stderr, "handleDecisionResolve: GetDecisionPoint(%s) returned nil\n", args.IssueID)
		return Response{
			Success: false,
			Error:   fmt.Sprintf("no decision point for issue %s", args.IssueID),
		}
	}

	// Update the decision point
	now := time.Now()
	dp.SelectedOption = args.SelectedOption
	dp.ResponseText = args.ResponseText
	dp.RespondedBy = args.RespondedBy
	dp.RespondedAt = &now
	dp.Guidance = args.Guidance

	if err := store.UpdateDecisionPoint(ctx, dp); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to update decision point: %v", err),
		}
	}

	// Get associated issue (before emit so we can enrich event)
	issue, _ := store.GetIssue(ctx, args.IssueID)

	// Emit mutation + decision event for resolve.
	// Resolve option ID to human-readable label for the event payload,
	// and include response text as rationale so bd yield delivers it. (bd-kpudsl)
	chosenLabel := args.SelectedOption
	if dp.Options != "" && args.SelectedOption != "" {
		var opts []types.DecisionOption
		if err := json.Unmarshal([]byte(dp.Options), &opts); err == nil {
			for _, o := range opts {
				if o.ID == args.SelectedOption {
					chosenLabel = o.Label
					break
				}
			}
		}
	}
	rationale := args.Guidance
	if rationale == "" {
		rationale = args.ResponseText
	}
	s.emitMutationForActor(MutationUpdate, issue, req.Actor)
	s.emitDecisionEvent(eventbus.EventDecisionResponded, eventbus.DecisionEventPayload{
		DecisionID:  args.IssueID,
		RequestedBy: dp.RequestedBy,
		ChosenLabel: chosenLabel,
		ResolvedBy:  args.RespondedBy,
		Rationale:   rationale,
	}, req.Actor)

	// Push decision response to requesting agent's inbox (bd-eo9xt).
	// This enables push-based delivery of decision responses instead of
	// relying solely on polling or NATS event subscription.
	if dp.RequestedBy != "" {
		s.pushDecisionResponseToInbox(ctx, dp, args)
	}

	// Re-mark the decision gate with a fresh TTL so the agent has time to
	// process the response without the Stop hook re-blocking. The original
	// gate (marked on decision create) may have expired during the wait for
	// human response, causing the Stop hook to re-fire immediately after
	// bd yield returns — creating an infinite checkpoint loop. (hq-g3v7qs.3)
	if s.gateBackend != nil && dp.RequestedBy != "" {
		agentName := eventbus.AgentBaseName(dp.RequestedBy)
		if agentName != "" {
			_ = s.gateBackend.Mark(agentName, "decision", gate.MarkOpts{
				Mechanism: "decision_respond",
				Actor:     agentName,
				TTL:       gate.DefaultTTLFor("decision"),
			})
		}
	}

	// Auto-assign bead when selected option has a bead_id. (bd-isufm)
	// This avoids the manual "bd update <id> --status=in_progress" step
	// after a checkpoint decision selects work for the agent.
	if args.SelectedOption != "" && dp.RequestedBy != "" {
		s.autoAssignBeadFromDecision(ctx, store, dp, args.SelectedOption)
	}

	resp := DecisionResponse{
		Decision: dp,
		Issue:    issue,
	}

	// Iterative refinement (bd-u4r9a): when guidance is provided without a
	// selected option, create a new decision point iteration server-side.
	shouldIterate := args.Guidance != "" && args.SelectedOption == ""
	if shouldIterate && issue != nil {
		iterResult, err := decision.CreateNextIteration(
			ctx, store, dp, issue, args.Guidance, args.RespondedBy, req.Actor,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "handleDecisionResolve: iteration error: %v\n", err)
			// Non-fatal: the resolve itself succeeded, iteration is best-effort
		} else if iterResult != nil {
			resp.IterationMaxHit = iterResult.MaxReached
			if !iterResult.MaxReached && iterResult.DecisionPoint != nil {
				resp.IterationCreated = true
				resp.NewDecision = iterResult.DecisionPoint
				resp.NewIssue = iterResult.Issue
			}
		}
	}

	return jsonOK(resp)
}

// pushDecisionResponseToInbox pushes a formatted decision response to the
// requesting agent's inbox. Best-effort: errors are logged but don't fail
// the resolve operation. (bd-eo9xt: captain→agent comms via inbox)
func (s *Server) pushDecisionResponseToInbox(ctx context.Context, dp *types.DecisionPoint, args DecisionResolveArgs) {
	store := s.storage
	if store == nil {
		return
	}

	// Format the response content for the agent.
	// Resolve option ID to label so agents see human-readable text. (bd-kpudsl)
	label := args.SelectedOption
	if dp.Options != "" && args.SelectedOption != "" {
		var opts []types.DecisionOption
		if err := json.Unmarshal([]byte(dp.Options), &opts); err == nil {
			for _, o := range opts {
				if o.ID == args.SelectedOption {
					label = o.Label
					break
				}
			}
		}
	}
	var content string
	if args.ResponseText != "" {
		content = fmt.Sprintf("Decision %s resolved: %s — %s",
			args.IssueID, label, args.ResponseText)
	} else {
		content = fmt.Sprintf("Decision %s resolved: %s",
			args.IssueID, label)
	}
	if args.RespondedBy != "" {
		content += fmt.Sprintf("\nResponded by: %s", args.RespondedBy)
	}

	now := time.Now().UTC()
	item := &types.InboxItem{
		ID:        fmt.Sprintf("decision-response-%s-%d", args.IssueID, now.UnixMilli()),
		AgentName: dp.RequestedBy,
		Type:      "decision",
		Source:    fmt.Sprintf("decision:%s", args.IssueID),
		Content:   content,
		Priority:  1, // high priority — agent is waiting for this
		CreatedAt: now,
		DedupKey:  fmt.Sprintf("decision:%s", args.IssueID),
	}

	if err := store.InboxPush(ctx, item); err != nil {
		fmt.Fprintf(os.Stderr, "handleDecisionResolve: inbox push failed (non-fatal): %v\n", err)
		return
	}

	// Also publish to JetStream for real-time delivery
	data, _ := json.Marshal(item)
	if s.bus != nil && s.bus.JetStreamEnabled() {
		subject := "inbox.agent." + dp.RequestedBy
		s.bus.PublishRaw(subject, data)
	}
}

// pushGateClosedToInbox pushes gate-closed notifications to waiters and assignee.
// Best-effort: errors are logged but don't fail the close operation. (bd-xtahx.6)
func (s *Server) pushGateClosedToInbox(ctx context.Context, gate *types.Issue, reason string) {
	store := s.storage
	if store == nil {
		return
	}

	content := fmt.Sprintf("Gate %s closed: %s", gate.ID, reason)
	if gate.Title != "" {
		content = fmt.Sprintf("Gate %s (%s) closed: %s", gate.ID, gate.Title, reason)
	}

	// Collect unique recipients: assignee + waiters
	recipients := make(map[string]bool)
	if gate.Assignee != "" {
		recipients[gate.Assignee] = true
	}
	for _, waiter := range gate.Waiters {
		if waiter != "" {
			recipients[waiter] = true
		}
	}

	now := time.Now().UTC()
	for recipient := range recipients {
		item := &types.InboxItem{
			ID:        fmt.Sprintf("gate-closed-%s-%s-%d", gate.ID, recipient, now.UnixMilli()),
			AgentName: recipient,
			Type:      "gate",
			Source:    fmt.Sprintf("gate:%s", gate.ID),
			Content:   content,
			Priority:  2, // normal
			CreatedAt: now,
			DedupKey:  fmt.Sprintf("gate:%s:%s", gate.ID, recipient),
		}

		if err := store.InboxPush(ctx, item); err != nil {
			fmt.Fprintf(os.Stderr, "handleGateClose: inbox push to %s failed (non-fatal): %v\n", recipient, err)
			continue
		}

		// Publish to JetStream for real-time delivery
		data, _ := json.Marshal(item)
		if s.bus != nil && s.bus.JetStreamEnabled() {
			subject := "inbox.agent." + recipient
			s.bus.PublishRaw(subject, data)
		}
	}
}

// autoAssignBeadFromDecision checks if the selected option has a bead_id field
// and, if so, assigns that bead to the requesting agent. Best-effort: errors
// are logged but don't fail the decision resolve. (bd-isufm)
//
// Fallback: when bead_id is not set, extracts the first bead-like ID from the
// option's label or ID field. This handles checkpoint decisions where agents
// include bead references in labels (e.g., "Start bd-rnjc6: ...") but don't
// set the structured bead_id field. (bd-rceeb)
func (s *Server) autoAssignBeadFromDecision(ctx context.Context, store storage.Storage, dp *types.DecisionPoint, selectedOptionID string) {
	// Parse options from the decision point
	var options []types.DecisionOption
	if err := json.Unmarshal([]byte(dp.Options), &options); err != nil {
		return // Options are unparseable — nothing to do
	}

	// Find the selected option
	var beadID string
	for _, opt := range options {
		if opt.ID == selectedOptionID {
			if opt.BeadID != "" {
				beadID = opt.BeadID
			} else {
				// Fallback: extract bead ID from label text (bd-rceeb)
				beadID = extractBeadIDFromText(opt.Label)
			}
			break
		}
	}
	if beadID == "" {
		return // No bead_id on this option
	}

	// Look up the referenced bead
	bead, err := store.GetIssue(ctx, beadID)
	if err != nil || bead == nil {
		fmt.Fprintf(os.Stderr, "decision-auto-assign: bead %s not found: %v\n", beadID, err)
		return
	}

	// Skip if already closed
	if bead.Status == types.StatusClosed {
		fmt.Fprintf(os.Stderr, "decision-auto-assign: bead %s already closed, skipping\n", beadID)
		return
	}

	// Skip if already assigned to a different agent
	if bead.Assignee != "" && bead.Assignee != dp.RequestedBy {
		fmt.Fprintf(os.Stderr, "decision-auto-assign: bead %s already assigned to %s (not %s), skipping\n",
			beadID, bead.Assignee, dp.RequestedBy)
		return
	}

	// Prevent over-claiming: skip if requesting agent already has in_progress work. (bd-z32l8)
	if dp.RequestedBy != "" {
		inProgressStatus := types.StatusInProgress
		existing, searchErr := store.SearchIssues(ctx, "", types.IssueFilter{
			Status:   &inProgressStatus,
			Assignee: &dp.RequestedBy,
		})
		if searchErr == nil {
			for _, ex := range existing {
				if ex.ID != beadID {
					fmt.Fprintf(os.Stderr, "decision-auto-assign: agent %s already has %s in_progress, skipping auto-assign of %s\n",
						dp.RequestedBy, ex.ID, beadID)
					return
				}
			}
		}
	}

	// Build update map
	updates := map[string]interface{}{
		"assignee": dp.RequestedBy,
	}
	if bead.Status == types.StatusOpen {
		updates["status"] = string(types.StatusInProgress)
	}

	if err := store.UpdateIssue(ctx, beadID, updates, "system:decision-auto-assign"); err != nil {
		fmt.Fprintf(os.Stderr, "decision-auto-assign: update bead %s failed: %v\n", beadID, err)
		return
	}

	s.emitMutation(MutationUpdate, beadID, "", "")
	fmt.Fprintf(os.Stderr, "decision-auto-assign: assigned %s to %s\n", beadID, dp.RequestedBy)
}

// beadIDPrefixes lists known bead ID prefixes for extraction from free text.
var beadIDPrefixes = []string{"bd-", "gt-", "hq-", "beads-"}

// extractBeadIDFromText extracts the first bead-like ID from free text.
// Matches patterns like "bd-rnjc6", "gt-abc123", "hq-18vg2m.3" in strings like
// "Start bd-rnjc6: Diagnose why agents show no task". Returns "" if no match.
// Scans for the earliest occurrence across all known prefixes. (bd-rceeb)
func extractBeadIDFromText(text string) string {
	bestStart := -1
	bestEnd := -1

	for _, prefix := range beadIDPrefixes {
		idx := strings.Index(text, prefix)
		if idx < 0 {
			continue
		}
		// Extract the ID: prefix + alphanumeric/hyphen/dot chars
		end := idx + len(prefix)
		for end < len(text) {
			c := text[end]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
				end++
			} else {
				break
			}
		}
		// Must have something after the prefix
		if end-idx <= len(prefix) {
			continue
		}
		if bestStart < 0 || idx < bestStart {
			bestStart = idx
			bestEnd = end
		}
	}

	if bestStart < 0 {
		return ""
	}
	return text[bestStart:bestEnd]
}

func (s *Server) handleDecisionList(req *Request) Response {
	var args DecisionListArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid decision list args: %v", err),
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

	// Get pending decisions (storage method already filters to pending)
	decisions, err := store.ListPendingDecisions(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list decisions: %v", err),
		}
	}

	// Build response with associated issues
	var respDecisions []*DecisionResponse
	for _, dp := range decisions {
		issue, _ := store.GetIssue(ctx, dp.IssueID)
		respDecisions = append(respDecisions, &DecisionResponse{
			Decision: dp,
			Issue:    issue,
		})
	}

	resp := DecisionListResponse{
		Decisions: respDecisions,
		Count:     len(respDecisions),
	}

	return jsonOK(resp)
}

func (s *Server) handleDecisionListRecent(req *Request) Response {
	var args DecisionListRecentArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid decision list recent args: %v", err),
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

	since, err := time.Parse(time.RFC3339, args.Since)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid since timestamp: %v", err),
		}
	}

	decisions, err := store.ListRecentlyRespondedDecisions(ctx, since, args.RequestedBy)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list recent decisions: %v", err),
		}
	}

	var respDecisions []*DecisionResponse
	for _, dp := range decisions {
		issue, _ := store.GetIssue(ctx, dp.IssueID)
		respDecisions = append(respDecisions, &DecisionResponse{
			Decision: dp,
			Issue:    issue,
		})
	}

	resp := DecisionListResponse{
		Decisions: respDecisions,
		Count:     len(respDecisions),
	}

	return jsonOK(resp)
}

func (s *Server) handleDecisionRemind(req *Request) Response {
	var args DecisionRemindArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid decision remind args: %v", err),
		}
	}

	if args.IssueID == "" {
		return Response{
			Success: false,
			Error:   "issue_id is required",
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

	// Verify issue is a decision gate
	issue, err := store.GetIssue(ctx, args.IssueID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get issue: %v", err),
		}
	}
	if issue == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("issue %s not found", args.IssueID),
		}
	}
	if issue.IssueType != "gate" || issue.AwaitType != "decision" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("%s is not a decision point", args.IssueID),
		}
	}

	// Get decision point
	dp, err := store.GetDecisionPoint(ctx, args.IssueID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get decision point: %v", err),
		}
	}
	if dp == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("no decision point data for %s", args.IssueID),
		}
	}

	// Check if already responded
	if dp.RespondedAt != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("decision %s already responded", args.IssueID),
		}
	}

	// Check reminder limit
	maxReminders := config.GetDecisionMaxReminders()
	if dp.ReminderCount >= maxReminders && !args.Force {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("decision %s has reached max reminders (%d/%d)", args.IssueID, dp.ReminderCount, maxReminders),
		}
	}

	// Increment reminder count
	dp.ReminderCount++
	if err := store.UpdateDecisionPoint(ctx, dp); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to update decision point: %v", err),
		}
	}

	s.emitMutationFor(MutationUpdate, issue)

	// Emit escalation event when reminder count reaches max.
	// The Slack bot's notifyEscalatedDecision handler picks this up.
	if dp.ReminderCount >= maxReminders {
		s.emitDecisionEvent(eventbus.EventDecisionEscalated, eventbus.DecisionEventPayload{
			DecisionID:  args.IssueID,
			Question:    dp.Prompt,
			Urgency:     dp.Urgency,
			RequestedBy: dp.RequestedBy,
		}, req.Actor)
	}

	result := DecisionRemindResult{
		IssueID:       args.IssueID,
		ReminderCount: dp.ReminderCount,
		MaxReminders:  maxReminders,
		Prompt:        dp.Prompt,
	}

	return jsonOK(result)
}

func (s *Server) handleDecisionCancel(req *Request) Response {
	var args DecisionCancelArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid decision cancel args: %v", err),
		}
	}

	if args.IssueID == "" {
		return Response{
			Success: false,
			Error:   "issue_id is required",
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

	// Verify issue is a decision gate
	issue, err := store.GetIssue(ctx, args.IssueID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get issue: %v", err),
		}
	}
	if issue == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("issue %s not found", args.IssueID),
		}
	}
	if issue.IssueType != "gate" || issue.AwaitType != "decision" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("%s is not a decision point", args.IssueID),
		}
	}

	// Get decision point
	dp, err := store.GetDecisionPoint(ctx, args.IssueID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get decision point: %v", err),
		}
	}
	if dp == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("no decision point data for %s", args.IssueID),
		}
	}

	// Already responded — no-op (idempotent cancel)
	if dp.RespondedAt != nil {
		result := DecisionCancelResult{
			IssueID:    args.IssueID,
			CanceledAt: dp.RespondedAt.Format(time.RFC3339),
			Reason:     "already responded",
			CanceledBy: args.CanceledBy,
			Prompt:     dp.Prompt,
		}
		return jsonOK(result)
	}

	// Mark as canceled
	now := time.Now()
	dp.RespondedAt = &now
	dp.RespondedBy = args.CanceledBy
	dp.SelectedOption = "_canceled"
	if args.Reason != "" {
		dp.ResponseText = args.Reason
	}

	if err := store.UpdateDecisionPoint(ctx, dp); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to update decision point: %v", err),
		}
	}

	// Close the gate issue
	closeReason := "Decision canceled"
	if args.Reason != "" {
		closeReason = fmt.Sprintf("Decision canceled: %s", args.Reason)
	}
	actor := req.Actor
	if actor == "" {
		actor = "daemon"
	}
	if err := store.CloseIssue(ctx, args.IssueID, closeReason, actor, ""); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to close gate: %v", err),
		}
	}

	s.emitMutationFor(MutationUpdate, issue)

	result := DecisionCancelResult{
		IssueID:    args.IssueID,
		CanceledAt: now.Format(time.RFC3339),
		Reason:     args.Reason,
		CanceledBy: args.CanceledBy,
		Prompt:     dp.Prompt,
	}

	return jsonOK(result)
}
