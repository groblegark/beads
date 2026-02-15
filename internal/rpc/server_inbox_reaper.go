package rpc

import (
	"context"
	"fmt"
	"os"
	"time"
)

// startInboxReaper runs a background goroutine that periodically cleans up
// old inbox items. Items are eligible for removal when they are both:
//   - delivered (delivered_at IS NOT NULL)
//   - older than 24 hours
//
// Expired but undelivered items are also cleaned up (they can't be delivered).
//
// The reaper runs every hour and stops when shutdownChan is closed.
// (bd-xtahx.6: Phase 5 inbox cleanup)
func (s *Server) startInboxReaper() {
	interval := 1 * time.Hour
	if env := os.Getenv("BEADS_INBOX_REAP_INTERVAL"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			interval = d
		}
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-s.shutdownChan:
				return
			case <-ticker.C:
				s.reapOldInboxItems()
			}
		}
	}()
}

// reapOldInboxItems deletes delivered inbox items older than 24h
// and expired undelivered items.
func (s *Server) reapOldInboxItems() {
	store := s.storage
	if store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := store.UnderlyingDB()
	if db == nil {
		return
	}

	cutoff := time.Now().UTC().Add(-24 * time.Hour)

	// Delete delivered items older than 24h
	result, err := db.ExecContext(ctx,
		`DELETE FROM inbox WHERE delivered_at IS NOT NULL AND delivered_at < ?`,
		cutoff,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inbox-reaper: failed to delete delivered items: %v\n", err)
		return
	}
	deliveredCount, _ := result.RowsAffected()

	// Delete expired undelivered items
	result, err = db.ExecContext(ctx,
		`DELETE FROM inbox WHERE delivered_at IS NULL AND expires_at IS NOT NULL AND expires_at < ?`,
		time.Now().UTC(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inbox-reaper: failed to delete expired items: %v\n", err)
		return
	}
	expiredCount, _ := result.RowsAffected()

	if deliveredCount > 0 || expiredCount > 0 {
		fmt.Fprintf(os.Stderr, "inbox-reaper: cleaned %d delivered + %d expired items\n",
			deliveredCount, expiredCount)
	}
}
