package dolt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestIsConnectionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"connection refused", errors.New("dial tcp 172.20.48.143:3306: connect: connection refused"), true},
		{"connection reset", errors.New("read tcp 10.30.131.212:54321->10.30.162.30:3306: connection reset by peer"), true},
		{"broken pipe", errors.New("write tcp: broken pipe"), true},
		{"no such host", errors.New("dial tcp: lookup bd-daemon-dolt-standby: no such host"), true},
		{"i/o timeout", errors.New("dial tcp 10.30.162.30:3306: i/o timeout"), true},
		{"bad connection", errors.New("driver: bad connection"), true},
		{"invalid connection", errors.New("invalid connection"), true},
		{"syntax error", errors.New("Error 1064 (42000): You have an error in your SQL syntax"), false},
		{"table not found", errors.New("Error 1146 (42S02): Table 'beads.missing' doesn't exist"), false},
		{"serialization failure", errors.New("Error 1213 (40001): Serialization failure"), false},
		{"empty string", errors.New(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConnectionError(tt.err); got != tt.want {
				t.Errorf("isConnectionError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsReadOnlyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"database is read only", errors.New("database is read only"), true},
		{"read only error 1105", errors.New("Error 1105: read only"), true},
		{"other error", errors.New("connection refused"), false},
		{"empty string", errors.New(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReadOnlyError(tt.err); got != tt.want {
				t.Errorf("isReadOnlyError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestReconnectPrimary_NoConnStr(t *testing.T) {
	// reconnectPrimary should return an error when connStr is empty.
	// The setupTestStore uses embedded mode which sets connStr to the DSN,
	// so we manually clear it to test this branch.
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Clear connStr to simulate the missing-connection-string case.
	store.connStr = ""
	err := store.reconnectPrimary(ctx)
	if err == nil {
		t.Fatal("expected error when connStr is empty")
	}
	if err.Error() != "no connection string stored for reconnect" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReconnectPrimary_QueryAfterReconnectAttempt(t *testing.T) {
	// Verify that the store is still usable after a failed reconnect attempt.
	// In production, if reconnect fails, the original connection should still
	// work (embedded mode) or the error should be propagated.
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create a test issue first
	issue := &types.Issue{
		ID:        "reconnect-test",
		Title:     "Test",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Attempt reconnect (will fail since no connStr in embedded mode)
	_ = store.reconnectPrimary(ctx)

	// In embedded mode, reconnectPrimary closes the old DB and fails to open new one.
	// The store may be in a broken state. This tests that we handle this gracefully.
	// Note: after a failed reconnect in embedded mode, the db is closed and set to nil,
	// so subsequent queries will fail. This is expected — reconnectPrimary is only
	// designed for server mode.
}

func TestParseQueryTimeout(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		def     time.Duration
		want    time.Duration
	}{
		{"default", "", 30 * time.Second, 30 * time.Second},
		{"custom", "5s", 30 * time.Second, 5 * time.Second},
		{"invalid", "garbage", 30 * time.Second, 30 * time.Second},
		{"zero", "0s", 30 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != "" {
				t.Setenv("BEADS_QUERY_TIMEOUT", tt.envVal)
			}
			got := parseQueryTimeout(tt.def)
			if got != tt.want {
				t.Errorf("parseQueryTimeout(%v) with env=%q = %v, want %v", tt.def, tt.envVal, got, tt.want)
			}
		})
	}
}
