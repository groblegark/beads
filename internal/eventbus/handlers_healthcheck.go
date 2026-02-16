package eventbus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
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

	// Check 1: Git repository exists and is functional
	if issue := checkGitRepo(ctx, event.CWD); issue != nil {
		issues = append(issues, *issue)
	}

	// Check 2: Beads workspace health (.beads/ directory accessible)
	if issue := checkBeadsWorkspace(ctx, event.CWD); issue != nil {
		issues = append(issues, *issue)
	}

	// Check 3: Daemon connectivity (check if bd health works)
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

// checkGitRepo verifies a git repository exists at the CWD.
// This catches the "dispatched to empty pod" failure mode (beads-j4de).
func checkGitRepo(ctx context.Context, cwd string) *healthIssue {
	if cwd == "" {
		return nil // Can't check without a CWD
	}
	// Check if .git exists in CWD or any parent
	if _, err := os.Stat(cwd); os.IsNotExist(err) {
		return &healthIssue{
			Category: "workspace",
			Message:  fmt.Sprintf("CWD does not exist: %s", cwd),
			Severity: "error",
		}
	}
	// Use git rev-parse to verify we're in a git repo
	stdout, _, err := runGitCommand(ctx, cwd, "rev-parse", "--git-dir")
	if err != nil || strings.TrimSpace(stdout) == "" {
		return &healthIssue{
			Category: "workspace",
			Message:  fmt.Sprintf("not a git repository: %s", cwd),
			Severity: "error",
		}
	}
	return nil
}

// checkBeadsWorkspace verifies the .beads/ directory is accessible.
func checkBeadsWorkspace(ctx context.Context, cwd string) *healthIssue {
	_, stderr, err := runBDCommand(ctx, cwd, "stats", "--json")
	if err != nil {
		return &healthIssue{
			Category: "beads",
			Message:  fmt.Sprintf("beads workspace check failed: %v (stderr: %s)", err, truncate(stderr, 200)),
			Severity: "warning",
		}
	}
	return nil
}

// runGitCommand executes a git subcommand and captures stdout/stderr.
func runGitCommand(ctx context.Context, cwd string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
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
