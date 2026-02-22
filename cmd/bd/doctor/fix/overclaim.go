package fix

import (
	"context"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/types"
)

// OverClaimedAgents fixes agents that have multiple in_progress tasks by
// releasing older tasks (keeping the most recently updated one). Released
// tasks are set to open with assignee cleared. (bd-6f09n)
func OverClaimedAgents(path string) error {
	beadsDir := path
	if _, err := fmt.Sscanf(beadsDir, "%s", &beadsDir); err == nil {
		// path is the project root, not the .beads dir
	}

	ctx := context.Background()
	store, err := factory.NewFromConfigWithOptions(ctx, beadsDir+"/.beads", factory.Options{
		ReadOnly: false, // need write access to fix
	})
	if err != nil {
		return fmt.Errorf("unable to open database: %w", err)
	}
	defer func() { _ = store.Close() }()

	inProgress := types.StatusInProgress
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{
		Status: &inProgress,
	})
	if err != nil {
		return fmt.Errorf("unable to query issues: %w", err)
	}

	// Group by assignee
	byAssignee := make(map[string][]*types.Issue)
	for _, issue := range issues {
		if issue.Assignee != "" {
			byAssignee[issue.Assignee] = append(byAssignee[issue.Assignee], issue)
		}
	}

	fixedCount := 0
	for assignee, agentIssues := range byAssignee {
		if len(agentIssues) <= 1 {
			continue
		}

		// Sort by UpdatedAt descending — keep the most recently updated
		sort.Slice(agentIssues, func(i, j int) bool {
			return agentIssues[i].UpdatedAt.After(agentIssues[j].UpdatedAt)
		})

		// Keep the first (most recent), release the rest
		kept := agentIssues[0]
		for _, issue := range agentIssues[1:] {
			updates := map[string]interface{}{
				"status":   string(types.StatusOpen),
				"assignee": "",
			}
			if err := store.UpdateIssue(ctx, issue.ID, updates, "doctor-fix"); err != nil {
				fmt.Printf("  ⚠ Failed to release %s from %s: %v\n", issue.ID, assignee, err)
				continue
			}
			fmt.Printf("  Released %s (%s) from %s — keeping %s\n", issue.ID, issue.Title, assignee, kept.ID)
			fixedCount++
		}
	}

	if fixedCount == 0 {
		return fmt.Errorf("no over-claimed agents found to fix")
	}

	fmt.Printf("  Released %d excess task(s)\n", fixedCount)
	return nil
}
