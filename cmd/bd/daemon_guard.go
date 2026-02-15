package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// guardDaemonStartForDolt blocks daemon start/restart commands when:
// 1. The workspace backend is embedded Dolt (unless --federation is specified)
// 2. The systemd bd-daemon service is actively running (unless run via systemd)
//
// Rationale for Dolt guard: embedded Dolt is effectively single-writer at the OS-process
// level. The daemon architecture relies on multiple processes (CLI + daemon + helper spawns),
// which can trigger lock contention and transient "read-only" failures.
//
// Rationale for systemd guard: prevents multiple daemon instances when systemd is
// managing the daemon. We check if the service is actually running (via systemctl
// is-active) rather than relying on config settings that can be bypassed.
//
// Exception: --federation flag enables dolt sql-server mode which is multi-writer.
//
// Note: This guard should only be attached to commands that START a daemon process
// (start, restart). Read-only commands (status, stop, logs, health, list) are allowed
// even with Dolt backend.
//
// We still allow help output so users can discover the command surface.
func guardDaemonStartForDolt(cmd *cobra.Command, _ []string) error {
	// Allow `--help` for any daemon subcommand.
	if helpFlag := cmd.Flags().Lookup("help"); helpFlag != nil {
		if help, _ := cmd.Flags().GetBool("help"); help {
			return nil
		}
	}

	// Allow `--federation` flag which enables dolt sql-server (multi-writer) mode.
	if fedFlag := cmd.Flags().Lookup("federation"); fedFlag != nil {
		if federation, _ := cmd.Flags().GetBool("federation"); federation {
			return nil
		}
	}

	// Check if running via systemd (BD_DAEMON_SYSTEMD=1 set by systemd unit)
	isSystemdInvocation := os.Getenv("BD_DAEMON_SYSTEMD") == "1"

	// If we're already running under systemd, allow the command
	// (systemd is managing the daemon, no need for duplicate detection)
	if isSystemdInvocation {
		return nil
	}

	// Check if systemd service is actively managing a daemon for this workspace.
	// This prevents agents from accidentally starting a second daemon instance
	// when systemd is already managing one.
	if isSystemdServiceActive() {
		return fmt.Errorf("daemon is managed by systemctl (bd-daemon service is active).\n\n" +
			"For user services:\n" +
			"  systemctl --user restart bd-daemon@...\n" +
			"  systemctl --user stop bd-daemon@...\n" +
			"  systemctl --user status bd-daemon@...\n" +
			"  systemctl --user list-units bd-daemon@*\n\n" +
			"For system services:\n" +
			"  sudo systemctl restart bd-daemon.service\n" +
			"  sudo systemctl stop bd-daemon.service\n" +
			"  sudo systemctl status bd-daemon.service")
	}

	return nil
}

// isSystemdServiceActive checks if any bd-daemon systemd service is active.
// This is used to prevent manual daemon starts when systemd is managing the daemon.
// We check for both the template instances (bd-daemon@*.service) and the simple
// service (bd-daemon.service) to cover both service styles.
// We check BOTH user-level (--user) and system-level services since the daemon
// may be installed in either location.
func isSystemdServiceActive() bool {
	// Check user-level services first
	if isSystemdServiceActiveForScope("--user") {
		return true
	}

	// Check system-level services (no --user flag)
	if isSystemdServiceActiveForScope("") {
		return true
	}

	return false
}

// isSystemdServiceActiveForScope checks if any bd-daemon service is active
// at the given systemctl scope. Pass "--user" for user services or "" for system services.
func isSystemdServiceActiveForScope(scope string) bool {
	// Build args for template-style service instances using list-units
	// The --state=active filter ensures we only see running instances
	args := []string{}
	if scope != "" {
		args = append(args, scope)
	}
	args = append(args, "--state=active", "--no-legend", "--no-pager", "list-units", "bd-daemon@*.service")

	cmd := exec.Command("systemctl", args...)
	output, err := cmd.Output()
	if err == nil && len(strings.TrimSpace(string(output))) > 0 {
		return true
	}

	// Check for simple service (bd-daemon.service)
	// Using is-active which returns 0 if the unit is active
	args = []string{}
	if scope != "" {
		args = append(args, scope)
	}
	args = append(args, "is-active", "--quiet", "bd-daemon.service")

	cmd = exec.Command("systemctl", args...)
	if err := cmd.Run(); err == nil {
		return true
	}

	return false
}
