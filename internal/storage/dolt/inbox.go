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
func (s *DoltStore) InboxList(ctx context.Context, agentName string, includeDelivered bool) ([]*types.InboxItem, error) {
	query := `
		SELECT id, agent_name, rig, session_id, type, source, content,
		       priority, created_at, delivered_at, expires_at, dedup_key
		FROM inbox
		WHERE agent_name = ?`
	if !includeDelivered {
		query += ` AND delivered_at IS NULL`
	}
	query += ` ORDER BY priority ASC, created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, agentName)
	if err != nil {
		return nil, fmt.Errorf("inbox list: %w", err)
	}
	defer rows.Close()

	return scanInboxItems(rows)
}

// InboxDrain returns undelivered, non-expired inbox items for an agent.
// Does NOT mark them as delivered — caller should use InboxMarkDelivered after output.
func (s *DoltStore) InboxDrain(ctx context.Context, agentName string) ([]*types.InboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, agent_name, rig, session_id, type, source, content,
		       priority, created_at, delivered_at, expires_at, dedup_key
		FROM inbox
		WHERE agent_name = ?
		  AND delivered_at IS NULL
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY priority ASC, created_at ASC
		LIMIT 20
	`, agentName)
	if err != nil {
		return nil, fmt.Errorf("inbox drain: %w", err)
	}
	defer rows.Close()

	return scanInboxItems(rows)
}

// InboxMarkDelivered marks the given inbox items as delivered.
func (s *DoltStore) InboxMarkDelivered(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("inbox mark delivered: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, id := range ids {
		_, err := tx.ExecContext(ctx, `UPDATE inbox SET delivered_at = ? WHERE id = ?`, now, id)
		if err != nil {
			return fmt.Errorf("inbox mark delivered %s: %w", id, err)
		}
	}

	return tx.Commit()
}

// scanInboxItems scans rows into InboxItem structs.
func scanInboxItems(rows *sql.Rows) ([]*types.InboxItem, error) {
	var items []*types.InboxItem
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
