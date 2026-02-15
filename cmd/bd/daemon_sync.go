package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// exportToJSONLWithStore exports issues to JSONL using the provided store.
// In dolt-native mode (the only mode), this is only called for explicit `bd export`.
func exportToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Single-repo mode
	// Get all issues including tombstones for sync propagation
	// Tombstones must be exported so they propagate to other clones and prevent resurrection
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Safety check: prevent exporting empty database over non-empty JSONL
	// Note: The main protection is in sync.go's reverse ZFC check which runs BEFORE export.
	// Here we only block the most catastrophic case (empty DB) to allow legitimate deletions.
	if len(issues) == 0 {
		existingCount, err := countIssuesInJSONL(jsonlPath)
		if err != nil {
			// If we can't read the file, it might not exist yet, which is fine
			if !os.IsNotExist(err) {
				return fmt.Errorf("warning: failed to read existing JSONL: %w", err)
			}
		} else if existingCount > 0 {
			return fmt.Errorf("refusing to export empty database over non-empty JSONL file (database: 0 issues, JSONL: %d issues). This would result in data loss", existingCount)
		}
	}

	// Sort by ID for consistent output
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	// Populate dependencies for all issues
	allDeps, err := store.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues (batch to avoid N+1 queries)
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}
	allLabels, err := store.GetLabelsForIssues(ctx, issueIDs)
	if err != nil {
		return fmt.Errorf("failed to get labels: %w", err)
	}
	for _, issue := range issues {
		issue.Labels = allLabels[issue.ID]
	}

	// Populate comments for all issues (batch to avoid N+1 queries)
	allComments, err := store.GetCommentsForIssues(ctx, issueIDs)
	if err != nil {
		return fmt.Errorf("failed to get comments: %w", err)
	}
	for _, issue := range issues {
		issue.Comments = allComments[issue.ID]
	}

	// Create temp file for atomic write
	dir := filepath.Dir(jsonlPath)
	base := filepath.Base(jsonlPath)
	tempFile, err := os.CreateTemp(dir, base+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	// Use defer pattern for proper cleanup
	var writeErr error
	defer func() {
		_ = tempFile.Close()
		if writeErr != nil {
			_ = os.Remove(tempPath) // Remove temp file on error
		}
	}()

	// Write JSONL
	for _, issue := range issues {
		data, marshalErr := json.Marshal(issue)
		if marshalErr != nil {
			writeErr = fmt.Errorf("failed to marshal issue %s: %w", issue.ID, marshalErr)
			return writeErr
		}
		if _, writeErr = tempFile.Write(data); writeErr != nil {
			writeErr = fmt.Errorf("failed to write issue %s: %w", issue.ID, writeErr)
			return writeErr
		}
		if _, writeErr = tempFile.WriteString("\n"); writeErr != nil {
			writeErr = fmt.Errorf("failed to write newline: %w", writeErr)
			return writeErr
		}
	}

	// Close before rename
	if writeErr = tempFile.Close(); writeErr != nil {
		writeErr = fmt.Errorf("failed to close temp file: %w", writeErr)
		return writeErr
	}

	// Atomic rename
	if writeErr = os.Rename(tempPath, jsonlPath); writeErr != nil {
		writeErr = fmt.Errorf("failed to rename temp file: %w", writeErr)
		return writeErr
	}

	// Update export_hashes for all exported issues (GH#1278)
	// This ensures child issues created with --parent are properly registered.
	// Use BatchSetExportHashes to avoid flooding Dolt with individual transactions
	// which saturates the connection pool and causes context deadline exceeded for
	// concurrent RPCs (beads-epc-fix_dolt_overload_export_hash_scan).
	hashes := make(map[string]string, len(issues))
	for _, issue := range issues {
		contentHash := issue.ContentHash
		if contentHash == "" {
			contentHash = issue.ComputeContentHash()
		}
		if contentHash != "" {
			hashes[issue.ID] = contentHash
		}
	}
	if len(hashes) > 0 {
		if err := store.BatchSetExportHashes(ctx, hashes); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to batch set export hashes: %v\n", err)
		}
	}

	return nil
}

// importToJSONLWithStore imports issues from JSONL using the provided store
func importToJSONLWithStore(ctx context.Context, store storage.Storage, jsonlPath string) error {
	// Single-repo mode
	// Read JSONL file
	file, err := os.Open(jsonlPath) // #nosec G304 - controlled path from config
	if err != nil {
		return fmt.Errorf("failed to open JSONL: %w", err)
	}
	defer file.Close()

	// Parse all issues
	var issues []*types.Issue
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // 10MB max line size
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines
		if line == "" {
			continue
		}

		// Parse JSON
		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			// Log error but continue - don't fail entire import
			fmt.Fprintf(os.Stderr, "Warning: failed to parse JSONL line %d: %v\n", lineNum, err)
			continue
		}
		issue.SetDefaults() // Apply defaults for omitted fields

		// Migrate old JSONL format: auto-correct deleted status to tombstone
		// This handles JSONL files from versions that used "deleted" instead of "tombstone"
		// (GH#1223: Stuck in sync diversion loop)
		if issue.Status == types.Status("deleted") && issue.DeletedAt != nil {
			issue.Status = types.StatusTombstone
		}

		// Fix: Any non-tombstone issue with deleted_at set is malformed and should be tombstone
		// This catches issues that may have been corrupted or migrated incorrectly
		if issue.Status != types.StatusTombstone && issue.DeletedAt != nil {
			issue.Status = types.StatusTombstone
		}

		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			now := time.Now()
			issue.ClosedAt = &now
		}

		// Ensure tombstones have deleted_at set (fix for malformed data)
		if issue.Status == types.StatusTombstone && issue.DeletedAt == nil {
			now := time.Now()
			issue.DeletedAt = &now
		}

		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read JSONL: %w", err)
	}

	// Use existing import logic with auto-conflict resolution
	opts := ImportOptions{
		DryRun:               false,
		SkipUpdate:           false,
		Strict:               false,
		SkipPrefixValidation: true, // Skip prefix validation for auto-import
	}

	_, err = importIssuesCore(ctx, "", store, issues, opts)
	return err
}

// getRepoKeyForPath extracts the stable repo identifier from a JSONL path.
// For single-repo mode, returns empty string (no suffix needed).
// For multi-repo mode, extracts the repo path (e.g., ".", "../frontend").
// This creates portable metadata keys that work across different machine paths.
func getRepoKeyForPath(jsonlPath string) string {
	multiRepo := config.GetMultiRepoConfig()
	if multiRepo == nil {
		return "" // Single-repo mode
	}

	// Normalize the jsonlPath for comparison
	// Remove trailing "/.beads/issues.jsonl" to get repo path
	const suffix = "/.beads/issues.jsonl"
	if strings.HasSuffix(jsonlPath, suffix) {
		repoPath := strings.TrimSuffix(jsonlPath, suffix)

		// Try to match against primary repo
		primaryPath := multiRepo.Primary
		if primaryPath == "" {
			primaryPath = "."
		}
		if repoPath == primaryPath {
			return primaryPath
		}

		// Try to match against additional repos
		for _, additional := range multiRepo.Additional {
			if repoPath == additional {
				return additional
			}
		}
	}

	// Fallback: return empty string for single-repo mode behavior
	return ""
}

// sanitizeMetadataKey removes or replaces characters that conflict with metadata key format.
// On Windows, absolute paths contain colons (e.g., C:\...) which conflict with the ':' separator
// used in multi-repo metadata keys. This function replaces colons with underscores to make
// paths safe for use as metadata key suffixes.
func sanitizeMetadataKey(key string) string {
	return strings.ReplaceAll(key, ":", "_")
}

// updateExportMetadata updates jsonl_content_hash and related metadata after a successful export.
// This prevents "JSONL content has changed since last import" errors on subsequent exports.
// In multi-repo mode, keySuffix should be the stable repo identifier (e.g., ".", "../frontend").
//
// Metadata key format:
//   - Single-repo mode: "jsonl_content_hash", "last_import_time"
//   - Multi-repo mode: "jsonl_content_hash:<repo_key>", "last_import_time:<repo_key>", etc.
//     where <repo_key> is a stable repo identifier like "." or "../frontend"
//   - Windows paths: Colons in absolute paths (e.g., C:\...) are replaced with underscores
//   - Note: "last_import_mtime" was removed (git doesn't preserve mtime)
//   - Note: "last_import_hash" renamed to "jsonl_content_hash" - more accurate name
//
// Transaction boundaries:
// This function does NOT provide atomicity between JSONL write, metadata updates, and DB mtime.
// If a crash occurs between these operations, metadata may be inconsistent. However, this is
// acceptable because:
//  1. The worst case is "JSONL content has changed" error on next export
//  2. User can fix by running 'bd import' (safe, no data loss)
//  3. Current approach is simple and doesn't require complex WAL or format changes
//
// Future: Consider defensive checks on startup if this becomes a common issue.
func updateExportMetadata(ctx context.Context, store storage.Storage, jsonlPath string, log daemonLogger, keySuffix string) {
	// Sanitize keySuffix to handle Windows paths with colons
	if keySuffix != "" {
		keySuffix = sanitizeMetadataKey(keySuffix)
	}

	currentHash, err := computeJSONLHash(jsonlPath)
	if err != nil {
		log.log("Warning: failed to compute JSONL hash for metadata update: %v", err)
		return
	}

	// Build metadata keys with optional suffix for per-repo tracking
	// Renamed from last_import_hash to jsonl_content_hash
	hashKey := "jsonl_content_hash"
	timeKey := "last_import_time"
	if keySuffix != "" {
		hashKey += ":" + keySuffix
		timeKey += ":" + keySuffix
	}

	// Note: Metadata update failures are treated as warnings, not errors.
	// This is acceptable because the worst case is the next export will require
	// an import first, which is safe and prevents data loss.
	// Alternative: Make this critical and fail the export if metadata updates fail,
	// but this makes exports more fragile and doesn't prevent data corruption.
	if err := store.SetMetadata(ctx, hashKey, currentHash); err != nil {
		log.log("Warning: failed to update %s: %v", hashKey, err)
		log.log("Next export may require running 'bd import' first")
	}

	// Use RFC3339Nano for nanosecond precision to avoid race with file mtime (fixes #399)
	exportTime := time.Now().Format(time.RFC3339Nano)
	if err := store.SetMetadata(ctx, timeKey, exportTime); err != nil {
		log.log("Warning: failed to update %s: %v", timeKey, err)
	}
	// Note: mtime tracking removed (git doesn't preserve mtime)
}

// validateDatabaseFingerprint checks that the database belongs to this repository
func validateDatabaseFingerprint(ctx context.Context, store storage.Storage, log *daemonLogger) error {

	// Get stored repo ID
	storedRepoID, err := store.GetMetadata(ctx, "repo_id")
	if err != nil && err.Error() != "metadata key not found: repo_id" {
		return fmt.Errorf("failed to read repo_id: %w", err)
	}

	// If no repo_id, this is a legacy database - require explicit migration
	if storedRepoID == "" {
		return fmt.Errorf(`
LEGACY DATABASE DETECTED!

This database was created before version 0.17.5 and lacks a repository fingerprint.
To continue using this database, you must explicitly set its repository ID:

  bd migrate --update-repo-id

This ensures the database is bound to this repository and prevents accidental
database sharing between different repositories.

If this is a fresh clone, run:
  rm -rf .beads && bd init

Note: Auto-claiming legacy databases is intentionally disabled to prevent
silent corruption when databases are copied between repositories.
`)
	}

	// Validate repo ID matches current repository
	currentRepoID, err := beads.ComputeRepoID()
	if err != nil {
		log.log("Warning: could not compute current repository ID: %v", err)
		return nil
	}

	if storedRepoID != currentRepoID {
		return fmt.Errorf(`
DATABASE MISMATCH DETECTED!

This database belongs to a different repository:
  Database repo ID:  %s (full: %s)
  Current repo ID:   %s (full: %s)

This usually means:
  1. You copied a .beads directory from another repo (don't do this!)
  2. Git remote URL changed (run 'bd migrate --update-repo-id')
  3. Database corruption
  4. bd was upgraded and URL canonicalization changed

⚠️  CRITICAL: This mismatch can cause beads to incorrectly delete issues during sync!
   The git-history-backfill mechanism may treat your local issues as deleted
   because they don't exist in the remote repository's history.

Solutions:
  - If remote URL changed: bd migrate --update-repo-id
  - If bd was upgraded: bd migrate --update-repo-id
  - If wrong database: rm -rf .beads && bd init
  - If correct database: BEADS_IGNORE_REPO_MISMATCH=1 bd daemon
    (Warning: This can cause data corruption and unwanted deletions across clones!)
`, storedRepoID[:8], storedRepoID, currentRepoID[:8], currentRepoID)
	}

	log.log("Repository fingerprint validated: %s", currentRepoID[:8])
	return nil
}

// =============================================================================
// Dolt-Native Sync Functions (hq-c005e8)
// =============================================================================
// These functions provide lightweight sync for dolt-native mode, bypassing
// all JSONL export/import logic since Dolt handles versioning natively.

// createDoltNativeExportFunc creates a function that commits to Dolt and
// optionally pushes. This replaces the JSONL export workflow for dolt-native mode.
func createDoltNativeExportFunc(ctx context.Context, store storage.Storage, autoCommit, autoPush bool, log daemonLogger) func() {
	return func() {
		exportCtx, exportCancel := context.WithTimeout(ctx, 30*time.Second)
		defer exportCancel()

		log.log("Starting dolt-native export...")

		// Get the remote storage interface
		rs, ok := storage.AsRemote(store)
		if !ok {
			log.log("Error: store does not support remote operations")
			return
		}

		// Commit changes if autoCommit enabled
		if autoCommit {
			message := fmt.Sprintf("bd daemon: %s", time.Now().Format("2006-01-02 15:04:05"))
			if err := rs.Commit(exportCtx, message); err != nil {
				// "nothing to commit" is not an error
				if !strings.Contains(err.Error(), "nothing to commit") {
					log.log("Dolt commit failed: %v", err)
					return
				}
				log.log("No changes to commit")
			} else {
				log.log("Committed to Dolt")
			}
		}

		// Push if autoPush enabled
		if autoPush {
			if err := rs.Push(exportCtx); err != nil {
				// "nothing to push" or "no remote" is not an error
				if !strings.Contains(err.Error(), "nothing to push") &&
					!strings.Contains(err.Error(), "remote") {
					log.log("Dolt push failed: %v", err)
					return
				}
			} else {
				log.log("Pushed to Dolt remote")
			}
		}

		log.log("Dolt-native export complete")
	}
}

// createDoltNativePullFunc creates a function that pulls from Dolt remote.
// This replaces the JSONL auto-import workflow for dolt-native mode.
func createDoltNativePullFunc(ctx context.Context, store storage.Storage, log daemonLogger) func() {
	return func() {
		pullCtx, pullCancel := context.WithTimeout(ctx, 60*time.Second)
		defer pullCancel()

		log.log("Starting dolt-native pull...")

		// Get the remote storage interface
		rs, ok := storage.AsRemote(store)
		if !ok {
			log.log("Error: store does not support remote operations")
			return
		}

		if err := rs.Pull(pullCtx); err != nil {
			// "nothing to pull" or "no remote" is not an error
			if !strings.Contains(err.Error(), "nothing to pull") &&
				!strings.Contains(err.Error(), "remote") &&
				!strings.Contains(err.Error(), "up to date") {
				log.log("Dolt pull failed: %v", err)
				return
			}
		} else {
			log.log("Pulled from Dolt remote")
		}

		log.log("Dolt-native pull complete")
	}
}

// createDoltNativeSyncFunc creates a sync function for dolt-native mode.
// This function performs periodic sync with a cheap status check to avoid
// expensive commit/push operations when there are no uncommitted changes.
// gt-p1mpqx: Reduced overhead by checking dolt_status before attempting commit.
func createDoltNativeSyncFunc(ctx context.Context, store storage.Storage, autoCommit, autoPush, autoPull bool, log daemonLogger) func() {
	return func() {
		syncCtx, syncCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer syncCancel()

		rs, ok := storage.AsRemote(store)
		if !ok {
			log.log("Error: store does not support remote operations")
			return
		}

		// Pull first if autoPull enabled
		if autoPull {
			if err := rs.Pull(syncCtx); err != nil {
				if !strings.Contains(err.Error(), "remote") &&
					!strings.Contains(err.Error(), "up to date") {
					log.log("Dolt pull failed: %v", err)
					// Continue anyway - commit/push might still work
				}
			} else {
				log.log("Pulled from Dolt remote")
			}
		}

		// Cheap status check before commit (gt-p1mpqx: reduce sync overhead)
		// This avoids expensive DOLT_COMMIT calls when there's nothing to commit
		hasChanges := true // Default to true if status check not supported
		if sc, ok := storage.AsStatusChecker(store); ok {
			var err error
			hasChanges, err = sc.HasUncommittedChanges(syncCtx)
			if err != nil {
				log.log("Warning: status check failed: %v (proceeding with commit)", err)
				hasChanges = true // Fall back to attempting commit
			}
		}

		// Skip commit and push if no changes
		if !hasChanges {
			debug.Logf("dolt-native sync: no uncommitted changes, skipping commit/push")
			return
		}

		// Commit if autoCommit enabled
		if autoCommit {
			message := fmt.Sprintf("bd daemon sync: %s", time.Now().Format("2006-01-02 15:04:05"))
			if err := rs.Commit(syncCtx, message); err != nil {
				if !strings.Contains(err.Error(), "nothing to commit") {
					log.log("Dolt commit failed: %v", err)
					return
				}
			} else {
				log.log("Committed to Dolt")
			}
		}

		// Push if autoPush enabled
		if autoPush {
			if err := rs.Push(syncCtx); err != nil {
				if !strings.Contains(err.Error(), "nothing to push") &&
					!strings.Contains(err.Error(), "remote") {
					log.log("Dolt push failed: %v", err)
					return
				}
			} else {
				log.log("Pushed to Dolt remote")
			}
		}

		log.log("Dolt-native sync cycle complete")
	}
}
