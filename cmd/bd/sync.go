package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:     "sync",
	GroupID: "sync",
	Short:   "[REMOVED] Dolt handles sync automatically",
	Long: `bd sync has been removed. Dolt handles synchronization automatically.

For manual JSONL operations, use:
  bd export    Export database to JSONL
  bd import    Import from JSONL file`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		// beads-79f8: Hard error. Dolt backend handles sync natively.
		fmt.Fprintf(os.Stderr, "Error: bd sync has been removed\n\n")
		fmt.Fprintf(os.Stderr, "Dolt handles synchronization automatically. No manual sync needed.\n\n")
		fmt.Fprintf(os.Stderr, "For manual operations:\n")
		fmt.Fprintf(os.Stderr, "  bd export    Export database to JSONL\n")
		fmt.Fprintf(os.Stderr, "  bd import    Import from JSONL file\n")
		os.Exit(1)
		return nil
	},
}

func init() {
	// beads-79f8: All flags removed. Command always errors.
	// Command stays registered so users get a clear error instead of "unknown command".
	rootCmd.AddCommand(syncCmd)
}
