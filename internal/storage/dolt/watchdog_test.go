package dolt

import (
	"errors"
	"testing"
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
