// Package sensitive provides secret detection and redaction for jack change values.
// Used by both CLI (jack_log.go, jack_show.go) and server (server_jack.go) to
// ensure consistent handling of sensitive data in before/after change records.
package sensitive

import (
	"regexp"
	"strings"
)

// RedactedPlaceholder is the text shown when a sensitive value is redacted.
const RedactedPlaceholder = "[SENSITIVE - use --reveal to show]"

// Patterns contains compiled regexes matching common secret formats.
var Patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[=:]\s*\S+`),
	regexp.MustCompile(`(?i)(api[_-]?key|apikey)\s*[=:]\s*\S+`),
	regexp.MustCompile(`(?i)(secret|token)\s*[=:]\s*\S+`),
	regexp.MustCompile(`(?i)(aws_secret_access_key|aws_access_key_id)\s*[=:]\s*\S+`),
	regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9._\-]+`),
	regexp.MustCompile(`-----BEGIN\s+(RSA\s+)?PRIVATE\s+KEY-----`),
	// Connection strings with embedded credentials
	regexp.MustCompile(`(?i)(mysql|postgres|postgresql|mongodb|redis|amqp)://[^@]+@`),
	// GitHub/GitLab tokens
	regexp.MustCompile(`(?i)(ghp_|gho_|ghu_|ghs_|ghr_|glpat-)[a-zA-Z0-9]+`),
	// Generic base64-encoded secrets (32+ chars of base64 after known prefixes)
	regexp.MustCompile(`(?i)(authorization|x-api-key)\s*[=:]\s*[A-Za-z0-9+/=]{32,}`),
}

// ContainsSecret returns true if value matches any known secret pattern.
func ContainsSecret(value string) bool {
	if value == "" {
		return false
	}
	for _, pat := range Patterns {
		if pat.MatchString(value) {
			return true
		}
	}
	return false
}

// Redact replaces secret-matching portions of a value with redacted placeholders.
// Returns the redacted string and whether any redaction was performed.
func Redact(value string) (string, bool) {
	if value == "" {
		return value, false
	}
	redacted := false
	result := value
	for _, pat := range Patterns {
		if pat.MatchString(result) {
			result = pat.ReplaceAllStringFunc(result, func(match string) string {
				redacted = true
				// For key=value patterns, preserve the key name
				if idx := strings.IndexAny(match, "=:"); idx >= 0 {
					return match[:idx+1] + "[REDACTED]"
				}
				return "[REDACTED]"
			})
		}
	}
	return result, redacted
}

// ScanFields checks before and after values for secrets.
// Returns the field name ("--before" or "--after") that matched, or "" if clean.
func ScanFields(before, after string) string {
	if ContainsSecret(before) {
		return "--before"
	}
	if ContainsSecret(after) {
		return "--after"
	}
	return ""
}

// MatchedPattern returns the pattern string that matched, for error messages.
// Returns "" if no match.
func MatchedPattern(value string) string {
	for _, pat := range Patterns {
		if pat.MatchString(value) {
			return pat.String()
		}
	}
	return ""
}
