package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

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
		args.ExcludeTypes = []string{"message", "config"}
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

	// Enforce limit after merging multiple status queries
	if len(issueMap) > args.Limit {
		sorted := make([]*types.Issue, 0, len(issueMap))
		for _, issue := range issueMap {
			sorted = append(sorted, issue)
		}
		sort.Slice(sorted, func(i, j int) bool {
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
		depRecords map[string][]*types.Dependency
		depCounts  map[string]*types.DependencyCounts
		labelsMap  map[string][]string
		blockedMap map[string][]string
		graphStats *types.Statistics

		depErr, countErr, labelErr, blockedErr, statsErr error
		wg sync.WaitGroup
	)

	wg.Add(5)

	// 1. Dependency records
	go func() {
		defer wg.Done()
		depRecords, depErr = store.GetDependencyRecordsForIssues(ctx, issueIDs)
	}()

	// 2. Dependency counts
	go func() {
		defer wg.Done()
		depCounts, countErr = store.GetDependencyCounts(ctx, issueIDs)
	}()

	// 3. Labels
	go func() {
		defer wg.Done()
		labelsMap, labelErr = store.GetLabelsForIssues(ctx, issueIDs)
		if labelErr != nil {
			labelsMap = make(map[string][]string)
		}
	}()

	// 4. Blocked-by (bd-5nrqx: targeted query instead of full GetBlockedIssues scan)
	go func() {
		defer wg.Done()
		blockedMap, blockedErr = s.getBlockedByForIssues(ctx, issueIDs)
		if blockedErr != nil {
			blockedMap = make(map[string][]string)
		}
	}()

	// 5. Stats
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

	// Build edges and collect missing dep target IDs for batch fetch
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

	// Build nodes
	nodes := make([]GraphNode, 0, len(issueMap))
	for _, issue := range issueMap {
		node := GraphNode{
			ID:        issue.ID,
			Title:     issue.Title,
			Status:    string(issue.Status),
			Priority:  issue.Priority,
			IssueType: string(issue.IssueType),
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

	// Add agent nodes with assignment edges (bd-cd86r)
	if args.IncludeAgents {
		agentIssues := make(map[string][]string) // agent name -> list of assigned issue IDs
		for _, issue := range issueMap {
			if issue.Assignee != "" {
				agentIssues[issue.Assignee] = append(agentIssues[issue.Assignee], issue.ID)
			}
		}
		for agent, assignedIDs := range agentIssues {
			agentNodeID := "agent:" + agent
			nodes = append(nodes, GraphNode{
				ID:        agentNodeID,
				Title:     agent,
				IssueType: "agent",
			})
			for _, issueID := range assignedIDs {
				edges = append(edges, GraphEdge{
					Source: agentNodeID,
					Target: issueID,
					Type:   "assigned_to",
				})
			}
		}
	}

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
