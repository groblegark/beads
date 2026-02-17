package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

// pollDecisionResponse waits for a decision response using NATS event bus if available,
// falling back to polling. Returns (selectedOption, responseText, error).
func pollDecisionResponse(ctx context.Context, decisionID string, timeout, pollInterval time.Duration) (string, string, error) {
	// Try event bus wake first — sub-second latency vs 2s polling.
	selected, text, err := waitForDecisionViaEventBus(ctx, decisionID, timeout)
	if err == nil {
		return selected, text, nil
	}
	// NATS unavailable — fall back to polling.
	fmt.Fprintf(os.Stderr, "NATS unavailable (%v), falling back to polling\n", err)
	return pollDecisionLoop(ctx, decisionID, timeout, pollInterval)
}

// pollDecisionLoop is the polling fallback when NATS is unavailable.
func pollDecisionLoop(ctx context.Context, decisionID string, timeout, pollInterval time.Duration) (string, string, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_, selected, text, done, err := checkDecisionResponse(ctx, decisionID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error polling decision: %v\n", err)
				continue
			}
			if done {
				return selected, text, nil
			}

			if time.Now().After(deadline) {
				return "", "", nil // timeout — allow stop
			}

		case <-ctx.Done():
			return "", "", nil // context canceled — allow stop
		}
	}
}

// waitForDecisionViaEventBus subscribes to NATS JetStream for DecisionResponded
// events and waits for the specific decision ID. Returns an error if NATS is
// unavailable (caller should fall back to polling).
func waitForDecisionViaEventBus(ctx context.Context, decisionID string, timeout time.Duration) (string, string, error) {
	if daemonClient == nil {
		return "", "", fmt.Errorf("no daemon client")
	}

	resp, err := daemonClient.Execute(rpc.OpBusStatus, nil)
	if err != nil {
		return "", "", fmt.Errorf("bus status RPC: %w", err)
	}
	if !resp.Success {
		return "", "", fmt.Errorf("bus status error: %s", resp.Error)
	}

	var status rpc.BusStatusResult
	if err := json.Unmarshal(resp.Data, &status); err != nil {
		return "", "", fmt.Errorf("parse bus status: %w", err)
	}
	if !status.NATSEnabled || status.NATSPort == 0 {
		return "", "", fmt.Errorf("NATS not enabled")
	}

	connectURL := fmt.Sprintf("nats://127.0.0.1:%d", status.NATSPort)
	connectOpts := []nats.Option{
		nats.Name("bd-decision-wait"),
		nats.Timeout(5 * time.Second),
	}
	if token := os.Getenv("BD_DAEMON_TOKEN"); token != "" {
		connectOpts = append(connectOpts, nats.Token(token))
	}

	nc, err := nats.Connect(connectURL, connectOpts...)
	if err != nil {
		return "", "", fmt.Errorf("NATS connect: %w", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		return "", "", fmt.Errorf("JetStream context: %w", err)
	}

	return awaitDecisionOnJetStream(ctx, js, decisionID, timeout)
}

// awaitDecisionOnJetStream subscribes to DecisionResponded events on the given
// JetStream context and waits for the specific decision ID.
func awaitDecisionOnJetStream(ctx context.Context, js nats.JetStreamContext, decisionID string, timeout time.Duration) (string, string, error) {
	subject := eventbus.SubjectForEvent(eventbus.EventDecisionResponded)
	sub, err := js.SubscribeSync(subject, nats.DeliverNew())
	if err != nil {
		return "", "", fmt.Errorf("subscribe %s: %w", subject, err)
	}
	defer sub.Unsubscribe()

	fmt.Fprintf(os.Stderr, "Listening on NATS %s for decision %s\n", subject, decisionID)

	// Check if already responded (race: response arrived before we subscribed).
	_, selected, text, done, err := checkDecisionResponse(ctx, decisionID)
	if err == nil && done {
		return selected, text, nil
	}

	// Wait for matching event.
	deadline := time.Now().Add(timeout)
	for {
		timeLeft := time.Until(deadline)
		if timeLeft <= 0 {
			return "", "", nil // timeout
		}

		msg, err := sub.NextMsg(timeLeft)
		if err != nil {
			if err == nats.ErrTimeout {
				return "", "", nil // timeout
			}
			_, selected, text, done, dbErr := checkDecisionResponse(ctx, decisionID)
			if dbErr == nil && done {
				return selected, text, nil
			}
			return "", "", fmt.Errorf("NATS NextMsg: %w", err)
		}

		var payload eventbus.DecisionEventPayload
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			_ = msg.Ack()
			continue
		}

		_ = msg.Ack()

		if payload.DecisionID == decisionID {
			_, selected, text, done, err := checkDecisionResponse(ctx, decisionID)
			if err == nil && done {
				return selected, text, nil
			}
			time.Sleep(100 * time.Millisecond)
			_, selected, text, done, err = checkDecisionResponse(ctx, decisionID)
			if err == nil && done {
				return selected, text, nil
			}
			return payload.ChosenLabel, payload.Rationale, nil
		}
	}
}

// checkDecisionResponse checks if a decision point has been responded to.
func checkDecisionResponse(ctx context.Context, decisionID string) (*types.DecisionPoint, string, string, bool, error) {
	if daemonClient == nil {
		if store == nil {
			return nil, "", "", false, nil
		}
		dp, err := store.GetDecisionPoint(ctx, decisionID)
		if err == nil && dp != nil && dp.RespondedAt != nil {
			return dp, dp.SelectedOption, decisionResponseText(dp), true, nil
		}
		issue, err := store.GetIssue(ctx, decisionID)
		if err != nil {
			return nil, "", "", false, err
		}
		if issue != nil && issue.Status == types.StatusClosed {
			return nil, "", "", true, nil
		}
		return nil, "", "", false, nil
	}

	getArgs := &rpc.DecisionGetArgs{IssueID: decisionID}
	result, err := daemonClient.DecisionGet(getArgs)
	if err == nil && result != nil && result.Decision != nil {
		dp := result.Decision
		if dp.RespondedAt != nil {
			return dp, dp.SelectedOption, decisionResponseText(dp), true, nil
		}
	}

	showArgs := &rpc.ShowArgs{ID: decisionID}
	resp, err := daemonClient.Show(showArgs)
	if err != nil {
		return nil, "", "", false, err
	}

	var issue types.Issue
	if err := json.Unmarshal(resp.Data, &issue); err != nil {
		return nil, "", "", false, err
	}

	if issue.Status == types.StatusClosed {
		return nil, "", "", true, nil
	}

	return nil, "", "", false, nil
}

// decisionResponseText returns the best available text from a decision response.
func decisionResponseText(dp *types.DecisionPoint) string {
	if dp.ResponseText != "" {
		return dp.ResponseText
	}
	if dp.Rationale != "" {
		return dp.Rationale
	}
	return dp.Guidance
}
