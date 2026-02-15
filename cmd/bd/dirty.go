package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var dirtyCmd = &cobra.Command{
	Use:     "dirty",
	GroupID: "advanced",
	Short:   "Manage the dirty_issues tracking table",
	Long: `Manage the dirty_issues table used for incremental JSONL export.

Issues are marked dirty when created, updated, or when dependencies change.
Dirty issues are cleared after successful export to JSONL. Over time, stale
entries can accumulate (orphaned references, already-exported issues) and
inflate query scan times.

Subcommands:
  count   Show the number of dirty issues
  flush   Remove stale entries from the dirty_issues table`,
}

var dirtyCountCmd = &cobra.Command{
	Use:   "count",
	Short: "Show the number of dirty issues",
	Run: func(cmd *cobra.Command, args []string) {
		if store == nil {
			fmt.Fprintf(os.Stderr, "Error: dirty count requires database access; ensure daemon is running\n")
			os.Exit(1)
		}

		ctx := getRootContext()
		s := getStore()

		count, err := s.DirtyCount(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			outputJSON(map[string]int{"dirty_count": count})
		} else {
			fmt.Printf("Dirty issues: %d\n", count)
		}
	},
}

var dirtyFlushCmd = &cobra.Command{
	Use:   "flush",
	Short: "Remove stale entries from the dirty_issues table",
	Long: `Remove stale dirty_issues entries that inflate query scan times.

This cleans up two categories of stale entries:
1. Orphaned entries — issue no longer exists in the issues table
2. Already-exported entries — content hash matches the last export

The daemon runs this automatically every 5 minutes. Use this command
for manual cleanup or when the daemon is not running.`,
	Run: func(cmd *cobra.Command, args []string) {
		if store == nil {
			fmt.Fprintf(os.Stderr, "Error: dirty flush requires database access; ensure daemon is running\n")
			os.Exit(1)
		}

		ctx := getRootContext()
		s := getStore()
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		beforeCount, err := s.DirtyCount(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting dirty count: %v\n", err)
			os.Exit(1)
		}

		if dryRun {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"dirty_count": beforeCount,
					"dry_run":     true,
				})
			} else {
				fmt.Printf("Dirty issues: %d\n", beforeCount)
				fmt.Println("  (dry run — no changes made)")
			}
			return
		}

		orphanRemoved, exportRemoved, err := s.FlushStaleDirtyIssues(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error flushing dirty issues: %v\n", err)
			os.Exit(1)
		}
		totalRemoved := orphanRemoved + exportRemoved

		afterCount, err := s.DirtyCount(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to get post-flush count: %v\n", err)
			afterCount = beforeCount - int(totalRemoved)
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"before":           beforeCount,
				"after":            afterCount,
				"orphaned_removed": orphanRemoved,
				"exported_removed": exportRemoved,
				"total_removed":    totalRemoved,
			})
		} else {
			fmt.Printf("Dirty issues before: %d\n", beforeCount)
			fmt.Printf("  Orphaned removed:  %d\n", orphanRemoved)
			fmt.Printf("  Exported removed:  %d\n", exportRemoved)
			fmt.Printf("  Total removed:     %d\n", totalRemoved)
			fmt.Printf("Dirty issues after:  %d\n", afterCount)
		}
	},
}

func init() {
	dirtyFlushCmd.Flags().Bool("dry-run", false, "Preview what would be removed without making changes")

	dirtyCmd.AddCommand(dirtyCountCmd)
	dirtyCmd.AddCommand(dirtyFlushCmd)
	rootCmd.AddCommand(dirtyCmd)
}
