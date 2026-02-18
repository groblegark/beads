package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// InboxPush inserts a new inbox item, ignoring duplicates by dedup_key.
func (s *DoltStore) InboxPush(ctx context.Context, item *types.InboxItem) error {
	var expiresAt interface{}
	if item.ExpiresAt != nil {
		expiresAt = *item.ExpiresAt
	}

	// INSERT IGNORE: if dedup_key already exists, silently skip (idempotent).
	_, err := s.db.ExecContext(ctx, `
		INSERT IGNORE INTO inbox (
			id, agent_name, rig, session_id, type, source, content,
			priority, created_at, expires_at, dedup_key
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.ID, item.AgentName, item.Rig, item.SessionID, item.Type, item.Source,
		item.Content, item.Priority, item.CreatedAt, expiresAt, item.DedupKey)
	if err != nil {
		return fmt.Errorf("inbox push: %w", err)
	}
	return nil
}

// InboxList returns inbox items for an agent, optionally including delivered items.
// Also includes broadcast messages (agent_name='all') so that --to=all pushes
// are visible to every agent. Broadcasts already acked by this agent (via
// inbox_broadcast_ack) are excluded unless includeDelivered is true. (bd-r7slg)
//
// Uses NOT EXISTS instead of LEFT JOIN for broadcast ack filtering because Dolt's
// LEFT JOIN can produce spurious duplicate rows, causing acked broadcasts to
// reappear endlessly. NOT EXISTS is immune to this issue. (bd-5dh7k)
func (s *DoltStore) InboxList(ctx context.Context, agentName string, includeDelivered bool) ([]*types.InboxItem, error) {
	query := `
		SELECT i.id, i.agent_name, i.rig, i.session_id, i.type, i.source, i.content,
		       i.priority, i.created_at, i.delivered_at, i.expires_at, i.dedup_key
		FROM inbox i
		WHERE (i.agent_name = ? OR i.agent_name = 'all')`
	args := []interface{}{agentName}
	if !includeDelivered {
		// Direct messages: delivered_at IS NULL.
		// Broadcasts: no ack row for this agent (NOT EXISTS subquery).
		query += ` AND (
			(i.agent_name != 'all' AND i.delivered_at IS NULL)
			OR (i.agent_name = 'all' AND NOT EXISTS (
				SELECT 1 FROM inbox_broadcast_ack ba
				WHERE ba.inbox_id = i.id AND ba.agent_name = ?
			))
		)`
		args = append(args, agentName)
	}
	query += ` ORDER BY i.priority ASC, i.created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inbox list: %w", err)
	}
	defer rows.Close()

	return scanInboxItems(rows)
}

// InboxDrain returns undelivered, non-expired inbox items for an agent.
// Does NOT mark them as delivered — caller should use InboxMarkDelivered after output.
// Also includes broadcast messages (agent_name='all') so that --to=all pushes
// are delivered to every agent on their next drain. Broadcasts already acked by
// this agent (via inbox_broadcast_ack) are excluded. (bd-r7slg)
// Optional maxPriority filters to only return items with priority <= the given value
// (0=critical only, 1=critical+high, etc.). Omit or pass no value for all priorities.
//
// Uses NOT EXISTS instead of LEFT JOIN for broadcast ack filtering because Dolt's
// LEFT JOIN can produce spurious duplicate rows, causing acked broadcasts to
// reappear endlessly. NOT EXISTS is immune to this issue. (bd-5dh7k)
func (s *DoltStore) InboxDrain(ctx context.Context, agentName string, maxPriority ...int) ([]*types.InboxItem, error) {
	query := `
		SELECT i.id, i.agent_name, i.rig, i.session_id, i.type, i.source, i.content,
		       i.priority, i.created_at, i.delivered_at, i.expires_at, i.dedup_key
		FROM inbox i
		WHERE (i.agent_name = ? OR i.agent_name = 'all')
		  AND (
		    (i.agent_name != 'all' AND i.delivered_at IS NULL)
		    OR (i.agent_name = 'all' AND NOT EXISTS (
		        SELECT 1 FROM inbox_broadcast_ack ba
		        WHERE ba.inbox_id = i.id AND ba.agent_name = ?
		    ))
		  )
		  AND (i.expires_at IS NULL OR i.expires_at > NOW())`
	args := []interface{}{agentName, agentName}

	if len(maxPriority) > 0 && maxPriority[0] > 0 {
		query += `
		  AND i.priority <= ?`
		args = append(args, maxPriority[0])
	}

	query += `
		ORDER BY i.priority ASC, i.created_at ASC
		LIMIT 20`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("inbox drain: %w", err)
	}
	defer rows.Close()

	return scanInboxItems(rows)
}

// InboxMarkDelivered marks the given inbox items as delivered for the specified agent.
// For broadcast messages (agent_name='all'), per-agent ack rows are inserted into
// inbox_broadcast_ack instead of updating the shared inbox row. This prevents
// one agent's drain from hiding broadcasts from other agents. (bd-r7slg)
func (s *DoltStore) InboxMarkDelivered(ctx context.Context, agentName string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	// Deduplicate IDs to avoid redundant work when drain returns
	// duplicate rows from corrupted inbox data. (bd-5dh7k)
	seen := make(map[string]bool, len(ids))
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	ids = unique

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("inbox mark delivered: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range ids {
		// Check if this is a broadcast message.
		var itemAgentName string
		err := tx.QueryRowContext(ctx, `SELECT agent_name FROM inbox WHERE id = ?`, id).Scan(&itemAgentName)
		if err != nil {
			// Item may have been deleted; skip silently.
			continue
		}

		if itemAgentName == "all" {
			// Broadcast: insert per-agent ack instead of marking the shared row.
			_, err = tx.ExecContext(ctx, `
				INSERT IGNORE INTO inbox_broadcast_ack (inbox_id, agent_name, delivered_at)
				VALUES (?, ?, ?)
			`, id, agentName, now)
		} else {
			// Direct message: mark delivered as before.
			_, err = tx.ExecContext(ctx, `UPDATE inbox SET delivered_at = ? WHERE id = ?`, now, id)
		}
		if err != nil {
			return fmt.Errorf("inbox mark delivered %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// scanInboxItems scans rows into InboxItem structs.
// Deduplicates by ID to handle edge cases where the inbox table contains
// duplicate rows (e.g., from data import or Dolt merge). (bd-5dh7k)
func scanInboxItems(rows *sql.Rows) ([]*types.InboxItem, error) {
	var items []*types.InboxItem
	seen := make(map[string]bool)
	for rows.Next() {
		item := &types.InboxItem{}
		var deliveredAt, expiresAt sql.NullTime
		var rig, sessionID sql.NullString

		err := rows.Scan(
			&item.ID, &item.AgentName, &rig, &sessionID,
			&item.Type, &item.Source, &item.Content,
			&item.Priority, &item.CreatedAt, &deliveredAt, &expiresAt, &item.DedupKey,
		)
		if err != nil {
			return nil, fmt.Errorf("scan inbox item: %w", err)
		}

		// Skip duplicate IDs — the inbox table may contain duplicates from
		// data imports or Dolt merges. Without dedup, the same broadcast
		// redelivers endlessly because the ack only matches one copy. (bd-5dh7k)
		if seen[item.ID] {
			continue
		}
		seen[item.ID] = true

		if rig.Valid {
			item.Rig = rig.String
		}
		if sessionID.Valid {
			item.SessionID = sessionID.String
		}
		if deliveredAt.Valid {
			item.DeliveredAt = &deliveredAt.Time
		}
		if expiresAt.Valid {
			item.ExpiresAt = &expiresAt.Time
		}

		items = append(items, item)
	}
	return items, rows.Err()
}
