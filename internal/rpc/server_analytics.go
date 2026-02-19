//go:build !windows

package rpc

import (
	"encoding/json"
	"fmt"
	"time"
)

// Analytics endpoint types (beads-cqpj)

// BurndownArgs configures the burndown time-series query.
type BurndownArgs struct {
	StartDate string `json:"start_date,omitempty"` // ISO 8601 date (default: 30 days ago)
	EndDate   string `json:"end_date,omitempty"`   // ISO 8601 date (default: now)
	Interval  string `json:"interval,omitempty"`   // "day" (default), "week", "month"
	Rig       string `json:"rig,omitempty"`        // Filter by rig/project
}

// BurndownDataPoint represents issue counts at a point in time.
type BurndownDataPoint struct {
	Date            string `json:"date"`             // YYYY-MM-DD
	CreatedCount    int    `json:"created_count"`    // Issues created in this interval
	ClosedCount     int    `json:"closed_count"`     // Issues closed in this interval
	OpenCumulative  int    `json:"open_cumulative"`  // Cumulative open (created - closed) up to this date
}

// BurndownResponse is the response from the Burndown endpoint.
type BurndownResponse struct {
	StartDate  string              `json:"start_date"`
	EndDate    string              `json:"end_date"`
	Interval   string              `json:"interval"`
	DataPoints []BurndownDataPoint `json:"data_points"`
}

// VelocityArgs configures the velocity time-series query.
type VelocityArgs struct {
	StartDate string `json:"start_date,omitempty"` // ISO 8601 (default: 30 days ago)
	EndDate   string `json:"end_date,omitempty"`   // ISO 8601 (default: now)
	Interval  string `json:"interval,omitempty"`   // "day", "week" (default), "month"
	Rig       string `json:"rig,omitempty"`
	Assignee  string `json:"assignee,omitempty"`   // Filter by assignee
}

// VelocityDataPoint represents throughput metrics for one interval.
type VelocityDataPoint struct {
	Date          string  `json:"date"`                     // Interval start YYYY-MM-DD
	ClosedCount   int     `json:"closed_count"`             // Issues closed this interval
	CreatedCount  int     `json:"created_count"`            // Issues created this interval
	AvgCycleHours float64 `json:"avg_cycle_hours,omitempty"` // Mean cycle time (created→closed) in hours
}

// VelocityResponse is the response from the Velocity endpoint.
type VelocityResponse struct {
	StartDate  string              `json:"start_date"`
	EndDate    string              `json:"end_date"`
	Interval   string              `json:"interval"`
	DataPoints []VelocityDataPoint `json:"data_points"`
}

// CycleTimeArgs configures the cycle-time analytics query.
type CycleTimeArgs struct {
	StartDate string `json:"start_date,omitempty"` // ISO 8601 (default: 90 days ago)
	EndDate   string `json:"end_date,omitempty"`   // ISO 8601 (default: now)
	GroupBy   string `json:"group_by,omitempty"`   // "type", "priority", "assignee" (default: overall)
	Rig       string `json:"rig,omitempty"`
}

// CycleTimeBucket represents cycle-time stats for one group.
type CycleTimeBucket struct {
	Group       string  `json:"group"`
	Count       int     `json:"count"`        // Number of closed issues in group
	MedianHours float64 `json:"median_hours"` // Median cycle time
	AvgHours    float64 `json:"avg_hours"`    // Mean cycle time
	P90Hours    float64 `json:"p90_hours"`    // 90th percentile
	MinHours    float64 `json:"min_hours"`
	MaxHours    float64 `json:"max_hours"`
}

// CycleTimeResponse is the response from the CycleTime endpoint.
type CycleTimeResponse struct {
	StartDate string            `json:"start_date"`
	EndDate   string            `json:"end_date"`
	GroupBy   string            `json:"group_by"`
	Buckets   []CycleTimeBucket `json:"buckets"`
}

// parseAnalyticsDate parses an ISO 8601 date string, falling back to a default.
func parseAnalyticsDate(s string, fallback time.Time) time.Time {
	if s == "" {
		return fallback
	}
	// Try full RFC3339 first
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	// Try date-only
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return fallback
}

// intervalToDateTrunc returns the SQL DATE_FORMAT / DATE expression for grouping.
// Dolt supports DATE() for day, and we compute week/month via string formatting.
func intervalToDateFormat(interval string) string {
	switch interval {
	case "week":
		// ISO week: group by Monday-start week. DATE_FORMAT YYYY-WXX isn't standard in Dolt,
		// so we compute the Monday of each week.
		return "DATE(DATE_SUB(created_at, INTERVAL WEEKDAY(created_at) DAY))"
	case "month":
		return "DATE_FORMAT(created_at, '%Y-%m-01')"
	default: // "day"
		return "DATE(created_at)"
	}
}

func intervalToClosedDateFormat(interval string) string {
	switch interval {
	case "week":
		return "DATE(DATE_SUB(closed_at, INTERVAL WEEKDAY(closed_at) DAY))"
	case "month":
		return "DATE_FORMAT(closed_at, '%Y-%m-01')"
	default:
		return "DATE(closed_at)"
	}
}

// handleBurndown returns a time-series of created vs closed issues (beads-cqpj).
func (s *Server) handleBurndown(req *Request) Response {
	var args BurndownArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("invalid burndown args: %v", err)}
		}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}

	db := store.UnderlyingDB()
	if db == nil {
		return Response{Success: false, Error: "database connection not available"}
	}

	now := time.Now()
	startDate := parseAnalyticsDate(args.StartDate, now.AddDate(0, 0, -30))
	endDate := parseAnalyticsDate(args.EndDate, now)
	interval := args.Interval
	if interval == "" {
		interval = "day"
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	startStr := startDate.Format("2006-01-02")
	endStr := endDate.Format("2006-01-02")

	createdGroupExpr := intervalToDateFormat(interval)
	closedGroupExpr := intervalToClosedDateFormat(interval)

	// Query created counts per interval
	rigFilter := ""
	rigArgs := []interface{}{startStr, endStr}
	if args.Rig != "" {
		rigFilter = " AND id LIKE CONCAT(?, '-%')"
		rigArgs = append(rigArgs, args.Rig)
	}

	createdQuery := fmt.Sprintf(`
		SELECT %s AS bucket_date, COUNT(*) AS cnt
		FROM issues
		WHERE status != 'tombstone'
		  AND created_at >= ? AND created_at < DATE_ADD(?, INTERVAL 1 DAY)
		  %s
		GROUP BY bucket_date
		ORDER BY bucket_date`, createdGroupExpr, rigFilter)

	createdRows, err := db.QueryContext(ctx, createdQuery, rigArgs...)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("burndown query (created) failed: %v", err)}
	}
	defer createdRows.Close()

	createdMap := map[string]int{}
	for createdRows.Next() {
		var date string
		var count int
		if err := createdRows.Scan(&date, &count); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("scan error: %v", err)}
		}
		// Normalize date format (MySQL may return with time component)
		if len(date) > 10 {
			date = date[:10]
		}
		createdMap[date] = count
	}
	if err := createdRows.Err(); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("row iteration error: %v", err)}
	}

	// Query closed counts per interval
	closedRigArgs := []interface{}{startStr, endStr}
	if args.Rig != "" {
		closedRigArgs = append(closedRigArgs, args.Rig)
	}

	closedQuery := fmt.Sprintf(`
		SELECT %s AS bucket_date, COUNT(*) AS cnt
		FROM issues
		WHERE closed_at IS NOT NULL
		  AND closed_at >= ? AND closed_at < DATE_ADD(?, INTERVAL 1 DAY)
		  %s
		GROUP BY bucket_date
		ORDER BY bucket_date`, closedGroupExpr, rigFilter)

	closedRows, err := db.QueryContext(ctx, closedQuery, closedRigArgs...)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("burndown query (closed) failed: %v", err)}
	}
	defer closedRows.Close()

	closedMap := map[string]int{}
	for closedRows.Next() {
		var date string
		var count int
		if err := closedRows.Scan(&date, &count); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("scan error: %v", err)}
		}
		if len(date) > 10 {
			date = date[:10]
		}
		closedMap[date] = count
	}
	if err := closedRows.Err(); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("row iteration error: %v", err)}
	}

	// Get baseline: how many issues were open before start date
	baselineQuery := `
		SELECT
			COALESCE(SUM(CASE WHEN created_at < ? AND status != 'tombstone' THEN 1 ELSE 0 END), 0) -
			COALESCE(SUM(CASE WHEN closed_at IS NOT NULL AND closed_at < ? THEN 1 ELSE 0 END), 0)
		FROM issues
		WHERE 1=1` + rigFilter
	baselineArgs := []interface{}{startStr, startStr}
	if args.Rig != "" {
		baselineArgs = append(baselineArgs, args.Rig)
	}

	var baseline int
	if err := db.QueryRowContext(ctx, baselineQuery, baselineArgs...).Scan(&baseline); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("baseline query failed: %v", err)}
	}

	// Build data points
	allDates := map[string]bool{}
	for d := range createdMap {
		allDates[d] = true
	}
	for d := range closedMap {
		allDates[d] = true
	}

	// Sort dates
	sortedDates := make([]string, 0, len(allDates))
	for d := range allDates {
		sortedDates = append(sortedDates, d)
	}
	sortStringSlice(sortedDates)

	cumulative := baseline
	dataPoints := make([]BurndownDataPoint, 0, len(sortedDates))
	for _, d := range sortedDates {
		created := createdMap[d]
		closed := closedMap[d]
		cumulative += created - closed
		dataPoints = append(dataPoints, BurndownDataPoint{
			Date:            d,
			CreatedCount:    created,
			ClosedCount:     closed,
			OpenCumulative:  cumulative,
		})
	}

	resp := &BurndownResponse{
		StartDate:  startStr,
		EndDate:    endStr,
		Interval:   interval,
		DataPoints: dataPoints,
	}
	data, _ := json.Marshal(resp)
	return Response{Success: true, Data: data}
}

// handleVelocity returns throughput metrics per interval (beads-cqpj).
func (s *Server) handleVelocity(req *Request) Response {
	var args VelocityArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("invalid velocity args: %v", err)}
		}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}
	db := store.UnderlyingDB()
	if db == nil {
		return Response{Success: false, Error: "database connection not available"}
	}

	now := time.Now()
	startDate := parseAnalyticsDate(args.StartDate, now.AddDate(0, 0, -30))
	endDate := parseAnalyticsDate(args.EndDate, now)
	interval := args.Interval
	if interval == "" {
		interval = "week"
	}

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	startStr := startDate.Format("2006-01-02")
	endStr := endDate.Format("2006-01-02")

	// Build optional filters
	var extraFilter string
	queryArgs := []interface{}{startStr, endStr}
	if args.Rig != "" {
		extraFilter += " AND id LIKE CONCAT(?, '-%')"
		queryArgs = append(queryArgs, args.Rig)
	}
	if args.Assignee != "" {
		extraFilter += " AND assignee = ?"
		queryArgs = append(queryArgs, args.Assignee)
	}

	closedGroupExpr := intervalToClosedDateFormat(interval)

	// Closed issues with avg cycle time per interval
	velocityQuery := fmt.Sprintf(`
		SELECT
			%s AS bucket_date,
			COUNT(*) AS closed_count,
			AVG(TIMESTAMPDIFF(SECOND, created_at, closed_at)) / 3600.0 AS avg_cycle_hours
		FROM issues
		WHERE closed_at IS NOT NULL
		  AND closed_at >= ? AND closed_at < DATE_ADD(?, INTERVAL 1 DAY)
		  AND status != 'tombstone'
		  %s
		GROUP BY bucket_date
		ORDER BY bucket_date`, closedGroupExpr, extraFilter)

	rows, err := db.QueryContext(ctx, velocityQuery, queryArgs...)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("velocity query failed: %v", err)}
	}
	defer rows.Close()

	closedData := map[string]VelocityDataPoint{}
	for rows.Next() {
		var date string
		var closedCount int
		var avgCycle *float64
		if err := rows.Scan(&date, &closedCount, &avgCycle); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("scan error: %v", err)}
		}
		if len(date) > 10 {
			date = date[:10]
		}
		dp := VelocityDataPoint{Date: date, ClosedCount: closedCount}
		if avgCycle != nil {
			dp.AvgCycleHours = *avgCycle
		}
		closedData[date] = dp
	}
	if err := rows.Err(); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("row iteration error: %v", err)}
	}

	// Created counts per interval
	createdGroupExpr := intervalToDateFormat(interval)
	createdArgs := []interface{}{startStr, endStr}
	var createdExtraFilter string
	if args.Rig != "" {
		createdExtraFilter += " AND id LIKE CONCAT(?, '-%')"
		createdArgs = append(createdArgs, args.Rig)
	}
	if args.Assignee != "" {
		createdExtraFilter += " AND assignee = ?"
		createdArgs = append(createdArgs, args.Assignee)
	}

	createdQuery := fmt.Sprintf(`
		SELECT %s AS bucket_date, COUNT(*) AS cnt
		FROM issues
		WHERE status != 'tombstone'
		  AND created_at >= ? AND created_at < DATE_ADD(?, INTERVAL 1 DAY)
		  %s
		GROUP BY bucket_date
		ORDER BY bucket_date`, createdGroupExpr, createdExtraFilter)

	cRows, err := db.QueryContext(ctx, createdQuery, createdArgs...)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("velocity created query failed: %v", err)}
	}
	defer cRows.Close()

	createdMap := map[string]int{}
	for cRows.Next() {
		var date string
		var count int
		if err := cRows.Scan(&date, &count); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("scan error: %v", err)}
		}
		if len(date) > 10 {
			date = date[:10]
		}
		createdMap[date] = count
	}
	if err := cRows.Err(); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("row iteration error: %v", err)}
	}

	// Merge into data points
	allDates := map[string]bool{}
	for d := range closedData {
		allDates[d] = true
	}
	for d := range createdMap {
		allDates[d] = true
	}

	sortedDates := make([]string, 0, len(allDates))
	for d := range allDates {
		sortedDates = append(sortedDates, d)
	}
	sortStringSlice(sortedDates)

	dataPoints := make([]VelocityDataPoint, 0, len(sortedDates))
	for _, d := range sortedDates {
		dp := closedData[d] // May be zero-valued if only created happened
		dp.Date = d
		dp.CreatedCount = createdMap[d]
		dataPoints = append(dataPoints, dp)
	}

	resp := &VelocityResponse{
		StartDate:  startStr,
		EndDate:    endStr,
		Interval:   interval,
		DataPoints: dataPoints,
	}
	data, _ := json.Marshal(resp)
	return Response{Success: true, Data: data}
}

// handleCycleTime returns cycle-time percentile stats grouped by dimension (beads-cqpj).
func (s *Server) handleCycleTime(req *Request) Response {
	var args CycleTimeArgs
	if len(req.Args) > 0 {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("invalid cycle_time args: %v", err)}
		}
	}

	store := s.storage
	if store == nil {
		return Response{Success: false, Error: "storage not available"}
	}
	db := store.UnderlyingDB()
	if db == nil {
		return Response{Success: false, Error: "database connection not available"}
	}

	now := time.Now()
	startDate := parseAnalyticsDate(args.StartDate, now.AddDate(0, -3, 0)) // 90 days default
	endDate := parseAnalyticsDate(args.EndDate, now)

	ctx, cancel := s.reqCtx(req)
	defer cancel()

	startStr := startDate.Format("2006-01-02")
	endStr := endDate.Format("2006-01-02")

	// Determine GROUP BY column
	var groupCol string
	switch args.GroupBy {
	case "type":
		groupCol = "issue_type"
	case "priority":
		groupCol = "CAST(priority AS CHAR)"
	case "assignee":
		groupCol = "COALESCE(NULLIF(assignee, ''), '(unassigned)')"
	default:
		groupCol = "'overall'"
	}

	var extraFilter string
	queryArgs := []interface{}{startStr, endStr}
	if args.Rig != "" {
		extraFilter += " AND id LIKE CONCAT(?, '-%')"
		queryArgs = append(queryArgs, args.Rig)
	}

	// Fetch all cycle times per group, then compute percentiles in Go.
	// This avoids Dolt's limited window function support.
	query := fmt.Sprintf(`
		SELECT
			%s AS grp,
			TIMESTAMPDIFF(SECOND, created_at, closed_at) / 3600.0 AS cycle_hours
		FROM issues
		WHERE closed_at IS NOT NULL
		  AND status != 'tombstone'
		  AND closed_at >= ? AND closed_at < DATE_ADD(?, INTERVAL 1 DAY)
		  AND created_at < closed_at
		  %s
		ORDER BY grp, cycle_hours`, groupCol, extraFilter)

	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("cycle_time query failed: %v", err)}
	}
	defer rows.Close()

	// Collect cycle times per group
	groupedHours := map[string][]float64{}
	for rows.Next() {
		var grp string
		var hours float64
		if err := rows.Scan(&grp, &hours); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("scan error: %v", err)}
		}
		groupedHours[grp] = append(groupedHours[grp], hours)
	}
	if err := rows.Err(); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("row iteration error: %v", err)}
	}

	// Compute percentiles
	buckets := make([]CycleTimeBucket, 0, len(groupedHours))
	for grp, hours := range groupedHours {
		if len(hours) == 0 {
			continue
		}
		// hours are already sorted (ORDER BY cycle_hours)
		n := len(hours)
		var sum float64
		for _, h := range hours {
			sum += h
		}

		bucket := CycleTimeBucket{
			Group:       grp,
			Count:       n,
			AvgHours:    sum / float64(n),
			MedianHours: percentile(hours, 0.50),
			P90Hours:    percentile(hours, 0.90),
			MinHours:    hours[0],
			MaxHours:    hours[n-1],
		}
		buckets = append(buckets, bucket)
	}

	// Sort buckets by group name for stable output
	sortCycleTimeBuckets(buckets)

	groupBy := args.GroupBy
	if groupBy == "" {
		groupBy = "overall"
	}

	resp := &CycleTimeResponse{
		StartDate: startStr,
		EndDate:   endStr,
		GroupBy:   groupBy,
		Buckets:   buckets,
	}
	data, _ := json.Marshal(resp)
	return Response{Success: true, Data: data}
}

// percentile returns the p-th percentile from a sorted slice using nearest-rank.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := int(p * float64(len(sorted)-1))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// sortStringSlice sorts a string slice in place (avoids importing sort in this file).
func sortStringSlice(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// sortCycleTimeBuckets sorts by group name.
func sortCycleTimeBuckets(b []CycleTimeBucket) {
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j].Group < b[j-1].Group; j-- {
			b[j], b[j-1] = b[j-1], b[j]
		}
	}
}
