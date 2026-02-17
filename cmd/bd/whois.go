package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
)

// whoisCmd looks up an agent by name across sessions, issues, and agent beads.
var whoisCmd = &cobra.Command{
	Use:   "whois <name-or-id>",
	Short: "Look up an agent by name, bead ID, mail address, or session key",
	Long: `Look up an agent identity across all naming systems.

Forward lookup (name → identity):
  1. Active daemon sessions (adjective-animal names like "fast-ox")
  2. Issues assigned to or created by this name
  3. Agent beads with matching pod/role information
  4. Derives gt mail address from agent bead identity

Reverse lookup (identifier → name):
  5. Bead ID (e.g., "bd-tds62", "gt-gastown-polecat-nux") → owner/assignee
  6. Session key hash → assigned name and project
  7. GT mail address (e.g., "gastown/polecats/nux") → agent bead + session

Bridges the naming systems:
  - Beads sessions: adjective-animal names (fast-ox, keen-newt)
  - K8s agents: polecat/crew names (nux, furiosa)
  - GT mail: rig/role/name addresses (gastown/polecats/nux)

Examples:
  bd whois fast-ox                    # Look up a session by adjective-animal name
  bd whois nux                        # Find K8s agent + gt mail address
  bd whois mayor                      # Town-level agent
  bd whois bd-tds62                   # Reverse: who owns this bead?
  bd whois gt-gastown-polecat-nux     # Reverse: agent bead → session + mail
  bd whois gastown/polecats/nux       # Reverse: mail address → agent bead
  bd whois keen-newt --json           # JSON output with all fields`,
	Args: cobra.ExactArgs(1),
	Run:  runWhois,
}

func init() {
	rootCmd.AddCommand(whoisCmd)
}

type whoisResult struct {
	Name     string          `json:"name"`
	Sessions []whoisSession  `json:"sessions,omitempty"`
	Issues   whoisIssues     `json:"issues,omitempty"`
	Agents   []whoisAgent    `json:"agents,omitempty"`
	Reverse  *whoisReverse   `json:"reverse,omitempty"`
}

// whoisReverse holds reverse-lookup results (bead ID → owner, mail → agent, etc.)
type whoisReverse struct {
	InputType   string `json:"input_type"`              // "bead_id", "mail_address", "session_key"
	BeadID      string `json:"bead_id,omitempty"`       // The resolved bead ID
	Title       string `json:"title,omitempty"`         // Bead title
	Status      string `json:"status,omitempty"`        // Bead status
	Assignee    string `json:"assignee,omitempty"`      // Who it's assigned to
	CreatedBy   string `json:"created_by,omitempty"`    // Who created it
	MailAddress string `json:"mail_address,omitempty"`  // Derived mail address
}

type whoisSession struct {
	AssignedName string    `json:"assigned_name"`
	BaseName     string    `json:"base_name"`
	SessionKey   string    `json:"session_key"`
	LastSeen     time.Time `json:"last_seen"`
	Active       bool      `json:"active"`
	ProjectRoot  string    `json:"project_root,omitempty"`
}

type whoisIssues struct {
	Assigned  int      `json:"assigned"`
	Created   int      `json:"created"`
	InProgress []string `json:"in_progress,omitempty"`
}

type whoisAgent struct {
	AgentID     string `json:"agent_id"`
	PodName     string `json:"pod_name,omitempty"`
	PodStatus   string `json:"pod_status,omitempty"`
	AgentState  string `json:"agent_state,omitempty"`
	Rig         string `json:"rig,omitempty"`
	RoleType    string `json:"role_type,omitempty"`
	MailAddress string `json:"mail_address,omitempty"`
}

func runWhois(cmd *cobra.Command, args []string) {
	name := args[0]
	result := whoisResult{Name: name}

	if daemonClient == nil {
		fmt.Println("Error: daemon not connected (bd whois requires a running daemon)")
		return
	}

	// 0. Reverse lookup detection — check if input is a bead ID, mail address, or session key
	if strings.Contains(name, "/") {
		// Mail address format (e.g., "gastown/polecats/nux")
		reverseFromMailAddress(&result, name)
	} else if looksLikeBeadID(name) {
		// Bead ID format (e.g., "bd-tds62", "gt-gastown-polecat-nux", "hq-mayor")
		reverseFromBeadID(&result, name)
	} else if looksLikeSessionKey(name) {
		// Session key hash (16 hex chars)
		reverseFromSessionKey(&result, name)
	}

	// 1. Search active sessions
	sessResp, err := daemonClient.SessionList(&rpc.SessionListArgs{IncludeStale: true})
	if err == nil {
		cutoff := time.Now().Add(-1 * time.Hour)
		for _, s := range sessResp.Sessions {
			if matchesName(s.AssignedName, name) || matchesName(s.BaseName, name) {
				result.Sessions = append(result.Sessions, whoisSession{
					AssignedName: s.AssignedName,
					BaseName:     s.BaseName,
					SessionKey:   s.SessionKey,
					LastSeen:     s.LastSeen,
					Active:       s.LastSeen.After(cutoff),
					ProjectRoot:  s.ProjectRoot,
				})
			}
		}
	}

	// 2. Search issues by assignee
	assignedResp, err := daemonClient.List(&rpc.ListArgs{Assignee: name, Limit: 100})
	if err == nil {
		var issues []map[string]interface{}
		if assignedResp.Data != nil {
			_ = json.Unmarshal(assignedResp.Data, &issues)
		}
		result.Issues.Assigned = len(issues)
		for _, iss := range issues {
			if status, ok := iss["status"].(string); ok && status == "in_progress" {
				if id, ok := iss["id"].(string); ok {
					title := ""
					if t, ok := iss["title"].(string); ok {
						title = t
					}
					result.Issues.InProgress = append(result.Issues.InProgress, fmt.Sprintf("%s: %s", id, title))
				}
			}
		}
	}

	// 3. Search issues by created_by (use query search as fallback)
	createdResp, err := daemonClient.List(&rpc.ListArgs{Query: name, Limit: 100})
	if err == nil {
		var issues []map[string]interface{}
		if createdResp.Data != nil {
			_ = json.Unmarshal(createdResp.Data, &issues)
		}
		createdCount := 0
		for _, iss := range issues {
			if cb, ok := iss["created_by"].(string); ok && matchesName(cb, name) {
				createdCount++
			}
		}
		result.Issues.Created = createdCount
	}

	// 4. Search agent beads (active pods first, then all gt:agent beads for stopped agents)
	seenAgentIDs := map[string]bool{}
	podResp, err := daemonClient.AgentPodList(&rpc.AgentPodListArgs{})
	if err == nil {
		for _, a := range podResp.Agents {
			// Match agent ID suffix, pod name, or role
			if matchesAgent(a, name) {
				seenAgentIDs[a.AgentID] = true
				result.Agents = append(result.Agents, whoisAgent{
					AgentID:     a.AgentID,
					PodName:     a.PodName,
					PodStatus:   a.PodStatus,
					AgentState:  a.AgentState,
					Rig:         a.Rig,
					RoleType:    a.RoleType,
					MailAddress: agentBeadToMailAddress(a.AgentID, a.Rig, a.RoleType),
				})
			}
		}
	}

	// 4b. Also search all gt:agent labeled beads (includes stopped/closed agents)
	for _, ab := range listAgentBeads() {
		if seenAgentIDs[ab.id] {
			continue // Already found via active pods
		}
		if matchesAgentBead(ab, name) {
			result.Agents = append(result.Agents, whoisAgent{
				AgentID:     ab.id,
				AgentState:  ab.status,
				Rig:         ab.rig,
				RoleType:    ab.roleType,
				MailAddress: agentBeadToMailAddress(ab.id, ab.rig, ab.roleType),
			})
		}
	}

	// Output
	if jsonOutput {
		outputJSON(result)
		return
	}

	found := false

	if len(result.Sessions) > 0 {
		found = true
		fmt.Printf("Sessions:\n")
		for _, s := range result.Sessions {
			status := "stale"
			if s.Active {
				status = "active"
			}
			ago := time.Since(s.LastSeen).Truncate(time.Second)
			if s.ProjectRoot != "" {
				fmt.Printf("  %s (base: %s) — %s, last seen %s ago, project: %s\n", s.AssignedName, s.BaseName, status, ago, s.ProjectRoot)
			} else {
				fmt.Printf("  %s (base: %s) — %s, last seen %s ago\n", s.AssignedName, s.BaseName, status, ago)
			}
		}
	}

	if len(result.Agents) > 0 {
		found = true
		fmt.Printf("Agent beads:\n")
		for _, a := range result.Agents {
			parts := []string{a.AgentID}
			if a.Rig != "" {
				parts = append(parts, fmt.Sprintf("rig=%s", a.Rig))
			}
			if a.RoleType != "" {
				parts = append(parts, fmt.Sprintf("role=%s", a.RoleType))
			}
			if a.PodName != "" {
				parts = append(parts, fmt.Sprintf("pod=%s", a.PodName))
			}
			if a.PodStatus != "" {
				parts = append(parts, fmt.Sprintf("pod_status=%s", a.PodStatus))
			}
			if a.AgentState != "" {
				parts = append(parts, fmt.Sprintf("state=%s", a.AgentState))
			}
			if a.MailAddress != "" {
				parts = append(parts, fmt.Sprintf("mail=%s", a.MailAddress))
			}
			fmt.Printf("  %s\n", strings.Join(parts, ", "))
		}
	}

	if result.Issues.Assigned > 0 || result.Issues.Created > 0 {
		found = true
		fmt.Printf("Issues:\n")
		fmt.Printf("  Assigned: %d", result.Issues.Assigned)
		if len(result.Issues.InProgress) > 0 {
			fmt.Printf(" (%d in progress)", len(result.Issues.InProgress))
		}
		fmt.Println()
		for _, ip := range result.Issues.InProgress {
			fmt.Printf("    ◐ %s\n", ip)
		}
		if result.Issues.Created > 0 {
			fmt.Printf("  Created:  %d\n", result.Issues.Created)
		}
	}

	if result.Reverse != nil {
		found = true
		r := result.Reverse
		fmt.Printf("Reverse lookup (%s):\n", r.InputType)
		if r.BeadID != "" {
			fmt.Printf("  Bead:       %s\n", r.BeadID)
		}
		if r.Title != "" {
			fmt.Printf("  Title:      %s\n", r.Title)
		}
		if r.Status != "" {
			fmt.Printf("  Status:     %s\n", r.Status)
		}
		if r.Assignee != "" {
			fmt.Printf("  Assignee:   %s\n", r.Assignee)
		}
		if r.CreatedBy != "" {
			fmt.Printf("  Created by: %s\n", r.CreatedBy)
		}
		if r.MailAddress != "" {
			fmt.Printf("  Mail:       %s\n", r.MailAddress)
		}
	}

	if !found {
		fmt.Printf("No records found for %q\n", name)
	}
}

// matchesName does case-insensitive prefix/exact matching.
func matchesName(candidate, query string) bool {
	c := strings.ToLower(candidate)
	q := strings.ToLower(query)
	return c == q || strings.HasPrefix(c, q+"-")
}

// matchesAgent checks if an AgentPodInfo matches the query name.
func matchesAgent(a rpc.AgentPodInfo, name string) bool {
	n := strings.ToLower(name)
	// Check agent ID (e.g., gt-beads-crew-scout matches "scout")
	if strings.HasSuffix(strings.ToLower(a.AgentID), "-"+n) || strings.ToLower(a.AgentID) == n {
		return true
	}
	// Check pod name
	if strings.HasSuffix(strings.ToLower(a.PodName), "-"+n) || strings.ToLower(a.PodName) == n {
		return true
	}
	return false
}

// agentBeadToMailAddress derives a gt mail address from agent bead metadata.
// Uses the agent bead ID structure (gt-{rig}-{role}-{name}) to construct
// the canonical mail address format used by gt mail.
//
// Examples:
//
//	"gt-gastown-polecat-nux", rig="gastown", role="polecat" → "gastown/polecats/nux"
//	"gt-gastown-crew-max", rig="gastown", role="crew" → "gastown/crew/max"
//	"gt-gastown-witness", rig="gastown", role="witness" → "gastown/witness"
//	"hq-mayor", rig="", role="mayor" → "mayor"
func agentBeadToMailAddress(agentID, rig, roleType string) string {
	switch roleType {
	case "mayor":
		return "mayor"
	case "deacon":
		return "deacon"
	case "witness":
		if rig != "" {
			return rig + "/witness"
		}
		return ""
	case "refinery":
		if rig != "" {
			return rig + "/refinery"
		}
		return ""
	case "polecat":
		name := extractAgentName(agentID, roleType)
		if rig != "" && name != "" {
			return rig + "/polecats/" + name
		}
		return ""
	case "crew":
		name := extractAgentName(agentID, roleType)
		if rig != "" && name != "" {
			return rig + "/crew/" + name
		}
		return ""
	}
	return ""
}

// extractAgentName extracts the agent name from a bead ID like "gt-gastown-polecat-nux".
// Looks for the "-{roleType}-" segment and returns everything after it.
func extractAgentName(agentID, roleType string) string {
	marker := "-" + roleType + "-"
	idx := strings.Index(agentID, marker)
	if idx < 0 {
		return ""
	}
	return agentID[idx+len(marker):]
}

// --- Reverse lookup helpers (bd-tds62) ---

// looksLikeBeadID returns true if the input looks like a bead ID.
// Matches patterns: bd-xxxxx, gt-xxxxx, hq-xxxxx, beads-xxxxx, or any prefix-alphanumeric.
func looksLikeBeadID(s string) bool {
	// Known prefixes for bead IDs
	for _, prefix := range []string{"bd-", "gt-", "hq-", "beads-"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// looksLikeSessionKey returns true if the input looks like a session key hash (16 hex chars).
func looksLikeSessionKey(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// reverseFromBeadID looks up a bead by ID and shows its owner/assignee/metadata.
func reverseFromBeadID(result *whoisResult, beadID string) {
	if daemonClient == nil {
		return
	}

	resp, err := daemonClient.Show(&rpc.ShowArgs{ID: beadID})
	if err != nil {
		return
	}

	var issue map[string]interface{}
	if resp.Data != nil {
		_ = json.Unmarshal(resp.Data, &issue)
	}
	if issue == nil {
		return
	}

	rev := &whoisReverse{
		InputType: "bead_id",
		BeadID:    beadID,
	}

	if t, ok := issue["title"].(string); ok {
		rev.Title = t
	}
	if s, ok := issue["status"].(string); ok {
		rev.Status = s
	}
	if a, ok := issue["assignee"].(string); ok && a != "" {
		rev.Assignee = a
	}
	if cb, ok := issue["created_by"].(string); ok && cb != "" {
		rev.CreatedBy = cb
	}

	// For agent beads (gt-*), try to derive mail address from bead metadata
	if strings.HasPrefix(beadID, "gt-") {
		rig := ""
		roleType := ""
		if r, ok := issue["rig"].(string); ok {
			rig = r
		}
		if rt, ok := issue["role_type"].(string); ok {
			roleType = rt
		}
		if mail := agentBeadToMailAddress(beadID, rig, roleType); mail != "" {
			rev.MailAddress = mail
		}
	}

	result.Reverse = rev
}

// reverseFromMailAddress resolves a gt mail address to an agent bead.
// Mail format: rig/role/name (e.g., "gastown/polecats/nux") or rig/role (e.g., "gastown/witness")
// Searches active pods first, then all gt:agent labeled beads (including stopped agents).
func reverseFromMailAddress(result *whoisResult, addr string) {
	if daemonClient == nil {
		return
	}

	parts := strings.Split(addr, "/")
	if len(parts) < 2 {
		return
	}

	rev := &whoisReverse{
		InputType:   "mail_address",
		MailAddress: addr,
	}

	// Try active pods first
	podResp, err := daemonClient.AgentPodList(&rpc.AgentPodListArgs{})
	if err == nil {
		for _, a := range podResp.Agents {
			mail := agentBeadToMailAddress(a.AgentID, a.Rig, a.RoleType)
			if strings.EqualFold(mail, addr) {
				rev.BeadID = a.AgentID
				rev.Status = a.AgentState
				populateReverseFromBead(rev, a.AgentID)
				result.Reverse = rev
				return
			}
		}
	}

	// Fall back to all gt:agent labeled beads (includes stopped/closed agents)
	for _, ab := range listAgentBeads() {
		mail := agentBeadToMailAddress(ab.id, ab.rig, ab.roleType)
		if strings.EqualFold(mail, addr) {
			rev.BeadID = ab.id
			rev.Status = ab.status
			rev.Title = ab.title
			rev.Assignee = ab.assignee
			rev.CreatedBy = ab.createdBy
			break
		}
	}

	result.Reverse = rev
}

// reverseFromSessionKey finds a session by its key hash.
func reverseFromSessionKey(result *whoisResult, key string) {
	if daemonClient == nil {
		return
	}

	sessResp, err := daemonClient.SessionList(&rpc.SessionListArgs{IncludeStale: true})
	if err != nil {
		return
	}

	for _, s := range sessResp.Sessions {
		if s.SessionKey == key {
			rev := &whoisReverse{
				InputType: "session_key",
				BeadID:    s.SessionKey,
				Title:     fmt.Sprintf("Session: %s", s.AssignedName),
				Assignee:  s.AssignedName,
			}
			result.Reverse = rev

			// Also add to sessions list for unified display
			cutoff := time.Now().Add(-1 * time.Hour)
			result.Sessions = append(result.Sessions, whoisSession{
				AssignedName: s.AssignedName,
				BaseName:     s.BaseName,
				SessionKey:   s.SessionKey,
				LastSeen:     s.LastSeen,
				Active:       s.LastSeen.After(cutoff),
				ProjectRoot:  s.ProjectRoot,
			})
			return
		}
	}
}

// --- Agent bead search helpers (bd-iaush) ---

// agentBead is a lightweight representation of a gt:agent labeled bead.
type agentBead struct {
	id        string
	title     string
	status    string
	rig       string
	roleType  string
	assignee  string
	createdBy string
}

// listAgentBeads returns all beads with the gt:agent label.
// This includes both active and stopped/closed agents.
func listAgentBeads() []agentBead {
	if daemonClient == nil {
		return nil
	}

	resp, err := daemonClient.List(&rpc.ListArgs{
		Labels: []string{"gt:agent"},
		Limit:  500,
	})
	if err != nil {
		return nil
	}

	var issues []map[string]interface{}
	if resp.Data != nil {
		_ = json.Unmarshal(resp.Data, &issues)
	}

	beads := make([]agentBead, 0, len(issues))
	for _, iss := range issues {
		ab := agentBead{}
		if id, ok := iss["id"].(string); ok {
			ab.id = id
		}
		if t, ok := iss["title"].(string); ok {
			ab.title = t
		}
		if s, ok := iss["status"].(string); ok {
			ab.status = s
		}
		if r, ok := iss["rig"].(string); ok {
			ab.rig = r
		}
		if rt, ok := iss["role_type"].(string); ok {
			ab.roleType = rt
		}
		if a, ok := iss["assignee"].(string); ok {
			ab.assignee = a
		}
		if cb, ok := iss["created_by"].(string); ok {
			ab.createdBy = cb
		}
		beads = append(beads, ab)
	}
	return beads
}

// matchesAgentBead checks if an agent bead matches the query name.
// Matches on agent ID suffix or bare name (same logic as matchesAgent for pods).
func matchesAgentBead(ab agentBead, name string) bool {
	n := strings.ToLower(name)
	id := strings.ToLower(ab.id)
	// Exact match
	if id == n {
		return true
	}
	// Suffix match (e.g., "nux" matches "gt-gastown-polecat-nux")
	if strings.HasSuffix(id, "-"+n) {
		return true
	}
	return false
}

// populateReverseFromBead fills in reverse lookup fields from a bead's issue data.
func populateReverseFromBead(rev *whoisReverse, beadID string) {
	if daemonClient == nil {
		return
	}
	resp, err := daemonClient.Show(&rpc.ShowArgs{ID: beadID})
	if err != nil {
		return
	}
	var issue map[string]interface{}
	if resp.Data != nil {
		_ = json.Unmarshal(resp.Data, &issue)
	}
	if issue == nil {
		return
	}
	if assignee, ok := issue["assignee"].(string); ok && assignee != "" {
		rev.Assignee = assignee
	}
	if cb, ok := issue["created_by"].(string); ok && cb != "" {
		rev.CreatedBy = cb
	}
	if t, ok := issue["title"].(string); ok {
		rev.Title = t
	}
}
