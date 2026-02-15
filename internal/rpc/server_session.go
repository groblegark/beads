package rpc

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// sessionEntry tracks a registered session in the daemon's in-memory registry.
type sessionEntry struct {
	SessionKey   string    // Hash identifying this session (PPID:TTY:project-root)
	AssignedName string    // Unique name assigned (e.g., "matthewbaker-2")
	BaseName     string    // Base name before suffix (e.g., "matthewbaker")
	LastSeen     time.Time // Last activity timestamp (for stale cleanup)
}

// sessionRegistry is the daemon's in-memory session→name mapping.
// It assigns unique, human-meaningful names to concurrent sessions.
type sessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string]*sessionEntry // session_key → entry
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{
		sessions: make(map[string]*sessionEntry),
	}
}

// register assigns a unique name for a session key, or returns the existing one.
// Name generation: if baseName is the only session, it keeps baseName as-is.
// If multiple sessions share the same baseName, they get baseName-1, baseName-2, etc.
func (r *sessionRegistry) register(sessionKey, baseName string) (assignedName string, isNew bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if already registered
	if entry, ok := r.sessions[sessionKey]; ok {
		entry.LastSeen = time.Now()
		return entry.AssignedName, false
	}

	// Find the lowest unused suffix for this baseName
	assignedName = r.findUnusedName(baseName)

	r.sessions[sessionKey] = &sessionEntry{
		SessionKey:   sessionKey,
		AssignedName: assignedName,
		BaseName:     baseName,
		LastSeen:     time.Now(),
	}

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
		})
	}

	assignedName, isNew := s.sessionReg.register(args.SessionKey, args.BaseName)

	resp := SessionRegisterResponse{
		AssignedName: assignedName,
		IsNew:        isNew,
		SessionKey:   args.SessionKey,
	}

	data, _ := json.Marshal(resp)
	return Response{
		Success: true,
		Data:    data,
	}
}
