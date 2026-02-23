package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var showCmd = &cobra.Command{
	Use:     "show [id...] [--id=<id>...]",
	Aliases: []string{"view"},
	GroupID: "issues",
	Short:   "Show issue details",
	Args:    cobra.ArbitraryArgs, // Allow zero positional args when --id is used
	Run: func(cmd *cobra.Command, args []string) {
		showThread, _ := cmd.Flags().GetBool("thread")
		shortMode, _ := cmd.Flags().GetBool("short")
		showRefs, _ := cmd.Flags().GetBool("refs")
		showChildren, _ := cmd.Flags().GetBool("children")
		asOfRef, _ := cmd.Flags().GetString("as-of")
		idFlags, _ := cmd.Flags().GetStringArray("id")
		localTime, _ := cmd.Flags().GetBool("local-time")
		ctx := rootCtx

		// Helper to format timestamp based on --local-time flag
		formatTime := func(t time.Time) string {
			if localTime {
				t = t.Local()
			}
			return t.Format("2006-01-02 15:04")
		}

		// Merge --id flag values with positional args
		// This allows IDs that look like flags (e.g., --xyz or gt--abc) to be passed safely
		args = append(args, idFlags...)

		// Validate that at least one ID is provided
		if len(args) == 0 {
			FatalErrorRespectJSON("at least one issue ID is required (use positional args or --id flag)")
		}

		// Handle --as-of flag: show issue at a specific point in history
		if asOfRef != "" {
			showIssueAsOf(ctx, args, asOfRef, shortMode)
			return
		}

		// Daemon auto-imports on staleness, so freshness check not needed

		// Resolve partial IDs, splitting into local vs routed
		batch, err := resolveIDsWithRouting(ctx, store, daemonClient, args)
		if err != nil {
			FatalErrorRespectJSON("%v", err)
		}
		resolvedIDs := batch.ResolvedIDs
		routedArgs := batch.RoutedArgs

		// Handle --thread flag: show full conversation thread
		if showThread {
			if len(resolvedIDs) > 0 {
				showMessageThread(ctx, resolvedIDs[0], jsonOutput)
				return
			}
		}

		// Handle --refs flag: show issues that reference this issue
		if showRefs {
			showIssueRefs(ctx, args, resolvedIDs, routedArgs, jsonOutput)
			return
		}

		// Handle --children flag: show only children of this issue
		if showChildren {
			showIssueChildren(ctx, args, resolvedIDs, routedArgs, jsonOutput, shortMode)
			return
		}

		// Use daemon RPC
		requireDaemon("show")
		{
			allDetails := []interface{}{}
			displayIdx := 0

			// First, handle routed IDs via centralized routing (bd-z344)
			forEachRoutedID(routedArgs, func(resolvedID string, routedClient *rpc.Client) error {
				showResp, showErr := routedClient.Show(&rpc.ShowArgs{ID: resolvedID})
				routedClient.Close()
				if showErr != nil {
					return showErr
				}
				if string(showResp.Data) == "null" || len(showResp.Data) == 0 {
					fmt.Fprintf(os.Stderr, "Issue %s not found\n", resolvedID)
					return nil
				}
				var details types.IssueDetails
				if err := json.Unmarshal(showResp.Data, &details); err != nil {
					return err
				}
				issue := &details.Issue
				if shortMode {
					fmt.Println(formatShortIssue(issue))
					return nil
				}
				if jsonOutput {
					for _, dep := range details.Dependencies {
						if dep.DependencyType == types.DepParentChild {
							details.Parent = &dep.ID
							break
						}
					}
					allDetails = append(allDetails, details)
				} else {
					agentMode := ui.IsAgentMode()
					if displayIdx > 0 {
						if agentMode {
							fmt.Println() // blank line separator in agent mode (bd-uh22f)
						} else {
							fmt.Println("\n" + ui.RenderMuted(strings.Repeat("─", 60)))
						}
					}
					fmt.Printf("\n%s\n", formatIssueHeader(issue))
					fmt.Println(formatIssueMetadata(issue))
					if issue.Description != "" {
						if agentMode {
							fmt.Printf("\nDESCRIPTION\n%s\n", ui.RenderMarkdown(issue.Description))
						} else {
							fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMarkdown(issue.Description))
						}
					}
					fmt.Println()
					displayIdx++
				}
				return nil
			})

			// Then, handle local IDs via daemon
			for _, id := range resolvedIDs {
				showArgs := &rpc.ShowArgs{ID: id}
				resp, err := daemonClient.Show(showArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
					continue
				}

				if jsonOutput {
					var details types.IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err == nil {
						// Compute parent from dependencies
						for _, dep := range details.Dependencies {
							if dep.DependencyType == types.DepParentChild {
								details.Parent = &dep.ID
								break
							}
						}
						allDetails = append(allDetails, details)
					}
				} else {
					// Check if issue exists (daemon returns null for non-existent issues)
					if string(resp.Data) == "null" || len(resp.Data) == 0 {
						fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
						continue
					}

					// Parse response first to check shortMode before output
					var details types.IssueDetails
					if err := json.Unmarshal(resp.Data, &details); err != nil {
						fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
						os.Exit(1)
					}
					issue := &details.Issue

					if shortMode {
						fmt.Println(formatShortIssue(issue))
						continue
					}

					agentMode := ui.IsAgentMode()
					if displayIdx > 0 {
						if agentMode {
							fmt.Println() // blank line separator in agent mode (bd-uh22f)
						} else {
							fmt.Println("\n" + ui.RenderMuted(strings.Repeat("─", 60)))
						}
					}
					displayIdx++

					// Tufte-aligned header: STATUS_ICON ID · Title   [Priority · STATUS]
					fmt.Printf("\n%s\n", formatIssueHeader(issue))

					// Metadata: Owner · Type | Created · Updated
					fmt.Println(formatIssueMetadata(issue))

					// Compaction info: suppress in agent mode (bd-uh22f)
					if !agentMode && issue.CompactionLevel > 0 {
						fmt.Println()
						if issue.OriginalSize > 0 {
							currentSize := len(issue.Description) + len(issue.Design) + len(issue.Notes) + len(issue.AcceptanceCriteria)
							saved := issue.OriginalSize - currentSize
							if saved > 0 {
								reduction := float64(saved) / float64(issue.OriginalSize) * 100
								fmt.Printf("📊 %d → %d bytes (%.0f%% reduction)\n",
									issue.OriginalSize, currentSize, reduction)
							}
						}
					}

					// Content sections — plain headers in agent mode (bd-uh22f)
					sectionHeader := func(name string) string {
						if agentMode {
							return name
						}
						return ui.RenderBold(name)
					}
					if issue.Description != "" {
						fmt.Printf("\n%s\n%s\n", sectionHeader("DESCRIPTION"), ui.RenderMarkdown(issue.Description))
					}
					if issue.Design != "" {
						fmt.Printf("\n%s\n%s\n", sectionHeader("DESIGN"), ui.RenderMarkdown(issue.Design))
					}
					if issue.Notes != "" {
						fmt.Printf("\n%s\n%s\n", sectionHeader("NOTES"), ui.RenderMarkdown(issue.Notes))
					}
					if issue.AcceptanceCriteria != "" {
						fmt.Printf("\n%s\n%s\n", sectionHeader("ACCEPTANCE CRITERIA"), ui.RenderMarkdown(issue.AcceptanceCriteria))
					}

					if len(details.Labels) > 0 {
						fmt.Printf("\n%s %s\n", sectionHeader("LABELS:"), strings.Join(details.Labels, ", "))
					}

					// Dependencies grouped by type with semantic colors
					if len(details.Dependencies) > 0 {
						var blocks, parent, related, discovered []*types.IssueWithDependencyMetadata
						for _, dep := range details.Dependencies {
							switch dep.DependencyType {
							case types.DepBlocks:
								blocks = append(blocks, dep)
							case types.DepParentChild:
								parent = append(parent, dep)
							case types.DepRelated:
								related = append(related, dep)
							case types.DepDiscoveredFrom:
								discovered = append(discovered, dep)
							default:
								blocks = append(blocks, dep)
							}
						}

						if len(parent) > 0 {
							fmt.Printf("\n%s\n", sectionHeader("PARENT"))
							for _, dep := range parent {
								fmt.Println(formatDependencyLine("↑", dep))
							}
						}
						if len(blocks) > 0 {
							fmt.Printf("\n%s\n", sectionHeader("DEPENDS ON"))
							for _, dep := range blocks {
								fmt.Println(formatDependencyLine("→", dep))
							}
						}
						if len(related) > 0 {
							fmt.Printf("\n%s\n", sectionHeader("RELATED"))
							for _, dep := range related {
								fmt.Println(formatDependencyLine("↔", dep))
							}
						}
						if len(discovered) > 0 {
							fmt.Printf("\n%s\n", sectionHeader("DISCOVERED FROM"))
							for _, dep := range discovered {
								fmt.Println(formatDependencyLine("◊", dep))
							}
						}
					}

					// Dependents grouped by type with semantic colors
					if len(details.Dependents) > 0 {
						var blocks, children, related, discovered []*types.IssueWithDependencyMetadata
						for _, dep := range details.Dependents {
							switch dep.DependencyType {
							case types.DepBlocks:
								blocks = append(blocks, dep)
							case types.DepParentChild:
								children = append(children, dep)
							case types.DepRelated:
								related = append(related, dep)
							case types.DepDiscoveredFrom:
								discovered = append(discovered, dep)
							default:
								blocks = append(blocks, dep)
							}
						}

						if len(children) > 0 {
							fmt.Printf("\n%s\n", sectionHeader("CHILDREN"))
							for _, dep := range children {
								fmt.Println(formatDependencyLine("↳", dep))
							}
						}
						if len(blocks) > 0 {
							fmt.Printf("\n%s\n", sectionHeader("BLOCKS"))
							for _, dep := range blocks {
								fmt.Println(formatDependencyLine("←", dep))
							}
						}
						if len(related) > 0 {
							fmt.Printf("\n%s\n", sectionHeader("RELATED"))
							for _, dep := range related {
								fmt.Println(formatDependencyLine("↔", dep))
							}
						}
						if len(discovered) > 0 {
							fmt.Printf("\n%s\n", sectionHeader("DISCOVERED"))
							for _, dep := range discovered {
								fmt.Println(formatDependencyLine("◊", dep))
							}
						}
					}

					if len(details.Comments) > 0 {
						fmt.Printf("\n%s\n", sectionHeader("COMMENTS"))
						for _, comment := range details.Comments {
							if agentMode {
								fmt.Printf("  %s %s\n", formatTime(comment.CreatedAt), comment.Author)
							} else {
								fmt.Printf("  %s %s\n", ui.RenderMuted(formatTime(comment.CreatedAt)), comment.Author)
							}
							rendered := ui.RenderMarkdown(comment.Text)
							// TrimRight removes trailing newlines that Glamour adds, preventing extra blank lines
							for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
								fmt.Printf("    %s\n", line)
							}
						}
					}

					// Linked commits (bd-as8xf)
					if len(details.Commits) > 0 {
						var commits []rpc.CommitRecord
						if json.Unmarshal(details.Commits, &commits) == nil && len(commits) > 0 {
							fmt.Printf("\n%s (%d)\n", sectionHeader("COMMITS"), len(commits))
							for _, c := range commits {
								sha := c.CommitSHA
								if len(sha) > 8 {
									sha = sha[:8]
								}
								var parts []string
								if agentMode {
									parts = []string{sha}
								} else {
									parts = []string{ui.RenderID(sha)}
								}
								if c.Branch != "" {
									if agentMode {
										parts = append(parts, "["+c.Branch+"]")
									} else {
										parts = append(parts, ui.RenderMuted("["+c.Branch+"]"))
									}
								}
								if c.Message != "" {
									msg := c.Message
									if len(msg) > 60 {
										msg = msg[:57] + "..."
									}
									parts = append(parts, msg)
								}
								if c.CommittedAt != "" {
									if t, err := time.Parse(time.RFC3339, c.CommittedAt); err == nil {
										if agentMode {
											parts = append(parts, "("+relativeTime(t)+")")
										} else {
											parts = append(parts, ui.RenderMuted("("+relativeTime(t)+")"))
										}
									}
								}
								fmt.Printf("  %s\n", strings.Join(parts, "  "))
							}
						}
					}

					fmt.Println()
				}
			}

			if jsonOutput && len(allDetails) > 0 {
				outputJSON(allDetails)
			}

			// Track first shown issue as last touched
			if len(resolvedIDs) > 0 {
				SetLastTouchedID(resolvedIDs[0])
			} else if len(routedArgs) > 0 {
				SetLastTouchedID(routedArgs[0])
			}
		}
	},
}

// formatShortIssue returns a compact one-line representation of an issue
// Format: STATUS_ICON ID PRIORITY [Type] Title
// Agent mode (bd-uh22f): plain text, no color escapes
func formatShortIssue(issue *types.Issue) string {
	// Agent mode: plain compact line (bd-uh22f)
	if ui.IsAgentMode() {
		icon := ui.GetStatusIcon(string(issue.Status))
		return fmt.Sprintf("%s %s P%d %s %s", icon, issue.ID, issue.Priority, issue.IssueType, issue.Title)
	}

	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	priorityTag := ui.RenderPriority(issue.Priority)

	// Type badge only for notable types
	typeBadge := ""
	switch issue.IssueType {
	case "epic":
		typeBadge = ui.TypeEpicStyle.Render("[epic]") + " "
	case "bug":
		typeBadge = ui.TypeBugStyle.Render("[bug]") + " "
	}

	// Closed issues: entire line is muted
	if issue.Status == types.StatusClosed {
		return fmt.Sprintf("%s %s %s %s%s",
			statusIcon,
			ui.RenderMuted(issue.ID),
			ui.RenderMuted(fmt.Sprintf("● P%d", issue.Priority)),
			ui.RenderMuted(string(issue.IssueType)),
			ui.RenderMuted(" "+issue.Title))
	}

	return fmt.Sprintf("%s %s %s %s%s", statusIcon, issue.ID, priorityTag, typeBadge, issue.Title)
}

// formatIssueHeader returns the Tufte-aligned header line
// Format: ID · Title   [Priority · STATUS]
// All elements in bd show get semantic colors since focus is on one issue
// Agent mode (bd-uh22f): plain text, no color escapes
func formatIssueHeader(issue *types.Issue) string {
	// Agent mode: compact plain-text header (bd-uh22f)
	if ui.IsAgentMode() {
		icon := ui.GetStatusIcon(string(issue.Status))
		typeBadge := ""
		if issue.IssueType == "epic" || issue.IssueType == "bug" {
			typeBadge = " [" + strings.ToUpper(string(issue.IssueType)) + "]"
		}
		return fmt.Sprintf("%s %s%s P%d %s — %s",
			icon, issue.ID, typeBadge, issue.Priority, strings.ToUpper(string(issue.Status)), issue.Title)
	}

	// Get status icon and style
	statusIcon := ui.RenderStatusIcon(string(issue.Status))
	statusStyle := ui.GetStatusStyle(string(issue.Status))
	statusStr := statusStyle.Render(strings.ToUpper(string(issue.Status)))

	// Priority with semantic color (includes ● icon)
	priorityTag := ui.RenderPriority(issue.Priority)

	// Type badge for notable types
	typeBadge := ""
	switch issue.IssueType {
	case "epic":
		typeBadge = " " + ui.TypeEpicStyle.Render("[EPIC]")
	case "bug":
		typeBadge = " " + ui.TypeBugStyle.Render("[BUG]")
	}

	// Compaction indicator
	tierEmoji := ""
	switch issue.CompactionLevel {
	case 1:
		tierEmoji = " 🗜️"
	case 2:
		tierEmoji = " 📦"
	}

	// Build header: STATUS_ICON ID · Title   [Priority · STATUS]
	idStyled := ui.RenderAccent(issue.ID)
	return fmt.Sprintf("%s %s%s · %s%s   [%s · %s]",
		statusIcon, idStyled, typeBadge, issue.Title, tierEmoji, priorityTag, statusStr)
}

// formatIssueMetadata returns the metadata line(s) with grouped info
// Format: Owner: user · Type: task
//
//	Created: 2026-01-06 · Updated: 2026-01-08
//
// Agent mode (bd-uh22f): plain text, no color escapes
func formatIssueMetadata(issue *types.Issue) string {
	agentMode := ui.IsAgentMode()
	var lines []string

	// Line 1: Owner/Assignee · Type
	metaParts := []string{}
	if issue.CreatedBy != "" {
		metaParts = append(metaParts, fmt.Sprintf("Owner: %s", issue.CreatedBy))
	}
	if issue.Assignee != "" {
		metaParts = append(metaParts, fmt.Sprintf("Assignee: %s", issue.Assignee))
	}

	// Git branch from metadata (bd-kg4lw)
	if branch := rpc.GetGitBranch(issue.Metadata); branch != "" {
		metaParts = append(metaParts, fmt.Sprintf("Branch: %s", branch))
	}

	// Type with semantic color (plain in agent mode — bd-uh22f)
	typeStr := string(issue.IssueType)
	if !agentMode {
		switch issue.IssueType {
		case "epic":
			typeStr = ui.TypeEpicStyle.Render("epic")
		case "bug":
			typeStr = ui.TypeBugStyle.Render("bug")
		}
	}
	metaParts = append(metaParts, fmt.Sprintf("Type: %s", typeStr))

	if len(metaParts) > 0 {
		lines = append(lines, strings.Join(metaParts, " · "))
	}

	// Line 2: Created · Updated · Due/Defer
	timeParts := []string{}
	timeParts = append(timeParts, fmt.Sprintf("Created: %s", issue.CreatedAt.Format("2006-01-02")))
	timeParts = append(timeParts, fmt.Sprintf("Updated: %s", issue.UpdatedAt.Format("2006-01-02")))

	if issue.DueAt != nil {
		timeParts = append(timeParts, fmt.Sprintf("Due: %s", issue.DueAt.Format("2006-01-02")))
	}
	if issue.DeferUntil != nil {
		timeParts = append(timeParts, fmt.Sprintf("Deferred: %s", issue.DeferUntil.Format("2006-01-02")))
	}
	if len(timeParts) > 0 {
		lines = append(lines, strings.Join(timeParts, " · "))
	}

	// Line 3: Close reason (if closed) — plain in agent mode (bd-uh22f)
	if issue.Status == types.StatusClosed && issue.CloseReason != "" {
		if agentMode {
			lines = append(lines, fmt.Sprintf("Close reason: %s", issue.CloseReason))
		} else {
			lines = append(lines, ui.RenderMuted(fmt.Sprintf("Close reason: %s", issue.CloseReason)))
		}
	}

	// Line 4: External ref (if exists)
	if issue.ExternalRef != nil && *issue.ExternalRef != "" {
		lines = append(lines, fmt.Sprintf("External: %s", *issue.ExternalRef))
	}

	// Line 5: Jack metadata (target, expiry) when viewing a jack bead (bd-fiil6)
	if issue.IssueType == types.TypeJack && len(issue.Metadata) > 0 {
		var meta map[string]interface{}
		if json.Unmarshal(issue.Metadata, &meta) == nil {
			jackParts := []string{}
			if target, _ := meta["jack_target"].(string); target != "" {
				jackParts = append(jackParts, fmt.Sprintf("Target: %s", target))
			}
			if issue.JackExpiresAt != nil {
				remaining := time.Until(*issue.JackExpiresAt)
				if remaining <= 0 {
					jackParts = append(jackParts, fmt.Sprintf("EXPIRED %s ago", jackShowFormatDuration(-remaining)))
				} else {
					jackParts = append(jackParts, fmt.Sprintf("Expires: %s (%s remaining)",
						issue.JackExpiresAt.Format("2006-01-02 15:04 MST"), jackShowFormatDuration(remaining)))
				}
			}
			if revertPlan, _ := meta["jack_revert_plan"].(string); revertPlan != "" {
				jackParts = append(jackParts, fmt.Sprintf("Revert: %s", revertPlan))
			}
			if len(jackParts) > 0 {
				lines = append(lines, strings.Join(jackParts, " · "))
			}
		}
	}

	return strings.Join(lines, "\n")
}

// formatDependencyLine formats a single dependency with semantic colors
// Closed items get entire row muted - the work is done, no need for attention
// Agent mode (bd-uh22f): plain text, no color escapes
// Jack dependencies show inline target/expiry info (bd-fiil6)
func formatDependencyLine(prefix string, dep *types.IssueWithDependencyMetadata) string {
	statusIcon := ui.GetStatusIcon(string(dep.Status))

	// Agent mode: plain compact line (bd-uh22f)
	if ui.IsAgentMode() {
		typeTag := ""
		if dep.IssueType == "epic" || dep.IssueType == "bug" {
			typeTag = "(" + strings.ToUpper(string(dep.IssueType)) + ") "
		}
		line := fmt.Sprintf("  %s %s %s: %s%s P%d [%s]",
			prefix, statusIcon, dep.ID, typeTag, dep.Title, dep.Priority, dep.Status)
		// Append jack metadata inline (bd-fiil6)
		if dep.IssueType == types.TypeJack {
			line += formatJackInline(&dep.Issue)
		}
		return line
	}

	// Closed items: mute entire row since the work is complete
	if dep.Status == types.StatusClosed {
		return fmt.Sprintf("  %s %s %s: %s %s",
			prefix, statusIcon,
			ui.RenderMuted(dep.ID),
			ui.RenderMuted(dep.Title),
			ui.RenderMuted(fmt.Sprintf("● P%d", dep.Priority)))
	}

	// Active items: ID with status color, priority with semantic color
	style := ui.GetStatusStyle(string(dep.Status))
	idStr := style.Render(dep.ID)
	priorityTag := ui.RenderPriority(dep.Priority)

	// Type indicator for epics/bugs
	typeStr := ""
	if dep.IssueType == "epic" {
		typeStr = ui.TypeEpicStyle.Render("(EPIC)") + " "
	} else if dep.IssueType == "bug" {
		typeStr = ui.TypeBugStyle.Render("(BUG)") + " "
	}

	line := fmt.Sprintf("  %s %s %s: %s%s %s", prefix, statusIcon, idStr, typeStr, dep.Title, priorityTag)
	// Append jack metadata inline (bd-fiil6)
	if dep.IssueType == types.TypeJack {
		line += formatJackInline(&dep.Issue)
	}
	return line
}

// formatJackInline returns inline jack info (target, expiry) for dependency display. (bd-fiil6)
func formatJackInline(issue *types.Issue) string {
	var parts []string
	if len(issue.Metadata) > 0 {
		var meta map[string]interface{}
		if json.Unmarshal(issue.Metadata, &meta) == nil {
			if target, _ := meta["jack_target"].(string); target != "" {
				parts = append(parts, "target="+target)
			}
		}
	}
	if issue.JackExpiresAt != nil {
		remaining := time.Until(*issue.JackExpiresAt)
		if remaining <= 0 {
			parts = append(parts, "EXPIRED "+jackShowFormatDuration(-remaining)+" ago")
		} else {
			parts = append(parts, "expires in "+jackShowFormatDuration(remaining))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, ", ") + "]"
}

// jackShowFormatDuration formats a duration for jack display in bd show. (bd-fiil6)
func jackShowFormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}

// formatSimpleDependencyLine formats a dependency without metadata (fallback)
// Closed items get entire row muted - the work is done, no need for attention
// Agent mode (bd-uh22f): plain text, no color escapes
func formatSimpleDependencyLine(prefix string, dep *types.Issue) string {
	statusIcon := ui.GetStatusIcon(string(dep.Status))

	// Agent mode: plain compact line (bd-uh22f)
	if ui.IsAgentMode() {
		return fmt.Sprintf("  %s %s %s: %s P%d [%s]",
			prefix, statusIcon, dep.ID, dep.Title, dep.Priority, dep.Status)
	}

	// Closed items: mute entire row since the work is complete
	if dep.Status == types.StatusClosed {
		return fmt.Sprintf("  %s %s %s: %s %s",
			prefix, statusIcon,
			ui.RenderMuted(dep.ID),
			ui.RenderMuted(dep.Title),
			ui.RenderMuted(fmt.Sprintf("● P%d", dep.Priority)))
	}

	// Active items: use semantic colors
	style := ui.GetStatusStyle(string(dep.Status))
	idStr := style.Render(dep.ID)
	priorityTag := ui.RenderPriority(dep.Priority)

	return fmt.Sprintf("  %s %s %s: %s %s", prefix, statusIcon, idStr, dep.Title, priorityTag)
}

// showIssueRefs displays issues that reference the given issue(s), grouped by relationship type
func showIssueRefs(_ context.Context, args []string, resolvedIDs []string, routedArgs []string, jsonOut bool) {
	// Collect all refs for all issues
	allRefs := make(map[string][]*types.IssueWithDependencyMetadata)

	// Handle routed IDs via centralized routing (bd-z344)
	forEachRoutedID(routedArgs, func(resolvedID string, routedClient *rpc.Client) error {
		showResp, showErr := routedClient.Show(&rpc.ShowArgs{ID: resolvedID})
		routedClient.Close()
		if showErr != nil {
			return showErr
		}
		var details types.IssueDetails
		if err := json.Unmarshal(showResp.Data, &details); err != nil {
			return err
		}
		allRefs[resolvedID] = details.Dependents
		return nil
	})

	// Handle resolved IDs - use Show RPC which returns IssueDetails with Dependents
	for _, id := range resolvedIDs {
		showArgs := &rpc.ShowArgs{ID: id}
		resp, err := daemonClient.Show(showArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
			continue
		}
		var details types.IssueDetails
		if err := json.Unmarshal(resp.Data, &details); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing response for %s: %v\n", id, err)
			continue
		}
		allRefs[id] = details.Dependents
	}

	// Output results
	if jsonOut {
		outputJSON(allRefs)
		return
	}

	// Display refs grouped by issue and relationship type
	agentMode := ui.IsAgentMode()
	for issueID, refs := range allRefs {
		if len(refs) == 0 {
			if agentMode {
				fmt.Printf("\n%s: No references found\n", issueID)
			} else {
				fmt.Printf("\n%s: No references found\n", ui.RenderAccent(issueID))
			}
			continue
		}

		if agentMode {
			fmt.Printf("\nReferences to %s:\n", issueID)
		} else {
			fmt.Printf("\n%s References to %s:\n", ui.RenderAccent("📎"), issueID)
		}

		// Group refs by type
		refsByType := make(map[types.DependencyType][]*types.IssueWithDependencyMetadata)
		for _, ref := range refs {
			refsByType[ref.DependencyType] = append(refsByType[ref.DependencyType], ref)
		}

		// Display each type
		typeOrder := []types.DependencyType{
			types.DepUntil, types.DepCausedBy, types.DepValidates,
			types.DepBlocks, types.DepParentChild, types.DepRelatesTo,
			types.DepTracks, types.DepDiscoveredFrom, types.DepRelated,
			types.DepSupersedes, types.DepDuplicates, types.DepRepliesTo,
			types.DepApprovedBy, types.DepAuthoredBy, types.DepAssignedTo,
		}

		// First show types in order, then any others
		shown := make(map[types.DependencyType]bool)
		for _, depType := range typeOrder {
			if refs, ok := refsByType[depType]; ok {
				displayRefGroup(depType, refs)
				shown[depType] = true
			}
		}
		// Show any remaining types
		for depType, refs := range refsByType {
			if !shown[depType] {
				displayRefGroup(depType, refs)
			}
		}
		fmt.Println()
	}
}

// displayRefGroup displays a group of references with a given type
// Closed items get entire row muted - the work is done, no need for attention
// Agent mode (bd-uh22f): plain text, no color escapes
func displayRefGroup(depType types.DependencyType, refs []*types.IssueWithDependencyMetadata) {
	agentMode := ui.IsAgentMode()

	if agentMode {
		fmt.Printf("\n  %s (%d):\n", depType, len(refs))
	} else {
		emoji := getRefTypeEmoji(depType)
		fmt.Printf("\n  %s %s (%d):\n", emoji, depType, len(refs))
	}

	for _, ref := range refs {
		// Agent mode: plain compact line (bd-uh22f)
		if agentMode {
			fmt.Printf("    %s: %s [P%d - %s]\n", ref.ID, ref.Title, ref.Priority, ref.Status)
			continue
		}

		// Closed items: mute entire row since the work is complete
		if ref.Status == types.StatusClosed {
			fmt.Printf("    %s: %s %s\n",
				ui.RenderMuted(ref.ID),
				ui.RenderMuted(ref.Title),
				ui.RenderMuted(fmt.Sprintf("[P%d - %s]", ref.Priority, ref.Status)))
			continue
		}

		// Active items: color ID based on status
		var idStr string
		switch ref.Status {
		case types.StatusOpen:
			idStr = ui.StatusOpenStyle.Render(ref.ID)
		case types.StatusInProgress:
			idStr = ui.StatusInProgressStyle.Render(ref.ID)
		case types.StatusBlocked:
			idStr = ui.StatusBlockedStyle.Render(ref.ID)
		default:
			idStr = ref.ID
		}
		fmt.Printf("    %s: %s [P%d - %s]\n", idStr, ref.Title, ref.Priority, ref.Status)
	}
}

// getRefTypeEmoji returns an emoji for a dependency/reference type
func getRefTypeEmoji(depType types.DependencyType) string {
	switch depType {
	case types.DepUntil:
		return "⏳" // Hourglass - waiting until
	case types.DepCausedBy:
		return "⚡" // Lightning - triggered by
	case types.DepValidates:
		return "✅" // Checkmark - validates
	case types.DepBlocks:
		return "🚫" // Blocked
	case types.DepParentChild:
		return "↳" // Child arrow
	case types.DepRelatesTo, types.DepRelated:
		return "↔" // Bidirectional
	case types.DepTracks:
		return "👁" // Watching
	case types.DepDiscoveredFrom:
		return "◊" // Diamond - discovered
	case types.DepSupersedes:
		return "⬆" // Upgrade
	case types.DepDuplicates:
		return "🔄" // Duplicate
	case types.DepRepliesTo:
		return "💬" // Chat
	case types.DepApprovedBy:
		return "👍" // Approved
	case types.DepAuthoredBy:
		return "✏" // Authored
	case types.DepAssignedTo:
		return "👤" // Assigned
	default:
		return "→" // Default arrow
	}
}

// showIssueChildren displays only the children of the specified issue(s)
func showIssueChildren(_ context.Context, args []string, resolvedIDs []string, routedArgs []string, jsonOut bool, shortMode bool) {
	// Collect all children for all issues
	allChildren := make(map[string][]*types.IssueWithDependencyMetadata)

	// Handle routed IDs via centralized routing (bd-z344)
	forEachRoutedID(routedArgs, func(resolvedID string, routedClient *rpc.Client) error {
		showResp, showErr := routedClient.Show(&rpc.ShowArgs{ID: resolvedID})
		routedClient.Close()
		if showErr != nil {
			return showErr
		}
		var details types.IssueDetails
		if err := json.Unmarshal(showResp.Data, &details); err != nil {
			return err
		}
		if _, exists := allChildren[resolvedID]; !exists {
			allChildren[resolvedID] = []*types.IssueWithDependencyMetadata{}
		}
		for _, dep := range details.Dependents {
			if dep.DependencyType == types.DepParentChild {
				allChildren[resolvedID] = append(allChildren[resolvedID], dep)
			}
		}
		return nil
	})

	// Handle resolved IDs - use Show RPC which returns IssueDetails with Dependents
	for _, id := range resolvedIDs {
		showArgs := &rpc.ShowArgs{ID: id}
		resp, err := daemonClient.Show(showArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", id, err)
			continue
		}
		var details types.IssueDetails
		if err := json.Unmarshal(resp.Data, &details); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing response for %s: %v\n", id, err)
			continue
		}
		// Filter dependents for parent-child relationships
		if _, exists := allChildren[id]; !exists {
			allChildren[id] = []*types.IssueWithDependencyMetadata{}
		}
		for _, dep := range details.Dependents {
			if dep.DependencyType == types.DepParentChild {
				allChildren[id] = append(allChildren[id], dep)
			}
		}
	}

	// Output results
	if jsonOut {
		outputJSON(allChildren)
		return
	}

	// Display children
	agentMode := ui.IsAgentMode()
	for issueID, children := range allChildren {
		if len(children) == 0 {
			if agentMode {
				fmt.Printf("%s: No children found\n", issueID)
			} else {
				fmt.Printf("%s: No children found\n", ui.RenderAccent(issueID))
			}
			continue
		}

		if agentMode {
			fmt.Printf("Children of %s (%d):\n", issueID, len(children))
		} else {
			fmt.Printf("%s Children of %s (%d):\n", ui.RenderAccent("↳"), issueID, len(children))
		}
		for _, child := range children {
			if shortMode {
				fmt.Printf("  %s\n", formatShortIssue(&child.Issue))
			} else {
				fmt.Println(formatDependencyLine("↳", child))
			}
		}
		fmt.Println()
	}
}

// showIssueAsOf displays issues as they existed at a specific commit or branch ref.
// This requires a versioned storage backend (e.g., Dolt).
func showIssueAsOf(ctx context.Context, args []string, ref string, shortMode bool) {
	// Check if storage supports versioning
	vs, ok := storage.AsVersioned(store)
	if !ok {
		FatalErrorRespectJSON("--as-of requires Dolt backend (current backend does not support versioning)")
	}

	var allIssues []*types.Issue
	for idx, id := range args {
		issue, err := vs.AsOf(ctx, id, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching %s as of %s: %v\n", id, ref, err)
			continue
		}
		if issue == nil {
			fmt.Fprintf(os.Stderr, "Issue %s did not exist at %s\n", id, ref)
			continue
		}

		if shortMode {
			fmt.Println(formatShortIssue(issue))
			continue
		}

		if jsonOutput {
			allIssues = append(allIssues, issue)
			continue
		}

		agentMode := ui.IsAgentMode()
		if idx > 0 {
			if agentMode {
				fmt.Println() // blank line separator in agent mode (bd-uh22f)
			} else {
				fmt.Println("\n" + ui.RenderMuted(strings.Repeat("-", 60)))
			}
		}

		// Display header with ref indicator
		if agentMode {
			fmt.Printf("\n%s (as of %s)\n", formatIssueHeader(issue), ref)
		} else {
			fmt.Printf("\n%s (as of %s)\n", formatIssueHeader(issue), ui.RenderMuted(ref))
		}
		fmt.Println(formatIssueMetadata(issue))

		if issue.Description != "" {
			if agentMode {
				fmt.Printf("\nDESCRIPTION\n%s\n", ui.RenderMarkdown(issue.Description))
			} else {
				fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), ui.RenderMarkdown(issue.Description))
			}
		}
		fmt.Println()
	}

	if jsonOutput && len(allIssues) > 0 {
		outputJSON(allIssues)
	}
}

func init() {
	showCmd.Flags().Bool("thread", false, "Show full conversation thread (for messages)")
	showCmd.Flags().Bool("short", false, "Show compact one-line output per issue")
	showCmd.Flags().Bool("refs", false, "Show issues that reference this issue (reverse lookup)")
	showCmd.Flags().Bool("children", false, "Show only the children of this issue")
	showCmd.Flags().String("as-of", "", "Show issue as it existed at a specific commit hash or branch (requires Dolt)")
	showCmd.Flags().StringArray("id", nil, "Issue ID (use for IDs that look like flags, e.g., --id=gt--xyz)")
	showCmd.Flags().Bool("local-time", false, "Show timestamps in local time instead of UTC")
	showCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(showCmd)
}
