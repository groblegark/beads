package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// BeadsTemplateLabel is the label used to identify Beads-based templates
const BeadsTemplateLabel = "template"

// Type aliases for types now defined in internal/formula.
// These keep backward compatibility for cmd/bd/ code.
type TemplateSubgraph = formula.TemplateSubgraph
type InstantiateResult = formula.InstantiateResult
type CloneOptions = formula.CloneOptions

// Deprecated template commands removed (bd-2wl4k) — use 'bd mol' and 'bd formula' instead.

// =============================================================================
// Beads Template Functions
// =============================================================================

// loadSubgraphPreferDaemon loads a template subgraph, preferring daemon RPC over direct store.
// Per epic gt-as9kdm, we want to eliminate direct DB connections.
// This function handles ID resolution and falls back to direct store if daemon unavailable.
func loadSubgraphPreferDaemon(_ context.Context, issueID string) (*TemplateSubgraph, error) {
	requireDaemon("template")
	// Resolve ID via daemon
	resolveResp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: issueID})
	if err != nil {
		return nil, fmt.Errorf("resolving ID via daemon: %w", err)
	}
	var resolvedID string
	if err := json.Unmarshal(resolveResp.Data, &resolvedID); err != nil {
		return nil, fmt.Errorf("parsing resolved ID: %w", err)
	}

	return loadTemplateSubgraphViaDaemon(daemonClient, resolvedID)
}

// loadTemplateSubgraph loads a template epic and all its descendants
func loadTemplateSubgraph(ctx context.Context, s storage.Storage, templateID string) (*TemplateSubgraph, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Get the root issue
	root, err := s.GetIssue(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("template %s not found", templateID)
	}

	subgraph := &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}

	// Recursively load all children
	if err := loadDescendants(ctx, s, subgraph, root.ID); err != nil {
		return nil, err
	}

	// Load all dependencies within the subgraph
	for _, issue := range subgraph.Issues {
		deps, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get dependencies for %s: %w", issue.ID, err)
		}
		for _, dep := range deps {
			// Only include dependencies where both ends are in the subgraph
			if _, ok := subgraph.IssueMap[dep.DependsOnID]; ok {
				subgraph.Dependencies = append(subgraph.Dependencies, dep)
			}
		}
	}

	return subgraph, nil
}

// loadDescendants recursively loads all child issues
// It uses two strategies to find children:
// 1. Check dependency records for parent-child relationships
// 2. Check for hierarchical IDs (parent.N) to catch children with missing/wrong deps
func loadDescendants(ctx context.Context, s storage.Storage, subgraph *TemplateSubgraph, parentID string) error {
	// Track children we've already added to avoid duplicates
	addedChildren := make(map[string]bool)

	// Strategy 1: GetDependents returns issues that depend on parentID
	dependents, err := s.GetDependents(ctx, parentID)
	if err != nil {
		return fmt.Errorf("failed to get dependents of %s: %w", parentID, err)
	}

	// Check each dependent to see if it's a child (has parent-child relationship)
	for _, dependent := range dependents {
		if _, exists := subgraph.IssueMap[dependent.ID]; exists {
			continue // Already in subgraph
		}

		// Check if this dependent has a parent-child relationship with parentID
		depRecs, err := s.GetDependencyRecords(ctx, dependent.ID)
		if err != nil {
			continue
		}

		isChild := false
		for _, depRec := range depRecs {
			if depRec.DependsOnID == parentID && depRec.Type == types.DepParentChild {
				isChild = true
				break
			}
		}

		if !isChild {
			continue
		}

		// Add to subgraph
		subgraph.Issues = append(subgraph.Issues, dependent)
		subgraph.IssueMap[dependent.ID] = dependent
		addedChildren[dependent.ID] = true

		// Recurse to get children of this child
		if err := loadDescendants(ctx, s, subgraph, dependent.ID); err != nil {
			return err
		}
	}

	// Strategy 2: Find hierarchical children by ID pattern
	// This catches children that have missing or incorrect dependency types.
	// Hierarchical IDs follow the pattern: parentID.N (e.g., "gt-abc.1", "gt-abc.2")
	hierarchicalChildren, err := findHierarchicalChildren(ctx, s, parentID)
	if err != nil {
		// Non-fatal: continue with what we have
		return nil
	}

	for _, child := range hierarchicalChildren {
		if addedChildren[child.ID] {
			continue // Already added via dependency
		}
		if _, exists := subgraph.IssueMap[child.ID]; exists {
			continue // Already in subgraph
		}

		// Add to subgraph
		subgraph.Issues = append(subgraph.Issues, child)
		subgraph.IssueMap[child.ID] = child
		addedChildren[child.ID] = true

		// Recurse to get children of this child
		if err := loadDescendants(ctx, s, subgraph, child.ID); err != nil {
			return err
		}
	}

	return nil
}

// findHierarchicalChildren finds issues with IDs that match the pattern parentID.N
// This catches hierarchical children that may be missing parent-child dependencies.
func findHierarchicalChildren(ctx context.Context, s storage.Storage, parentID string) ([]*types.Issue, error) {
	// Look for issues with IDs starting with "parentID."
	// We need to query by ID pattern, which requires listing issues
	pattern := parentID + "."

	// Use the storage's search capability with a filter
	allIssues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, err
	}

	var children []*types.Issue
	for _, issue := range allIssues {
		// Check if ID starts with pattern and is a direct child (no further dots after the pattern)
		if len(issue.ID) > len(pattern) && issue.ID[:len(pattern)] == pattern {
			// Check it's a direct child, not a grandchild
			// e.g., "parent.1" is a child, "parent.1.2" is a grandchild
			remaining := issue.ID[len(pattern):]
			if !strings.Contains(remaining, ".") {
				children = append(children, issue)
			}
		}
	}

	return children, nil
}

// =============================================================================
// Proto Lookup Functions
// =============================================================================

// resolveProtoIDOrTitle resolves a proto by ID or title.
// It first tries to resolve as an ID (via ResolvePartialID).
// If that fails, it searches for protos with matching titles.
// Returns the proto ID if found, or an error if not found or ambiguous.
func resolveProtoIDOrTitle(ctx context.Context, s storage.Storage, input string) (string, error) {
	// Strategy 1: Try to resolve as an ID
	protoID, err := utils.ResolvePartialID(ctx, s, input)
	if err == nil {
		// Verify it's a proto (has template label)
		issue, getErr := s.GetIssue(ctx, protoID)
		if getErr == nil && issue != nil {
			labels, _ := s.GetLabels(ctx, protoID)
			for _, label := range labels {
				if label == BeadsTemplateLabel {
					return protoID, nil // Found a valid proto by ID
				}
			}
		}
		// ID resolved but not a proto - continue to title search
	}

	// Strategy 2: Search for protos by title
	protos, err := s.GetIssuesByLabel(ctx, BeadsTemplateLabel)
	if err != nil {
		return "", fmt.Errorf("failed to search protos: %w", err)
	}

	var matches []*types.Issue
	var exactMatch *types.Issue

	for _, proto := range protos {
		// Check for exact title match (case-insensitive)
		if strings.EqualFold(proto.Title, input) {
			exactMatch = proto
			break
		}
		// Check for partial title match (case-insensitive)
		if strings.Contains(strings.ToLower(proto.Title), strings.ToLower(input)) {
			matches = append(matches, proto)
		}
	}

	if exactMatch != nil {
		return exactMatch.ID, nil
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no proto found matching %q (by ID or title)", input)
	}

	if len(matches) == 1 {
		return matches[0].ID, nil
	}

	// Multiple matches - show them all for disambiguation
	var matchNames []string
	for _, m := range matches {
		matchNames = append(matchNames, fmt.Sprintf("%s: %s", m.ID, m.Title))
	}
	return "", fmt.Errorf("ambiguous: %q matches %d protos:\n  %s\nUse the ID or a more specific title", input, len(matches), strings.Join(matchNames, "\n  "))
}

// =============================================================================
// Daemon-compatible Template Functions
// =============================================================================

// IssueDetailsFromShow represents the response structure from daemon Show RPC
type IssueDetailsFromShow struct {
	types.Issue
	Labels       []string                              `json:"labels,omitempty"`
	Dependencies []*types.IssueWithDependencyMetadata `json:"dependencies,omitempty"`
	Dependents   []*types.IssueWithDependencyMetadata `json:"dependents,omitempty"`
}

// loadTemplateSubgraphViaDaemon loads a template subgraph using daemon RPC calls
func loadTemplateSubgraphViaDaemon(client *rpc.Client, templateID string) (*TemplateSubgraph, error) {
	// Get root issue with dependencies/dependents
	resp, err := client.Show(&rpc.ShowArgs{ID: templateID})
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	var rootDetails IssueDetailsFromShow
	if err := json.Unmarshal(resp.Data, &rootDetails); err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	root := &rootDetails.Issue
	subgraph := &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}

	// Find children from dependents (those with parent-child relationship)
	// and recursively load them
	if err := loadDescendantsViaDaemon(client, subgraph, rootDetails.Dependents); err != nil {
		return nil, err
	}

	// Now build dependencies list by examining each issue's dependencies
	// We need to get the dependency records, which Show provides
	for _, issue := range subgraph.Issues {
		resp, err := client.Show(&rpc.ShowArgs{ID: issue.ID})
		if err != nil {
			continue
		}

		var details IssueDetailsFromShow
		if err := json.Unmarshal(resp.Data, &details); err != nil {
			continue
		}

		// Dependencies are issues that THIS issue depends on
		for _, dep := range details.Dependencies {
			// Only include if the dependency target is also in the subgraph
			if _, ok := subgraph.IssueMap[dep.Issue.ID]; ok {
				subgraph.Dependencies = append(subgraph.Dependencies, &types.Dependency{
					IssueID:     issue.ID,
					DependsOnID: dep.Issue.ID,
					Type:        dep.DependencyType,
				})
			}
		}
	}

	return subgraph, nil
}

// loadDescendantsViaDaemon recursively loads child issues via daemon RPC
func loadDescendantsViaDaemon(client *rpc.Client, subgraph *TemplateSubgraph, dependents []*types.IssueWithDependencyMetadata) error {
	for _, dep := range dependents {
		// Check if this is a child (parent-child relationship)
		if dep.DependencyType != types.DepParentChild {
			continue
		}

		if _, exists := subgraph.IssueMap[dep.Issue.ID]; exists {
			continue // Already in subgraph
		}

		// Add to subgraph
		issue := &dep.Issue
		subgraph.Issues = append(subgraph.Issues, issue)
		subgraph.IssueMap[issue.ID] = issue

		// Get this issue's dependents for recursion
		resp, err := client.Show(&rpc.ShowArgs{ID: issue.ID})
		if err != nil {
			continue
		}

		var details IssueDetailsFromShow
		if err := json.Unmarshal(resp.Data, &details); err != nil {
			continue
		}

		// Recurse on children
		if err := loadDescendantsViaDaemon(client, subgraph, details.Dependents); err != nil {
			return err
		}
	}

	return nil
}

// cloneSubgraphViaDaemon creates new issues from the template using daemon RPC calls.
// Uses the atomic CreateWithDependencies RPC to create all issues, labels, and dependencies
// in a single transaction for consistency and performance.
func cloneSubgraphViaDaemon(client *rpc.Client, subgraph *TemplateSubgraph, opts CloneOptions) (*InstantiateResult, error) {
	// Build the list of issues to create
	issues := make([]rpc.CreateWithDepsIssue, 0, len(subgraph.Issues))

	for _, oldIssue := range subgraph.Issues {
		// Determine assignee: use override for root epic, otherwise keep template's
		issueAssignee := oldIssue.Assignee
		if oldIssue.ID == subgraph.Root.ID && opts.Assignee != "" {
			issueAssignee = opts.Assignee
		}

		issue := rpc.CreateWithDepsIssue{
			ID:                 oldIssue.ID, // Use old ID as reference for mapping
			Title:              substituteVariables(oldIssue.Title, opts.Vars),
			Description:        substituteVariables(oldIssue.Description, opts.Vars),
			IssueType:          string(oldIssue.IssueType),
			Priority:           oldIssue.Priority,
			Design:             substituteVariables(oldIssue.Design, opts.Vars),
			AcceptanceCriteria: substituteVariables(oldIssue.AcceptanceCriteria, opts.Vars),
			Assignee:           issueAssignee,
			EstimatedMinutes:   oldIssue.EstimatedMinutes,
			Ephemeral:          opts.Ephemeral,
			IDPrefix:           opts.Prefix, // distinct prefixes for mols/wisps
			Labels:             oldIssue.Labels,
		}

		// Generate custom ID for dynamic bonding if ParentID is set
		if opts.ParentID != "" {
			bondedID, err := generateBondedID(oldIssue.ID, subgraph.Root.ID, opts)
			if err != nil {
				return nil, fmt.Errorf("failed to generate bonded ID for %s: %w", oldIssue.ID, err)
			}
			issue.ID = bondedID
		}

		issues = append(issues, issue)
	}

	// Build the list of dependencies to create
	deps := make([]rpc.CreateWithDepsDependency, 0, len(subgraph.Dependencies))

	// Collect old IDs for reference
	oldIDs := make(map[string]bool)
	for _, oldIssue := range subgraph.Issues {
		oldIDs[oldIssue.ID] = true
	}

	// Add dependencies from the template
	for _, dep := range subgraph.Dependencies {
		// Only include if both ends are in the subgraph
		if !oldIDs[dep.IssueID] || !oldIDs[dep.DependsOnID] {
			continue
		}

		deps = append(deps, rpc.CreateWithDepsDependency{
			FromID:  dep.IssueID,
			ToID:    dep.DependsOnID,
			DepType: string(dep.Type),
		})
	}

	// Add requires-skill dependencies for all issues
	if len(subgraph.RequiredSkills) > 0 {
		for _, skillID := range subgraph.RequiredSkills {
			// Normalize skill ID (add skill- prefix if needed)
			normalizedSkillID := skillID
			if !strings.HasPrefix(skillID, "skill-") {
				normalizedSkillID = "skill-" + skillID
			}

			// Add requires-skill dependency for each issue
			for _, oldIssue := range subgraph.Issues {
				deps = append(deps, rpc.CreateWithDepsDependency{
					FromID:  oldIssue.ID,
					ToID:    normalizedSkillID,
					DepType: string(types.DepRequiresSkill),
				})
			}
		}
	}

	// Execute the atomic creation
	args := &rpc.CreateWithDepsArgs{
		Issues:       issues,
		Dependencies: deps,
	}

	result, err := client.CreateWithDependencies(args)
	if err != nil {
		return nil, fmt.Errorf("failed to create issues with dependencies: %w", err)
	}

	return &InstantiateResult{
		NewEpicID: result.IDMapping[subgraph.Root.ID],
		IDMapping: result.IDMapping,
		Created:   result.Created,
	}, nil
}

// extractVariables finds all {{variable}} patterns in text.
// Delegates to formula.ExtractVariablesFromText.
func extractVariables(text string) []string {
	return formula.ExtractVariablesFromText(text)
}

// extractAllVariables finds all variables across the entire subgraph.
// Delegates to formula.ExtractAllSubgraphVariables.
func extractAllVariables(subgraph *TemplateSubgraph) []string {
	return formula.ExtractAllSubgraphVariables(subgraph.Issues)
}

// extractRequiredVariables returns only variables that are defined in VarDefs and don't have defaults.
// Delegates to formula.ExtractRequiredSubgraphVariables.
func extractRequiredVariables(subgraph *TemplateSubgraph) []string {
	return formula.ExtractRequiredSubgraphVariables(subgraph.Issues, subgraph.VarDefs)
}

// applyVariableDefaults merges formula default values with provided variables.
// Delegates to formula.ApplyVariableDefaults.
func applyVariableDefaults(vars map[string]string, subgraph *TemplateSubgraph) map[string]string {
	return formula.ApplyVariableDefaults(vars, subgraph.VarDefs)
}

// substituteVariables replaces {{variable}} with values.
// Delegates to formula.SubstituteVariables.
func substituteVariables(text string, vars map[string]string) string {
	return formula.SubstituteVariables(text, vars)
}

// getRelativeID extracts the relative portion of a child ID from its parent.
// Delegates to formula.GetRelativeID.
func getRelativeID(oldID, rootID string) string {
	return formula.GetRelativeID(oldID, rootID)
}

// extractIDSuffix extracts a suffix from an ID for use when IDs aren't hierarchical.
// Delegates to formula.ExtractIDSuffix.
func extractIDSuffix(id string) string {
	return formula.ExtractIDSuffix(id)
}

// generateBondedID creates a custom ID for dynamically bonded molecules.
// Delegates to formula.GenerateBondedID.
func generateBondedID(oldID string, rootID string, opts CloneOptions) (string, error) {
	return formula.GenerateBondedID(oldID, rootID, opts)
}

// cloneSubgraph creates new issues from the template with variable substitution.
// Delegates to formula.CloneSubgraph.
func cloneSubgraph(ctx context.Context, s storage.Storage, subgraph *TemplateSubgraph, opts CloneOptions) (*InstantiateResult, error) {
	return formula.CloneSubgraph(ctx, s, subgraph, opts)
}

// printTemplateTree prints the template structure as a tree
func printTemplateTree(subgraph *TemplateSubgraph, parentID string, depth int, isRoot bool) {
	indent := strings.Repeat("  ", depth)

	// Print root
	if isRoot {
		fmt.Printf("%s   %s (root)\n", indent, subgraph.Root.Title)
	}

	// Find children of this parent
	var children []*types.Issue
	for _, dep := range subgraph.Dependencies {
		if dep.DependsOnID == parentID && dep.Type == types.DepParentChild {
			if child, ok := subgraph.IssueMap[dep.IssueID]; ok {
				children = append(children, child)
			}
		}
	}

	// Print children
	for i, child := range children {
		connector := "├──"
		if i == len(children)-1 {
			connector = "└──"
		}
		vars := extractVariables(child.Title)
		varStr := ""
		if len(vars) > 0 {
			varStr = fmt.Sprintf(" [%s]", strings.Join(vars, ", "))
		}
		fmt.Printf("%s   %s %s%s\n", indent, connector, child.Title, varStr)
		printTemplateTree(subgraph, child.ID, depth+1, false)
	}
}
