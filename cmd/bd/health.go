package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/rpc"
)

var healthCmd = &cobra.Command{
	Use:     "health",
	GroupID: "maint",
	Short:   "Quick daemon connectivity and health check",
	Long: `Check daemon connectivity and health status.

Works in both local and remote daemon modes:
  - Local: connects via Unix socket in .beads/bd.sock
  - Remote: connects via HTTP to BD_DAEMON_HOST

Exits 0 if healthy, 1 if unhealthy or unreachable.

Examples:
  bd health          # Quick health check
  bd health --json   # Machine-readable output`,
	Run: func(cmd *cobra.Command, args []string) {
		runHealth()
	},
}

func init() {
	rootCmd.AddCommand(healthCmd)
}

// healthResult is the JSON output for bd health.
type healthResult struct {
	Status         string  `json:"status"`                      // "healthy", "degraded", "unhealthy", "unreachable"
	Mode           string  `json:"mode"`                        // "local" or "remote"
	Host           string  `json:"host,omitempty"`              // daemon host (remote mode)
	Version        string  `json:"version,omitempty"`           // daemon version
	CLIVersion     string  `json:"cli_version"`                 // this CLI's version
	Compatible     bool    `json:"compatible"`                  // version compatibility
	Uptime         float64 `json:"uptime_seconds,omitempty"`    // daemon uptime
	DBResponseTime float64 `json:"db_response_ms,omitempty"`    // database response time
	ResponseTime   float64 `json:"response_ms"`                 // health check round-trip time
	Error          string  `json:"error,omitempty"`              // error message if unhealthy
}

func runHealth() {
	start := time.Now()

	// Determine remote address: BD_DAEMON_HTTP_URL (includes port) takes
	// priority over BD_DAEMON_HOST (may omit port, defaulting to 80).
	// This mirrors the priority in rpc.TryConnectAuto. (hq-bib)
	remoteAddr := rpc.GetDaemonHTTPURL()
	if remoteAddr == "" {
		remoteAddr = rpc.GetDaemonHost()
	}
	mode := "local"
	host := ""

	var client *rpc.Client
	var err error

	if remoteAddr != "" {
		// Remote mode
		mode = "remote"
		host = remoteAddr
		addr := remoteAddr
		if !rpc.IsHTTPURL(addr) {
			addr = "http://" + addr
		}
		token := rpc.GetDaemonToken()
		httpClient, connectErr := rpc.TryConnectHTTP(addr, token)
		if connectErr != nil {
			elapsed := time.Since(start)
			outputHealthResult(healthResult{
				Status:       "unreachable",
				Mode:         mode,
				Host:         host,
				CLIVersion:   Version,
				ResponseTime: float64(elapsed.Milliseconds()),
				Error:        connectErr.Error(),
			})
			os.Exit(1)
		}
		client = rpc.WrapHTTPClient(httpClient)
	} else {
		// Local mode
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			fmt.Fprintf(os.Stderr, "No .beads directory found\n")
			fmt.Fprintf(os.Stderr, "Run 'bd init' to initialize, or set BD_DAEMON_HOST for remote mode\n")
			os.Exit(1)
		}
		socketPath := filepath.Join(beadsDir, "bd.sock")
		client, err = rpc.TryConnectAuto(socketPath)
		if err != nil {
			elapsed := time.Since(start)
			outputHealthResult(healthResult{
				Status:       "unreachable",
				Mode:         mode,
				CLIVersion:   Version,
				ResponseTime: float64(elapsed.Milliseconds()),
				Error:        err.Error(),
			})
			os.Exit(1)
		}
		if client == nil {
			elapsed := time.Since(start)
			outputHealthResult(healthResult{
				Status:       "unreachable",
				Mode:         mode,
				CLIVersion:   Version,
				ResponseTime: float64(elapsed.Milliseconds()),
				Error:        "daemon is not running",
			})
			os.Exit(1)
		}
	}
	defer func() { _ = client.Close() }()

	health, err := client.Health()
	elapsed := time.Since(start)

	if err != nil {
		outputHealthResult(healthResult{
			Status:       "unreachable",
			Mode:         mode,
			Host:         host,
			CLIVersion:   Version,
			ResponseTime: float64(elapsed.Milliseconds()),
			Error:        err.Error(),
		})
		os.Exit(1)
	}

	compatible := health.Version == "" || health.Version == Version
	result := healthResult{
		Status:         health.Status,
		Mode:           mode,
		Host:           host,
		Version:        health.Version,
		CLIVersion:     Version,
		Compatible:     compatible,
		Uptime:         health.Uptime,
		DBResponseTime: health.DBResponseTime,
		ResponseTime:   float64(elapsed.Milliseconds()),
		Error:          health.Error,
	}

	outputHealthResult(result)

	if health.Status == "unhealthy" {
		os.Exit(1)
	}
}

func outputHealthResult(r healthResult) {
	if jsonOutput {
		data, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(data))
		return
	}

	// Human-readable output
	switch r.Status {
	case "healthy":
		fmt.Printf("Daemon: healthy")
	case "degraded":
		fmt.Printf("Daemon: degraded")
	case "unhealthy":
		fmt.Printf("Daemon: unhealthy")
	case "unreachable":
		fmt.Printf("Daemon: unreachable")
	default:
		fmt.Printf("Daemon: %s", r.Status)
	}

	if r.Mode == "remote" && r.Host != "" {
		fmt.Printf(" (%s)", r.Host)
	}
	fmt.Println()

	if r.Version != "" {
		fmt.Printf("  Version:  %s", r.Version)
		if !r.Compatible {
			fmt.Printf(" (CLI: %s — mismatch)", r.CLIVersion)
		}
		fmt.Println()
	}

	if r.Uptime > 0 {
		fmt.Printf("  Uptime:   %s\n", formatUptime(r.Uptime))
	}

	if r.DBResponseTime > 0 {
		fmt.Printf("  DB:       %.1fms\n", r.DBResponseTime)
	}

	fmt.Printf("  Latency:  %dms\n", int(r.ResponseTime))

	if r.Error != "" {
		fmt.Printf("  Error:    %s\n", r.Error)
	}
}
