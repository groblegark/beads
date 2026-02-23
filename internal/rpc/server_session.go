package rpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/steveyegge/beads/internal/debug"
)

// sessionEntry tracks a registered session in the daemon's in-memory registry.
type sessionEntry struct {
	SessionKey   string    // Hash identifying this session (PPID:TTY:project-root)
	AssignedName string    // Unique name assigned (e.g., "matthewbaker-2")
	BaseName     string    // Base name before suffix (e.g., "matthewbaker")
	LastSeen     time.Time // Last activity timestamp (for stale cleanup)
	ProjectRoot  string    // Project directory this session is working in (bd-djohp)
}

// sessionRegistry is the daemon's session→name mapping.
// Names are kept in memory for fast lookups and persisted to the session_registry
// table so they survive daemon restarts (bd-2rvp1).
type sessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string]*sessionEntry // session_key → entry
	db       *sql.DB                  // database for persistence (nil = in-memory only)
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{
		sessions: make(map[string]*sessionEntry),
	}
}

// loadFromDB populates the in-memory registry from the session_registry table.
// Called once on daemon startup to restore names from a previous run.
// Entries older than maxAge are pruned during load.
func (r *sessionRegistry) loadFromDB(ctx context.Context, db *sql.DB, maxAge time.Duration) error {
	if db == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.db = db

	// Prune stale entries from the database first
	cutoff := time.Now().Add(-maxAge)
	_, _ = db.ExecContext(ctx, `DELETE FROM session_registry WHERE last_seen < ?`, cutoff)

	rows, err := db.QueryContext(ctx, `
		SELECT session_key, assigned_name, base_name, last_seen, project_root
		FROM session_registry
	`)
	if err != nil {
		// Table might not exist yet (pre-migration). Not fatal.
		debug.Logf("session registry: load from DB failed (table may not exist): %v", err)
		return nil
	}
	defer rows.Close()

	loaded := 0
	for rows.Next() {
		var entry sessionEntry
		if err := rows.Scan(&entry.SessionKey, &entry.AssignedName, &entry.BaseName, &entry.LastSeen, &entry.ProjectRoot); err != nil {
			debug.Logf("session registry: scan error: %v", err)
			continue
		}
		r.sessions[entry.SessionKey] = &entry
		loaded++
	}
	if err := rows.Err(); err != nil {
		debug.Logf("session registry: rows error: %v", err)
	}

	if loaded > 0 {
		debug.Logf("session registry: restored %d sessions from database", loaded)
	}
	return nil
}

// persistEntry writes a single session entry to the database.
// Uses INSERT ... ON DUPLICATE KEY UPDATE for upsert semantics.
func (r *sessionRegistry) persistEntry(entry *sessionEntry) {
	if r.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO session_registry (session_key, assigned_name, base_name, last_seen, project_root)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE assigned_name = VALUES(assigned_name),
		                        base_name = VALUES(base_name),
		                        last_seen = VALUES(last_seen),
		                        project_root = VALUES(project_root)
	`, entry.SessionKey, entry.AssignedName, entry.BaseName, entry.LastSeen, entry.ProjectRoot)
	if err != nil {
		debug.Logf("session registry: persist failed for %s: %v", entry.SessionKey, err)
	}
}

// register assigns a unique name for a session key, or returns the existing one.
// Name generation: if baseName is the only session, it keeps baseName as-is.
// If multiple sessions share the same baseName, they get baseName-1, baseName-2, etc.
// New and updated entries are persisted to the database.
func (r *sessionRegistry) register(sessionKey, baseName, projectRoot string) (assignedName string, isNew bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if already registered
	if entry, ok := r.sessions[sessionKey]; ok {
		entry.LastSeen = time.Now()
		if projectRoot != "" {
			entry.ProjectRoot = projectRoot
		}
		// Persist updated last_seen (fire-and-forget in background)
		go r.persistEntry(entry)
		return entry.AssignedName, false
	}

	// Find the lowest unused suffix for this baseName
	assignedName = r.findUnusedName(baseName)

	entry := &sessionEntry{
		SessionKey:   sessionKey,
		AssignedName: assignedName,
		BaseName:     baseName,
		LastSeen:     time.Now(),
		ProjectRoot:  projectRoot,
	}
	r.sessions[sessionKey] = entry

	// Persist new entry (fire-and-forget in background)
	go r.persistEntry(entry)

	return assignedName, true
}

// findUnusedName returns the lowest-numbered name not already in use.
// If baseName itself is unused, returns baseName (no suffix for single sessions).
// Otherwise returns baseName-1, baseName-2, etc.
func (r *sessionRegistry) findUnusedName(baseName string) string {
	// Collect all names in use for this baseName
	used := map[string]bool{}
	for _, entry := range r.sessions {
		if entry.BaseName == baseName {
			used[entry.AssignedName] = true
		}
	}

	// If no sessions with this baseName exist, use the bare name
	if len(used) == 0 {
		return baseName
	}

	// If the bare name is taken (another session), find numbered slot.
	// Start at 1 and find first unused.
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-%d", baseName, n)
		if !used[candidate] {
			return candidate
		}
	}
}

// pruneStale removes sessions not seen within the given duration.
// Also deletes stale entries from the database.
func (r *sessionRegistry) pruneStale(maxAge time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	pruned := 0
	for key, entry := range r.sessions {
		if entry.LastSeen.Before(cutoff) {
			delete(r.sessions, key)
			pruned++
		}
	}

	// Also prune from database
	if r.db != nil && pruned > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = r.db.ExecContext(ctx, `DELETE FROM session_registry WHERE last_seen < ?`, cutoff)
	}

	return pruned
}

// list returns all registered sessions (for bd whoami / debugging).
func (r *sessionRegistry) list() []*sessionEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]*sessionEntry, 0, len(r.sessions))
	for _, entry := range r.sessions {
		entries = append(entries, entry)
	}
	return entries
}

// handleSessionRegister processes the session_register RPC.
func (s *Server) handleSessionRegister(req *Request) Response {
	var args SessionRegisterArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid session register args: %v", err),
		}
	}

	if args.SessionKey == "" {
		return Response{
			Success: false,
			Error:   "session_key is required",
		}
	}
	if args.BaseName == "" {
		return Response{
			Success: false,
			Error:   "base_name is required",
		}
	}

	// Initialize registry on first use (lazy init, thread-safe via Server fields)
	if s.sessionReg == nil {
		s.sessionRegOnce.Do(func() {
			s.sessionReg = newSessionRegistry()
			// Load persisted sessions from database, pruning entries older than
			// the configured session TTL (default 2h). (bd-n00hy)
			if db := s.storage.UnderlyingDB(); db != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = s.sessionReg.loadFromDB(ctx, db, sessionTTL())
			}
		})
	}

	assignedName, isNew := s.sessionReg.register(args.SessionKey, args.BaseName, args.ProjectRoot)

	resp := SessionRegisterResponse{
		AssignedName: assignedName,
		IsNew:        isNew,
		SessionKey:   args.SessionKey,
	}

	return jsonOK(resp)
}

// handleSessionList returns all registered sessions (bd-tp3r6).
func (s *Server) handleSessionList(req *Request) Response {
	var args SessionListArgs
	if req.Args != nil {
		_ = json.Unmarshal(req.Args, &args)
	}

	if s.sessionReg == nil {
		// No sessions registered yet
		return jsonOK(SessionListResponse{
			Sessions: []SessionListEntry{},
			Count:    0,
		})
	}

	entries := s.sessionReg.list()

	// Filter stale entries unless explicitly requested (uses same TTL as pruner)
	cutoff := time.Now().Add(-sessionTTL())
	sessions := make([]SessionListEntry, 0, len(entries))
	for _, e := range entries {
		if !args.IncludeStale && e.LastSeen.Before(cutoff) {
			continue
		}
		sessions = append(sessions, SessionListEntry{
			AssignedName: e.AssignedName,
			BaseName:     e.BaseName,
			SessionKey:   e.SessionKey,
			LastSeen:     e.LastSeen,
			ProjectRoot:  e.ProjectRoot,
		})
	}

	resp := SessionListResponse{
		Sessions: sessions,
		Count:    len(sessions),
	}

	return jsonOK(resp)
}
