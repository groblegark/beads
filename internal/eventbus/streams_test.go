package eventbus

import (
	"testing"
)

func TestSubjectForHookEvent(t *testing.T) {
	tests := []struct {
		eventType EventType
		want      string
	}{
		{EventSessionStart, "hooks.SessionStart"},
		{EventStop, "hooks.Stop"},
		{EventPreToolUse, "hooks.PreToolUse"},
		{EventNotification, "hooks.Notification"},
	}
	for _, tt := range tests {
		got := SubjectForEvent(tt.eventType)
		if got != tt.want {
			t.Errorf("SubjectForEvent(%s) = %q, want %q", tt.eventType, got, tt.want)
		}
	}
}

func TestSubjectForDecisionEvent(t *testing.T) {
	tests := []struct {
		eventType EventType
		want      string
	}{
		{EventDecisionCreated, "decisions.DecisionCreated"},
		{EventDecisionResponded, "decisions.DecisionResponded"},
		{EventDecisionEscalated, "decisions.DecisionEscalated"},
		{EventDecisionExpired, "decisions.DecisionExpired"},
	}
	for _, tt := range tests {
		got := SubjectForEvent(tt.eventType)
		if got != tt.want {
			t.Errorf("SubjectForEvent(%s) = %q, want %q", tt.eventType, got, tt.want)
		}
	}
}

// TestSubjectForDecisionEventScoped verifies scoped decision event subjects.
func TestSubjectForDecisionEventScoped(t *testing.T) {
	tests := []struct {
		eventType   EventType
		requestedBy string
		want        string
	}{
		{EventDecisionCreated, "agent-123", "decisions.agent-123.DecisionCreated"},
		{EventDecisionResponded, "8DF460B1", "decisions.8DF460B1.DecisionResponded"},
		{EventDecisionEscalated, "", "decisions._global.DecisionEscalated"},
		{EventDecisionExpired, "", "decisions._global.DecisionExpired"},
		{EventDecisionCreated, "", "decisions._global.DecisionCreated"},
	}
	for _, tt := range tests {
		got := SubjectForDecisionEvent(tt.eventType, tt.requestedBy)
		if got != tt.want {
			t.Errorf("SubjectForDecisionEvent(%s, %q) = %q, want %q",
				tt.eventType, tt.requestedBy, got, tt.want)
		}
	}
}

// TestSubjectForHookEventScoped verifies agent-scoped hook event subjects. (bd-fwylb)
func TestSubjectForHookEventScoped(t *testing.T) {
	tests := []struct {
		eventType EventType
		actor     string
		want      string
	}{
		{EventSessionStart, "bright-hog", "hooks.bright-hog.SessionStart"},
		{EventPreToolUse, "kind-ape", "hooks.kind-ape.PreToolUse"},
		{EventStop, "agent-123", "hooks.agent-123.Stop"},
		{EventSessionStart, "", "hooks._global.SessionStart"},
		{EventStop, "", "hooks._global.Stop"},
		{EventNotification, "fast-gull", "hooks.fast-gull.Notification"},
	}
	for _, tt := range tests {
		got := SubjectForHookEvent(tt.eventType, tt.actor)
		if got != tt.want {
			t.Errorf("SubjectForHookEvent(%s, %q) = %q, want %q",
				tt.eventType, tt.actor, got, tt.want)
		}
	}
}

// TestEventTypeFromSubjectScopedHook verifies EventTypeFromSubject works with
// agent-scoped hook subjects. (bd-fwylb)
func TestEventTypeFromSubjectScopedHook(t *testing.T) {
	tests := []struct {
		subject string
		want    string
	}{
		{"hooks.bright-hog.SessionStart", "SessionStart"},
		{"hooks._global.Stop", "Stop"},
		{"hooks.kind-ape.PreToolUse", "PreToolUse"},
		// Legacy flat format still works
		{"hooks.SessionStart", "SessionStart"},
		// Decision scoped
		{"decisions.agent-123.DecisionCreated", "DecisionCreated"},
	}
	for _, tt := range tests {
		got := EventTypeFromSubject(tt.subject)
		if got != tt.want {
			t.Errorf("EventTypeFromSubject(%q) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}

// TestStreamForSubjectScopedHook verifies StreamForSubject works with
// agent-scoped hook subjects. (bd-fwylb)
func TestStreamForSubjectScopedHook(t *testing.T) {
	tests := []struct {
		subject string
		want    string
	}{
		{"hooks.bright-hog.SessionStart", "hooks"},
		{"hooks._global.Stop", "hooks"},
		{"hooks.SessionStart", "hooks"},
		{"decisions.agent-123.DecisionCreated", "decisions"},
	}
	for _, tt := range tests {
		got := StreamForSubject(tt.subject)
		if got != tt.want {
			t.Errorf("StreamForSubject(%q) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}

func TestEnsureStreamsCreatesDecisionStream(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	// EnsureStreams was already called in startTestNATS. Verify both streams exist.
	info, err := js.StreamInfo(StreamHookEvents)
	if err != nil {
		t.Fatalf("StreamInfo(%s): %v", StreamHookEvents, err)
	}
	if info.Config.Name != StreamHookEvents {
		t.Errorf("expected stream name %q, got %q", StreamHookEvents, info.Config.Name)
	}

	info, err = js.StreamInfo(StreamDecisionEvents)
	if err != nil {
		t.Fatalf("StreamInfo(%s): %v", StreamDecisionEvents, err)
	}
	if info.Config.Name != StreamDecisionEvents {
		t.Errorf("expected stream name %q, got %q", StreamDecisionEvents, info.Config.Name)
	}
	// Verify the decision stream captures decision subjects.
	foundDecisionSubject := false
	for _, s := range info.Config.Subjects {
		if s == SubjectDecisionPrefix+">" {
			foundDecisionSubject = true
		}
	}
	if !foundDecisionSubject {
		t.Errorf("expected %q in stream subjects, got %v", SubjectDecisionPrefix+">", info.Config.Subjects)
	}
}

func TestSubjectForOjEvent(t *testing.T) {
	tests := []struct {
		eventType EventType
		want      string
	}{
		{EventOjJobCreated, "oj.OjJobCreated"},
		{EventOjStepAdvanced, "oj.OjStepAdvanced"},
		{EventOjAgentSpawned, "oj.OjAgentSpawned"},
		{EventOjAgentIdle, "oj.OjAgentIdle"},
		{EventOjAgentEscalated, "oj.OjAgentEscalated"},
		{EventOjJobCompleted, "oj.OjJobCompleted"},
		{EventOjJobFailed, "oj.OjJobFailed"},
		{EventOjWorkerPollComplete, "oj.OjWorkerPollComplete"},
	}
	for _, tt := range tests {
		got := SubjectForEvent(tt.eventType)
		if got != tt.want {
			t.Errorf("SubjectForEvent(%s) = %q, want %q", tt.eventType, got, tt.want)
		}
	}
}

func TestEnsureStreamsCreatesOjStream(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	info, err := js.StreamInfo(StreamOjEvents)
	if err != nil {
		t.Fatalf("StreamInfo(%s): %v", StreamOjEvents, err)
	}
	if info.Config.Name != StreamOjEvents {
		t.Errorf("expected stream name %q, got %q", StreamOjEvents, info.Config.Name)
	}
	foundOjSubject := false
	for _, s := range info.Config.Subjects {
		if s == SubjectOjPrefix+">" {
			foundOjSubject = true
		}
	}
	if !foundOjSubject {
		t.Errorf("expected %q in stream subjects, got %v", SubjectOjPrefix+">", info.Config.Subjects)
	}
}

func TestIsOjEvent(t *testing.T) {
	ojEvents := []EventType{
		EventOjJobCreated, EventOjStepAdvanced, EventOjAgentSpawned,
		EventOjAgentIdle, EventOjAgentEscalated, EventOjJobCompleted,
		EventOjJobFailed, EventOjWorkerPollComplete,
	}
	for _, e := range ojEvents {
		if !e.IsOjEvent() {
			t.Errorf("expected %s.IsOjEvent() = true", e)
		}
		if e.IsDecisionEvent() {
			t.Errorf("expected %s.IsDecisionEvent() = false", e)
		}
	}

	// Non-OJ events should return false.
	nonOj := []EventType{EventSessionStart, EventStop, EventDecisionCreated}
	for _, e := range nonOj {
		if e.IsOjEvent() {
			t.Errorf("expected %s.IsOjEvent() = false", e)
		}
	}
}

func TestSubjectForAgentEvent(t *testing.T) {
	tests := []struct {
		eventType EventType
		want      string
	}{
		{EventAgentStarted, "agents.AgentStarted"},
		{EventAgentStopped, "agents.AgentStopped"},
		{EventAgentCrashed, "agents.AgentCrashed"},
		{EventAgentIdle, "agents.AgentIdle"},
		{EventAgentHeartbeat, "agents.AgentHeartbeat"},
	}
	for _, tt := range tests {
		got := SubjectForEvent(tt.eventType)
		if got != tt.want {
			t.Errorf("SubjectForEvent(%s) = %q, want %q", tt.eventType, got, tt.want)
		}
	}
}

func TestEnsureStreamsCreatesAgentStream(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	info, err := js.StreamInfo(StreamAgentEvents)
	if err != nil {
		t.Fatalf("StreamInfo(%s): %v", StreamAgentEvents, err)
	}
	if info.Config.Name != StreamAgentEvents {
		t.Errorf("expected stream name %q, got %q", StreamAgentEvents, info.Config.Name)
	}
	foundAgentSubject := false
	for _, s := range info.Config.Subjects {
		if s == SubjectAgentPrefix+">" {
			foundAgentSubject = true
		}
	}
	if !foundAgentSubject {
		t.Errorf("expected %q in stream subjects, got %v", SubjectAgentPrefix+">", info.Config.Subjects)
	}
}

func TestIsAgentEvent(t *testing.T) {
	agentEvents := []EventType{
		EventAgentStarted, EventAgentStopped, EventAgentCrashed,
		EventAgentIdle, EventAgentHeartbeat,
	}
	for _, e := range agentEvents {
		if !e.IsAgentEvent() {
			t.Errorf("expected %s.IsAgentEvent() = true", e)
		}
		if e.IsDecisionEvent() {
			t.Errorf("expected %s.IsDecisionEvent() = false", e)
		}
		if e.IsOjEvent() {
			t.Errorf("expected %s.IsOjEvent() = false", e)
		}
	}

	// Non-agent events should return false.
	nonAgent := []EventType{EventSessionStart, EventStop, EventDecisionCreated, EventOjJobCreated}
	for _, e := range nonAgent {
		if e.IsAgentEvent() {
			t.Errorf("expected %s.IsAgentEvent() = false", e)
		}
	}
}

// TestAgentBaseName verifies continuation suffix stripping. (bd-6vtx4)
func TestAgentBaseName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sharp-seal", "sharp-seal"},
		{"sharp-seal-1", "sharp-seal"},
		{"sharp-seal-23", "sharp-seal"},
		{"stout-fish-0", "stout-fish"},
		{"mayor-113", "mayor"},
		{"furiosa-274", "furiosa"},
		{"hq-mayor", "hq-mayor"},       // Not a numeric suffix
		{"toolbox-3", "toolbox"},        // Numeric suffix
		{"a-1", "a"},                    // Minimal case
		{"noHyphens", "noHyphens"},      // No hyphen at all
		{"trailing-", "trailing-"},      // Trailing hyphen, no digits
		{"abc-def-1", "abc-def"},        // Multiple hyphens, last is numeric
	}
	for _, tt := range tests {
		got := AgentBaseName(tt.input)
		if got != tt.want {
			t.Errorf("AgentBaseName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestEnsureKVBucketsCreatesGateState verifies the GATE_STATE KV bucket is created. (bd-vecxd)
func TestEnsureKVBucketsCreatesGateState(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	kv, err := EnsureKVBuckets(js)
	if err != nil {
		t.Fatalf("EnsureKVBuckets: %v", err)
	}
	if kv == nil {
		t.Fatal("expected non-nil KeyValue")
	}

	// Verify we can write and read from it.
	_, err = kv.Put("test-agent.decision", []byte(`{"satisfied":true}`))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	entry, err := kv.Get("test-agent.decision")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(entry.Value()) != `{"satisfied":true}` {
		t.Errorf("value = %q, want %q", string(entry.Value()), `{"satisfied":true}`)
	}
}

// TestEnsureKVBucketsIdempotent verifies EnsureKVBuckets can be called multiple times.
func TestEnsureKVBucketsIdempotent(t *testing.T) {
	_, js, cleanup := startTestNATS(t)
	defer cleanup()

	kv1, err := EnsureKVBuckets(js)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	kv2, err := EnsureKVBuckets(js)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Both should return a usable KV.
	if kv1 == nil || kv2 == nil {
		t.Fatal("expected non-nil KeyValue from both calls")
	}
}

// TestEnsureStreamsIdempotent lives in bus_test.go (more thorough version).
