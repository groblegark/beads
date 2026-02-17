package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// defaultQueryTimeout is the maximum time a single query can run before
	// the watchdog fires KILL QUERY. This protects against Dolt queries that
	// can hang for 25-47 minutes when the query planner encounters pathological
	// cases (large IN clauses, complex JOINs, etc.).
	//
	// go-sql-driver/mysql does NOT send KILL QUERY on context cancel (GitHub
	// issue #863) - it only closes the client-side connection. The server-side
	// query continues running and consuming resources. This watchdog is the
	// ONLY way to actually stop a stuck query on Dolt.
	defaultQueryTimeout = 30 * time.Second

	// killQueryTimeout is how long we wait for the KILL QUERY command itself.
	killQueryTimeout = 5 * time.Second
)

// rowScanner is the interface needed by scanIssueRow and similar per-row
// scan functions. Both *sql.Rows and *Rows satisfy this via their Scan method.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// rowIterator is the interface needed by functions that iterate over result
// sets (e.g. scanDependencyRows). Both *sql.Rows and *Rows satisfy this.
type rowIterator interface {
	rowScanner
	Next() bool
	Err() error
}

// Rows wraps *sql.Rows with optional KILL QUERY watchdog cleanup.
// In server mode, closing Rows also cancels the watchdog goroutine and
// returns the dedicated connection to the pool. In embedded mode, Rows
// is a thin wrapper with no extra behavior.
type Rows struct {
	*sql.Rows
	conn   *sql.Conn         // Non-nil in watchdog mode (server mode)
	cancel context.CancelFunc // Non-nil in watchdog mode
	done   chan struct{}       // Non-nil in watchdog mode
}

// Close releases the underlying rows, stops the watchdog, and returns
// the connection to the pool.
func (r *Rows) Close() error {
	err := r.Rows.Close()
	if r.done != nil {
		close(r.done)
	}
	if r.cancel != nil {
		r.cancel()
	}
	if r.conn != nil {
		r.conn.Close()
	}
	return err
}

// queryContext executes a query with KILL QUERY watchdog in server mode.
//
// In server mode:
//  1. Acquires a dedicated connection from the pool
//  2. Gets the MySQL CONNECTION_ID() for that connection
//  3. Runs the query with a timeout context
//  4. Spawns a watchdog goroutine that fires KILL QUERY if the timeout expires
//  5. Returns *Rows whose Close() method cleans up the watchdog
//
// Standby fallback (bd-4az2z): if the primary returns a connection error
// and a standby pool is configured, the query is transparently retried on
// the standby replica. This allows read-only operations (bd list, bd show)
// to continue during primary failover.
//
// In embedded mode, delegates directly to db.QueryContext (KILL QUERY is not
// possible with a single-connection embedded engine).
func (s *DoltStore) queryContext(ctx context.Context, query string, args ...interface{}) (*Rows, error) {
	rows, err := s.queryContextOnDB(ctx, s.db, query, args...)
	if err != nil && s.dbStandby != nil && isConnectionError(err) {
		// Primary unreachable — try standby for this read query.
		// Log at stderr so daemon logs capture the failover event.
		fmt.Fprintf(os.Stderr, "standby: primary connection failed, falling back to standby for read query\n")
		standbyRows, standbyErr := s.queryContextOnDB(ctx, s.dbStandby, query, args...)
		if standbyErr == nil {
			return standbyRows, nil
		}
		// Both failed — return the original primary error for clarity
		fmt.Fprintf(os.Stderr, "standby: standby also failed: %v\n", standbyErr)
	}
	return rows, err
}

// queryContextOnDB executes a query against a specific *sql.DB pool with
// KILL QUERY watchdog support.
func (s *DoltStore) queryContextOnDB(ctx context.Context, db *sql.DB, query string, args ...interface{}) (*Rows, error) {
	if !s.serverMode || s.queryTimeout <= 0 {
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		return &Rows{Rows: rows}, nil
	}

	// Acquire a dedicated connection for this query
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}

	// Get the MySQL connection ID so we can KILL QUERY if needed
	var connID int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connID); err != nil {
		conn.Close()
		return nil, fmt.Errorf("get connection ID for watchdog: %w", err)
	}

	// Create a timeout context for the query
	queryCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)

	// Start the KILL QUERY watchdog goroutine
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
			// Query completed or rows closed - nothing to do
		case <-queryCtx.Done():
			// Context expired (timeout or parent cancel) - kill the server-side query.
			// Only fire KILL if this was a deadline exceeded (not a normal cancel from Close).
			if queryCtx.Err() == context.DeadlineExceeded {
				killCtx, killCancel := context.WithTimeout(context.Background(), killQueryTimeout)
				defer killCancel()
				if _, killErr := db.ExecContext(killCtx, fmt.Sprintf("KILL QUERY %d", connID)); killErr != nil {
					fmt.Fprintf(os.Stderr, "watchdog: KILL QUERY %d failed: %v\n", connID, killErr)
				} else {
					fmt.Fprintf(os.Stderr, "watchdog: KILL QUERY %d fired (query timeout %v exceeded)\n", connID, s.queryTimeout)
				}
			}
		}
	}()

	rows, err := conn.QueryContext(queryCtx, query, args...)
	if err != nil {
		close(done)
		cancel()
		conn.Close()
		return nil, err
	}

	return &Rows{Rows: rows, conn: conn, cancel: cancel, done: done}, nil
}

// execContext executes a statement with KILL QUERY watchdog in server mode.
//
// Unlike queryContext, exec completes synchronously so cleanup happens
// before returning (no wrapper type needed).
//
// Note: execContext does NOT fall back to standby — writes must always go
// to the primary. If the primary is down, the error is returned as-is.
//
// Read-only auto-reconnect: if the primary returns "database is read only"
// (indicating a Dolt cluster failover moved primary to a different pod),
// the connection pool is reconnected to pick up the new primary endpoint
// from the K8s service, and the operation is retried once.
func (s *DoltStore) execContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	result, err := s.execContextOnDB(ctx, s.db, query, args...)
	if err != nil && s.serverMode && s.connStr != "" && isReadOnlyError(err) {
		fmt.Fprintf(os.Stderr, "primary: write failed (read-only) — reconnecting to pick up new primary\n")
		if reconnErr := s.reconnectPrimary(ctx); reconnErr != nil {
			fmt.Fprintf(os.Stderr, "primary: reconnect failed: %v\n", reconnErr)
			return result, err // Return original error
		}
		fmt.Fprintf(os.Stderr, "primary: reconnected successfully, retrying write\n")
		return s.execContextOnDB(ctx, s.db, query, args...)
	}
	return result, err
}

// execContextOnDB executes a statement against a specific *sql.DB pool with
// KILL QUERY watchdog support.
func (s *DoltStore) execContextOnDB(ctx context.Context, db *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	if !s.serverMode || s.queryTimeout <= 0 {
		return db.ExecContext(ctx, query, args...)
	}

	// Acquire a dedicated connection
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// Get CONNECTION_ID
	var connID int64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connID); err != nil {
		return nil, fmt.Errorf("get connection ID for watchdog: %w", err)
	}

	// Timeout context
	queryCtx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	// Start watchdog
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-queryCtx.Done():
			if queryCtx.Err() == context.DeadlineExceeded {
				killCtx, killCancel := context.WithTimeout(context.Background(), killQueryTimeout)
				defer killCancel()
				if _, killErr := db.ExecContext(killCtx, fmt.Sprintf("KILL QUERY %d", connID)); killErr != nil {
					fmt.Fprintf(os.Stderr, "watchdog: KILL QUERY %d failed: %v\n", connID, killErr)
				} else {
					fmt.Fprintf(os.Stderr, "watchdog: KILL QUERY %d fired (exec timeout %v exceeded)\n", connID, s.queryTimeout)
				}
			}
		}
	}()

	result, err := conn.ExecContext(queryCtx, query, args...)
	close(done)
	return result, err
}

// reconnectPrimary closes the existing primary connection pool and opens
// a new one using the stored connection string. This is used when the Dolt
// cluster primary has failed over to a different pod — the K8s ClusterIP
// service now points to the new primary, but existing connections in the
// pool still go to the old (now read-only) pod.
func (s *DoltStore) reconnectPrimary(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.connStr == "" {
		return fmt.Errorf("no connection string stored for reconnect")
	}

	// Close existing pool (drains all connections)
	oldDB := s.db
	if oldDB != nil {
		_ = oldDB.Close()
	}

	// Open new pool with the same DSN — K8s service will resolve to new primary
	newDB, err := sql.Open("mysql", s.connStr)
	if err != nil {
		return fmt.Errorf("reconnect: failed to open new connection: %w", err)
	}

	// Apply same pool settings as openServerConnection
	newDB.SetMaxOpenConns(1000)
	newDB.SetMaxIdleConns(100)
	newDB.SetConnMaxLifetime(5 * time.Minute)
	newDB.SetConnMaxIdleTime(20 * time.Minute)

	// Verify new connection works
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := newDB.PingContext(pingCtx); err != nil {
		_ = newDB.Close()
		return fmt.Errorf("reconnect: ping failed: %w", err)
	}

	s.db = newDB
	return nil
}

// isConnectionError returns true if the error indicates a connection-level
// failure (TCP connect refused, reset, timeout) rather than a query-level
// error (syntax, missing table, etc.). Only connection errors trigger
// standby read fallback.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "connect: connection") ||
		strings.Contains(msg, "bad connection") ||
		strings.Contains(msg, "invalid connection") ||
		strings.Contains(msg, "driver: bad connection")
}

// isReadOnlyError returns true if the error indicates the database is read-only.
// This happens when a Dolt primary failover moves the primary role to a different
// pod, and the daemon is still connected to the old primary (now a standby).
func isReadOnlyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is read only") ||
		strings.Contains(msg, "read only") && strings.Contains(msg, "error 1105")
}

// parseQueryTimeout reads the query timeout from the environment, falling back
// to the provided default. Returns 0 to disable the watchdog.
func parseQueryTimeout(defaultTimeout time.Duration) time.Duration {
	if env := os.Getenv("BEADS_QUERY_TIMEOUT"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			return d
		}
	}
	return defaultTimeout
}
