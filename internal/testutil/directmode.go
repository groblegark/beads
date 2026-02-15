// Package testutil provides shared test helpers.
package testutil

import "testing"

// ForceDirectMode clears BD_DAEMON_HOST to ensure the test uses direct
// database access instead of routing through a daemon. Use this only in
// tests that genuinely need direct Dolt/storage access (e.g., migration
// logic, config parsing, doctor diagnostics, socket discovery).
//
// For tests that exercise normal bd command behavior, prefer
// testdaemon.Start(t) to get a real daemon URL instead.
//
// This helper exists to make the intent explicit and to make it easy to
// find all direct-mode tests when migrating to daemon-only test execution
// (see epic beads-6dz8).
func ForceDirectMode(t testing.TB) {
	t.Helper()
	t.Setenv("BD_DAEMON_HOST", "")
}
