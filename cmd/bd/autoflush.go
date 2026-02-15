package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// outputJSON outputs data as pretty-printed JSON
func outputJSON(v interface{}) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
		os.Exit(1)
	}
}

// outputJSONError outputs an error as JSON to stderr and exits with code 1.
// Use this when jsonOutput is true and an error occurs, to ensure consistent
// machine-readable error output. The error is formatted as:
//
//	{"error": "error message", "code": "error_code"}
//
// The code parameter is optional (pass "" to omit).
func outputJSONError(err error, code string) {
	errObj := map[string]string{"error": err.Error()}
	if code != "" {
		errObj["code"] = code
	}
	encoder := json.NewEncoder(os.Stderr)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(errObj)
	os.Exit(1)
}

// findJSONLPath finds the JSONL file path for the current database
// findJSONLPath discovers the JSONL file path for the current database and ensures
// the parent directory exists. Uses beads.FindJSONLPath() for discovery (checking
// BEADS_JSONL env var first, then using .beads/issues.jsonl next to the database).
//
// GH#1103: When sync-branch is configured, returns the worktree JSONL path instead
// of the main repo JSONL. This ensures all writes go only to the worktree, and the
// main repo's JSONL is only updated via merges from the sync branch. This fixes
// "local changes would be overwritten by merge" errors caused by daemon writes to
// main's JSONL while skip-worktree is set.
//
// Creates the .beads directory if it doesn't exist (important for new databases).
// If directory creation fails, returns the path anyway - the subsequent write will
// fail with a clearer error message.
//
// Thread-safe: No shared state access.
func findJSONLPath() string {
	// Allow explicit override (useful in no-db mode or non-standard layouts)
	if jsonlEnv := os.Getenv("BEADS_JSONL"); jsonlEnv != "" {
		return utils.CanonicalizePath(jsonlEnv)
	}

	// Use public API for path discovery
	jsonlPath := beads.FindJSONLPath(dbPath)

	// In --no-db mode, dbPath may be empty. Fall back to locating the .beads directory.
	if jsonlPath == "" {
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			return ""
		}
		jsonlPath = utils.FindJSONLInDir(beadsDir)
	}

	// Ensure the directory exists (important for new databases)
	// This is the only difference from the public API - we create the directory
	dbDir := filepath.Dir(jsonlPath)
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		// If we can't create the directory, return discovered path anyway
		// (the subsequent write will fail with a clearer error)
		return utils.CanonicalizeIfRelative(jsonlPath)
	}

	return utils.CanonicalizeIfRelative(jsonlPath)
}

// detectPrefixFromJSONL extracts the issue prefix from JSONL data.
// Returns empty string if prefix cannot be detected.
// Used by cold-start bootstrap to initialize the database (GH#b09).
func detectPrefixFromJSONL(jsonlData []byte) string {
	// Parse first issue to extract prefix from its ID
	scanner := bufio.NewScanner(bytes.NewReader(jsonlData))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // 10MB max line size
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			continue
		}

		if issue.ID == "" {
			continue
		}

		// Extract prefix from ID (e.g., "gt-abc" -> "gt", "test-001" -> "test")
		if idx := strings.Index(issue.ID, "-"); idx > 0 {
			return issue.ID[:idx]
		}
		// No hyphen - use whole ID as prefix
		return issue.ID
	}
	return ""
}

// autoImportIfNewer checks if JSONL content changed (via hash) and imports if so
// Hash-based comparison is git-proof (mtime comparison fails after git pull).
// Uses collision detection to prevent silently overwriting local changes.
// Defense-in-depth check to respect --no-auto-import flag.
func autoImportIfNewer() {
	// Defense-in-depth: always check noAutoImport flag directly
	// This ensures auto-import is disabled even if caller forgot to check autoImportEnabled
	if noAutoImport {
		debug.Logf("auto-import skipped (--no-auto-import flag)")
		return
	}

	// hq-c2495f: Skip auto-import for Dolt backend (Dolt is source of truth, not JSONL)
	// bd-dfw: Use store.BackendName() directly to detect Dolt, as config files may be in
	// a different directory than dbPath when using Dolt server mode
	if store != nil && store.BackendName() == configfile.BackendDolt {
		debug.Logf("auto-import skipped for Dolt backend (Dolt is source of truth)")
		return
	}

	// Find JSONL path
	jsonlPath := findJSONLPath()

	// Read JSONL file
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		// JSONL doesn't exist or can't be accessed, skip import
		debug.Logf("auto-import skipped, JSONL not found: %v", err)
		return
	}

	// Compute current JSONL hash
	hasher := sha256.New()
	hasher.Write(jsonlData)
	currentHash := hex.EncodeToString(hasher.Sum(nil))

	// Get content hash from DB metadata (try new key first, fall back to old for migration)
	ctx := rootCtx
	lastHash, err := store.GetMetadata(ctx, "jsonl_content_hash")
	if err != nil || lastHash == "" {
		lastHash, err = store.GetMetadata(ctx, "last_import_hash")
		if err != nil {
			// Metadata error - treat as first import rather than skipping
			// This allows auto-import to recover from corrupt/missing metadata
			debug.Logf("metadata read failed (%v), treating as first import", err)
			lastHash = ""
		}
	}

	// Compare hashes
	if currentHash == lastHash {
		// Content unchanged, skip import
		debug.Logf("auto-import skipped, JSONL unchanged (hash match)")
		return
	}

	debug.Logf("auto-import triggered (hash changed)")

	// Check if database needs initialization (GH#b09 - cold-start bootstrap)
	// If issue_prefix is not set, the DB is uninitialized and import will fail.
	// Auto-detect and set the prefix to enable seamless cold-start recovery.
	// Note: Use global store directly as cmdCtx.Store may not be synced yet (GH#b09)
	if store != nil {
		prefix, prefixErr := store.GetConfig(ctx, "issue_prefix")
		if prefixErr != nil || prefix == "" {
			// Database needs initialization - detect prefix from JSONL or directory
			detectedPrefix := detectPrefixFromJSONL(jsonlData)
			if detectedPrefix == "" {
				// Fallback: detect from directory name
				beadsDir := filepath.Dir(jsonlPath)
				parentDir := filepath.Dir(beadsDir)
				detectedPrefix = filepath.Base(parentDir)
				if detectedPrefix == "." || detectedPrefix == "/" {
					detectedPrefix = "bd"
				}
			}
			detectedPrefix = strings.TrimRight(detectedPrefix, "-")

			if setErr := store.SetConfig(ctx, "issue_prefix", detectedPrefix); setErr != nil {
				fmt.Fprintf(os.Stderr, "Auto-import: failed to initialize database prefix: %v\n", setErr)
				return
			}
			debug.Logf("auto-import: initialized database with prefix '%s'", detectedPrefix)
		}
	}

	// Check for Git merge conflict markers
	// Only match if they appear as standalone lines (not embedded in JSON strings)
	lines := bytes.Split(jsonlData, []byte("\n"))
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if bytes.HasPrefix(trimmed, []byte("<<<<<<< ")) ||
			bytes.Equal(trimmed, []byte("=======")) ||
			bytes.HasPrefix(trimmed, []byte(">>>>>>> ")) {
			fmt.Fprintf(os.Stderr, "\n❌ Git merge conflict detected in %s\n\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "The JSONL file contains unresolved merge conflict markers.\n")
			fmt.Fprintf(os.Stderr, "This prevents auto-import from loading your issues.\n\n")
			fmt.Fprintf(os.Stderr, "To resolve:\n")
			fmt.Fprintf(os.Stderr, "  1. Resolve the merge conflict in your Git client, OR\n")
			fmt.Fprintf(os.Stderr, "  2. Export from database to regenerate clean JSONL:\n")
			fmt.Fprintf(os.Stderr, "     bd export -o %s\n\n", jsonlPath)
			fmt.Fprintf(os.Stderr, "After resolving, commit the fixed JSONL file.\n")
			return
		}
	}

	// Content changed - parse all issues
	scanner := bufio.NewScanner(bytes.NewReader(jsonlData))
	scanner.Buffer(make([]byte, 0, 1024), 2*1024*1024) // 2MB buffer for large JSON lines
	var allIssues []*types.Issue
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			// Parse error, skip this import
			snippet := line
			if len(snippet) > 80 {
				snippet = snippet[:80] + "..."
			}
			fmt.Fprintf(os.Stderr, "Auto-import skipped: parse error at line %d: %v\nSnippet: %s\n", lineNo, err, snippet)
			return
		}
		issue.SetDefaults() // Apply defaults for omitted fields (beads-399)

		// Fix closed_at invariant: closed issues must have closed_at timestamp
		if issue.Status == types.StatusClosed && issue.ClosedAt == nil {
			now := time.Now()
			issue.ClosedAt = &now
		}

		allIssues = append(allIssues, &issue)
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Auto-import skipped: scanner error: %v\n", err)
		return
	}

	// Clear export_hashes before import to prevent staleness
	// Import operations may add/update issues, so export_hashes entries become invalid
	if err := store.ClearAllExportHashes(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to clear export_hashes before import: %v\n", err)
	}
	
	// Use shared import logic
	opts := ImportOptions{
		DryRun:               false,
		SkipUpdate:           false,
		Strict:               false,
		SkipPrefixValidation: true, // Auto-import is lenient about prefixes
	}

	result, err := importIssuesCore(ctx, dbPath, store, allIssues, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Auto-import failed: %v\n", err)
		return
	}

	// Show collision remapping notification if any occurred
	if len(result.IDMapping) > 0 {
		// Build title lookup map to avoid O(n^2) search
		titleByID := make(map[string]string)
		for _, issue := range allIssues {
			titleByID[issue.ID] = issue.Title
		}

		// Sort remappings by old ID for consistent output
		type mapping struct {
			oldID string
			newID string
		}
		mappings := make([]mapping, 0, len(result.IDMapping))
		for oldID, newID := range result.IDMapping {
			mappings = append(mappings, mapping{oldID, newID})
		}
		slices.SortFunc(mappings, func(a, b mapping) int {
			return cmp.Compare(a.oldID, b.oldID)
		})

		maxShow := 10
		numRemapped := len(mappings)
		if numRemapped < maxShow {
			maxShow = numRemapped
		}

		fmt.Fprintf(os.Stderr, "\nAuto-import: remapped %d colliding issue(s) to new IDs:\n", numRemapped)
		for i := 0; i < maxShow; i++ {
			m := mappings[i]
			title := titleByID[m.oldID]
			fmt.Fprintf(os.Stderr, "  %s → %s (%s)\n", m.oldID, m.newID, title)
		}
		if numRemapped > maxShow {
			fmt.Fprintf(os.Stderr, "  ... and %d more\n", numRemapped-maxShow)
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Schedule export to sync JSONL after successful import
	changed := (result.Created + result.Updated + len(result.IDMapping)) > 0
	if changed {
		if len(result.IDMapping) > 0 {
			// Remappings may affect many issues, do a full export
			markDirtyAndScheduleFullExport()
		} else {
			// Regular import, incremental export is fine
			markDirtyAndScheduleFlush()
		}
	}

	// Store new hash after successful import (renamed from last_import_hash)
	if err := store.SetMetadata(ctx, "jsonl_content_hash", currentHash); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update jsonl_content_hash after import: %v\n", err)
		fmt.Fprintf(os.Stderr, "This may cause auto-import to retry the same import on next operation.\n")
	}

	// Store import timestamp for staleness detection
	// Use RFC3339Nano for nanosecond precision to avoid race with file mtime
	importTime := time.Now().Format(time.RFC3339Nano)
	if err := store.SetMetadata(ctx, "last_import_time", importTime); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update last_import_time after import: %v\n", err)
	}
}



// markDirtyAndScheduleFlush marks the database as dirty and schedules a flush
// markDirtyAndScheduleFlush marks the database as dirty and schedules a debounced
// export to JSONL. Uses FlushManager's event-driven architecture.
//
// Debouncing behavior: If multiple operations happen within the debounce window, only
// one flush occurs after the burst of activity completes. This prevents excessive
// writes during rapid issue creation/updates.
//
// Flush-on-exit guarantee: PersistentPostRun calls flushManager.Shutdown() which
// performs a final flush before the command exits, ensuring no data is lost.
//
// Thread-safe: Safe to call from multiple goroutines (uses atomic.Bool).
// No-op if auto-flush is disabled via --no-auto-flush flag.
func markDirtyAndScheduleFlush() {
	// Track that this command performed a write (atomic to avoid data races).
	commandDidWrite.Store(true)

	// Use FlushManager if available
	// No FlushManager means sandbox mode or test without flush setup - no-op is correct
	if flushManager != nil {
		flushManager.MarkDirty(false) // Incremental export
	}
}

// markDirtyAndScheduleFullExport marks DB as needing a full export (for ID-changing operations)
func markDirtyAndScheduleFullExport() {
	// Track that this command performed a write (atomic to avoid data races).
	commandDidWrite.Store(true)

	// Use FlushManager if available
	// No FlushManager means sandbox mode or test without flush setup - no-op is correct
	if flushManager != nil {
		flushManager.MarkDirty(true) // Full export
	}
}

// clearAutoFlushState cancels pending flush and marks DB as clean (after manual export)
func clearAutoFlushState() {
	// With FlushManager, clearing state is unnecessary
	// If a flush is pending and fires after manual export, flushToJSONLWithState()
	// will detect nothing is dirty and skip the flush. This is harmless.
	// Reset failure counters on manual export success
	flushMutex.Lock()
	flushFailureCount = 0
	lastFlushError = nil
	flushMutex.Unlock()
}

// writeJSONLAtomic writes issues to a JSONL file atomically using temp file + rename.
// This is the common implementation used by flushToJSONLWithState (SQLite mode) and
// writeIssuesToJSONL (--no-db mode).
//
// Atomic write pattern:
//
//  1. Create temp file with PID suffix: issues.jsonl.tmp.12345
//  2. Write all issues as JSONL to temp file
//  3. Close temp file
//  4. Atomic rename: temp → target
//  5. Set file permissions to 0644
//
// Error handling: Returns error on any failure. Cleanup is guaranteed via defer.
// Thread-safe: No shared state access. Safe to call from multiple goroutines.
// validateJSONLIntegrity checks if JSONL file hash matches stored hash.
// If mismatch detected, clears export_hashes and logs warning.
// Returns (needsFullExport, error) where needsFullExport=true if export_hashes was cleared.
//
// hq-c2495f: Skip validation for Dolt backend since Dolt is source of truth, not JSONL.
// JSONL in Dolt mode is export-only, so hash mismatches are not concerning.
func validateJSONLIntegrity(ctx context.Context, jsonlPath string) (bool, error) {
	// Debug: Check if store is closed before calling
	if vc, ok := store.(interface{ IsClosed() bool }); ok && vc.IsClosed() {
		debug.Logf("validateJSONLIntegrity: store reports IsClosed()=true")
	}

	// hq-c2495f: Skip integrity validation for Dolt backend
	// In Dolt mode, JSONL is export-only, so mismatches don't matter
	// bd-dfw: Use store.BackendName() directly to detect Dolt
	if store != nil && store.BackendName() == configfile.BackendDolt {
		debug.Logf("validateJSONLIntegrity: skipped for Dolt backend")
		return false, nil
	}

	// Get stored JSONL file hash
	storedHash, err := store.GetJSONLFileHash(ctx)
	if err != nil {
		debug.Logf("validateJSONLIntegrity: GetJSONLFileHash failed: %v", err)
		return false, fmt.Errorf("failed to get stored JSONL hash: %w", err)
	}
	
	// If no hash stored, this is first export - skip validation
	if storedHash == "" {
		return false, nil
	}
	
	// Read current JSONL file
	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			// JSONL doesn't exist but we have a stored hash - clear export_hashes and jsonl_file_hash
			// bd-36869a: Log at debug level since this is normal recovery (e.g., after git checkout)
			debug.Logf("validateJSONLIntegrity: JSONL file missing but hash exists, triggering full re-export")
			if err := store.ClearAllExportHashes(ctx); err != nil {
				return false, fmt.Errorf("failed to clear export_hashes: %w", err)
			}
			// Also clear jsonl_file_hash to prevent perpetual mismatch warnings
			if err := store.SetJSONLFileHash(ctx, ""); err != nil {
				return false, fmt.Errorf("failed to clear jsonl_file_hash: %w", err)
			}
			return true, nil // Signal full export needed
		}
		return false, fmt.Errorf("failed to read JSONL file: %w", err)
	}
	
	// Compute current JSONL hash
	hasher := sha256.New()
	hasher.Write(jsonlData)
	currentHash := hex.EncodeToString(hasher.Sum(nil))
	
	// Compare hashes
	if currentHash != storedHash {
		// bd-36869a: Log at debug level since this is normal recovery after external changes
		// (e.g., git pull bringing in JSONL modifications). The system auto-recovers by
		// forcing a full re-export, so users don't need to see alarming warnings.
		debug.Logf("validateJSONLIntegrity: JSONL hash mismatch (stored=%s, current=%s), triggering full re-export",
			storedHash[:8], currentHash[:8])

		// Clear export_hashes to force full re-export
		if err := store.ClearAllExportHashes(ctx); err != nil {
			return false, fmt.Errorf("failed to clear export_hashes: %w", err)
		}
		// Also clear jsonl_file_hash to prevent perpetual mismatch warnings
		if err := store.SetJSONLFileHash(ctx, ""); err != nil {
			return false, fmt.Errorf("failed to clear jsonl_file_hash: %w", err)
		}
		return true, nil // Signal full export needed
	}
	
	return false, nil
}

func writeJSONLAtomic(jsonlPath string, issues []*types.Issue) ([]string, error) {
	// Sort issues by ID for consistent output
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	// Create temp file with PID suffix to avoid collisions
	tempPath := fmt.Sprintf("%s.tmp.%d", jsonlPath, os.Getpid())
	f, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	// Ensure cleanup on failure
	defer func() {
		if f != nil {
			_ = f.Close()
			_ = os.Remove(tempPath)
		}
	}()

	// Write all issues as JSONL (timestamp-only deduplication DISABLED)
	encoder := json.NewEncoder(f)
	skippedCount := 0
	exportedIDs := make([]string, 0, len(issues))
	
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
		 return nil, fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		
		exportedIDs = append(exportedIDs, issue.ID)
	}
	
	// Report skipped issues if any (helps debugging)
	if skippedCount > 0 {
		debug.Logf("auto-flush skipped %d issue(s) with timestamp-only changes", skippedCount)
	}

	// Close temp file before renaming
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}
	f = nil // Prevent defer cleanup

	// Atomic rename
	if err := os.Rename(tempPath, jsonlPath); err != nil {
		_ = os.Remove(tempPath) // Clean up on rename failure
		return nil, fmt.Errorf("failed to rename file: %w", err)
	}

	// Set appropriate file permissions (0644: rw-r--r--)
	// nolint:gosec // G302: JSONL needs to be readable by other tools
	if err := os.Chmod(jsonlPath, 0644); err != nil {
		// Non-fatal - file is already written
		debug.Logf("failed to set file permissions: %v", err)
	}

	return exportedIDs, nil
}


// flushState captures the state needed for a flush operation
type flushState struct {
	forceDirty      bool // Force flush even if isDirty is false
	forceFullExport bool // Force full export even if needsFullExport is false
}

// flushToJSONLWithState is a no-op in dolt-native mode.
// Retained because callers (flush_manager, import, tests) still invoke it.
func flushToJSONLWithState(state flushState) {
	// Check if store is still active (not closed) and not nil
	storeMutex.Lock()
	if !storeActive || store == nil {
		storeMutex.Unlock()
		return
	}
	storeMutex.Unlock()

	ctx := rootCtx
	if ctx == nil {
		ctx = context.Background()
	}

	// Dolt-native mode: JSONL export is disabled — Dolt is the source of truth
	debug.Logf("skipping autoflush (dolt-native mode)")
}

