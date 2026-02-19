package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/tui/bus"
)

var busWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Interactive TUI for monitoring live event bus activity",
	Long: `Launch an interactive terminal UI that monitors the event bus in real-time.

Shows events as they flow through the daemon's event bus with color-coded
streams, timestamps, and payload summaries. Connects via SSE (HTTP) to the
daemon's /bus/events endpoint.

By default subscribes to hook events only. Use --stream to subscribe to other
streams, or --all to subscribe to all streams.

Streams: hooks, decisions, oj, agents, mail, mutations, config, inbox

Views:
  Timeline   Live scrolling event list (default)
  Detail     Full JSON payload of selected event (press Enter)

Key bindings:
  j/k        Scroll up/down through events
  space      Pause/resume auto-scroll
  enter      Show full event detail
  g/G        Jump to first/last event
  r          Reconnect to SSE endpoint
  ?          Toggle help
  q/Ctrl+C   Quit

Examples:
  bd bus watch                           # Hook events (default)
  bd bus watch --stream=all              # All streams
  bd bus watch --stream=agents           # Agent lifecycle events
  bd bus watch --stream=decisions        # Decision events
  bd bus watch --filter=MailSent         # Specific event type
  bd bus watch --history-size=5000       # Keep 5000 events in buffer`,
	RunE: runBusWatch,
}

func init() {
	busWatchCmd.Flags().String("stream", "", "Stream to watch (hooks, decisions, oj, agents, mail, mutations, config, inbox)")
	busWatchCmd.Flags().Bool("all", false, "Watch all streams")
	busWatchCmd.Flags().String("filter", "", "Filter by event type (e.g., Stop, MailSent)")
	busWatchCmd.Flags().Int("history-size", 2000, "Number of events to keep in ring buffer")
	busCmd.AddCommand(busWatchCmd)
}

func runBusWatch(cmd *cobra.Command, args []string) error {
	streamFlag, _ := cmd.Flags().GetString("stream")
	allFlag, _ := cmd.Flags().GetBool("all")
	filter, _ := cmd.Flags().GetString("filter")
	historySize, _ := cmd.Flags().GetInt("history-size")

	// Determine stream parameter
	var streamParam string
	switch {
	case allFlag:
		streamParam = "all"
	case streamFlag != "":
		s := strings.ToLower(streamFlag)
		switch s {
		case "hooks", "hook":
			streamParam = "hooks"
		case "decisions", "decision":
			streamParam = "decisions"
		case "oj", "oddjobs":
			streamParam = "oj"
		case "agents", "agent":
			streamParam = "agents"
		case "mail":
			streamParam = "mail"
		case "mutations", "mutation":
			streamParam = "mutations"
		case "config", "formula":
			streamParam = "config"
		case "inbox":
			streamParam = "inbox"
		default:
			return fmt.Errorf("unknown stream %q (valid: %s)", streamFlag, strings.Join(eventbus.StreamNames, ", "))
		}
	default:
		streamParam = "hooks"
	}

	// Resolve daemon HTTP URL and token
	var baseURL, token string
	if hc := daemonClient.HTTPClient(); hc != nil {
		baseURL = hc.BaseURL()
		token = hc.Token()
	}
	if baseURL == "" {
		host := os.Getenv("BD_DAEMON_HOST")
		if host == "" {
			host = "http://127.0.0.1:9080"
		}
		baseURL = host
		token = os.Getenv("BD_DAEMON_TOKEN")
	}

	opts := rpc.BusSSEClientOptions{
		BaseURL: baseURL,
		Token:   token,
		Stream:  streamParam,
		Filter:  filter,
	}

	model := bus.New(opts)
	model.SetHistorySize(historySize)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running bus watch: %v\n", err)
		return err
	}

	return nil
}
