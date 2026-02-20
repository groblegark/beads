package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// handleGraph returns nodes and edges for graph visualization (bd-hpk9f).
// Combines issue data with dependency edges in a single response optimized
// for 3D force-directed graph rendering.
//
// Performance (bd-5nrqx): Uses concurrent queries for deps/labels/counts/blocked
// and avoids the full-table GetBlockedIssues() scan.
// Performance (bd-xlv1i): Redis cache with 30s TTL absorbs polling load from
// beads3d frontend, reducing Dolt CPU saturation from ~800m to near-zero for
// repeated identical queries.
func (s *Server) handleGraph(req *Request) Response {
	var args GraphArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid graph args: %v", err),
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

	// Redis cache fast-path (bd-xlv1i): check Redis before any Dolt queries.
	// The Redis cache uses a 30s TTL and is NOT invalidated by writes,
	// which is acceptable for a visualization endpoint.
	if s.redisCache != nil {
		if cached, ok := s.redisCache.Get(ctx, OpGraph, req.Args); ok {
			return cached
		}
	}

	// Defaults
	if args.Limit == 0 {
		args.Limit = 500
	}
	if len(args.Status) == 0 {
		args.Status = []string{"open", "in_progress"}
	}
	if len(args.ExcludeTypes) == 0 {
		args.ExcludeTypes = []string{"message", "config", "molecule", "formula", "advice", "role"}
	}

	// Build base filter
	filter := types.IssueFilter{
		Limit: args.Limit,
	}
	if args.Assignee != "" {
		filter.Assignee = &args.Assignee
	}
	if args.Priority != nil {
		filter.Priority = args.Priority
	}
	if args.PriorityMin != nil {
		filter.PriorityMin = args.PriorityMin
	}
	if args.PriorityMax != nil {
		filter.PriorityMax = args.PriorityMax
	}
	if len(args.Labels) > 0 {
		filter.Labels = args.Labels
	}
	if len(args.LabelsAny) > 0 {
		filter.LabelsAny = args.LabelsAny
	}
	if args.ParentID != "" {
		filter.ParentID = &args.ParentID
	}
	if args.Rig != nil {
		filter.Rig = args.Rig
	}
	// Apply type exclusions at filter level
	for _, t := range args.ExcludeTypes {
		filter.ExcludeTypes = append(filter.ExcludeTypes, types.IssueType(t))
	}
	// Exclude ephemeral by default
	nonEphemeral := false
	filter.Ephemeral = &nonEphemeral

	// Multi-status: query each status separately and merge,
	// since IssueFilter only supports a single status pointer.
	issueMap := make(map[string]*types.Issue)
	query := args.Query

	for _, statusStr := range args.Status {
		status := types.Status(statusStr)
		f := filter
		f.Status = &status

		// Age-based filtering for closed issues (bd-uc0mw): only fetch
		// recently-updated closed beads to avoid pulling thousands of stale ones.
		if statusStr == "closed" && args.MaxAgeDays > 0 {
			cutoff := time.Now().AddDate(0, 0, -args.MaxAgeDays)
			f.UpdatedAfter = &cutoff
		}

		if len(args.Types) > 0 {
			for _, t := range args.Types {
				issueType := types.IssueType(t)
				tf := f
				tf.IssueType = &issueType
				issues, err := store.SearchIssues(ctx, query, tf)
				if err != nil {
					return Response{
						Success: false,
						Error:   fmt.Sprintf("failed to list issues: %v", err),
					}
				}
				for _, issue := range issues {
					issueMap[issue.ID] = issue
				}
			}
			continue
		}

		issues, err := store.SearchIssues(ctx, query, f)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to list issues: %v", err),
			}
		}
		for _, issue := range issues {
			issueMap[issue.ID] = issue
		}
	}

	// Enforce limit after merging multiple status queries.
	// Sort by relevance (bd-uc0mw): active > open/blocked > recent closed > old closed.
	// Within the same relevance tier, sort by priority.
	if len(issueMap) > args.Limit {
		sorted := make([]*types.Issue, 0, len(issueMap))
		for _, issue := range issueMap {
			sorted = append(sorted, issue)
		}
		sort.Slice(sorted, func(i, j int) bool {
			ri, rj := issueRelevance(sorted[i]), issueRelevance(sorted[j])
			if ri != rj {
				return ri < rj // lower = more relevant
			}
			return sorted[i].Priority < sorted[j].Priority
		})
		issueMap = make(map[string]*types.Issue, args.Limit)
		for i := 0; i < args.Limit && i < len(sorted); i++ {
			issueMap[sorted[i].ID] = sorted[i]
		}
	}

	// Collect IDs for batch queries
	issueIDs := make([]string, 0, len(issueMap))
	for id := range issueMap {
		issueIDs = append(issueIDs, id)
	}

	// Run independent batch queries concurrently (bd-5nrqx)
	var (
		depRecords    map[string][]*types.Dependency
		revDepRecords map[string][]*types.Dependency
		depCounts     map[string]*types.DependencyCounts
		labelsMap     map[string][]string
		blockedMap    map[string][]string
		graphStats    *types.Statistics

		depErr, revDepErr, countErr, labelErr, blockedErr, statsErr error
		wg                                                          sync.WaitGroup
	)

	wg.Add(6)

	// 1. Forward dependency records (edges FROM these issues)
	go func() {
		defer wg.Done()
		depRecords, depErr = store.GetDependencyRecordsForIssues(ctx, issueIDs)
	}()

	// 2. Reverse dependency records (edges TO these issues, bd-j3pex)
	// Finds parent-child and other edges where the graph node is the target,
	// e.g., closed children pointing to open epics.
	go func() {
		defer wg.Done()
		revDepRecords, revDepErr = store.GetReverseDepRecordsForIssues(ctx, issueIDs)
	}()

	// 3. Dependency counts
	go func() {
		defer wg.Done()
		depCounts, countErr = store.GetDependencyCounts(ctx, issueIDs)
	}()

	// 4. Labels
	go func() {
		defer wg.Done()
		labelsMap, labelErr = store.GetLabelsForIssues(ctx, issueIDs)
		if labelErr != nil {
			labelsMap = make(map[string][]string)
		}
	}()

	// 5. Blocked-by (bd-5nrqx: targeted query instead of full GetBlockedIssues scan)
	go func() {
		defer wg.Done()
		blockedMap, blockedErr = s.getBlockedByForIssues(ctx, issueIDs)
		if blockedErr != nil {
			blockedMap = make(map[string][]string)
		}
	}()

	// 6. Stats
	go func() {
		defer wg.Done()
		graphStats, statsErr = store.GetStatistics(ctx)
	}()

	wg.Wait()

	if depErr != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get dependencies: %v", depErr),
		}
	}
	if countErr != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get dependency counts: %v", countErr),
		}
	}

	// Build edges from forward deps and collect missing dep target IDs
	edges := make([]GraphEdge, 0)
	seenEdges := make(map[string]bool)
	missingDepIDs := make(map[string]bool)

	for issueID, deps := range depRecords {
		for _, dep := range deps {
			edgeKey := fmt.Sprintf("%s->%s:%s", issueID, dep.DependsOnID, dep.Type)
			if seenEdges[edgeKey] {
				continue
			}
			seenEdges[edgeKey] = true

			edges = append(edges, GraphEdge{
				Source: issueID,
				Target: dep.DependsOnID,
				Type:   string(dep.Type),
			})

			// Collect missing dep targets for batch fetch (bd-5nrqx)
			if args.IncludeDeps {
				if _, exists := issueMap[dep.DependsOnID]; !exists {
					missingDepIDs[dep.DependsOnID] = true
				}
			}
		}
	}

	// Build edges from reverse deps (bd-j3pex: finds edges TO graph nodes,
	// e.g., children pointing to parent epics, even if children are closed/filtered)
	if revDepErr == nil && revDepRecords != nil {
		for _, deps := range revDepRecords {
			for _, dep := range deps {
				edgeKey := fmt.Sprintf("%s->%s:%s", dep.IssueID, dep.DependsOnID, dep.Type)
				if seenEdges[edgeKey] {
					continue
				}
				seenEdges[edgeKey] = true

				edges = append(edges, GraphEdge{
					Source: dep.IssueID,
					Target: dep.DependsOnID,
					Type:   string(dep.Type),
				})

				// The source of this edge may not be in the graph — pull it in
				if args.IncludeDeps {
					if _, exists := issueMap[dep.IssueID]; !exists {
						missingDepIDs[dep.IssueID] = true
					}
				}
			}
		}
	}

	// Batch-fetch missing dep targets (bd-5nrqx: replaces N individual GetIssue calls)
	if len(missingDepIDs) > 0 {
		missingIDs := make([]string, 0, len(missingDepIDs))
		for id := range missingDepIDs {
			missingIDs = append(missingIDs, id)
		}
		depFilter := types.IssueFilter{IDs: missingIDs}
		depIssues, err := store.SearchIssues(ctx, "", depFilter)
		if err == nil {
			for _, issue := range depIssues {
				issueMap[issue.ID] = issue
			}
		}
	}

	// Build parent-child lookup from dep records (bd-j3pex, bd-uqkpq)
	// First pass: explicit parent-child deps (canonical)
	parentOf := make(map[string]string) // childID -> parentID
	for issueID, deps := range depRecords {
		for _, dep := range deps {
			if dep.Type == types.DepParentChild {
				parentOf[issueID] = dep.DependsOnID
			}
		}
	}
	if revDepRecords != nil {
		for _, deps := range revDepRecords {
			for _, dep := range deps {
				if dep.Type == types.DepParentChild {
					parentOf[dep.IssueID] = dep.DependsOnID
				}
			}
		}
	}
	// Second pass (bd-uqkpq): infer parent-child from "blocks" deps where the
	// target is an epic. Most epics use blocks deps rather than explicit
	// parent-child deps, so without this inference tasks appear unlinked.
	for issueID, deps := range depRecords {
		if _, hasParent := parentOf[issueID]; hasParent {
			continue // explicit parent-child takes priority
		}
		for _, dep := range deps {
			if dep.Type == types.DepBlocks {
				if target, ok := issueMap[dep.DependsOnID]; ok && target.IssueType == types.TypeEpic {
					parentOf[issueID] = dep.DependsOnID
					break
				}
			}
		}
	}

	// Promote "blocks" edges to "parent-child" where parentOf says so (bd-uqkpq).
	// This makes the edge type match the inferred relationship for frontend rendering.
	for i, e := range edges {
		if e.Type == string(types.DepBlocks) {
			if parentOf[e.Source] == e.Target {
				edges[i].Type = string(types.DepParentChild)
			}
		}
	}

	// Build nodes
	nodes := make([]GraphNode, 0, len(issueMap))
	for _, issue := range issueMap {
		node := GraphNode{
			ID:        issue.ID,
			Title:     issue.Title,
			Status:    string(issue.Status),
			Priority:  issue.Priority,
			IssueType: string(issue.IssueType),
			ParentID:  parentOf[issue.ID],
			Assignee:  issue.Assignee,
			Rig:       issue.Rig,
			Labels:    labelsMap[issue.ID],
			CreatedAt: issue.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: issue.UpdatedAt.Format("2006-01-02T15:04:05Z"),
			Ephemeral: issue.Ephemeral,
			BlockedBy: blockedMap[issue.ID],
		}

		if counts, ok := depCounts[issue.ID]; ok {
			node.DepCount = counts.DependencyCount
			node.DepByCount = counts.DependentCount
		}

		if args.IncludeBody {
			node.Description = issue.Description
			node.Notes = issue.Notes
			node.Design = issue.Design
		}

		nodes = append(nodes, node)
	}

	// Add agent nodes with assignment edges (bd-cd86r, bd-bpcqy, bd-sym2a)
	if args.IncludeAgents {
		agentNodes := make(map[string]*GraphNode) // agent name -> node

		// Source 0: Real agent beads from storage (gt:agent label). (bd-sym2a)
		// These have rich metadata (labels, notes, rig, role_type) and are the
		// preferred source. Synthetic nodes from roster/assignees are fallbacks.
		agentBeads, beadErr := store.GetIssuesByLabel(ctx, "gt:agent")
		if beadErr == nil {
			for _, ab := range agentBeads {
				if ab.Status == "tombstone" || ab.Status == "closed" {
					continue
				}
				agentNodes[ab.ID] = &GraphNode{
					ID:        "agent:" + ab.ID,
					Title:     ab.ID,
					IssueType: "agent",
					Rig:       ab.Rig,
					Labels:    ab.Labels,
					CreatedAt: ab.CreatedAt.Format("2006-01-02T15:04:05Z"),
				}
			}
		}

		// Source 1: Live roster from presence tracker (shows ALL active agents,
		// including ephemeral workers without assigned beads)
		if s.bus != nil {
			if pt := s.bus.Presence(); pt != nil {
				for _, e := range pt.Roster(0) {
					if e.Reaped {
						continue
					}
					status := "idle"
					if e.IdleSecs < 30 {
						status = "active"
					}
					if existing, ok := agentNodes[e.Actor]; ok {
						// Enrich real bead node with live status. (bd-sym2a)
						existing.Status = status
					} else {
						agentNodes[e.Actor] = &GraphNode{
							ID:        "agent:" + e.Actor,
							Title:     e.Actor,
							IssueType: "agent",
							Status:    status,
						}
					}
				}
			}
		}

		// Source 2: Issue assignees (catches agents not in roster,
		// e.g. local dev without NATS)
		for _, issue := range issueMap {
			if issue.Assignee == "" {
				continue
			}
			agentNodeID := "agent:" + issue.Assignee
			if _, exists := agentNodes[issue.Assignee]; !exists {
				agentNodes[issue.Assignee] = &GraphNode{
					ID:        agentNodeID,
					Title:     issue.Assignee,
					IssueType: "agent",
				}
			}
			edges = append(edges, GraphEdge{
				Source: agentNodeID,
				Target: issue.ID,
				Type:   "assigned_to",
			})
		}

		for _, node := range agentNodes {
			nodes = append(nodes, *node)
		}
	}

	// Filter gate/decision nodes: only show them if they have at least one edge
	// connecting them to an open/in_progress task (bd-2qn9y).
	connectedIDs := make(map[string]bool)
	for _, e := range edges {
		connectedIDs[e.Source] = true
		connectedIDs[e.Target] = true
	}
	filteredNodes := make([]GraphNode, 0, len(nodes))
	for _, n := range nodes {
		switch n.IssueType {
		case "gate", "decision", "molecule":
			if !connectedIDs[n.ID] {
				continue // drop disconnected gates, decisions, and molecules (bd-t25i1)
			}
		}
		filteredNodes = append(filteredNodes, n)
	}
	nodes = filteredNodes

	// Build result with stats
	var result GraphResult
	result.Nodes = nodes
	result.Edges = edges

	if statsErr == nil && graphStats != nil {
		result.Stats.TotalOpen = graphStats.OpenIssues
		result.Stats.TotalInProgress = graphStats.InProgressIssues
		result.Stats.TotalBlocked = graphStats.BlockedIssues
	}

	data, _ := json.Marshal(result)
	resp := Response{
		Success: true,
		Data:    data,
	}

	// Store in Redis cache for next poll (bd-xlv1i)
	if s.redisCache != nil {
		s.redisCache.Set(ctx, OpGraph, req.Args, resp)
	}

	return resp
}

// issueRelevance returns a sort key for relevance-based ordering (bd-uc0mw).
// Lower values = higher relevance. Tiers:
//
//	0: in_progress (actively being worked)
//	1: blocked/hooked (needs attention)
//	2: open (ready for work)
//	3: deferred (on ice but still alive)
//	4: recently closed (updated within 7 days)
//	5: older closed
//	6: everything else (tombstone, pinned, etc.)
//
// Within the same tier, the caller breaks ties by priority.
func issueRelevance(issue *types.Issue) int {
	switch issue.Status {
	case types.StatusInProgress:
		return 0
	case types.StatusBlocked, types.StatusHooked:
		return 1
	case types.StatusOpen:
		return 2
	case types.StatusDeferred:
		return 3
	case types.StatusClosed:
		if !issue.UpdatedAt.IsZero() && time.Since(issue.UpdatedAt) < 7*24*time.Hour {
			return 4
		}
		return 5
	default:
		return 6
	}
}

// getBlockedByForIssues returns a map of issueID -> blocker IDs for the given issues.
// This is a targeted query that only checks the given issue IDs, unlike GetBlockedIssues()
// which scans every issue in the database (bd-5nrqx).
func (s *Server) getBlockedByForIssues(ctx context.Context, issueIDs []string) (map[string][]string, error) {
	if len(issueIDs) == 0 {
		return make(map[string][]string), nil
	}

	store := s.storage
	if store == nil {
		return nil, fmt.Errorf("storage not available")
	}

	db := store.UnderlyingDB()
	if db == nil {
		// Fallback: no direct SQL access (e.g., non-Dolt backend)
		return make(map[string][]string), nil
	}

	// Build IN clause with placeholders
	placeholders := make([]byte, 0, len(issueIDs)*2)
	args := make([]interface{}, len(issueIDs))
	for i, id := range issueIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT d.issue_id, GROUP_CONCAT(d.depends_on_id) AS blocker_ids
		FROM dependencies d
		JOIN issues blocker ON d.depends_on_id = blocker.id
		WHERE d.issue_id IN (%s)
		  AND d.type = 'blocks'
		  AND blocker.status IN ('open', 'in_progress', 'blocked', 'deferred', 'hooked')
		GROUP BY d.issue_id`, string(placeholders))

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("blocked-by query failed: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var issueID, blockerCSV string
		if err := rows.Scan(&issueID, &blockerCSV); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		if blockerCSV != "" {
			// Split CSV blocker IDs
			ids := splitCSV(blockerCSV)
			result[issueID] = ids
		}
	}
	return result, rows.Err()
}

// splitCSV splits a comma-separated string into a slice.
func splitCSV(s string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			if i > start {
				result = append(result, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}
