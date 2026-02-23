package sensitive

import (
	"testing"
)

func TestContainsSecret(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		// Positive matches
		{"password assignment", "password=hunter2", true},
		{"PASSWORD uppercase", "PASSWORD=secret123", true},
		{"api_key", "api_key=abc123def456", true},
		{"api-key", "api-key: sk-abc123", true},
		{"secret", "secret=my-secret-value", true},
		{"token", "token=eyJhbGciOiJI...", true},
		{"AWS secret key", "aws_secret_access_key=wJalrXUtnFE/K7MDENG", true},
		{"bearer token", "bearer eyJhbGciOiJIUzI1NiJ9.eyJ", true},
		{"Bearer uppercase", "Bearer abc123.def456.ghi789", true},
		{"private key", "-----BEGIN PRIVATE KEY-----", true},
		{"RSA private key", "-----BEGIN RSA PRIVATE KEY-----", true},
		{"postgres connection string", "postgres://user:pass@localhost:5432/db", true},
		{"mysql connection string", "mysql://root:password@db:3306/app", true},
		{"mongodb URI", "mongodb://admin:secret@mongo:27017/test", true},
		{"redis URI", "redis://default:mypassword@redis:6379", true},
		{"GitHub PAT", "ghp_1234567890abcdefghijklmnopqrst", true},
		{"GitLab PAT", "glpat-xxxxxxxxxxxxxxxxxxxx", true},
		{"authorization header", "authorization=QWxhZGRpbjpvcGVuIHNlc2FtZQ==aabbcc", true},

		// Negative matches (should NOT trigger)
		{"normal config value", "log_level: debug", false},
		{"empty string", "", false},
		{"numeric value", "replicas=3", false},
		{"normal URL", "https://example.com/api/v1", false},
		{"resource name", "pod/my-app-xyz", false},
		{"k8s annotation", "app.kubernetes.io/name=daemon", false},
		{"short base64", "authorization=abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsSecret(tt.value)
			if got != tt.want {
				t.Errorf("ContainsSecret(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestRedact(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		wantRedact bool
		wantKeep   string // substring that should still be present
	}{
		{
			name:       "password value",
			value:      "password=hunter2",
			wantRedact: true,
			wantKeep:   "password=",
		},
		{
			name:       "api key value",
			value:      "api_key=sk-1234567890",
			wantRedact: true,
			wantKeep:   "api_key=",
		},
		{
			name:       "bearer token",
			value:      "bearer eyJhbGciOiJIUzI1NiJ9",
			wantRedact: true,
		},
		{
			name:       "connection string",
			value:      "postgres://user:pass@localhost:5432/db",
			wantRedact: true,
		},
		{
			name:       "clean value",
			value:      "log_level: debug",
			wantRedact: false,
		},
		{
			name:       "empty string",
			value:      "",
			wantRedact: false,
		},
		{
			name:       "mixed content",
			value:      "config changed: password=old123 to password=new456",
			wantRedact: true,
			wantKeep:   "config changed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, didRedact := Redact(tt.value)
			if didRedact != tt.wantRedact {
				t.Errorf("Redact(%q) redacted=%v, want %v (result: %q)", tt.value, didRedact, tt.wantRedact, result)
			}
			if didRedact {
				// Redacted value should contain [REDACTED]
				if !containsStr(result, "[REDACTED]") {
					t.Errorf("Redact(%q) = %q, should contain [REDACTED]", tt.value, result)
				}
				// Original secret should not be present
				if result == tt.value {
					t.Errorf("Redact(%q) returned original unchanged", tt.value)
				}
			}
			if tt.wantKeep != "" && !containsStr(result, tt.wantKeep) {
				t.Errorf("Redact(%q) = %q, should contain %q", tt.value, result, tt.wantKeep)
			}
		})
	}
}

func TestScanFields(t *testing.T) {
	tests := []struct {
		name   string
		before string
		after  string
		want   string
	}{
		{"clean", "log_level: info", "log_level: debug", ""},
		{"secret in before", "password=old", "log_level: debug", "--before"},
		{"secret in after", "log_level: info", "token=abc123", "--after"},
		{"both empty", "", "", ""},
		{"secret in both returns before first", "password=x", "token=y", "--before"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanFields(tt.before, tt.after)
			if got != tt.want {
				t.Errorf("ScanFields(%q, %q) = %q, want %q", tt.before, tt.after, got, tt.want)
			}
		})
	}
}

func TestMatchedPattern(t *testing.T) {
	tests := []struct {
		name  string
		value string
		empty bool
	}{
		{"has match", "password=hunter2", false},
		{"no match", "log_level: debug", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchedPattern(tt.value)
			if tt.empty && got != "" {
				t.Errorf("MatchedPattern(%q) = %q, want empty", tt.value, got)
			}
			if !tt.empty && got == "" {
				t.Errorf("MatchedPattern(%q) = empty, want non-empty", tt.value)
			}
		})
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && searchStr(s, substr)))
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
