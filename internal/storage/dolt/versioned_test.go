package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestDoltStoreImplementsVersionedStorage verifies DoltStore implements VersionedStorage.
// This is a compile-time check.
func TestDoltStoreImplementsVersionedStorage(t *testing.T) {
	// The var _ declaration in versioned.go already ensures this at compile time.
	// This test just documents the expectation.

	var _ storage.VersionedStorage = (*DoltStore)(nil)
}

// TestVersionedStorageMethodsExist ensures all required methods are defined.
// This is mostly a documentation test since Go's type system enforces this.
func TestVersionedStorageMethodsExist(t *testing.T) {
	// If DoltStore doesn't implement all VersionedStorage methods,
	// this file won't compile. This test exists for documentation.
	t.Log("DoltStore implements all VersionedStorage methods")
}

// NOTE: The following tests were removed because they reference methods that don't exist:
// - TestCommitExists (CommitExists method doesn't exist on DoltStore)
// - TestGetChangesSinceExport (GetChangesSinceExport method doesn't exist on DoltStore)
// These tests were written for planned functionality that was never implemented or
// was removed in an upstream rebase.
