package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/factory"
	"github.com/steveyegge/beads/internal/types"
)

// CheckOverClaimedAgents detects assignees with multiple in_progress tasks.
// Agents should work on one task at a time; having multiple in_progress items
// signals over-claiming (an agent grabbed too many tasks). (bd-teo3d)
func CheckOverClaimedAgents(path string) DoctorCheck {
	beadsDir := resolveBeadsDir(path + "/.beads")

	ctx := context.Background()
	store, err := factory.NewFromConfigWithOptions(ctx, beadsDir, factory.Options{
		ReadOnly:             true,
		AllowWithRemoteDaemon: true,
	})
	if err != nil {
		return DoctorCheck{
			Name:     "Over-Claimed Agents",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryRuntime,
		}
	}
	defer func() { _ = store.Close() }()

	inProgress := types.StatusInProgress
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{
		Status: &inProgress,
	})
	if err != nil {
		return DoctorCheck{
			Name:     "Over-Claimed Agents",
			Status:   StatusOK,
			Message:  "N/A (unable to query issues)",
			Category: CategoryRuntime,
		}
	}

	return AnalyzeOverClaims(issues)
}

// AnalyzeOverClaims groups issues by assignee and warns on any with >1 in_progress.
// Exported for use by the remote daemon check path in doctor.go. (bd-teo3d)
func AnalyzeOverClaims(issues []*types.Issue) DoctorCheck {
	// Group by assignee
	byAssignee := make(map[string][]string) // assignee -> list of issue IDs
	for _, issue := range issues {
		if issue.Assignee != "" {
			byAssignee[issue.Assignee] = append(byAssignee[issue.Assignee], issue.ID)
		}
	}

	// Find over-claimers
	var overClaimers []string
	for assignee, ids := range byAssignee {
		if len(ids) > 1 {
			overClaimers = append(overClaimers, fmt.Sprintf("%s (%d: %s)", assignee, len(ids), strings.Join(ids, ", ")))
		}
	}

	if len(overClaimers) == 0 {
		return DoctorCheck{
			Name:     "Over-Claimed Agents",
			Status:   StatusOK,
			Message:  fmt.Sprintf("No agents with multiple in_progress tasks (%d checked)", len(byAssignee)),
			Category: CategoryRuntime,
		}
	}

	detail := strings.Join(overClaimers, "; ")
	if len(detail) > 300 {
		detail = detail[:300] + "..."
	}

	return DoctorCheck{
		Name:     "Over-Claimed Agents",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d agent(s) have multiple in_progress tasks", len(overClaimers)),
		Detail:   detail,
		Fix:      "Run 'bd update <id> --status=open --assignee=\"\"' to unclaim excess tasks",
		Category: CategoryRuntime,
	}
}
