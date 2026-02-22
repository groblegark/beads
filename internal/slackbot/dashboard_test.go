package slackbot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/slack-go/slack"
	"github.com/steveyegge/beads/internal/eventbus"
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

// ── bd-kfsyj: additional unit tests ──────────────────────────────────────

// --- Block Kit structure tests ---

func TestRenderDashboardBlocks_HeaderContainsAgentCount(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a1", TaskID: "bd-1"},
			{Actor: "a2", TaskID: "bd-2"},
			{Actor: "a3"},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})

	// First block is the header section.
	if len(blocks) < 1 {
		t.Fatal("no blocks rendered")
	}
	headerJSON, err := json.Marshal(blocks[0])
	if err != nil {
		t.Fatal(err)
	}
	headerStr := string(headerJSON)
	if !strings.Contains(headerStr, "3 agents") {
		t.Errorf("header should contain '3 agents', got: %s", headerStr)
	}
	if !strings.Contains(headerStr, "Agent Dashboard") {
		t.Errorf("header should contain 'Agent Dashboard', got: %s", headerStr)
	}
}

func TestRenderDashboardBlocks_SummaryCountersMatchEmoji(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "worker-1", TaskID: "bd-1"},
			{Actor: "idle-1"},
			{Actor: "dead-1", Reaped: true},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})

	if len(blocks) < 2 {
		t.Fatal("expected at least 2 blocks (header + summary)")
	}
	summaryJSON, _ := json.Marshal(blocks[1])
	summaryStr := string(summaryJSON)
	// Verify emoji indicators
	if !strings.Contains(summaryStr, ":large_green_circle:") {
		t.Error("summary missing green circle emoji for working")
	}
	if !strings.Contains(summaryStr, ":white_circle:") {
		t.Error("summary missing white circle emoji for idle")
	}
	if !strings.Contains(summaryStr, ":red_circle:") {
		t.Error("summary missing red circle emoji for dead")
	}
	// Verify counts
	if !strings.Contains(summaryStr, "1 working") {
		t.Errorf("summary should show '1 working', got: %s", summaryStr)
	}
	if !strings.Contains(summaryStr, "1 idle") {
		t.Errorf("summary should show '1 idle', got: %s", summaryStr)
	}
	if !strings.Contains(summaryStr, "1 dead") {
		t.Errorf("summary should show '1 dead', got: %s", summaryStr)
	}
}

func TestRenderDashboardBlocks_WorkingSortedByEventsPerMin(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "slow-agent", TaskID: "bd-1", TaskTitle: "Slow task", EventsPerMin: 2.0},
			{Actor: "fast-agent", TaskID: "bd-2", TaskTitle: "Fast task", EventsPerMin: 20.0},
			{Actor: "mid-agent", TaskID: "bd-3", TaskTitle: "Mid task", EventsPerMin: 10.0},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})

	// Extract all block text to verify ordering.
	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)
	fastIdx := strings.Index(allStr, "fast-agent")
	midIdx := strings.Index(allStr, "mid-agent")
	slowIdx := strings.Index(allStr, "slow-agent")

	if fastIdx < 0 || midIdx < 0 || slowIdx < 0 {
		t.Fatalf("missing agent names in blocks: fast=%d mid=%d slow=%d", fastIdx, midIdx, slowIdx)
	}
	if !(fastIdx < midIdx && midIdx < slowIdx) {
		t.Errorf("working agents should be sorted by events/min desc: fast(%d) < mid(%d) < slow(%d)",
			fastIdx, midIdx, slowIdx)
	}
}

func TestRenderDashboardBlocks_UnclaimedPrioritiesShown(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		UnclaimedTasks: []rpc.UnclaimedTask{
			{ID: "bd-x1", Title: "High priority fix", Priority: 0},
			{ID: "bd-x2", Title: "Medium priority task", Priority: 2},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})
	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)

	if !strings.Contains(allStr, "[P0]") {
		t.Error("unclaimed section should show [P0] priority")
	}
	if !strings.Contains(allStr, "[P2]") {
		t.Error("unclaimed section should show [P2] priority")
	}
	if !strings.Contains(allStr, "Unclaimed Ready Work (2)") {
		t.Error("unclaimed section header should show count")
	}
}

// --- Title truncation tests ---

func TestRenderDashboardBlocks_LongTitlesTruncated(t *testing.T) {
	longTitle := strings.Repeat("x", 100) // 100 chars, should be truncated to 50+...
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "worker", TaskID: "bd-1", TaskTitle: longTitle},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})
	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)

	// Full 100-char title should NOT appear.
	if strings.Contains(allStr, longTitle) {
		t.Error("full 100-char title should be truncated")
	}
	// Truncation marker should appear.
	if !strings.Contains(allStr, "...") {
		t.Error("truncated title should contain '...'")
	}
}

// --- Change detection / hash tests ---

func TestBuildRosterHash_ChangesWhenRosterChanges(t *testing.T) {
	r1 := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a1", TaskID: "bd-1"},
		},
	}
	r2 := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a1", TaskID: "bd-2"}, // different task
		},
	}
	h1 := buildRosterHash(r1)
	h2 := buildRosterHash(r2)
	if h1 == h2 {
		t.Errorf("hash should change when task changes: both %q", h1)
	}
}

func TestBuildRosterHash_ChangesWhenAgentAdded(t *testing.T) {
	r1 := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a1", TaskID: "bd-1"},
		},
	}
	r2 := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a1", TaskID: "bd-1"},
			{Actor: "a2"},
		},
	}
	h1 := buildRosterHash(r1)
	h2 := buildRosterHash(r2)
	if h1 == h2 {
		t.Error("hash should change when agent added")
	}
}

func TestBuildRosterHash_ChangesOnStateTransition(t *testing.T) {
	// Agent goes from working to dead.
	r1 := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a1", TaskID: "bd-1"},
		},
	}
	r2 := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a1", Reaped: true},
		},
	}
	h1 := buildRosterHash(r1)
	h2 := buildRosterHash(r2)
	if h1 == h2 {
		t.Error("hash should change when agent transitions from working to dead")
	}
}

func TestBuildRosterHash_UnclaimedTasksAffectHash(t *testing.T) {
	r1 := &rpc.AgentRosterResult{}
	r2 := &rpc.AgentRosterResult{
		UnclaimedTasks: []rpc.UnclaimedTask{
			{ID: "bd-new", Title: "New task", Priority: 1},
		},
	}
	h1 := buildRosterHash(r1)
	h2 := buildRosterHash(r2)
	if h1 == h2 {
		t.Error("hash should change when unclaimed tasks appear")
	}
}

func TestBuildRosterHash_OrderIndependent(t *testing.T) {
	r1 := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a1", TaskID: "bd-1"},
			{Actor: "a2", TaskID: "bd-2"},
		},
	}
	r2 := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "a2", TaskID: "bd-2"},
			{Actor: "a1", TaskID: "bd-1"},
		},
	}
	h1 := buildRosterHash(r1)
	h2 := buildRosterHash(r2)
	if h1 != h2 {
		t.Errorf("hash should be order-independent: %q != %q", h1, h2)
	}
}

// --- NATS event handling ---

func TestHandleAgentEvent_MarksDirtyOnLifecycleEvents(t *testing.T) {
	lifecycleEvents := []eventbus.EventType{
		eventbus.EventAgentStarted,
		eventbus.EventAgentStopped,
		eventbus.EventAgentCrashed,
		eventbus.EventAgentIdle,
	}

	for _, evtType := range lifecycleEvents {
		t.Run(string(evtType), func(t *testing.T) {
			d := &Dashboard{
				cfg: DashboardConfig{Enabled: true},
			}

			payload := eventbus.AgentEventPayload{AgentID: "test-agent"}
			data, _ := json.Marshal(payload)

			msg := &nats.Msg{
				Subject: "agents." + string(evtType),
				Data:    data,
			}
			// HandleAgentEvent calls msg.Ack() which panics on nil sub.
			// Create a wrapper that catches the panic from Ack().
			func() {
				defer func() { recover() }()
				d.HandleAgentEvent(msg)
			}()

			d.mu.Lock()
			dirty := d.dirty
			d.mu.Unlock()
			if !dirty {
				t.Errorf("HandleAgentEvent(%s) should set dirty=true", evtType)
			}
		})
	}
}

func TestHandleAgentEvent_DisabledDashboardIgnoresEvents(t *testing.T) {
	d := &Dashboard{
		cfg: DashboardConfig{Enabled: false},
	}

	payload := eventbus.AgentEventPayload{AgentID: "test-agent"}
	data, _ := json.Marshal(payload)
	msg := &nats.Msg{
		Subject: "agents." + string(eventbus.EventAgentStarted),
		Data:    data,
	}
	func() {
		defer func() { recover() }()
		d.HandleAgentEvent(msg)
	}()

	d.mu.Lock()
	dirty := d.dirty
	d.mu.Unlock()
	if dirty {
		t.Error("disabled dashboard should not set dirty flag")
	}
}

// --- Dashboard refresh with mock SlackAPI ---

// dashboardMockProvider implements DecisionProvider for dashboard tests.
type dashboardMockProvider struct {
	mu     sync.Mutex
	roster *rpc.AgentRosterResult
}

func (p *dashboardMockProvider) AgentRoster(_ context.Context) (*rpc.AgentRosterResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.roster == nil {
		return &rpc.AgentRosterResult{}, nil
	}
	return p.roster, nil
}
func (p *dashboardMockProvider) ListPending(_ context.Context) ([]Decision, error) {
	return nil, nil
}
func (p *dashboardMockProvider) GetDecision(_ context.Context, id string) (*Decision, error) {
	return nil, fmt.Errorf("not found")
}
func (p *dashboardMockProvider) Resolve(_ context.Context, id string, idx int, rationale, by string) (*Decision, error) {
	return nil, fmt.Errorf("not found")
}
func (p *dashboardMockProvider) ResolveWithText(_ context.Context, id, text, by string) (*Decision, error) {
	return nil, fmt.Errorf("not found")
}
func (p *dashboardMockProvider) Cancel(_ context.Context, id string) error { return nil }
func (p *dashboardMockProvider) AddComment(_ context.Context, id, author, text string) error {
	return nil
}
func (p *dashboardMockProvider) PeekAgent(_ context.Context, name string) (string, error) {
	return "", nil
}

// dashboardMockSlack captures PostMessage and UpdateMessage calls.
type dashboardMockSlack struct {
	mu       sync.Mutex
	posts    int
	updates  int
	nextTS   int
	updateFn func() error // optional error injection
}

func (m *dashboardMockSlack) AuthTest() (*slack.AuthTestResponse, error) {
	return &slack.AuthTestResponse{}, nil
}
func (m *dashboardMockSlack) PostMessage(ch string, opts ...slack.MsgOption) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.posts++
	m.nextTS++
	return ch, fmt.Sprintf("1234.%06d", m.nextTS), nil
}
func (m *dashboardMockSlack) PostEphemeral(ch, uid string, opts ...slack.MsgOption) (string, error) {
	return "", nil
}
func (m *dashboardMockSlack) UpdateMessage(ch, ts string, opts ...slack.MsgOption) (string, string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateFn != nil {
		if err := m.updateFn(); err != nil {
			return "", "", "", err
		}
	}
	m.updates++
	return ch, ts, "", nil
}
func (m *dashboardMockSlack) DeleteMessage(ch, ts string) (string, string, error) {
	return ch, ts, nil
}
func (m *dashboardMockSlack) OpenView(id string, v slack.ModalViewRequest) (*slack.ViewResponse, error) {
	return &slack.ViewResponse{}, nil
}
func (m *dashboardMockSlack) CreateConversation(p slack.CreateConversationParams) (*slack.Channel, error) {
	return nil, nil
}
func (m *dashboardMockSlack) GetConversations(p *slack.GetConversationsParameters) ([]slack.Channel, string, error) {
	return nil, "", nil
}
func (m *dashboardMockSlack) GetConversationReplies(p *slack.GetConversationRepliesParameters) ([]slack.Message, bool, string, error) {
	return nil, false, "", nil
}
func (m *dashboardMockSlack) InviteUsersToConversation(ch string, users ...string) (*slack.Channel, error) {
	return nil, nil
}
func (m *dashboardMockSlack) JoinConversation(ch string) (*slack.Channel, string, []string, error) {
	return nil, "", nil, nil
}
func (m *dashboardMockSlack) OpenConversation(p *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error) {
	return nil, false, false, nil
}
func (m *dashboardMockSlack) GetUserInfo(id string) (*slack.User, error) { return nil, nil }

func TestDashboard_RefreshSkipsWhenHashUnchanged(t *testing.T) {
	provider := &dashboardMockProvider{
		roster: &rpc.AgentRosterResult{
			Actors: []rpc.AgentRosterEntry{
				{Actor: "a1", TaskID: "bd-1"},
			},
		},
	}
	mockSlack := &dashboardMockSlack{}
	cfg := DashboardConfig{
		Enabled:   true,
		ChannelID: "C-test",
		Interval:  time.Second,
	}

	d := NewDashboard(mockSlack, provider, nil, cfg)

	// Simulate having an existing message with the correct hash.
	_, hash := renderDashboardBlocks(provider.roster, cfg)
	d.msg = &DashboardMessage{
		ChannelID: "C-test",
		Timestamp: "1234.000001",
		LastHash:  hash,
	}
	d.lastUpdate = time.Now().Add(-10 * time.Second) // past min interval

	// Refresh should detect same hash and skip the update.
	d.refresh(context.Background())

	mockSlack.mu.Lock()
	updates := mockSlack.updates
	mockSlack.mu.Unlock()

	if updates != 0 {
		t.Errorf("expected 0 updates when hash unchanged, got %d", updates)
	}
}

func TestDashboard_RefreshUpdatesWhenHashChanges(t *testing.T) {
	provider := &dashboardMockProvider{
		roster: &rpc.AgentRosterResult{
			Actors: []rpc.AgentRosterEntry{
				{Actor: "a1", TaskID: "bd-1"},
			},
		},
	}
	mockSlack := &dashboardMockSlack{}
	cfg := DashboardConfig{
		Enabled:   true,
		ChannelID: "C-test",
	}

	d := NewDashboard(mockSlack, provider, nil, cfg)

	// Set an old hash that doesn't match current roster.
	d.msg = &DashboardMessage{
		ChannelID: "C-test",
		Timestamp: "1234.000001",
		LastHash:  "stale-hash",
	}
	d.lastUpdate = time.Now().Add(-10 * time.Second)

	d.refresh(context.Background())

	mockSlack.mu.Lock()
	updates := mockSlack.updates
	mockSlack.mu.Unlock()

	if updates != 1 {
		t.Errorf("expected 1 update when hash changed, got %d", updates)
	}
}

func TestDashboard_RefreshRespectsMinInterval(t *testing.T) {
	provider := &dashboardMockProvider{
		roster: &rpc.AgentRosterResult{
			Actors: []rpc.AgentRosterEntry{{Actor: "a1", TaskID: "bd-1"}},
		},
	}
	mockSlack := &dashboardMockSlack{}
	cfg := DashboardConfig{
		Enabled:   true,
		ChannelID: "C-test",
	}

	d := NewDashboard(mockSlack, provider, nil, cfg)
	d.msg = &DashboardMessage{
		ChannelID: "C-test",
		Timestamp: "1234.000001",
		LastHash:  "stale",
	}
	d.lastUpdate = time.Now() // just updated
	d.dirty = true

	d.refresh(context.Background())

	mockSlack.mu.Lock()
	updates := mockSlack.updates
	mockSlack.mu.Unlock()

	if updates != 0 {
		t.Errorf("expected 0 updates within min interval, got %d", updates)
	}

	// dirty should be re-set for next tick
	d.mu.Lock()
	stillDirty := d.dirty
	d.mu.Unlock()
	if !stillDirty {
		t.Error("dirty flag should be re-set when skipped due to min interval")
	}
}

func TestDashboard_RefreshPostsNewWhenNoMessage(t *testing.T) {
	provider := &dashboardMockProvider{}
	mockSlack := &dashboardMockSlack{}
	cfg := DashboardConfig{
		Enabled:   true,
		ChannelID: "C-test",
	}

	d := NewDashboard(mockSlack, provider, nil, cfg)
	// msg is nil — should post new.
	d.refresh(context.Background())

	mockSlack.mu.Lock()
	posts := mockSlack.posts
	mockSlack.mu.Unlock()

	if posts != 1 {
		t.Errorf("expected 1 post when no existing message, got %d", posts)
	}
}

func TestDashboard_RefreshForcesUpdateAfterFiveMinutes(t *testing.T) {
	provider := &dashboardMockProvider{
		roster: &rpc.AgentRosterResult{
			Actors: []rpc.AgentRosterEntry{{Actor: "a1", TaskID: "bd-1"}},
		},
	}
	mockSlack := &dashboardMockSlack{}
	cfg := DashboardConfig{
		Enabled:   true,
		ChannelID: "C-test",
	}

	d := NewDashboard(mockSlack, provider, nil, cfg)

	// Hash matches, but last update was >5 minutes ago — should force update for timestamp.
	_, hash := renderDashboardBlocks(provider.roster, cfg)
	d.msg = &DashboardMessage{
		ChannelID: "C-test",
		Timestamp: "1234.000001",
		LastHash:  hash,
	}
	d.lastUpdate = time.Now().Add(-6 * time.Minute)

	d.refresh(context.Background())

	mockSlack.mu.Lock()
	updates := mockSlack.updates
	mockSlack.mu.Unlock()

	if updates != 1 {
		t.Errorf("expected forced update after 5 minutes, got %d updates", updates)
	}
}

// --- MarkDirty concurrency ---

func TestDashboard_MarkDirtyConcurrentSafe(t *testing.T) {
	d := &Dashboard{
		cfg: DashboardConfig{Enabled: true},
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.MarkDirty()
		}()
	}
	wg.Wait()

	d.mu.Lock()
	dirty := d.dirty
	d.mu.Unlock()
	if !dirty {
		t.Error("expected dirty=true after concurrent MarkDirty calls")
	}
}

// --- NewDashboard configuration ---

func TestNewDashboard_DisablesWhenNoChannel(t *testing.T) {
	d := NewDashboard(nil, nil, nil, DashboardConfig{
		Enabled:   true,
		ChannelID: "", // no channel
	})
	if d.cfg.Enabled {
		t.Error("dashboard should be disabled when no channel configured")
	}
}

func TestNewDashboard_DefaultMinInterval(t *testing.T) {
	d := NewDashboard(nil, nil, nil, DashboardConfig{
		Enabled:   true,
		ChannelID: "C-test",
	})
	if d.minInterval != 3*time.Second {
		t.Errorf("expected 3s min interval, got %v", d.minInterval)
	}
}

// --- Edge cases ---

func TestRenderDashboardBlocks_OnlyIdleAgents(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "idle-1", IdleSecs: 10},
			{Actor: "idle-2", IdleSecs: 60},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})

	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)
	// Should have idle section but no working or dead sections.
	if !strings.Contains(allStr, "Idle (2)") {
		t.Error("should have idle section with count")
	}
	if strings.Contains(allStr, "Working") {
		t.Error("should not have working section when no working agents")
	}
	if strings.Contains(allStr, "Dead") {
		t.Error("should not have dead section when no dead agents")
	}
}

func TestRenderDashboardBlocks_OnlyDeadAgents(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "dead-1", Reaped: true, IdleSecs: 300},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})

	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)
	if !strings.Contains(allStr, "Dead (1)") {
		t.Error("should have dead section with count")
	}
	if strings.Contains(allStr, "Working") {
		t.Error("should not have working section when no working agents")
	}
}

func TestRenderDashboardBlocks_AgentPathNames(t *testing.T) {
	// Agent names with path prefixes should show short names.
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "gastown/crew/wise-fish", TaskID: "bd-1", TaskTitle: "Fix things"},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})

	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)
	// Should show "wise-fish" not the full path.
	if !strings.Contains(allStr, "wise-fish") {
		t.Error("should show short agent name")
	}
}

func TestRenderDashboardBlocks_WorkingAgentShowsRig(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "agent-1", TaskID: "bd-1", TaskTitle: "Work", Rig: "beads", IdleSecs: 5},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})

	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)
	if !strings.Contains(allStr, "beads") {
		t.Error("working agent context should include rig name")
	}
}

func TestRenderDashboardBlocks_WorkingAgentShowsEventsPerMin(t *testing.T) {
	roster := &rpc.AgentRosterResult{
		Actors: []rpc.AgentRosterEntry{
			{Actor: "agent-1", TaskID: "bd-1", TaskTitle: "Work", EventsPerMin: 15.7, IdleSecs: 2},
		},
	}
	blocks, _ := renderDashboardBlocks(roster, DashboardConfig{})

	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)
	if !strings.Contains(allStr, "events/min") {
		t.Error("working agent context should include events/min")
	}
}

func TestRenderDashboardBlocks_IdleOverflow(t *testing.T) {
	var actors []rpc.AgentRosterEntry
	for i := 0; i < 10; i++ {
		actors = append(actors, rpc.AgentRosterEntry{
			Actor:    fmt.Sprintf("idle-%d", i),
			IdleSecs: float64(i * 10),
		})
	}
	roster := &rpc.AgentRosterResult{Actors: actors}
	cfg := DashboardConfig{MaxIdleShown: 3}

	blocks, _ := renderDashboardBlocks(roster, cfg)
	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)

	if !strings.Contains(allStr, "+7 more idle") {
		t.Errorf("should show overflow: got %s", allStr)
	}
}

func TestRenderDashboardBlocks_DeadOverflow(t *testing.T) {
	var actors []rpc.AgentRosterEntry
	for i := 0; i < 8; i++ {
		actors = append(actors, rpc.AgentRosterEntry{
			Actor:    fmt.Sprintf("dead-%d", i),
			Reaped:   true,
			IdleSecs: float64(i * 60),
		})
	}
	roster := &rpc.AgentRosterResult{Actors: actors}
	cfg := DashboardConfig{MaxDeadShown: 2}

	blocks, _ := renderDashboardBlocks(roster, cfg)
	allJSON, _ := json.Marshal(blocks)
	allStr := string(allJSON)

	if !strings.Contains(allStr, "+6 more dead") {
		t.Errorf("should show dead overflow: got %s", allStr)
	}
}
