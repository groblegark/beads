package rpc

import (
	"encoding/json"
	"fmt"
	"strings"
)

// handleSQL executes a read-only SQL query against the Dolt database (gt-kmapkw).
// Only SELECT statements are allowed — all other SQL is rejected server-side.
func (s *Server) handleSQL(req *Request) Response {
	var args SQLArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid sql args: %v", err),
		}
	}

	query := strings.TrimSpace(args.Query)
	if query == "" {
		return Response{
			Success: false,
			Error:   "query cannot be empty",
		}
	}

	// Security: reject anything that isn't a SELECT statement.
	// Normalize to uppercase for comparison, but execute the original.
	upper := strings.ToUpper(query)

	// Strip leading WITH (CTEs are read-only and fine)
	check := upper
	if strings.HasPrefix(check, "WITH ") {
		// Find the final SELECT after CTE(s)
		// CTEs are read-only, so we allow them
	} else if !strings.HasPrefix(check, "SELECT ") && !strings.HasPrefix(check, "SELECT\n") && !strings.HasPrefix(check, "SELECT\t") && check != "SELECT" {
		return Response{
			Success: false,
			Error:   "only SELECT queries are allowed (read-only access)",
		}
	}

	// Reject statements that could modify data even inside SELECT context
	dangerousKeywords := []string{
		"INSERT ", "UPDATE ", "DELETE ", "DROP ", "ALTER ", "CREATE ",
		"TRUNCATE ", "REPLACE ", "GRANT ", "REVOKE ",
		"CALL ", "EXEC ", "EXECUTE ",
	}
	for _, kw := range dangerousKeywords {
		if strings.Contains(upper, kw) {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("query contains forbidden keyword %q (read-only access only)", strings.TrimSpace(kw)),
			}
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	db := store.UnderlyingDB()
	if db == nil {
		return Response{
			Success: false,
			Error:   "database connection not available",
		}
	}

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("query failed: %v", err),
		}
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get columns: %v", err),
		}
	}

	var results []map[string]interface{}
	for rows.Next() {
		// Create a slice of interface{} to hold column values
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to scan row: %v", err),
			}
		}

		row := make(map[string]interface{}, len(columns))
		for i, col := range columns {
			val := values[i]
			// Convert []byte to string for JSON serialization
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
	}

	if err := rows.Err(); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("row iteration error: %v", err),
		}
	}

	if results == nil {
		results = []map[string]interface{}{}
	}

	result := &SQLResult{
		Columns: columns,
		Rows:    results,
		Count:   len(results),
	}

	data, _ := json.Marshal(result)
	return Response{Success: true, Data: data}
}
