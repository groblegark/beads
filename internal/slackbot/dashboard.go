package slackbot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"github.com/steveyegge/beads/internal/rpc"
)

// DashboardConfig holds configuration for the agent activity dashboard.
type DashboardConfig struct {
	Enabled           bool
	ChannelID         string // If empty, uses the bot's default channel.
	MaxWorkingShown   int    // Max working agents to display (default 10).
	MaxIdleShown      int    // Max idle agents to display (default 5).
	MaxDeadShown      int    // Max dead agents to display (default 5).
	MaxUnclaimedShown int    // Max unclaimed tasks to display (default 5).
}

// DashboardMessage tracks the posted dashboard message for later updates.
type DashboardMessage struct {
	ChannelID string
	Timestamp string
	LastHash  string // Content hash for change detection.
}

// BuildDashboardBlocks calls AgentRoster and renders the dashboard as Block Kit blocks.
func BuildDashboardBlocks(ctx context.Context, provider DecisionProvider, cfg DashboardConfig) ([]slack.Block, string, error) {
	roster, err := provider.AgentRoster(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("agent roster: %w", err)
	}

	blocks, hash := renderDashboardBlocks(roster, cfg)
	return blocks, hash, nil
}

// renderDashboardBlocks builds Block Kit blocks from roster data.
// Returns the blocks and a content hash for change detection.
func renderDashboardBlocks(roster *rpc.AgentRosterResult, cfg DashboardConfig) ([]slack.Block, string) {
	if cfg.MaxWorkingShown == 0 {
		cfg.MaxWorkingShown = 10
	}
	if cfg.MaxIdleShown == 0 {
		cfg.MaxIdleShown = 5
	}
	if cfg.MaxDeadShown == 0 {
		cfg.MaxDeadShown = 5
	}
	if cfg.MaxUnclaimedShown == 0 {
		cfg.MaxUnclaimedShown = 5
	}

	// Classify agents into working/idle/dead buckets.
	var working, idle, dead []rpc.AgentRosterEntry
	for _, a := range roster.Actors {
		switch {
		case a.Reaped:
			dead = append(dead, a)
		case a.TaskID != "":
			working = append(working, a)
		default:
			idle = append(idle, a)
		}
	}

	// Sort working by events/min descending (most active first).
	sort.Slice(working, func(i, j int) bool {
		return working[i].EventsPerMin > working[j].EventsPerMin
	})
	// Sort idle by idle time ascending.
	sort.Slice(idle, func(i, j int) bool {
		return idle[i].IdleSecs < idle[j].IdleSecs
	})
	// Sort dead by idle time ascending (most recently dead first).
	sort.Slice(dead, func(i, j int) bool {
		return dead[i].IdleSecs < dead[j].IdleSecs
	})

	var blocks []slack.Block

	// ── Header ──
	totalAgents := len(roster.Actors)
	headerText := fmt.Sprintf(":factory: *Agent Dashboard* · %d agents · Updated %s",
		totalAgents, time.Now().UTC().Format("15:04 UTC"))
	blocks = append(blocks, slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", headerText, false, false),
		nil, nil))

	// ── Summary counters ──
	summaryText := fmt.Sprintf(":large_green_circle: %d working  ·  :white_circle: %d idle  ·  :red_circle: %d dead",
		len(working), len(idle), len(dead))
	blocks = append(blocks, slack.NewContextBlock("",
		slack.NewTextBlockObject("mrkdwn", summaryText, false, false)))

	// ── Working section ──
	if len(working) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		sectionLabel := fmt.Sprintf("*Working (%d)*", len(working))
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", sectionLabel, false, false)))

		shown := working
		if len(shown) > cfg.MaxWorkingShown {
			shown = shown[:cfg.MaxWorkingShown]
		}
		for _, a := range shown {
			blocks = append(blocks, agentWorkingBlocks(a)...)
		}
		if overflow := len(working) - cfg.MaxWorkingShown; overflow > 0 {
			blocks = append(blocks, slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_+%d more working agents_", overflow), false, false)))
		}
	}

	// ── Idle section ──
	if len(idle) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		sectionLabel := fmt.Sprintf("*Idle (%d)*", len(idle))
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", sectionLabel, false, false)))

		shown := idle
		if len(shown) > cfg.MaxIdleShown {
			shown = shown[:cfg.MaxIdleShown]
		}
		for _, a := range shown {
			blocks = append(blocks, agentIdleBlock(a))
		}
		if overflow := len(idle) - cfg.MaxIdleShown; overflow > 0 {
			blocks = append(blocks, slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_+%d more idle agents_", overflow), false, false)))
		}
	}

	// ── Dead section ──
	if len(dead) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		sectionLabel := fmt.Sprintf("*Dead (%d)*", len(dead))
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", sectionLabel, false, false)))

		shown := dead
		if len(shown) > cfg.MaxDeadShown {
			shown = shown[:cfg.MaxDeadShown]
		}
		for _, a := range shown {
			blocks = append(blocks, agentDeadBlock(a))
		}
		if overflow := len(dead) - cfg.MaxDeadShown; overflow > 0 {
			blocks = append(blocks, slack.NewContextBlock("",
				slack.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("_+%d more dead agents_", overflow), false, false)))
		}
	}

	// ── Unclaimed work ──
	if len(roster.UnclaimedTasks) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())
		sectionLabel := fmt.Sprintf("*Unclaimed Ready Work (%d)*", len(roster.UnclaimedTasks))
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", sectionLabel, false, false)))

		shown := roster.UnclaimedTasks
		if len(shown) > cfg.MaxUnclaimedShown {
			shown = shown[:cfg.MaxUnclaimedShown]
		}
		var lines []string
		for _, t := range shown {
			line := fmt.Sprintf("• [P%d] %s %s", t.Priority, t.ID, truncateForSlack(t.Title, 60))
			lines = append(lines, line)
		}
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", strings.Join(lines, "\n"), false, false),
			nil, nil))
	}

	// ── Conflict warnings ──
	var conflicts []string
	for _, a := range roster.Actors {
		if a.Conflict && len(a.ConflictWith) > 0 {
			names := append([]string{extractAgentShortName(a.Actor)}, a.ConflictWith...)
			loc := ""
			if a.Repo != "" && a.Branch != "" {
				loc = fmt.Sprintf(" on %s/%s", a.Repo, a.Branch)
			}
			conflicts = append(conflicts, strings.Join(names, " + ")+loc)
		}
	}
	if len(conflicts) > 0 {
		// Deduplicate conflict pairs (A+B and B+A).
		seen := make(map[string]bool)
		var unique []string
		for _, c := range conflicts {
			if !seen[c] {
				seen[c] = true
				unique = append(unique, c)
			}
		}
		blocks = append(blocks, slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				":warning: *Conflicts:* "+strings.Join(unique, " | "), false, false)))
	}

	// Build content hash for change detection.
	hash := buildRosterHash(roster)

	return blocks, hash
}

// agentWorkingBlocks returns 2 blocks for a working agent: section + context.
func agentWorkingBlocks(a rpc.AgentRosterEntry) []slack.Block {
	name := extractAgentShortName(a.Actor)
	taskRef := "no task"
	if a.TaskID != "" {
		taskRef = fmt.Sprintf("%s %s", a.TaskID, truncateForSlack(a.TaskTitle, 50))
	}
	line := fmt.Sprintf(":large_green_circle: *%s* · %s", name, taskRef)

	meta := []string{}
	if a.Rig != "" {
		meta = append(meta, a.Rig)
	} else if a.Repo != "" {
		meta = append(meta, a.Repo)
	}
	meta = append(meta, fmt.Sprintf("idle %s", formatIdleDuration(a.IdleSecs)))
	if a.EventsPerMin > 0 {
		meta = append(meta, fmt.Sprintf("%.0f events/min", a.EventsPerMin))
	}

	return []slack.Block{
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", line, false, false),
			nil, nil),
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn", strings.Join(meta, " · "), false, false)),
	}
}

// agentIdleBlock returns 1 block for an idle agent.
func agentIdleBlock(a rpc.AgentRosterEntry) slack.Block {
	name := extractAgentShortName(a.Actor)
	line := fmt.Sprintf(":white_circle: *%s* · no task · idle %s", name, formatIdleDuration(a.IdleSecs))
	return slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", line, false, false),
		nil, nil)
}

// agentDeadBlock returns 1 block for a dead/reaped agent.
func agentDeadBlock(a rpc.AgentRosterEntry) slack.Block {
	name := extractAgentShortName(a.Actor)
	line := fmt.Sprintf(":red_circle: *%s* · reaped %s ago", name, formatIdleDuration(a.IdleSecs))
	return slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", line, false, false),
		nil, nil)
}

// formatIdleDuration formats seconds into a human-readable duration string.
func formatIdleDuration(secs float64) string {
	d := time.Duration(secs * float64(time.Second))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh%dm", h, m)
	}
}

// buildRosterHash produces a string hash of the roster state for change detection.
// Only the meaningful fields are included (agent names, states, task IDs).
func buildRosterHash(roster *rpc.AgentRosterResult) string {
	var parts []string
	for _, a := range roster.Actors {
		state := "idle"
		if a.Reaped {
			state = "dead"
		} else if a.TaskID != "" {
			state = "working"
		}
		parts = append(parts, fmt.Sprintf("%s:%s:%s", a.Actor, state, a.TaskID))
	}
	sort.Strings(parts)
	for _, t := range roster.UnclaimedTasks {
		parts = append(parts, fmt.Sprintf("unclaimed:%s", t.ID))
	}
	return strings.Join(parts, "|")
}
