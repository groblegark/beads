package rpc

import (
	"os"
	"time"

	"github.com/steveyegge/beads/internal/debug"
)

// startSessionPruner runs a background goroutine that periodically removes
// stale session entries from the registry. Without this, long-running daemons
// accumulate hundreds of expired sessions (e.g., mayor-770+) because pruning
// only happened on daemon startup.
//
// Sessions not seen within the TTL (default 24h) are removed from both the
// in-memory registry and the session_registry database table.
//
// The pruner runs every 30 minutes and stops when shutdownChan is closed.
// (bd-ycqks)
func (s *Server) startSessionPruner() {
	interval := 30 * time.Minute
	if env := os.Getenv("BEADS_SESSION_PRUNE_INTERVAL"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			interval = d
		}
	}

	ttl := 24 * time.Hour
	if env := os.Getenv("BEADS_SESSION_TTL"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			ttl = d
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
				if s.sessionReg != nil {
					pruned := s.sessionReg.pruneStale(ttl)
					if pruned > 0 {
						debug.Logf("session pruner: removed %d stale sessions (ttl=%s)", pruned, ttl)
					}
				}
			}
		}
	}()
}
