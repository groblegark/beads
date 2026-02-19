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

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// findJSONLPath finds the JSONL file path for the current database and ensures
// the parent directory exists. Uses beads.FindJSONLPath() for discovery (checking
// BEADS_JSONL env var first, then using .beads/issues.jsonl next to the database).
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
	dbDir := filepath.Dir(jsonlPath)
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		return utils.CanonicalizeIfRelative(jsonlPath)
	}

	return utils.CanonicalizeIfRelative(jsonlPath)
}

// detectPrefixFromJSONL extracts the issue prefix from JSONL data.
// Returns empty string if prefix cannot be detected.
// Used by cold-start bootstrap to initialize the database (GH#b09).
func detectPrefixFromJSONL(jsonlData []byte) string {
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
		return issue.ID
	}
	return ""
}

// validateJSONLIntegrity checks if JSONL file hash matches stored hash.
// If mismatch detected, clears export_hashes and logs warning.
// Returns (needsFullExport, error) where needsFullExport=true if export_hashes was cleared.
//
// hq-c2495f: Skip validation for Dolt backend since Dolt is source of truth, not JSONL.
func validateJSONLIntegrity(ctx context.Context, jsonlPath string) (bool, error) {
	if vc, ok := store.(interface{ IsClosed() bool }); ok && vc.IsClosed() {
		debug.Logf("validateJSONLIntegrity: store reports IsClosed()=true")
	}

	// hq-c2495f: Skip integrity validation for Dolt backend
	if store != nil && store.BackendName() == configfile.BackendDolt {
		debug.Logf("validateJSONLIntegrity: skipped for Dolt backend")
		return false, nil
	}

	storedHash, err := store.GetJSONLFileHash(ctx)
	if err != nil {
		debug.Logf("validateJSONLIntegrity: GetJSONLFileHash failed: %v", err)
		return false, fmt.Errorf("failed to get stored JSONL hash: %w", err)
	}

	if storedHash == "" {
		return false, nil
	}

	jsonlData, err := os.ReadFile(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			debug.Logf("validateJSONLIntegrity: JSONL file missing but hash exists, triggering full re-export")
			if err := store.ClearAllExportHashes(ctx); err != nil {
				return false, fmt.Errorf("failed to clear export_hashes: %w", err)
			}
			if err := store.SetJSONLFileHash(ctx, ""); err != nil {
				return false, fmt.Errorf("failed to clear jsonl_file_hash: %w", err)
			}
			return true, nil
		}
		return false, fmt.Errorf("failed to read JSONL file: %w", err)
	}

	hasher := sha256.New()
	hasher.Write(jsonlData)
	currentHash := hex.EncodeToString(hasher.Sum(nil))

	if currentHash != storedHash {
		debug.Logf("validateJSONLIntegrity: JSONL hash mismatch (stored=%s, current=%s), triggering full re-export",
			storedHash[:8], currentHash[:8])

		if err := store.ClearAllExportHashes(ctx); err != nil {
			return false, fmt.Errorf("failed to clear export_hashes: %w", err)
		}
		if err := store.SetJSONLFileHash(ctx, ""); err != nil {
			return false, fmt.Errorf("failed to clear jsonl_file_hash: %w", err)
		}
		return true, nil
	}

	return false, nil
}

// writeJSONLAtomic writes issues to a JSONL file atomically using temp file + rename.
//
// Atomic write pattern:
//  1. Create temp file with PID suffix
//  2. Write all issues as JSONL to temp file
//  3. Close temp file
//  4. Atomic rename: temp -> target
//  5. Set file permissions to 0644
func writeJSONLAtomic(jsonlPath string, issues []*types.Issue) ([]string, error) {
	slices.SortFunc(issues, func(a, b *types.Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	tempPath := fmt.Sprintf("%s.tmp.%d", jsonlPath, os.Getpid())
	f, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	defer func() {
		if f != nil {
			_ = f.Close()
			_ = os.Remove(tempPath)
		}
	}()

	encoder := json.NewEncoder(f)
	skippedCount := 0
	exportedIDs := make([]string, 0, len(issues))

	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return nil, fmt.Errorf("failed to encode issue %s: %w", issue.ID, err)
		}
		exportedIDs = append(exportedIDs, issue.ID)
	}

	if skippedCount > 0 {
		debug.Logf("auto-flush skipped %d issue(s) with timestamp-only changes", skippedCount)
	}

	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("failed to close temp file: %w", err)
	}
	f = nil

	if err := os.Rename(tempPath, jsonlPath); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("failed to rename file: %w", err)
	}

	// nolint:gosec // G302: JSONL needs to be readable by other tools
	if err := os.Chmod(jsonlPath, 0644); err != nil {
		debug.Logf("failed to set file permissions: %v", err)
	}

	return exportedIDs, nil
}

// markDirtyAndScheduleFlush is a no-op stub retained for callers that haven't
// been cleaned up yet. In daemon-only mode, the daemon handles persistence.
// Still tracks commandDidWrite for Dolt auto-commit.
func markDirtyAndScheduleFlush() {
	commandDidWrite.Store(true)
}

// markDirtyAndScheduleFullExport is a no-op stub retained for callers.
// Still tracks commandDidWrite for Dolt auto-commit.
func markDirtyAndScheduleFullExport() {
	commandDidWrite.Store(true)
}

// clearAutoFlushState is a no-op stub. In daemon-only mode, flush state
// is managed by the daemon.
func clearAutoFlushState() {}

// flushState captures the state needed for a flush operation (retained for type compatibility).
type flushState struct {
	forceDirty bool
}

// flushToJSONLWithState is a no-op in daemon-only mode.
// Retained because callers (import) still invoke it.
func flushToJSONLWithState(_ flushState) {
	debug.Logf("skipping flushToJSONLWithState (daemon-only mode)")
}
