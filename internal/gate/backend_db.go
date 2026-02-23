// Package gate — backend_db.go implements GateBackend using a SQL database (Dolt/MySQL).
// This enables session gate operations over HTTP without requiring direct NATS access.
// The DB backend is the primary backend for 100% HTTP operation; NATS KV can layer
// on top as a cache for local daemons. (bd-c50kp)
package gate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DBBackend stores gate state in a SQL database table (session_gates).
// This replaces the NATS KV backend for HTTP-only operation. (bd-c50kp)
type DBBackend struct {
	db *sql.DB
}

// NewDBBackend creates a gate backend backed by the given SQL database.
// The session_gates table must already exist (created by EnsureSessionGatesTable).
func NewDBBackend(db *sql.DB) *DBBackend {
	return &DBBackend{db: db}
}

// EnsureSessionGatesTable creates the session_gates table if it doesn't exist.
// Call this during daemon startup after DB initialization.
func EnsureSessionGatesTable(db *sql.DB) error {
	_, err := db.ExecContext(context.Background(), sessionGatesSchema)
	if err != nil {
		return fmt.Errorf("create session_gates table: %w", err)
	}
	return nil
}

const sessionGatesSchema = `
CREATE TABLE IF NOT EXISTS session_gates (
    agent VARCHAR(255) NOT NULL,
    gate_id VARCHAR(255) NOT NULL,
    mechanism VARCHAR(64) DEFAULT '',
    actor VARCHAR(255) DEFAULT '',
    session_id VARCHAR(255) DEFAULT '',
    marked_at BIGINT NOT NULL,
    ttl_seconds INT DEFAULT 0,
    PRIMARY KEY (agent, gate_id),
    INDEX idx_session_gates_agent (agent)
)`

func (b *DBBackend) Mark(agent, gateID string, opts MarkOpts) error {
	now := time.Now().Unix()
	ttlSecs := 0
	if opts.TTL > 0 {
		ttlSecs = int(opts.TTL.Seconds())
	}

	// Upsert: INSERT ... ON DUPLICATE KEY UPDATE (MySQL/Dolt syntax)
	_, err := b.db.ExecContext(context.Background(), `
		INSERT INTO session_gates (agent, gate_id, mechanism, actor, session_id, marked_at, ttl_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			mechanism = VALUES(mechanism),
			actor = VALUES(actor),
			session_id = VALUES(session_id),
			marked_at = VALUES(marked_at),
			ttl_seconds = VALUES(ttl_seconds)`,
		agent, gateID, opts.Mechanism, opts.Actor, opts.SessionID, now, ttlSecs,
	)
	if err != nil {
		return fmt.Errorf("mark gate %s.%s: %w", agent, gateID, err)
	}
	return nil
}

func (b *DBBackend) Clear(agent, gateID string) error {
	_, err := b.db.ExecContext(context.Background(),
		`DELETE FROM session_gates WHERE agent = ? AND gate_id = ?`,
		agent, gateID,
	)
	if err != nil {
		return fmt.Errorf("clear gate %s.%s: %w", agent, gateID, err)
	}
	return nil
}

func (b *DBBackend) ClearAll(agent string) error {
	_, err := b.db.ExecContext(context.Background(),
		`DELETE FROM session_gates WHERE agent = ?`,
		agent,
	)
	if err != nil {
		return fmt.Errorf("clear all gates for %s: %w", agent, err)
	}
	return nil
}

func (b *DBBackend) ClearGateAllAgents(gateID string) error {
	_, err := b.db.ExecContext(context.Background(),
		`DELETE FROM session_gates WHERE gate_id = ?`,
		gateID,
	)
	if err != nil {
		return fmt.Errorf("clear gate %s for all agents: %w", gateID, err)
	}
	return nil
}

func (b *DBBackend) IsSatisfied(agent, gateID string) bool {
	entry := b.Get(agent, gateID)
	return entry != nil && entry.Satisfied
}

func (b *DBBackend) Get(agent, gateID string) *GateEntry {
	row := b.db.QueryRowContext(context.Background(),
		`SELECT mechanism, actor, session_id, marked_at, ttl_seconds
		 FROM session_gates WHERE agent = ? AND gate_id = ?`,
		agent, gateID,
	)

	var mechanism, actor, sessionID string
	var markedAt int64
	var ttlSecs int
	if err := row.Scan(&mechanism, &actor, &sessionID, &markedAt, &ttlSecs); err != nil {
		return nil
	}

	// Check TTL expiration (application-level, same as NATS backend)
	if ttlSecs > 0 {
		expires := time.Unix(markedAt, 0).Add(time.Duration(ttlSecs) * time.Second)
		if time.Now().After(expires) {
			// Expired — clean up and return nil
			_ = b.Clear(agent, gateID)
			return nil
		}
	}

	return &GateEntry{
		Satisfied: true,
		Mechanism: mechanism,
		Actor:     actor,
		SessionID: sessionID,
		Timestamp: markedAt,
		TTL:       ttlSecs,
	}
}

// ListForAgent returns all satisfied gates for the given agent.
// Unlike the NATS backend which requires checking hardcoded gate IDs,
// this queries the DB directly for all rows matching the agent.
func (b *DBBackend) ListForAgent(agent string) []GateEntry {
	rows, err := b.db.QueryContext(context.Background(),
		`SELECT gate_id, mechanism, actor, session_id, marked_at, ttl_seconds
		 FROM session_gates WHERE agent = ?`,
		agent,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	now := time.Now()
	var expired []string
	var entries []GateEntry

	for rows.Next() {
		var gateID, mechanism, actor, sessionID string
		var markedAt int64
		var ttlSecs int
		if err := rows.Scan(&gateID, &mechanism, &actor, &sessionID, &markedAt, &ttlSecs); err != nil {
			continue
		}

		// Check TTL expiration
		if ttlSecs > 0 {
			expires := time.Unix(markedAt, 0).Add(time.Duration(ttlSecs) * time.Second)
			if now.After(expires) {
				expired = append(expired, gateID)
				continue
			}
		}

		entries = append(entries, GateEntry{
			Satisfied: true,
			Mechanism: mechanism,
			Actor:     actor,
			SessionID: sessionID,
			Timestamp: markedAt,
			TTL:       ttlSecs,
		})
	}

	// Clean up expired entries
	if len(expired) > 0 {
		b.deleteExpired(agent, expired)
	}

	return entries
}

// deleteExpired removes expired gate entries in batch.
func (b *DBBackend) deleteExpired(agent string, gateIDs []string) {
	if len(gateIDs) == 0 {
		return
	}
	placeholders := make([]string, len(gateIDs))
	args := make([]any, 0, len(gateIDs)+1)
	args = append(args, agent)
	for i, id := range gateIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf(
		"DELETE FROM session_gates WHERE agent = ? AND gate_id IN (%s)",
		strings.Join(placeholders, ","),
	)
	_, _ = b.db.ExecContext(context.Background(), query, args...)
}

