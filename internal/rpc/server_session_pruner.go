package rpc

import (
	"os"
	"time"

	"github.com/steveyegge/beads/internal/debug"
)

// sessionTTL returns the configured session TTL, checking BEADS_SESSION_TTL
// env var first, falling back to the default (2h). Used by both the pruner
// and the loadFromDB startup path to stay consistent. (bd-n00hy)
func sessionTTL() time.Duration {
	if env := os.Getenv("BEADS_SESSION_TTL"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			return d
		}
	}
	return 2 * time.Hour
}

// startSessionPruner runs a background goroutine that periodically removes
// stale session entries from the registry. Without this, long-running daemons
// accumulate hundreds of expired sessions (e.g., mayor-770+) because pruning
// only happened on daemon startup.
//
// Sessions not seen within the TTL (default 2h) are removed from both the
// in-memory registry and the session_registry database table.
//
// The pruner runs every 5 minutes and stops when shutdownChan is closed.
// (bd-ycqks, bd-n00hy)
func (s *Server) startSessionPruner() {
	interval := 5 * time.Minute
	if env := os.Getenv("BEADS_SESSION_PRUNE_INTERVAL"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			interval = d
		}
	}

	ttl := sessionTTL()

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
