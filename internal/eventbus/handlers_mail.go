package eventbus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// NudgeStore is the minimal storage interface for resolving agent bead
// coop_url in-process, avoiding subprocess round-trips. (gt-2md5kf)
type NudgeStore interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
}

// labelQueryStore is an optional extension of NudgeStore for label-based
// agent resolution. If the underlying store implements this interface,
// the mail router can resolve agents by label. (bd-p1mt)
type labelQueryStore interface {
	GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error)
	GetLabels(ctx context.Context, issueID string) ([]string, error)
}

// MailNudgeHandler pushes mail notifications to the recipient's inbox and
// nudges them via Coop HTTP API. Inbox is the reliable delivery path (Phase 4);
// the HTTP nudge ensures idle agents wake up to drain it. (bd-cdp8, bd-xtahx.5)
//
// Priority 50 (runs after standard handlers; nudging is supplementary).
type MailNudgeHandler struct {
	httpClient *http.Client
	store      NudgeStore
}

// SetNudgeStore wires in direct storage access for coop URL resolution. (gt-2md5kf)
func (h *MailNudgeHandler) SetNudgeStore(store NudgeStore) { h.store = store }

func (h *MailNudgeHandler) ID() string          { return "mail-nudge" }
func (h *MailNudgeHandler) Handles() []EventType { return []EventType{EventMailSent} }
func (h *MailNudgeHandler) Priority() int        { return 50 }

func (h *MailNudgeHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	var payload MailEventPayload
	if err := unmarshalEventPayload(event, &payload); err != nil {
		return fmt.Errorf("mail-nudge: %w", err)
	}

	to := payload.To
	if to == "" {
		return nil
	}

	// Convert mail address to agent bead ID.
	agentID := mailAddressToAgentID(to)
	if agentID == "" {
		log.Printf("mail-nudge: cannot resolve agent ID for address %q, skipping", to)
		return nil
	}

	// Phase 4: push to inbox for reliable delivery.
	// Idempotent via dedup_key; complements InboxDrainHandler at priority 30.
	message := fmt.Sprintf("You have new mail from %s: %s", payload.From, payload.Subject)
	dedupKey := fmt.Sprintf("mail:%s", payload.MessageID)
	_, _, pushErr := runBDCommand(ctx, event.CWD,
		"inbox", "push",
		"--to", agentID,
		"--type", "mail",
		"--source", "mail-nudge",
		"--dedup-key", dedupKey,
		message,
	)
	if pushErr != nil {
		log.Printf("mail-nudge: inbox push for %s failed (non-fatal): %v", agentID, pushErr)
	}

	// Nudge via Coop HTTP to wake idle agents immediately.
	var coopURL string
	var resolveErr error
	if h.store != nil {
		coopURL, resolveErr = resolveCoopURLFromStore(ctx, h.store, agentID)
	} else {
		coopURL, resolveErr = resolveCoopURLFromBead(ctx, event.CWD, agentID)
	}
	if resolveErr != nil {
		log.Printf("mail-nudge: no coop_url for agent %q: %v", agentID, resolveErr)
		return nil // Not a coop agent or not reachable; skip silently.
	}

	delivered, reason, err := postNudge(ctx, h.httpClient, coopURL, message)
	if err != nil {
		log.Printf("mail-nudge: nudge to %s (%s) failed: %v", agentID, coopURL, err)
		return nil // Best-effort; don't fail the event chain.
	}

	if delivered {
		log.Printf("mail-nudge: nudged %s successfully", agentID)
	} else {
		log.Printf("mail-nudge: nudge to %s not delivered (reason: %s)", agentID, reason)
	}

	return nil
}

// mailAddressToAgentID converts a mail address to a beads agent bead ID.
//
// Address normalization (matching gastown's AddressToIdentity):
//   - "mayor" or "mayor/" -> "hq-mayor" (town-level)
//   - "deacon" or "deacon/" -> "hq-deacon" (town-level)
//   - "gastown/polecats/Toast" -> "gt-gastown-polecat-Toast" (strip middle)
//   - "gastown/crew/max" -> "gt-gastown-crew-max"
//   - "gastown/Toast" -> "gt-gastown-polecat-Toast" (canonical)
//   - "gastown/witness" -> "gt-gastown-witness"
//   - "gastown/refinery" -> "gt-gastown-refinery"
func mailAddressToAgentID(address string) string {
	// Trim trailing slash
	address = strings.TrimSuffix(address, "/")

	// Already a bead-style ID (e.g., "hq-mayor", "gt-gastown-polecat-furiosa",
	// "bd-beads-polecat-obsidian"). beads3d sends these directly from graph
	// node IDs; pass them through as-is. (bd-sd39b)
	if strings.HasPrefix(address, "hq-") ||
		strings.HasPrefix(address, "gt-") ||
		strings.HasPrefix(address, "bd-") {
		return address
	}

	// Town-level agents — bead IDs use "hq-" prefix (e.g., "hq-mayor").
	switch address {
	case "mayor":
		return "hq-mayor"
	case "deacon":
		return "hq-deacon"
	}

	parts := strings.Split(address, "/")

	switch len(parts) {
	case 2:
		rig := parts[0]
		role := parts[1]
		switch role {
		case "witness":
			return fmt.Sprintf("gt-%s-witness", rig)
		case "refinery":
			return fmt.Sprintf("gt-%s-refinery", rig)
		default:
			if strings.HasPrefix(role, "crew/") {
				crewName := strings.TrimPrefix(role, "crew/")
				return fmt.Sprintf("gt-%s-crew-%s", rig, crewName)
			}
			// Default: assume polecat
			return fmt.Sprintf("gt-%s-polecat-%s", rig, role)
		}
	case 3:
		rig := parts[0]
		middle := parts[1]
		name := parts[2]
		switch middle {
		case "polecats":
			return fmt.Sprintf("gt-%s-polecat-%s", rig, name)
		case "crew":
			return fmt.Sprintf("gt-%s-crew-%s", rig, name)
		default:
			return ""
		}
	}

	return ""
}

// resolveCoopURLFromStore looks up an agent bead's coop_url using direct
// storage access, avoiding subprocess overhead. Tries:
//  1. Direct ID lookup (agentID might already be a bead ID)
//  2. Convert agent name to bead ID via mailAddressToAgentID, then lookup
//  3. Label-based resolution: parse address into labels and query (bd-p1mt)
//
// (gt-2md5kf)
func resolveCoopURLFromStore(ctx context.Context, store NudgeStore, agentID string) (string, error) {
	// Strategy 1: direct lookup (agentID is already a bead ID).
	if issue, err := store.GetIssue(ctx, agentID); err == nil && issue != nil {
		if url := extractCoopURLFromNotes(issue.Notes); url != "" {
			return url, nil
		}
	}

	// Strategy 2: convert agent name to bead ID via pattern matching.
	beadID := mailAddressToAgentID(agentID)
	if beadID != "" && beadID != agentID {
		if issue, err := store.GetIssue(ctx, beadID); err == nil && issue != nil {
			if url := extractCoopURLFromNotes(issue.Notes); url != "" {
				return url, nil
			}
		}
	}

	// Strategy 3: label-based resolution. Parse the mail address into labels
	// (rig:X, role_type:Y) and find a matching agent bead. This handles new
	// roles and rigs without code changes. (bd-p1mt)
	if lqs, ok := store.(labelQueryStore); ok {
		if labels := mailAddressToLabels(agentID); len(labels) > 0 {
			if issue, err := findAgentByLabels(ctx, lqs, labels); err == nil && issue != nil {
				if url := extractCoopURLFromNotes(issue.Notes); url != "" {
					return url, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no coop_url for agent %q (store-based)", agentID)
}

// findAgentByLabels queries the store for agent beads matching ALL given labels.
// Returns the first match, or nil if none found. (bd-p1mt)
func findAgentByLabels(ctx context.Context, store labelQueryStore, wantLabels []string) (*types.Issue, error) {
	if len(wantLabels) == 0 {
		return nil, nil
	}

	// Query by the first label, then filter by remaining labels.
	candidates, err := store.GetIssuesByLabel(ctx, wantLabels[0])
	if err != nil {
		return nil, err
	}

	for _, issue := range candidates {
		labels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			continue
		}
		labelSet := make(map[string]bool, len(labels))
		for _, l := range labels {
			labelSet[l] = true
		}
		// Check that all wanted labels are present.
		allMatch := true
		for _, want := range wantLabels {
			if !labelSet[want] {
				allMatch = false
				break
			}
		}
		if allMatch {
			return issue, nil
		}
	}

	return nil, nil
}

// mailAddressToLabels converts a mail address to a set of labels for
// label-based agent resolution. Returns nil if the address can't be parsed.
//
// Examples:
//
//	"mayor"                  → ["gt:agent", "role_type:mayor"]
//	"gastown/witness"        → ["gt:agent", "rig:gastown", "role_type:witness"]
//	"gastown/polecats/Toast" → ["gt:agent", "rig:gastown", "role_type:polecat"]
//	"gastown/crew/max"       → ["gt:agent", "rig:gastown", "role_type:crew"]
//
// (bd-p1mt)
func mailAddressToLabels(address string) []string {
	address = strings.TrimSuffix(address, "/")
	if address == "" {
		return nil
	}

	labels := []string{"gt:agent"}

	// Town-level agents: single-word address is a role_type.
	parts := strings.Split(address, "/")
	switch len(parts) {
	case 1:
		// "mayor", "deacon" → role_type:mayor
		labels = append(labels, "role_type:"+parts[0])
		return labels
	case 2:
		rig := parts[0]
		role := parts[1]
		labels = append(labels, "rig:"+rig)
		// Known singleton roles keep their role name; others default to polecat.
		switch role {
		case "witness", "refinery":
			labels = append(labels, "role_type:"+role)
		default:
			labels = append(labels, "role_type:polecat")
		}
		return labels
	case 3:
		rig := parts[0]
		middle := parts[1]
		labels = append(labels, "rig:"+rig)
		switch middle {
		case "polecats":
			labels = append(labels, "role_type:polecat")
		case "crew":
			labels = append(labels, "role_type:crew")
		default:
			return nil // Unknown middle component
		}
		return labels
	}

	return nil
}

// resolveCoopURLFromBead looks up an agent bead and extracts coop_url from
// its notes field. Tries three strategies:
//  1. Direct ID lookup via `bd show <agentID> --json`
//  2. Search for beads containing coop_url whose ID or title matches agentID
//     (handles naming mismatch: requested_by="mayor" but bead ID="hq-mayor")
func resolveCoopURLFromBead(ctx context.Context, cwd, agentID string) (string, error) {
	// Strategy 1: direct bead ID lookup.
	if url, err := extractCoopURLFromBead(ctx, cwd, agentID); err == nil {
		return url, nil
	}

	// Strategy 2: search beads with coop_url in notes and match by agent name.
	// The agentID from RequestedBy (e.g. "mayor/", "furiosa") may not match the
	// bead ID (e.g. "hq-mayor", "gt-gastown-polecat-furiosa") because the agent
	// controller uses K8s-style names while BD_ACTOR is a short name.
	cleanID := strings.TrimRight(agentID, "/")
	if cleanID == "" {
		return "", fmt.Errorf("empty agent ID after cleanup")
	}
	stdout, _, err := runBDCommand(ctx, cwd, "search", "coop_url", "--json")
	if err != nil {
		return "", fmt.Errorf("search for coop_url beads: %w", err)
	}

	var issues []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Notes string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(stdout), &issues); err != nil {
		return "", fmt.Errorf("parse search output: %w", err)
	}

	// Match: bead ID or title contains the agent name (case-insensitive).
	lower := strings.ToLower(cleanID)
	for _, issue := range issues {
		idLower := strings.ToLower(issue.ID)
		titleLower := strings.ToLower(issue.Title)
		if strings.Contains(idLower, lower) || strings.Contains(titleLower, lower) {
			if url := extractCoopURLFromNotes(issue.Notes); url != "" {
				return url, nil
			}
		}
	}

	return "", fmt.Errorf("no bead with coop_url matching agent %q", agentID)
}

// extractCoopURLFromBead does a direct `bd show --json` lookup and extracts coop_url.
func extractCoopURLFromBead(ctx context.Context, cwd, beadID string) (string, error) {
	stdout, _, err := runBDCommand(ctx, cwd, "show", beadID, "--json")
	if err != nil {
		return "", fmt.Errorf("bd show %s: %w", beadID, err)
	}

	var issues []struct {
		Notes string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(stdout), &issues); err != nil {
		return "", fmt.Errorf("parse bd show output: %w", err)
	}
	if len(issues) == 0 {
		return "", fmt.Errorf("bead %q not found", beadID)
	}

	url := extractCoopURLFromNotes(issues[0].Notes)
	if url == "" {
		return "", fmt.Errorf("no coop_url in notes for %q", beadID)
	}
	return url, nil
}

// extractCoopURLFromNotes parses a notes string for a "coop_url: <url>" line.
func extractCoopURLFromNotes(notes string) string {
	for _, line := range strings.Split(notes, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "coop_url" {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

// postNudge POSTs a nudge message to a Coop sidecar's nudge endpoint.
// Returns (delivered, reason, error). If client is nil, a default is used.
func postNudge(ctx context.Context, client *http.Client, coopURL, message string) (bool, string, error) {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return false, "", err
	}

	url := strings.TrimRight(coopURL, "/") + "/api/v1/agent/nudge"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return false, "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var nudgeResp struct {
		Delivered   bool   `json:"delivered"`
		StateBefore string `json:"state_before,omitempty"`
		Reason      string `json:"reason,omitempty"`
	}
	if err := json.Unmarshal(respBody, &nudgeResp); err != nil {
		return false, "", fmt.Errorf("parse nudge response: %w", err)
	}

	return nudgeResp.Delivered, nudgeResp.Reason, nil
}

// CoopURLResolver looks up the Coop sidecar URL for a given agent ID.
// The default implementation shells out to `bd show --json` and extracts
// coop_url from the agent bead's notes field.
type CoopURLResolver func(ctx context.Context, cwd, agentID string) (string, error)

// DecisionNudgeHandler nudges the requesting agent via their Coop HTTP API when
// a decision is resolved. The server-side handleDecisionResolve already pushes
// to inbox (bd-eo9xt), so this handler focuses on the Coop HTTP nudge to wake
// idle agents immediately. (bd-5mkhu, gt-2md5kf)
//
// Priority 50 (runs after standard handlers; nudging is supplementary).
type DecisionNudgeHandler struct {
	HTTPClient      *http.Client    // If nil, a default client with 5s timeout is used.
	CoopURLResolver CoopURLResolver // If nil, uses store-based or subprocess resolver.
	store           NudgeStore
}

// SetNudgeStore wires in direct storage access for coop URL resolution. (gt-2md5kf)
func (h *DecisionNudgeHandler) SetNudgeStore(store NudgeStore) { h.store = store }

func (h *DecisionNudgeHandler) ID() string          { return "decision-nudge" }
func (h *DecisionNudgeHandler) Handles() []EventType { return []EventType{EventDecisionResponded} }
func (h *DecisionNudgeHandler) Priority() int        { return 50 }

func (h *DecisionNudgeHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	var payload DecisionEventPayload
	if err := unmarshalEventPayload(event, &payload); err != nil {
		return fmt.Errorf("decision-nudge: %w", err)
	}

	agentID := payload.RequestedBy
	if agentID == "" {
		return nil // No agent to nudge
	}

	// Note: inbox push is handled server-side in handleDecisionResolve (bd-eo9xt).
	// This handler focuses on the Coop HTTP nudge to wake idle agents. (gt-2md5kf)

	message := fmt.Sprintf("Decision %s resolved by %s (chose: %s)", payload.DecisionID, payload.ResolvedBy, payload.ChosenLabel)

	// Resolve coop URL: explicit resolver > store-based > subprocess fallback.
	var coopURL string
	var resolveErr error
	if h.CoopURLResolver != nil {
		coopURL, resolveErr = h.CoopURLResolver(ctx, event.CWD, agentID)
	} else if h.store != nil {
		coopURL, resolveErr = resolveCoopURLFromStore(ctx, h.store, agentID)
	} else {
		coopURL, resolveErr = resolveCoopURLFromBead(ctx, event.CWD, agentID)
	}
	if resolveErr != nil {
		log.Printf("decision-nudge: no coop_url for agent %q: %v", agentID, resolveErr)
		return nil // Not a coop agent or not reachable; skip silently.
	}

	delivered, reason, err := postNudge(ctx, h.HTTPClient, coopURL, message)
	if err != nil {
		log.Printf("decision-nudge: nudge to %s (%s) failed: %v", agentID, coopURL, err)
		return nil // Best-effort; don't fail the event chain.
	}

	if delivered {
		log.Printf("decision-nudge: nudged %s for decision %s", agentID, payload.DecisionID)
	} else {
		log.Printf("decision-nudge: nudge to %s not delivered (reason: %s)", agentID, reason)
	}

	return nil
}

// DefaultMailHandlers returns the mail and decision nudge event bus handlers
// for daemon registration.
func DefaultMailHandlers() []Handler {
	return []Handler{
		&MailNudgeHandler{},
		&DecisionNudgeHandler{},
	}
}
