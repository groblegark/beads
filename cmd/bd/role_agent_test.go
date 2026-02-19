package main

import (
	"testing"
	"time"
)

func TestPluralRole(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{"refinery", "refineries"},
		{"witness", "witnesses"},
		{"polecat", "polecats"},
		{"mayor", "mayors"},
		{"deacon", "deacons"},
		{"crew", "crews"},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := pluralRole(tt.role)
			if got != tt.want {
				t.Errorf("pluralRole(%q) = %q, want %q", tt.role, got, tt.want)
			}
		})
	}
}

func TestFriendlyDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s ago"},
		{30 * time.Second, "30s ago"},
		{59 * time.Second, "59s ago"},
		{1 * time.Minute, "1m ago"},
		{5 * time.Minute, "5m ago"},
		{59 * time.Minute, "59m ago"},
		{1 * time.Hour, "1h ago"},
		{3 * time.Hour, "3h ago"},
		{23 * time.Hour, "23h ago"},
		{24 * time.Hour, "1d ago"},
		{48 * time.Hour, "2d ago"},
		{72 * time.Hour, "3d ago"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := friendlyDuration(tt.d)
			if got != tt.want {
				t.Errorf("friendlyDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}
