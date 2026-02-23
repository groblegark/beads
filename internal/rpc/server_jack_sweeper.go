package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/types"
)

// startJackSweeper runs a background goroutine that periodically checks
// for expired jacks. A jack is expired when:
//
//	now > jack.metadata.jack_expires_at
//
// Expired jacks trigger auto-escalation: a P0 alert bead is created and
// an EventJackExpired event is emitted so downstream consumers (Slack bot,
// etc.) can react.
//
// The sweeper checks every 5 minutes and stops when shutdownChan is closed.
// Interval is configurable via BEADS_JACK_SWEEP_INTERVAL.
func (s *Server) startJackSweeper() {
	interval := 5 * time.Minute
	if env := os.Getenv("BEADS_JACK_SWEEP_INTERVAL"); env != "" {
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
				s.sweepExpiredJacks()
			}
		}
	}()
}

// sweepExpiredJacks scans active jacks and escalates those that have
// exceeded their TTL.
func (s *Server) sweepExpiredJacks() {
	store := s.storage
	if store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find all active jacks (type=jack, status=in_progress)
	jackType := types.TypeJack
	inProgress := types.StatusInProgress
	jacks, err := store.SearchIssues(ctx, "", types.IssueFilter{
		IssueType: &jackType,
		Status:    &inProgress,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "jack-sweeper: search active jacks: %v\n", err)
		return
	}

	now := time.Now().UTC()

	for _, jack := range jacks {
		// Parse metadata to get expiry
		metadata := make(map[string]interface{})
		if len(jack.Metadata) > 0 {
			if err := json.Unmarshal(jack.Metadata, &metadata); err != nil {
				continue
			}
		}

		expiresAtStr, _ := metadata["jack_expires_at"].(string)
		if expiresAtStr == "" {
			continue
		}

		expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
		if err != nil {
			continue
		}

		if now.Before(expiresAt) {
			continue // Not expired yet
		}

		// Check if we already escalated (avoid duplicate alerts on each sweep)
		if _, alreadyEscalated := metadata["jack_escalated"]; alreadyEscalated {
			continue
		}

		target, _ := metadata["jack_target"].(string)
		overdue := now.Sub(expiresAt)

		// Mark as escalated in metadata to prevent duplicate alerts
		metadata["jack_escalated"] = true
		metadata["jack_escalated_at"] = now.Format(time.RFC3339)
		metadataJSON, _ := json.Marshal(metadata)

		updates := map[string]interface{}{
			"metadata": string(metadataJSON),
		}
		if err := store.UpdateIssue(ctx, jack.ID, updates, "system:jack-sweeper"); err != nil {
			fmt.Fprintf(os.Stderr, "jack-sweeper: update metadata %s: %v\n", jack.ID, err)
			continue
		}

		// Emit jack expired event
		s.emitJackEvent(
			eventbus.EventJackExpired,
			jack.ID,
			target,
			jack.Assignee,
			fmt.Sprintf("Jack expired %s ago (TTL was %s)", overdue.Round(time.Second), metadata["jack_ttl"]),
			"",
			expiresAtStr,
		)

		// Also emit mutation so SSE/event-driven sync picks it up
		s.emitMutation(MutationUpdate, jack.ID, "", "")

		fmt.Fprintf(os.Stderr, "jack-sweeper: expired %s (target=%s, expired %s ago)\n",
			jack.ID, target, overdue.Round(time.Second))
	}
}
