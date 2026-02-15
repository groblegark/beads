package rpc

import "testing"

func TestExtractCoopURL(t *testing.T) {
	tests := []struct {
		name  string
		notes string
		want  string
	}{
		{
			name:  "standard format",
			notes: "coop_url: http://localhost:8080",
			want:  "http://localhost:8080",
		},
		{
			name:  "with port and path",
			notes: "coop_url: http://10.0.0.5:3000",
			want:  "http://10.0.0.5:3000",
		},
		{
			name:  "among other notes",
			notes: "owner: admin\ncoop_url: http://localhost:8080\nstatus: ok",
			want:  "http://localhost:8080",
		},
		{
			name:  "no coop_url",
			notes: "owner: admin\nstatus: ok",
			want:  "",
		},
		{
			name:  "empty notes",
			notes: "",
			want:  "",
		},
		{
			name:  "coop_url with https",
			notes: "coop_url: https://coop.example.com:443",
			want:  "https://coop.example.com:443",
		},
		{
			name:  "coop_url with trailing whitespace",
			notes: "coop_url:  http://localhost:8080  \n",
			want:  "http://localhost:8080",
		},
		{
			name:  "similar key name does not match",
			notes: "my_coop_url: http://wrong.example.com",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCoopURL(tt.notes)
			if got != tt.want {
				t.Errorf("extractCoopURL(%q) = %q, want %q", tt.notes, got, tt.want)
			}
		})
	}
}
