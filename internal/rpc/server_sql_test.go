package rpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHandleSQL_RejectsNonSelect(t *testing.T) {
	s := &Server{}

	tests := []struct {
		name  string
		query string
		want  string // substring of expected error
	}{
		{"empty query", "", "cannot be empty"},
		{"insert", "INSERT INTO issues (id) VALUES ('x')", "only SELECT"},
		{"update", "UPDATE issues SET title='x'", "only SELECT"},
		{"delete", "DELETE FROM issues", "only SELECT"},
		{"drop", "DROP TABLE issues", "only SELECT"},
		{"alter", "ALTER TABLE issues ADD col TEXT", "only SELECT"},
		{"create table", "CREATE TABLE foo (id TEXT)", "only SELECT"},
		{"truncate", "TRUNCATE TABLE issues", "only SELECT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := json.Marshal(SQLArgs{Query: tt.query})
			resp := s.handleSQL(&Request{Args: args})
			if resp.Success {
				t.Errorf("expected failure for query %q", tt.query)
			}
			if tt.want != "" && !strings.Contains(resp.Error, tt.want) {
				t.Errorf("error %q does not contain %q", resp.Error, tt.want)
			}
		})
	}
}

func TestHandleSQL_RejectsDangerousKeywords(t *testing.T) {
	s := &Server{}

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"subquery insert", "SELECT * FROM issues WHERE id IN (INSERT INTO foo VALUES ('x'))", "INSERT"},
		{"subquery delete", "SELECT * FROM issues; DELETE FROM issues", "DELETE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := json.Marshal(SQLArgs{Query: tt.query})
			resp := s.handleSQL(&Request{Args: args})
			if resp.Success {
				t.Errorf("expected failure for query %q", tt.query)
			}
			if !strings.Contains(resp.Error, tt.want) {
				t.Errorf("error %q does not contain %q", resp.Error, tt.want)
			}
		})
	}
}

func TestHandleSQL_AllowsSelect(t *testing.T) {
	// Without a real storage backend, we can only test that SELECT queries
	// pass validation and fail at the storage layer (which is expected).
	s := &Server{}

	tests := []struct {
		name  string
		query string
	}{
		{"simple select", "SELECT * FROM issues"},
		{"select with where", "SELECT id, title FROM issues WHERE status = 'open'"},
		{"select count", "SELECT COUNT(*) FROM issues"},
		{"select with join", "SELECT i.id, l.label FROM issues i JOIN labels l ON i.id = l.issue_id"},
		{"CTE", "WITH open AS (SELECT * FROM issues WHERE status = 'open') SELECT * FROM open"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := json.Marshal(SQLArgs{Query: tt.query})
			resp := s.handleSQL(&Request{Args: args})
			// Should pass validation but fail on missing storage
			if resp.Success {
				t.Errorf("expected storage error (no storage configured) but got success")
			}
			if strings.Contains(resp.Error, "only SELECT") || strings.Contains(resp.Error, "forbidden keyword") {
				t.Errorf("query %q was incorrectly rejected: %s", tt.query, resp.Error)
			}
		})
	}
}
