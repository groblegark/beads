package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// handleAdminGC runs dolt gc on the server-side Dolt repository (bd-ma0s.5).
func (s *Server) handleAdminGC(req *Request) Response {
	var args AdminGCArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid admin_gc args: %v", err),
		}
	}

	// Derive dolt path from the database path.
	// dbPath is typically .beads/beads.db; dolt dir is .beads/dolt
	doltPath := filepath.Join(filepath.Dir(s.dbPath), "dolt")
	if _, err := os.Stat(doltPath); os.IsNotExist(err) {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("dolt directory not found at %s", doltPath),
		}
	}

	// Check that dolt binary is available
	if _, err := exec.LookPath("dolt"); err != nil {
		return Response{
			Success: false,
			Error:   "dolt command not found in PATH",
		}
	}

	start := time.Now()

	// Get size before GC
	bytesBefore, err := adminGetDirSize(doltPath)
	if err != nil {
		bytesBefore = 0
	}

	if args.DryRun {
		elapsed := time.Since(start)
		result := AdminGCResult{
			DoltPath:    doltPath,
			BytesBefore: bytesBefore,
			BytesAfter:  bytesBefore,
			SpaceFreed:  0,
			DryRun:      true,
			ElapsedMs:   elapsed.Milliseconds(),
		}
		data, _ := json.Marshal(result)
		return Response{Success: true, Data: data}
	}

	// Run dolt gc with a timeout context
	ctx, cancel := s.reqCtx(req)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dolt", "gc") // #nosec G204 -- fixed command, no user input
	cmd.Dir = doltPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("dolt gc failed: %v (output: %s)", err, string(output)),
		}
	}

	// Get size after GC
	bytesAfter, err := adminGetDirSize(doltPath)
	if err != nil {
		bytesAfter = 0
	}

	elapsed := time.Since(start)
	spaceFreed := bytesBefore - bytesAfter
	if spaceFreed < 0 {
		spaceFreed = 0
	}

	result := AdminGCResult{
		DoltPath:    doltPath,
		BytesBefore: bytesBefore,
		BytesAfter:  bytesAfter,
		SpaceFreed:  spaceFreed,
		ElapsedMs:   elapsed.Milliseconds(),
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// handleGCTombstones deletes tombstone (and optionally closed) issues from the database (bd-t8b0).
func (s *Server) handleGCTombstones(req *Request) Response {
	var args GCTombstonesArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gc_tombstones args: %v", err),
		}
	}

	ds, errResp := s.getDoltStore()
	if errResp != nil {
		return *errResp
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	var olderThan time.Duration
	if args.OlderThanDays > 0 {
		olderThan = time.Duration(args.OlderThanDays) * 24 * time.Hour
	}

	if args.DryRun {
		return gcTombstonesDryRun(ctx, ds, olderThan, args.IncludeClosed)
	}

	start := time.Now()
	gcResult, err := ds.GCTombstones(ctx, olderThan, args.IncludeClosed)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gc_tombstones failed: %v", err),
		}
	}

	elapsed := time.Since(start)
	result := GCTombstonesResult{
		TombstonesDeleted: gcResult.TombstonesDeleted,
		ClosedDeleted:     gcResult.ClosedDeleted,
		DepsDeleted:       gcResult.DepsDeleted,
		EventsDeleted:     gcResult.EventsDeleted,
		CommentsDeleted:   gcResult.CommentsDeleted,
		LabelsDeleted:     gcResult.LabelsDeleted,
		DirtyDeleted:      gcResult.DirtyDeleted,
		TotalBefore:       gcResult.TotalBefore,
		TotalAfter:        gcResult.TotalAfter,
		ElapsedMs:         elapsed.Milliseconds(),
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// gcTombstonesDryRun counts what would be deleted without modifying the database.
func gcTombstonesDryRun(ctx context.Context, ds *dolt.DoltStore, olderThan time.Duration, includeClosed bool) Response {
	start := time.Now()
	db := ds.UnderlyingDB()

	var tombstoneCount, closedCount, totalCount int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&totalCount)

	if olderThan > 0 {
		cutoff := time.Now().UTC().Add(-olderThan)
		_ = db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issues WHERE status = 'tombstone' AND deleted_at IS NOT NULL AND deleted_at < ? AND (pinned = 0 OR pinned IS NULL)",
			cutoff).Scan(&tombstoneCount)
		if includeClosed {
			_ = db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM issues WHERE status = 'closed' AND closed_at IS NOT NULL AND closed_at < ? AND (pinned = 0 OR pinned IS NULL)",
				cutoff).Scan(&closedCount)
		}
	} else {
		_ = db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM issues WHERE status = 'tombstone' AND (pinned = 0 OR pinned IS NULL)").Scan(&tombstoneCount)
		if includeClosed {
			_ = db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM issues WHERE status = 'closed' AND (pinned = 0 OR pinned IS NULL)").Scan(&closedCount)
		}
	}

	elapsed := time.Since(start)
	result := GCTombstonesResult{
		TombstonesDeleted: tombstoneCount,
		ClosedDeleted:     closedCount,
		TotalBefore:       totalCount,
		TotalAfter:        totalCount - tombstoneCount - closedCount,
		DryRun:            true,
		ElapsedMs:         elapsed.Milliseconds(),
	}
	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}

// adminGetDirSize calculates the total size of a directory.
func adminGetDirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}
