// Store interface for import operations
// This interface abstracts the storage backend to support both SQLite and Dolt.

package importer

import (
	"context"

	"github.com/steveyegge/beads/internal/storage"
)

// ImportStore defines the interface required for import operations.
// Both SQLiteStorage and DoltStore implement this interface.
// Note: CreateIssuesWithFullOptions and ImportIssueComment are inherited from storage.Storage.
type ImportStore interface {
	storage.Storage

	// Path returns the database directory path
	Path() string

	// CheckpointWAL checkpoints the WAL (SQLite) or is a no-op (Dolt)
	CheckpointWAL(ctx context.Context) error

	// GetOrphanHandling returns the configured orphan handling mode
	GetOrphanHandling(ctx context.Context) string
}

// BatchCreateOptions is an alias for storage.BatchCreateOptions
type BatchCreateOptions = storage.BatchCreateOptions

// AsImportStore attempts to convert a Storage to ImportStore
func AsImportStore(store storage.Storage) (ImportStore, bool) {
	is, ok := store.(ImportStore)
	return is, ok
}
