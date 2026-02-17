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
	Use:   "whois <name>",
	Short: "Look up an agent by name",
	Long: `Look up an agent identity across all naming systems.

Searches:
  1. Active daemon sessions (adjective-animal names like "fast-ox")
  2. Issues assigned to or created by this name
  3. Agent beads with matching pod/role information
  4. Derives gt mail address from agent bead identity

Bridges the naming systems:
  - Beads sessions: adjective-animal names (fast-ox, keen-newt)
  - K8s agents: polecat/crew names (nux, furiosa)
  - GT mail: rig/role/name addresses (gastown/polecats/nux)

Examples:
  bd whois fast-ox                # Look up a session by adjective-animal name
  bd whois nux                    # Find K8s agent + gt mail address
  bd whois mayor                  # Town-level agent
  bd whois keen-newt --json       # JSON output with all fields`,
	Args: cobra.ExactArgs(1),
	Run:  runWhois,
}

func init() {
	rootCmd.AddCommand(whoisCmd)
}

type whoisResult struct {
	Name     string        `json:"name"`
	Sessions []whoisSession `json:"sessions,omitempty"`
	Issues   whoisIssues    `json:"issues,omitempty"`
	Agents   []whoisAgent   `json:"agents,omitempty"`
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

	// 4. Search agent beads
	podResp, err := daemonClient.AgentPodList(&rpc.AgentPodListArgs{})
	if err == nil {
		for _, a := range podResp.Agents {
			// Match agent ID suffix, pod name, or role
			if matchesAgent(a, name) {
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
		name := extractAgentName(agentID, rig, roleType)
		if rig != "" && name != "" {
			return rig + "/polecats/" + name
		}
		return ""
	case "crew":
		name := extractAgentName(agentID, rig, roleType)
		if rig != "" && name != "" {
			return rig + "/crew/" + name
		}
		return ""
	}
	return ""
}

// extractAgentName extracts the agent name from a bead ID like "gt-gastown-polecat-nux".
// Looks for the "-{roleType}-" segment and returns everything after it.
func extractAgentName(agentID, rig, roleType string) string {
	marker := "-" + roleType + "-"
	idx := strings.Index(agentID, marker)
	if idx < 0 {
		return ""
	}
	return agentID[idx+len(marker):]
}
