package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestIssueDataChanged tests the issueDataChanged helper function
func TestIssueDataChanged(t *testing.T) {
	tests := []struct {
		name     string
		existing *types.Issue
		updates  map[string]interface{}
		want     bool // true if changed, false if unchanged
	}{
		{
			name: "no changes",
			existing: &types.Issue{
				Title:       "Test",
				Description: "Desc",
				Status:      types.StatusOpen,
				Priority:    2,
				IssueType:   types.TypeTask,
			},
			updates: map[string]interface{}{
				"title":       "Test",
				"description": "Desc",
				"status":      types.StatusOpen,
				"priority":    2,
				"issue_type":  types.TypeTask,
			},
			want: false,
		},
		{
			name: "title changed",
			existing: &types.Issue{
				Title: "Old Title",
			},
			updates: map[string]interface{}{
				"title": "New Title",
			},
			want: true,
		},
		{
			name: "status as string vs enum - unchanged",
			existing: &types.Issue{
				Status: types.StatusOpen,
			},
			updates: map[string]interface{}{
				"status": "open", // string instead of enum
			},
			want: false,
		},
		{
			name: "status as enum - unchanged",
			existing: &types.Issue{
				Status: types.StatusInProgress,
			},
			updates: map[string]interface{}{
				"status": types.StatusInProgress, // enum
			},
			want: false,
		},
		{
			name: "issue_type as string vs enum - unchanged",
			existing: &types.Issue{
				IssueType: types.TypeBug,
			},
			updates: map[string]interface{}{
				"issue_type": "bug", // string instead of enum
			},
			want: false,
		},
		{
			name: "priority as int - unchanged",
			existing: &types.Issue{
				Priority: 3,
			},
			updates: map[string]interface{}{
				"priority": 3,
			},
			want: false,
		},
		{
			name: "priority as int64 - unchanged",
			existing: &types.Issue{
				Priority: 2,
			},
			updates: map[string]interface{}{
				"priority": int64(2),
			},
			want: false,
		},
		{
			name: "priority as float64 whole number - unchanged",
			existing: &types.Issue{
				Priority: 1,
			},
			updates: map[string]interface{}{
				"priority": float64(1),
			},
			want: false,
		},
		{
			name: "priority as float64 fractional - changed",
			existing: &types.Issue{
				Priority: 1,
			},
			updates: map[string]interface{}{
				"priority": 1.5, // fractional not allowed
			},
			want: true,
		},
		{
			name: "empty string vs empty - unchanged",
			existing: &types.Issue{
				Design: "",
			},
			updates: map[string]interface{}{
				"design": "",
			},
			want: false,
		},
		{
			name: "empty string vs nil - unchanged (treated as equal)",
			existing: &types.Issue{
				Assignee: "",
			},
			updates: map[string]interface{}{
				"assignee": nil,
			},
			want: false,
		},
		{
			name: "non-empty to empty - changed",
			existing: &types.Issue{
				Notes: "Some notes",
			},
			updates: map[string]interface{}{
				"notes": "",
			},
			want: true,
		},
		{
			name: "external_ref nil vs empty - unchanged",
			existing: &types.Issue{
				ExternalRef: nil,
			},
			updates: map[string]interface{}{
				"external_ref": nil,
			},
			want: false,
		},
		{
			name: "external_ref pointer to empty vs nil - unchanged",
			existing: &types.Issue{
				ExternalRef: strPtr(""),
			},
			updates: map[string]interface{}{
				"external_ref": nil,
			},
			want: false,
		},
		{
			name: "external_ref changed",
			existing: &types.Issue{
				ExternalRef: strPtr("gh-123"),
			},
			updates: map[string]interface{}{
				"external_ref": "gh-456",
			},
			want: true,
		},
		{
			name: "unknown field - treated as changed",
			existing: &types.Issue{
				Title: "Test",
			},
			updates: map[string]interface{}{
				"title":         "Test",
				"unknown_field": "value",
			},
			want: true,
		},
		{
			name: "invalid type for title - treated as changed",
			existing: &types.Issue{
				Title: "Test",
			},
			updates: map[string]interface{}{
				"title": 123, // wrong type
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := issueDataChanged(tt.existing, tt.updates)
			if got != tt.want {
				t.Errorf("issueDataChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

// strPtr helper for tests
func strPtr(s string) *string {
	return &s
}
