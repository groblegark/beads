package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/ui"
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Garbage collect dead issues from the database",
	Long: `Permanently delete tombstone issues (and optionally closed issues) from the
Dolt database to reduce table size and improve query performance.

Unlike 'bd cleanup' which only prunes JSONL exports, 'bd gc' deletes rows
from the actual database tables (issues, dependencies, events, comments,
labels, dirty_issues). This reduces JOIN overhead for all queries.

By default, only tombstone issues are deleted. Use --include-closed to also
delete closed issues (freeing the most space).

EXAMPLES:
  # Preview what would be deleted
  bd gc --dry-run

  # Delete all tombstones
  bd gc --force

  # Delete tombstones older than 7 days
  bd gc --older-than 7 --force

  # Delete tombstones AND closed issues
  bd gc --include-closed --force

  # Delete everything older than 30 days (tombstones + closed)
  bd gc --include-closed --older-than 30 --force

SAFETY:
  - Pinned issues are never deleted
  - Requires --force to actually delete
  - Use --dry-run to preview
  - This operation is irreversible`,
	Run: runGC,
}

func init() {
	gcCmd.Flags().BoolP("force", "f", false, "Actually delete (required to proceed)")
	gcCmd.Flags().Bool("dry-run", false, "Preview what would be deleted")
	gcCmd.Flags().Int("older-than", 0, "Only delete issues older than N days (0 = all)")
	gcCmd.Flags().Bool("include-closed", false, "Also delete closed issues (not just tombstones)")
	adminCmd.AddCommand(gcCmd)
}

func runGC(cmd *cobra.Command, args []string) {
	CheckReadonly("gc")

	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	olderThanDays, _ := cmd.Flags().GetInt("older-than")
	includeClosed, _ := cmd.Flags().GetBool("include-closed")

	requireDaemon("gc")

	start := time.Now()

	gcArgs := &rpc.GCTombstonesArgs{
		OlderThanDays: olderThanDays,
		IncludeClosed: includeClosed,
		DryRun:        dryRun || !force,
	}

	result, err := daemonClient.GCTombstones(gcArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	elapsed := time.Since(start)

	if jsonOutput {
		outputJSON(result)
		return
	}

	if result.DryRun {
		fmt.Printf("%s DRY RUN — Database Garbage Collection\n\n", ui.RenderWarn(""))
		fmt.Printf("  Issues in database: %d\n", result.TotalBefore)
		fmt.Printf("  Tombstones to delete: %d\n", result.TombstonesDeleted)
		if includeClosed {
			fmt.Printf("  Closed to delete: %d\n", result.ClosedDeleted)
		}
		total := result.TombstonesDeleted + result.ClosedDeleted
		fmt.Printf("  Total to delete: %d\n", total)
		fmt.Printf("  Issues after GC: %d\n", result.TotalAfter)
		if total > 0 {
			pct := float64(total) / float64(result.TotalBefore) * 100
			fmt.Printf("  Reduction: %.1f%%\n", pct)
		}

		if !force {
			fmt.Printf("\n  Use --force to proceed, or --dry-run to preview.\n")
		}
		return
	}

	total := result.TombstonesDeleted + result.ClosedDeleted
	fmt.Printf("%s Garbage collection complete\n\n", ui.RenderPass("✓"))
	fmt.Printf("  Issues before: %d\n", result.TotalBefore)
	fmt.Printf("  Tombstones deleted: %d\n", result.TombstonesDeleted)
	if result.ClosedDeleted > 0 {
		fmt.Printf("  Closed deleted: %d\n", result.ClosedDeleted)
	}
	fmt.Printf("  Dependencies deleted: %d\n", result.DepsDeleted)
	fmt.Printf("  Events deleted: %d\n", result.EventsDeleted)
	fmt.Printf("  Comments deleted: %d\n", result.CommentsDeleted)
	fmt.Printf("  Labels deleted: %d\n", result.LabelsDeleted)
	fmt.Printf("  Issues after: %d\n", result.TotalAfter)
	if total > 0 && result.TotalBefore > 0 {
		pct := float64(total) / float64(result.TotalBefore) * 100
		fmt.Printf("  Reduction: %.1f%%\n", pct)
	}
	fmt.Printf("  Time: %v\n", elapsed.Round(time.Millisecond))
}
