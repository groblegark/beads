package rpc

import "testing"

func TestUpsertNotesField(t *testing.T) {
	tests := []struct {
		name  string
		notes string
		key   string
		value string
		want  string
	}{
		{
			name:  "add to empty notes",
			notes: "",
			key:   "coop_url",
			value: "http://host:3000",
			want:  "coop_url: http://host:3000",
		},
		{
			name:  "add new key",
			notes: "role_type: polecat",
			key:   "coop_url",
			value: "http://host:3000",
			want:  "role_type: polecat\ncoop_url: http://host:3000",
		},
		{
			name:  "replace existing key",
			notes: "coop_url: http://old:3000\nrig: beads",
			key:   "coop_url",
			value: "http://new:8080",
			want:  "coop_url: http://new:8080\nrig: beads",
		},
		{
			name:  "replace middle key",
			notes: "role_type: polecat\ncoop_url: http://old:3000\nrig: beads",
			key:   "coop_url",
			value: "http://new:8080",
			want:  "role_type: polecat\ncoop_url: http://new:8080\nrig: beads",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := upsertNotesField(tt.notes, tt.key, tt.value)
			if got != tt.want {
				t.Errorf("upsertNotesField(%q, %q, %q) = %q, want %q",
					tt.notes, tt.key, tt.value, got, tt.want)
			}
		})
	}
}

func TestRemoveNotesField(t *testing.T) {
	tests := []struct {
		name  string
		notes string
		key   string
		want  string
	}{
		{
			name:  "remove from single field",
			notes: "coop_url: http://host:3000",
			key:   "coop_url",
			want:  "",
		},
		{
			name:  "remove first field",
			notes: "coop_url: http://host:3000\nrig: beads",
			key:   "coop_url",
			want:  "rig: beads",
		},
		{
			name:  "remove last field",
			notes: "role_type: polecat\ncoop_url: http://host:3000",
			key:   "coop_url",
			want:  "role_type: polecat",
		},
		{
			name:  "key not present",
			notes: "role_type: polecat\nrig: beads",
			key:   "coop_url",
			want:  "role_type: polecat\nrig: beads",
		},
		{
			name:  "empty notes",
			notes: "",
			key:   "coop_url",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeNotesField(tt.notes, tt.key)
			if got != tt.want {
				t.Errorf("removeNotesField(%q, %q) = %q, want %q",
					tt.notes, tt.key, got, tt.want)
			}
		})
	}
}
