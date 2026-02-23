package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
)

var sqlCSV bool

var sqlCmd = &cobra.Command{
	Use:   "sql <query>",
	Short: "Run a read-only SQL query against the Dolt database",
	Long: `Execute a read-only (SELECT) SQL query against the bd-daemon's Dolt database.

Only SELECT statements are allowed. The daemon rejects any query that could
modify data (INSERT, UPDATE, DELETE, DROP, ALTER, CREATE, etc.).

Output formats:
  Default: table format (aligned columns)
  --json:  JSON array of objects
  --csv:   CSV format for machine parsing

Examples:
  bd sql 'SELECT id, title FROM issues WHERE status = "open"'
  bd sql --csv 'SELECT id, title, status FROM issues WHERE issue_type = "epic"'
  bd sql --json 'SELECT COUNT(*) as total, status FROM issues GROUP BY status'
  bd sql 'SELECT id, title FROM issues WHERE description LIKE "%runbook%"'`,
	Args: cobra.ExactArgs(1),
	RunE: runSQL,
}

func init() {
	sqlCmd.Flags().BoolVar(&sqlCSV, "csv", false, "Output in CSV format")
	rootCmd.AddCommand(sqlCmd)
}

func runSQL(_ *cobra.Command, args []string) error {
	query := args[0]

	requireDaemon("sql")

	result, err := getDaemonClient().SQL(&rpc.SQLArgs{Query: query})
	if err != nil {
		return fmt.Errorf("execute sql query: %w", err)
	}

	if isJSONOutput() {
		return outputSQLJSON(result)
	}
	if sqlCSV {
		return outputSQLCSV(result)
	}
	return outputSQLTable(result)
}

func outputSQLJSON(result *rpc.SQLResult) error {
	data, err := json.MarshalIndent(result.Rows, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func outputSQLCSV(result *rpc.SQLResult) error {
	w := csv.NewWriter(os.Stdout)

	// Header
	if err := w.Write(result.Columns); err != nil {
		return fmt.Errorf("write csv header: %w", err)
	}

	// Rows
	for _, row := range result.Rows {
		record := make([]string, len(result.Columns))
		for i, col := range result.Columns {
			record[i] = fmt.Sprintf("%v", row[col])
		}
		if err := w.Write(record); err != nil {
			return fmt.Errorf("write csv row: %w", err)
		}
	}

	w.Flush()
	return w.Error()
}

func outputSQLTable(result *rpc.SQLResult) error {
	if len(result.Rows) == 0 {
		fmt.Println("(0 rows)")
		return nil
	}

	// Calculate column widths
	widths := make([]int, len(result.Columns))
	for i, col := range result.Columns {
		widths[i] = len(col)
	}
	for _, row := range result.Rows {
		for i, col := range result.Columns {
			val := fmt.Sprintf("%v", row[col])
			if len(val) > widths[i] {
				widths[i] = len(val)
			}
		}
	}

	// Cap column widths at 60 chars for readability
	for i := range widths {
		if widths[i] > 60 {
			widths[i] = 60
		}
	}

	// Print header
	parts := make([]string, len(result.Columns))
	for i, col := range result.Columns {
		parts[i] = fmt.Sprintf("%-*s", widths[i], col)
	}
	fmt.Println(strings.Join(parts, " | "))

	// Separator
	seps := make([]string, len(result.Columns))
	for i := range result.Columns {
		seps[i] = strings.Repeat("-", widths[i])
	}
	fmt.Println(strings.Join(seps, "-+-"))

	// Rows
	for _, row := range result.Rows {
		vals := make([]string, len(result.Columns))
		for i, col := range result.Columns {
			val := fmt.Sprintf("%v", row[col])
			if len(val) > 60 {
				val = val[:57] + "..."
			}
			vals[i] = fmt.Sprintf("%-*s", widths[i], val)
		}
		fmt.Println(strings.Join(vals, " | "))
	}

	fmt.Printf("(%d rows)\n", result.Count)
	return nil
}
