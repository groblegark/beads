package rpc

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/types"
)

// handleGraph returns nodes and edges for graph visualization (bd-hpk9f).
// Combines issue data with dependency edges in a single response optimized
// for 3D force-directed graph rendering.
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

	// Batch fetch dependency records for all matched issues
	depRecords, err := store.GetDependencyRecordsForIssues(ctx, issueIDs)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get dependencies: %v", err),
		}
	}

	// Get dependency counts (both directions)
	depCounts, err := store.GetDependencyCounts(ctx, issueIDs)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get dependency counts: %v", err),
		}
	}

	// Batch fetch labels
	labelsMap, err := store.GetLabelsForIssues(ctx, issueIDs)
	if err != nil {
		labelsMap = make(map[string][]string)
	}

	// Batch fetch blocked-by info
	blockedMap := make(map[string][]string)
	blocked, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err == nil {
		for _, bi := range blocked {
			if _, ok := issueMap[bi.ID]; ok {
				blockedMap[bi.ID] = bi.BlockedBy
			}
		}
	}

	// Build edges and optionally pull in dep targets
	edges := make([]GraphEdge, 0)
	seenEdges := make(map[string]bool)

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

			// If include_deps, fetch the target issue even if it doesn't match filters
			if args.IncludeDeps {
				if _, exists := issueMap[dep.DependsOnID]; !exists {
					depIssue, err := store.GetIssue(ctx, dep.DependsOnID)
					if err == nil && depIssue != nil {
						issueMap[dep.DependsOnID] = depIssue
					}
				}
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

	// Build result with stats
	var result GraphResult
	result.Nodes = nodes
	result.Edges = edges

	stats, err := store.GetStatistics(ctx)
	if err == nil {
		result.Stats.TotalOpen = stats.OpenIssues
		result.Stats.TotalInProgress = stats.InProgressIssues
		result.Stats.TotalBlocked = stats.BlockedIssues
	}

	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}
