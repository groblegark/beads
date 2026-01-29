// Package storage defines the interface for issue storage backends.
package storage

// OrphanHandling specifies how to handle issues with missing parent references.
type OrphanHandling string

const (
	// OrphanStrict fails import on missing parent (safest)
	OrphanStrict OrphanHandling = "strict"
	// OrphanResurrect auto-resurrects missing parents from JSONL history
	OrphanResurrect OrphanHandling = "resurrect"
	// OrphanSkip skips orphaned issues with warning
	OrphanSkip OrphanHandling = "skip"
	// OrphanAllow imports orphans without validation (default, works around bugs)
	OrphanAllow OrphanHandling = "allow"
)

// BatchCreateOptions contains options for batch issue creation.
// This is a backend-agnostic type that can be used by any storage implementation.
type BatchCreateOptions struct {
	// OrphanHandling specifies how to handle issues with missing parent references
	OrphanHandling OrphanHandling
	// SkipPrefixValidation skips prefix validation for existing IDs (used during import)
	SkipPrefixValidation bool
	// PreserveDates preserves created_at/updated_at from issue (used during import)
	PreserveDates bool
	// SkipValidation skips type/status validation (used during import)
	SkipValidation bool
	// SkipDirtyTracking skips marking issues as dirty
	SkipDirtyTracking bool
}
