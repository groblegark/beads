//go:build cgo

// Package dolt - database migrations
package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Migration represents a single database migration
type Migration struct {
	Name string
	Func func(context.Context, *sql.DB) error
}

// migrations is the ordered list of all migrations to run
// Migrations are run in order during database initialization
// NOTE: advice_target_fields migration removed - those columns are deprecated (bd-hhbu)
var migrations = []Migration{
	{"advice_hook_fields", migrateAdviceHookFields},
	{"advice_subscription_fields", migrateAdviceSubscriptionFields},
	{"blocked_issues_cache", migrateBlockedIssuesCache},
	{"drop_ready_issues_view", migrateDropReadyIssuesView},
	{"inbox_table", migrateInboxTable},
	{"session_registry_table", migrateSessionRegistryTable},
	{"inbox_dedup_unique", migrateInboxDedupUnique},
	{"inbox_broadcast_ack", migrateInboxBroadcastAck},
	{"session_registry_project_root", migrateSessionRegistryProjectRoot},
	{"created_by_session", migrateCreatedBySession},
	{"inbox_dedup_rows", migrateInboxDedupRows},
	{"issue_commits_table", migrateIssueCommitsTable},
	{"decision_yielded_at", migrateDecisionYieldedAt},
	{"decision_pending_index", migrateDecisionPendingIndex},
	{"query_perf_indexes", migrateQueryPerfIndexes},
	{"blocked_cache_dep_index", migrateBlockedCacheDepIndex},
	{"time_column_indexes", migrateTimeColumnIndexes},
	{"jack_expires_at", migrateJackExpiresAt},
	{"inbox_subject", migrateInboxSubject},
}

// RunMigrations executes all registered migrations in order.
// Each migration is idempotent - it checks if changes are needed before applying.
func RunMigrations(ctx context.Context, db *sql.DB) error {
	for _, migration := range migrations {
		if err := migration.Func(ctx, db); err != nil {
			return fmt.Errorf("migration %s failed: %w", migration.Name, err)
		}
	}
	return nil
}

// columnExists checks if a column exists in the specified table using information_schema
func columnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name = ?
		  AND column_name = ?
	`, table, column).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("failed to check column %s.%s: %w", table, column, err)
	}
	return count > 0, nil
}

// addColumnIfNotExists adds a column to a table if it doesn't already exist
func addColumnIfNotExists(ctx context.Context, db *sql.DB, table, column, colType string) error {
	exists, err := columnExists(ctx, db, table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colType))
	if err != nil {
		// Ignore "duplicate column" errors (race condition with another process)
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "duplicate column") ||
			strings.Contains(errLower, "already exists") {
			return nil
		}
		return fmt.Errorf("failed to add column %s.%s: %w", table, column, err)
	}
	return nil
}

// migrateAdviceHookFields adds advice hook columns to the issues table (hq--uaim).
// These fields enable advice beads to register stop hooks - commands that run
// at specific lifecycle points (session-end, before-commit, before-push, before-handoff).
//
// New columns:
//   - advice_hook_command: the shell command to execute
//   - advice_hook_trigger: when to run (session-end, before-commit, etc.)
//   - advice_hook_timeout: max execution time in seconds (default: 30)
//   - advice_hook_on_failure: what to do if hook fails (block, warn, ignore)
func migrateAdviceHookFields(ctx context.Context, db *sql.DB) error {
	columns := []struct {
		name    string
		sqlType string
	}{
		{"advice_hook_command", "TEXT DEFAULT ''"},
		{"advice_hook_trigger", "VARCHAR(32) DEFAULT ''"},
		{"advice_hook_timeout", "INT DEFAULT 0"},
		{"advice_hook_on_failure", "VARCHAR(16) DEFAULT ''"},
	}

	for _, col := range columns {
		if err := addColumnIfNotExists(ctx, db, "issues", col.name, col.sqlType); err != nil {
			return err
		}
	}

	// Add index for efficient advice hook queries.
	// Note: MySQL/Dolt doesn't support partial indexes like SQLite,
	// so we create a simple index on the trigger column.
	_, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_issues_advice_hook_trigger
		ON issues (advice_hook_trigger)
	`)
	if err != nil {
		// Ignore "index already exists" errors
		errLower := strings.ToLower(err.Error())
		if !strings.Contains(errLower, "duplicate key") &&
			!strings.Contains(errLower, "already exists") {
			return fmt.Errorf("failed to create advice hooks index: %w", err)
		}
	}

	return nil
}

// migrateBlockedIssuesCache creates the blocked_issues_cache table for performance (bd-b2ts).
// This materializes the recursive CTE from the ready_issues view, converting
// expensive recursive joins on every read into a simple NOT EXISTS check.
func migrateBlockedIssuesCache(ctx context.Context, db *sql.DB) error {
	// Check if table already exists
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name = 'blocked_issues_cache'
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check for blocked_issues_cache: %w", err)
	}
	if count > 0 {
		return nil // Already exists
	}

	_, err = db.ExecContext(ctx, `
		CREATE TABLE blocked_issues_cache (
			issue_id VARCHAR(255) PRIMARY KEY,
			CONSTRAINT fk_blocked_cache FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create blocked_issues_cache: %w", err)
	}

	return nil
}

// migrateDropReadyIssuesView drops the ready_issues VIEW which is no longer needed.
// GetReadyWork now uses the blocked_issues_cache table for O(1) lookups instead of
// the expensive recursive CTE that the VIEW executed on every query. (bd-b2ts)
func migrateDropReadyIssuesView(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "DROP VIEW IF EXISTS ready_issues")
	// DROP VIEW IF EXISTS is idempotent - no error if view doesn't exist
	return err
}

// migrateInboxTable creates the inbox table for agent async message delivery (bd-xtahx).
func migrateInboxTable(ctx context.Context, db *sql.DB) error {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name = 'inbox'
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check for inbox table: %w", err)
	}
	if count > 0 {
		return nil
	}

	_, err = db.ExecContext(ctx, `
		CREATE TABLE inbox (
			id VARCHAR(255) PRIMARY KEY,
			agent_name VARCHAR(255) NOT NULL,
			rig VARCHAR(255) DEFAULT '',
			session_id VARCHAR(255) DEFAULT '',
			type VARCHAR(32) NOT NULL,
			source VARCHAR(255) NOT NULL,
			content TEXT NOT NULL,
			priority INT DEFAULT 2,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			delivered_at DATETIME,
			expires_at DATETIME,
			dedup_key VARCHAR(255) NOT NULL,
			INDEX idx_inbox_pending (agent_name, delivered_at),
			UNIQUE INDEX idx_inbox_dedup (dedup_key)
		)
	`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create inbox table: %w", err)
	}

	return nil
}

// migrateAdviceSubscriptionFields adds advice subscription columns to the issues table.
// These fields enable agent-customized advice filtering (gt-w2mh8a.4).
//
// New columns:
//   - advice_subscriptions: comma-separated list of advice tags to subscribe to
//   - advice_subscriptions_exclude: comma-separated list of advice tags to exclude
func migrateAdviceSubscriptionFields(ctx context.Context, db *sql.DB) error {
	columns := []struct {
		name    string
		sqlType string
	}{
		{"advice_subscriptions", "TEXT DEFAULT ''"},
		{"advice_subscriptions_exclude", "TEXT DEFAULT ''"},
	}

	for _, col := range columns {
		if err := addColumnIfNotExists(ctx, db, "issues", col.name, col.sqlType); err != nil {
			return err
		}
	}

	return nil
}

// migrateSessionRegistryTable creates the session_registry table for daemon session
// identity persistence across restarts (bd-2rvp1).
func migrateSessionRegistryTable(ctx context.Context, db *sql.DB) error {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name = 'session_registry'
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check for session_registry table: %w", err)
	}
	if count > 0 {
		return nil
	}

	_, err = db.ExecContext(ctx, `
		CREATE TABLE session_registry (
			session_key VARCHAR(255) PRIMARY KEY,
			assigned_name VARCHAR(255) NOT NULL,
			base_name VARCHAR(255) NOT NULL,
			last_seen DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			INDEX idx_session_registry_name (assigned_name)
		)
	`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create session_registry table: %w", err)
	}

	return nil
}

// migrateInboxDedupUnique upgrades the inbox dedup_key index from INDEX to UNIQUE INDEX (bd-56da5).
// Without UNIQUE, INSERT IGNORE cannot deduplicate — duplicates are silently inserted.
func migrateInboxDedupUnique(ctx context.Context, db *sql.DB) error {
	// Check if the inbox table exists at all.
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name = 'inbox'
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check for inbox table: %w", err)
	}
	if count == 0 {
		return nil // inbox table doesn't exist yet; migrateInboxTable will create it with UNIQUE.
	}

	// Check if the index is already unique.
	var nonUnique int
	err = db.QueryRowContext(ctx, `
		SELECT non_unique FROM information_schema.statistics
		WHERE table_schema = DATABASE()
		  AND table_name = 'inbox'
		  AND index_name = 'idx_inbox_dedup'
	`).Scan(&nonUnique)
	if err != nil {
		// Index doesn't exist at all — nothing to fix.
		return nil
	}
	if nonUnique == 0 {
		return nil // Already unique.
	}

	// Remove duplicate dedup_keys before adding UNIQUE constraint.
	// Keep the earliest entry (smallest created_at) for each dedup_key.
	_, err = db.ExecContext(ctx, `
		DELETE i1 FROM inbox i1
		INNER JOIN inbox i2
		ON i1.dedup_key = i2.dedup_key
		AND i1.created_at > i2.created_at
	`)
	if err != nil {
		return fmt.Errorf("failed to remove duplicate dedup_keys: %w", err)
	}

	// Drop old non-unique index and create unique one.
	_, err = db.ExecContext(ctx, `ALTER TABLE inbox DROP INDEX idx_inbox_dedup`)
	if err != nil {
		return fmt.Errorf("failed to drop old inbox dedup index: %w", err)
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE inbox ADD UNIQUE INDEX idx_inbox_dedup (dedup_key)`)
	if err != nil {
		return fmt.Errorf("failed to add unique inbox dedup index: %w", err)
	}

	return nil
}

// migrateInboxBroadcastAck creates the inbox_broadcast_ack table for per-agent
// delivery tracking of broadcast messages (bd-r7slg).
func migrateInboxBroadcastAck(ctx context.Context, db *sql.DB) error {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name = 'inbox_broadcast_ack'
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check for inbox_broadcast_ack table: %w", err)
	}
	if count > 0 {
		return nil
	}

	_, err = db.ExecContext(ctx, `
		CREATE TABLE inbox_broadcast_ack (
			inbox_id VARCHAR(255) NOT NULL,
			agent_name VARCHAR(255) NOT NULL,
			delivered_at DATETIME NOT NULL,
			PRIMARY KEY (inbox_id, agent_name)
		)
	`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create inbox_broadcast_ack table: %w", err)
	}

	return nil
}

// migrateSessionRegistryProjectRoot adds project_root column to session_registry
// for tracking which project directory a session is working in (bd-djohp).
func migrateSessionRegistryProjectRoot(ctx context.Context, db *sql.DB) error {
	// Check if session_registry table exists.
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name = 'session_registry'
	`).Scan(&count)
	if err != nil || count == 0 {
		return nil // Table doesn't exist yet; schema.go will create it with the column.
	}

	// Check if column already exists.
	var colCount int
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND table_name = 'session_registry'
		  AND column_name = 'project_root'
	`).Scan(&colCount)
	if err != nil || colCount > 0 {
		return nil // Column already exists.
	}

	_, err = db.ExecContext(ctx, `ALTER TABLE session_registry ADD COLUMN project_root VARCHAR(1024) DEFAULT ''`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return nil
		}
		return fmt.Errorf("failed to add project_root column: %w", err)
	}

	return nil
}

// migrateCreatedBySession adds the created_by_session column to the issues table
// for tracking which Claude Code session created each issue (bd-5azzq).
func migrateCreatedBySession(ctx context.Context, db *sql.DB) error {
	return addColumnIfNotExists(ctx, db, "issues", "created_by_session", "VARCHAR(255) DEFAULT ''")
}

// migrateInboxDedupRows removes duplicate rows from the inbox table (bd-5dh7k).
// Duplicate rows with the same id (PK) can appear after Dolt merges or data imports.
// These cause broadcast messages to redeliver endlessly because the LEFT JOIN on
// inbox_broadcast_ack matches one copy but the duplicate remains unacked.
func migrateInboxDedupRows(ctx context.Context, db *sql.DB) error {
	// Check if inbox table exists.
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name = 'inbox'
	`).Scan(&count)
	if err != nil || count == 0 {
		return nil
	}

	// Find and remove duplicate rows by dedup_key (keeps the earliest).
	// We can't easily detect duplicate PKs via SQL (the DB shouldn't allow them),
	// but duplicate dedup_keys with different IDs cause similar redelivery issues.
	_, err = db.ExecContext(ctx, `
		DELETE i1 FROM inbox i1
		INNER JOIN inbox i2
		ON i1.dedup_key = i2.dedup_key
		AND i1.id != i2.id
		AND i1.created_at > i2.created_at
	`)
	if err != nil {
		// Non-fatal: duplicates will be handled by scanInboxItems dedup.
		return nil
	}

	return nil
}

// migrateIssueCommitsTable creates the issue_commits table for commit tracking (bd-xxabm).
func migrateIssueCommitsTable(ctx context.Context, db *sql.DB) error {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_name = 'issue_commits'
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check for issue_commits table: %w", err)
	}
	if count > 0 {
		return nil
	}

	_, err = db.ExecContext(ctx, `
		CREATE TABLE issue_commits (
			issue_id VARCHAR(255) NOT NULL,
			commit_sha VARCHAR(64) NOT NULL,
			repo_url VARCHAR(512) DEFAULT '',
			branch VARCHAR(255) DEFAULT '',
			author VARCHAR(255) DEFAULT '',
			message TEXT DEFAULT '',
			committed_at DATETIME,
			recorded_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			recorded_by VARCHAR(255) DEFAULT '',
			PRIMARY KEY (issue_id, commit_sha),
			INDEX idx_issue_commits_sha (commit_sha),
			INDEX idx_issue_commits_recorded (recorded_at),
			CONSTRAINT fk_issue_commits_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create issue_commits table: %w", err)
	}
	return nil
}

// migrateDecisionYieldedAt adds yielded_at column to decision_points table.
// This tracks when a resolved decision was delivered to an agent via bd yield,
// preventing stale decision events from being replayed on subsequent yields. (bd-03bym)
func migrateDecisionYieldedAt(ctx context.Context, db *sql.DB) error {
	return addColumnIfNotExists(ctx, db, "decision_points", "yielded_at", "DATETIME")
}

// migrateDecisionPendingIndex adds an index on (responded_at, created_at) to
// decision_points for efficient pending decision lookups (bd-oc7ni).
// The decision sweeper queries WHERE responded_at IS NULL ORDER BY created_at,
// which was doing a full table scan without this index.
func migrateDecisionPendingIndex(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_decision_points_pending
		ON decision_points (responded_at, created_at)
	`)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "duplicate key") ||
			strings.Contains(errLower, "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create decision pending index: %w", err)
	}
	return nil
}

// migrateQueryPerfIndexes adds composite indexes to improve GetReadyWork and
// dependency query performance (bd-5mwz6, bd-towmg).
func migrateQueryPerfIndexes(ctx context.Context, db *sql.DB) error {
	indexes := []struct{ name, table, cols string }{
		{"idx_issues_status_type", "issues", "status, issue_type"},
		{"idx_dependencies_issue_type", "dependencies", "issue_id, type"},
	}
	for _, idx := range indexes {
		_, err := db.ExecContext(ctx, fmt.Sprintf(
			"CREATE INDEX IF NOT EXISTS %s ON %s (%s)", idx.name, idx.table, idx.cols,
		))
		if err != nil {
			errLower := strings.ToLower(err.Error())
			if strings.Contains(errLower, "duplicate key") ||
				strings.Contains(errLower, "already exists") {
				continue
			}
			return fmt.Errorf("failed to create index %s: %w", idx.name, err)
		}
	}
	return nil
}

// migrateJackExpiresAt adds jack_expires_at column and index to the issues table (bd-duvjc).
// This column tracks when a jack infrastructure modification expires, enabling the sweeper
// to automatically clean up expired jacks.
func migrateJackExpiresAt(ctx context.Context, db *sql.DB) error {
	if err := addColumnIfNotExists(ctx, db, "issues", "jack_expires_at", "DATETIME"); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_issues_jack_expires_at
		ON issues (jack_expires_at)
	`)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "duplicate key") ||
			strings.Contains(errLower, "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create jack_expires_at index: %w", err)
	}
	return nil
}

// migrateBlockedCacheDepIndex adds a covering index on dependencies(type, depends_on_id)
// to accelerate the blocked_issues_cache recursive CTE which filters by dep type and
// joins on depends_on_id (bd-zg6tc).
func migrateBlockedCacheDepIndex(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_dependencies_type_depends_on ON dependencies (type, depends_on_id)")
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "duplicate key") ||
			strings.Contains(errLower, "already exists") {
			return nil
		}
		return fmt.Errorf("failed to create dep type index: %w", err)
	}
	return nil
}

// migrateTimeColumnIndexes adds indexes on time-based columns used for filtering:
// updated_at (GetStaleIssues), due_at, defer_until (GetReadyWork) (bd-c771m).
func migrateTimeColumnIndexes(ctx context.Context, db *sql.DB) error {
	indexes := []struct{ name, cols string }{
		{"idx_issues_updated_at", "updated_at"},
		{"idx_issues_due_at", "due_at"},
		{"idx_issues_defer_until", "defer_until"},
	}
	for _, idx := range indexes {
		_, err := db.ExecContext(ctx, fmt.Sprintf(
			"CREATE INDEX IF NOT EXISTS %s ON issues (%s)", idx.name, idx.cols,
		))
		if err != nil {
			errLower := strings.ToLower(err.Error())
			if strings.Contains(errLower, "duplicate key") ||
				strings.Contains(errLower, "already exists") {
				continue
			}
			return fmt.Errorf("failed to create index %s: %w", idx.name, err)
		}
	}
	return nil
}

// migrateInboxSubject adds the subject column to the inbox table (bd-pwoii).
func migrateInboxSubject(ctx context.Context, db *sql.DB) error {
	return addColumnIfNotExists(ctx, db, "inbox", "subject", "VARCHAR(512) DEFAULT ''")
}

