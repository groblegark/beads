package slackbot

import (
	"testing"

	"github.com/steveyegge/beads/internal/rpc"
)

func TestRenderDashboardBlocks_Empty(t *testing.T) {
	roster := &rpc.AgentRosterResult{}
	cfg := DashboardConfig{}

	blocks, hash := renderDashboardBlocks(roster, cfg)

	// Should have header + summary = 2 blocks minimum.
	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", len(blocks))
	}
	if hash != "" {
		t.Errorf("expected empty hash for empty roster, got %q", hash)
	}
}

func TestRenderDashboardBlocks_WorkingIdleDead(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "wise-fish", TaskID: "bd-uvuqp", TaskTitle: "Build dashboard", EventsPerMin: 12.5, IdleSecs: 2},
			{Actor: "snug-rook", TaskID: "bd-h6m6n", TaskTitle: "Cut coop release", EventsPerMin: 8.0, IdleSecs: 30},
			{Actor: "fair-jay-1", IdleSecs: 5},
			{Actor: "pure-lynx", Reaped: true, IdleSecs: 300},
		},
		UnclaimedTasks: []rpc.UnclaimedTask{
			{ID: "bd-abc12", Title: "Fix tests", Priority: 1},
		},
		Working: 2,
		Idle:    1,
		Dead:    1,
	}
	cfg := DashboardConfig{}

	blocks, hash := renderDashboardBlocks(roster, cfg)

	// Header(1) + Summary(1) + Working divider+label(2) + 2 working agents × 2 blocks(4)
	// + Idle divider+label(2) + 1 idle(1) + Dead divider+label(2) + 1 dead(1)
	// + Unclaimed divider+label+list(3) = 16
	if len(blocks) < 10 {
		t.Fatalf("expected at least 10 blocks, got %d", len(blocks))
	}
	if hash == "" {
		t.Error("expected non-empty hash")
	}
}

func TestRenderDashboardBlocks_MaxShown(t *testing.T) {
	var actors []rpc.AgentRosterEntry
	for i := 0; i < 15; i++ {
		actors = append(actors, rpc.AgentRosterEntry{
			Actor:  "agent-" + string(rune('a'+i)),
			TaskID: "bd-" + string(rune('a'+i)),
		})
	}
	roster := &rpc.AgentRosterResult{Actors: actors, Working: 15}
	cfg := DashboardConfig{MaxWorkingShown: 3}

	blocks, _ := renderDashboardBlocks(roster, cfg)

	// Should cap working agents at 3, plus show "+12 more" overflow.
	// Header(1) + Summary(1) + Working divider+label(2) + 3×2(6) + overflow(1) = 11
	if len(blocks) < 11 {
		t.Fatalf("expected at least 11 blocks, got %d", len(blocks))
	}
}

func TestRenderDashboardBlocks_Conflicts(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "wise-fish", TaskID: "bd-1", Conflict: true, ConflictWith: []string{"snug-rook"}, Repo: "beads", Branch: "main"},
			{Actor: "snug-rook", TaskID: "bd-2", Conflict: true, ConflictWith: []string{"wise-fish"}, Repo: "beads", Branch: "main"},
		},
	}
	cfg := DashboardConfig{}

	blocks, _ := renderDashboardBlocks(roster, cfg)

	// Should have a conflict warning block.
	if len(blocks) < 3 {
		t.Fatalf("expected conflict blocks, got %d total", len(blocks))
	}
}

func TestFormatIdleDuration(t *testing.T) {
	tests := []struct {
		secs float64
		want string
	}{
		{0, "0s"},
		{5, "5s"},
		{59, "59s"},
		{60, "1m"},
		{90, "1m30s"},
		{3600, "1h0m"},
		{3661, "1h1m"},
	}
	for _, tt := range tests {
		got := formatIdleDuration(tt.secs)
		if got != tt.want {
			t.Errorf("formatIdleDuration(%v) = %q, want %q", tt.secs, got, tt.want)
		}
	}
}

func TestBuildRosterHash_Deterministic(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "b-agent", TaskID: "bd-2"},
			{Actor: "a-agent", TaskID: "bd-1"},
		},
	}
	h1 := buildRosterHash(roster)
	h2 := buildRosterHash(roster)
	if h1 != h2 {
		t.Errorf("hash not deterministic: %q != %q", h1, h2)
	}
	if h1 == "" {
		t.Error("expected non-empty hash")
	}
}
