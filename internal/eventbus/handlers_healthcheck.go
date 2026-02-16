package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

// HealthCheckHandler runs quick health checks on SessionStart to detect
// stuck conditions (workspace broken, daemon degraded, credentials expired).
// If a stuck condition is found, it pushes a P0 alert to the captain/inbox
// and injects a warning into the agent's context.
// Priority 5 (runs before prime — early detection before work begins).
type HealthCheckHandler struct{}

func (h *HealthCheckHandler) ID() string          { return "health-check" }
func (h *HealthCheckHandler) Handles() []EventType { return []EventType{EventSessionStart} }
func (h *HealthCheckHandler) Priority() int        { return 5 }

// healthIssue represents a detected stuck condition.
type healthIssue struct {
	Category string `json:"category"` // "workspace", "daemon", "credentials"
	Message  string `json:"message"`
	Severity string `json:"severity"` // "error", "warning"
}

func (h *HealthCheckHandler) Handle(ctx context.Context, event *Event, result *Result) error {
	// Only run for agents (non-interactive sessions)
	if os.Getenv("BD_ACTOR") == "" && os.Getenv("GT_ROLE") == "" {
		return nil
	}

	var issues []healthIssue

	// Check 1: Git workspace health
	if issue := checkGitWorkspace(ctx, event.CWD); issue != nil {
		issues = append(issues, *issue)
	}

	// Check 2: Daemon connectivity (check if bd health works)
	if issue := checkDaemonHealth(ctx, event.CWD); issue != nil {
		issues = append(issues, *issue)
	}

	if len(issues) == 0 {
		return nil // All healthy
	}

	// Build alert message
	var alerts []string
	for _, issue := range issues {
		alerts = append(alerts, fmt.Sprintf("[%s] %s: %s", issue.Severity, issue.Category, issue.Message))
	}
	alertMsg := fmt.Sprintf("HEALTH CHECK: %d issue(s) detected:\n%s", len(issues), strings.Join(alerts, "\n"))

	log.Printf("health-check: %s", alertMsg)

	// Push P0 alert to captain via inbox (fire-and-forget)
	agentName := os.Getenv("BD_ACTOR")
	if agentName == "" {
		agentName = os.Getenv("GT_ROLE")
	}
	pushAlert(ctx, event.CWD, agentName, alertMsg)

	// Inject warning into agent context
	warning := fmt.Sprintf("**HEALTH WARNING**: %s\nAction: Check your workspace and daemon connectivity before proceeding.", alertMsg)
	result.Inject = append(result.Inject, warning)

	return nil
}

// checkGitWorkspace verifies the git workspace is functional.
func checkGitWorkspace(ctx context.Context, cwd string) *healthIssue {
	// Check if .beads/ directory is accessible by running bd stats (lightweight)
	_, stderr, err := runBDCommand(ctx, cwd, "stats", "--json")
	if err != nil {
		return &healthIssue{
			Category: "workspace",
			Message:  fmt.Sprintf("workspace check failed: %v (stderr: %s)", err, truncate(stderr, 200)),
			Severity: "error",
		}
	}
	return nil
}

// checkDaemonHealth verifies daemon is reachable and healthy.
func checkDaemonHealth(ctx context.Context, cwd string) *healthIssue {
	stdout, _, err := runBDCommand(ctx, cwd, "health", "--json")
	if err != nil {
		return &healthIssue{
			Category: "daemon",
			Message:  fmt.Sprintf("daemon health check failed: %v", err),
			Severity: "error",
		}
	}

	// Parse health response
	var health struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if json.Unmarshal([]byte(stdout), &health) == nil {
		if health.Status == "unhealthy" || health.Status == "unreachable" {
			return &healthIssue{
				Category: "daemon",
				Message:  fmt.Sprintf("daemon %s: %s", health.Status, health.Error),
				Severity: "error",
			}
		}
	}

	return nil
}

// pushAlert sends a P0 alert to the captain via inbox.
func pushAlert(ctx context.Context, cwd string, agentName string, message string) {
	args := []string{
		"inbox", "push",
		"--to", "captain",
		"--type", "alert",
		"--source", agentName,
		"--dedup-key", fmt.Sprintf("health-%s", agentName),
		message,
	}
	_, _, _ = runBDCommand(ctx, cwd, args...)
}

// truncate shortens a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
