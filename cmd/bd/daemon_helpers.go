// Temporary helper functions to fix build

package main

import (
	"context"
	"sync/atomic"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// commandDidWrite tracks whether the current command performed any write operations
// Used to trigger Dolt auto-commit after modifications
var commandDidWrite atomic.Bool

// commandDidExplicitDoltCommit tracks whether an explicit vc commit was done
// Used to skip auto-commit in PersistentPostRun when already committed
var commandDidExplicitDoltCommit bool

// IsDoltSyncer checks if the storage is a Dolt backend
func IsDoltSyncer(store storage.Storage) bool {
	_, ok := store.(*dolt.DoltStore)
	return ok
}

// loadYAMLDaemonSettings loads daemon settings from YAML config
func loadYAMLDaemonSettings() (commit, push, pull bool, hasSettings bool) {
	// Return false for hasSettings to use defaults
	return false, false, false, false
}

// shouldSkipDueToSameBranch checks if an operation should be skipped
func shouldSkipDueToSameBranch(ctx context.Context, store storage.Storage, operation string, log daemonLogger) bool {
	// Default: don't skip
	return false
}

// ShouldAutoDoltCommit returns whether Dolt should auto-commit after writes
func ShouldAutoDoltCommit(ctx context.Context, store storage.Storage) bool {
	if store == nil {
		return false
	}
	// Check config setting
	val, err := store.GetConfig(ctx, "dolt.auto_commit")
	if err != nil || val == "" {
		return true // Default to true
	}
	return val == "true" || val == "1" || val == "yes" || val == "on"
}

// ShouldAutoDoltPush returns whether Dolt should auto-push after commits
func ShouldAutoDoltPush(ctx context.Context, store storage.Storage) bool {
	if store == nil {
		return false
	}
	// Check config setting
	val, err := store.GetConfig(ctx, "dolt.auto_push")
	if err != nil || val == "" {
		return false // Default to false
	}
	return val == "true" || val == "1" || val == "yes" || val == "on"
}
