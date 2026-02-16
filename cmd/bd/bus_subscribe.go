package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
)

var busSubscribeCmd = &cobra.Command{
	Use:   "subscribe",
	Short: "Subscribe to event bus streams and print events as they arrive",
	Long: `Subscribe to event bus streams and print events as they arrive.

By default uses the daemon's HTTP SSE endpoint (GET /bus/events), which works
through any HTTP proxy without direct NATS access. Use --nats to force a direct
NATS JetStream connection instead.

By default subscribes to hook events only. Use --stream to subscribe to other
streams, or --all to subscribe to all streams.

Streams: hooks, decisions, oj, agents, mail, mutations, config, inbox

Examples:
  bd bus subscribe                                          # Hook events via HTTP (default)
  bd bus subscribe --stream=mail                            # Mail events only
  bd bus subscribe --stream=decisions                       # Decision events only
  bd bus subscribe --all                                    # All streams
  bd bus subscribe --filter=MailSent                        # Specific event type
  bd bus subscribe --nats                                   # Force direct NATS connection
  bd bus subscribe --nats --nats-url=nats://remote:4222     # Remote NATS server
  bd bus subscribe --nats --nats-url=wss://host/nats        # Via WebSocket
  bd bus subscribe --json                                   # Machine-readable output`,
	RunE: runBusSubscribe,
}

func runBusSubscribe(cmd *cobra.Command, args []string) error {
	natsMode, _ := cmd.Flags().GetBool("nats")
	flagURL, _ := cmd.Flags().GetString("nats-url")

	// --nats-url implies --nats mode
	if flagURL != "" {
		natsMode = true
	}

	if natsMode {
		return runBusSubscribeNATS(cmd)
	}
	return runBusSubscribeHTTP(cmd)
}

// runBusSubscribeHTTP subscribes via the daemon's HTTP SSE endpoint (default).
func runBusSubscribeHTTP(cmd *cobra.Command) error {
	filter, _ := cmd.Flags().GetString("filter")
	streamFlag, _ := cmd.Flags().GetString("stream")
	allFlag, _ := cmd.Flags().GetBool("all")

	// Determine stream parameter for HTTP endpoint
	var streamParam string
	switch {
	case allFlag:
		streamParam = "all"
	case streamFlag != "":
		// Normalize stream name
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

	// Resolve daemon HTTP URL and token from the RPC client
	var baseURL, token string
	if hc := daemonClient.HTTPClient(); hc != nil {
		baseURL = hc.BaseURL()
		token = hc.Token()
	}
	if baseURL == "" {
		// Fallback: construct from daemon host env
		host := os.Getenv("BD_DAEMON_HOST")
		if host == "" {
			host = "http://127.0.0.1:9080"
		}
		baseURL = host
		token = os.Getenv("BD_DAEMON_TOKEN")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	opts := rpc.BusSSEClientOptions{
		BaseURL: baseURL,
		Token:   token,
		Stream:  streamParam,
		Filter:  filter,
	}

	fmt.Fprintf(os.Stderr, "Subscribing to stream=%s via %s/bus/events (Ctrl-C to stop)\n", streamParam, baseURL)

	events, errs := rpc.ConnectBusSSE(ctx, opts)

	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return nil
			}
			if jsonOutput {
				out, _ := json.Marshal(evt)
				fmt.Println(string(out))
			} else {
				fmt.Printf("[%s] seq=%d %s.%s ", truncateTS(evt.TS), evt.Seq, evt.Stream, evt.Type)
				printPayloadSummary(evt.Payload)
				fmt.Println()
			}
		case err, ok := <-errs:
			if !ok {
				return nil
			}
			return err
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nUnsubscribed.")
			return nil
		}
	}
}

// runBusSubscribeNATS subscribes via direct NATS JetStream connection (--nats flag).
func runBusSubscribeNATS(cmd *cobra.Command) error {
	filter, _ := cmd.Flags().GetString("filter")
	flagURL, _ := cmd.Flags().GetString("nats-url")
	flagToken, _ := cmd.Flags().GetString("nats-token")

	// Resolve NATS URL
	var natsURL, natsToken string
	if flagURL != "" {
		natsURL = flagURL
	} else if envURL := os.Getenv("BD_NATS_URL"); envURL != "" {
		natsURL = envURL
	} else {
		resp, err := daemonClient.Execute(rpc.OpBusStatus, nil)
		if err == nil && resp.Success {
			var result rpc.BusStatusResult
			if err := json.Unmarshal(resp.Data, &result); err == nil && result.NATSEnabled {
				natsURL = fmt.Sprintf("nats://127.0.0.1:%d", result.NATSPort)
			}
		}
	}
	if natsURL == "" {
		port := os.Getenv("BD_NATS_PORT")
		if port == "" {
			port = fmt.Sprintf("%d", daemon.DefaultNATSPort)
		}
		natsURL = fmt.Sprintf("nats://127.0.0.1:%s", port)
	}

	if flagToken != "" {
		natsToken = flagToken
	} else {
		natsToken = os.Getenv("BD_DAEMON_TOKEN")
	}

	// Connect to NATS
	connectOpts := []nats.Option{
		nats.Name("bd-bus-subscribe"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
	}
	if natsToken != "" {
		connectOpts = append(connectOpts, nats.Token(natsToken))
	}

	if strings.HasPrefix(natsURL, "ws://") || strings.HasPrefix(natsURL, "wss://") {
		if u, err := url.Parse(natsURL); err == nil && u.Path != "" && u.Path != "/" {
			connectOpts = append(connectOpts, nats.ProxyPath(u.Path))
			u.Path = ""
			natsURL = u.String()
		}
	}

	nc, err := nats.Connect(natsURL, connectOpts...)
	if err != nil {
		return fmt.Errorf("connect to NATS at %s: %w", natsURL, err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("JetStream context: %w", err)
	}

	// Determine subjects
	streamFlag, _ := cmd.Flags().GetString("stream")
	allFlag, _ := cmd.Flags().GetBool("all")

	var subjects []string
	switch {
	case filter != "":
		subjects = []string{eventbus.SubjectForEvent(eventbus.EventType(filter))}
	case allFlag:
		for _, name := range eventbus.StreamNames {
			prefix := eventbus.SubjectPrefixForStream(name)
			if prefix != "" {
				subjects = append(subjects, prefix+">")
			}
		}
	case streamFlag != "":
		switch strings.ToLower(streamFlag) {
		case "hooks", "hook":
			subjects = []string{eventbus.SubjectHookPrefix + ">"}
		case "decisions", "decision":
			subjects = []string{eventbus.SubjectDecisionPrefix + ">"}
		case "oj", "oddjobs":
			subjects = []string{eventbus.SubjectOjPrefix + ">"}
		case "agents", "agent":
			subjects = []string{eventbus.SubjectAgentPrefix + ">"}
		case "mail":
			subjects = []string{eventbus.SubjectMailPrefix + ">"}
		case "mutations", "mutation":
			subjects = []string{eventbus.SubjectMutationPrefix + ">"}
		case "config", "formula":
			subjects = []string{eventbus.SubjectConfigPrefix + ">"}
		case "inbox":
			subjects = []string{eventbus.SubjectInboxPrefix + ">"}
		default:
			return fmt.Errorf("unknown stream %q (valid: %s)", streamFlag, strings.Join(eventbus.StreamNames, ", "))
		}
	default:
		subjects = []string{eventbus.SubjectHookPrefix + ">"}
	}

	// Subscribe
	handler := func(msg *nats.Msg) {
		meta, _ := msg.Metadata()
		if jsonOutput {
			entry := map[string]interface{}{
				"subject": msg.Subject,
				"data":    json.RawMessage(msg.Data),
			}
			if meta != nil {
				entry["seq"] = meta.Sequence.Stream
				entry["timestamp"] = meta.Timestamp.UTC().Format(time.RFC3339Nano)
			}
			out, _ := json.Marshal(entry)
			fmt.Println(string(out))
		} else {
			seq := uint64(0)
			ts := ""
			if meta != nil {
				seq = meta.Sequence.Stream
				ts = meta.Timestamp.UTC().Format("15:04:05.000")
			}
			fmt.Printf("[%s] seq=%d %s ", ts, seq, msg.Subject)
			printPayloadSummary(msg.Data)
			fmt.Println()
		}
		_ = msg.Ack()
	}

	var subs []*nats.Subscription
	for _, subject := range subjects {
		sub, err := js.Subscribe(subject, handler, nats.DeliverNew(), nats.AckExplicit())
		if err != nil {
			for _, s := range subs {
				_ = s.Unsubscribe()
			}
			return fmt.Errorf("subscribe to %s: %w", subject, err)
		}
		subs = append(subs, sub)
	}
	defer func() {
		for _, s := range subs {
			_ = s.Unsubscribe()
		}
	}()

	for _, subject := range subjects {
		fmt.Fprintf(os.Stderr, "Subscribed to %s on %s via NATS (Ctrl-C to stop)\n", subject, natsURL)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Fprintln(os.Stderr, "\nUnsubscribed.")
	return nil
}

// printPayloadSummary extracts brief summary fields from a JSON payload.
func printPayloadSummary(data []byte) {
	var event struct {
		SessionID string `json:"session_id"`
		ToolName  string `json:"tool_name,omitempty"`
		From      string `json:"from,omitempty"`
		To        string `json:"to,omitempty"`
	}
	if json.Unmarshal(data, &event) == nil {
		if event.ToolName != "" {
			fmt.Printf("tool=%s ", event.ToolName)
		}
		if event.From != "" {
			fmt.Printf("from=%s ", event.From)
		}
		if event.To != "" {
			fmt.Printf("to=%s ", event.To)
		}
		if event.SessionID != "" {
			sid := event.SessionID
			if len(sid) > 12 {
				sid = sid[:12] + "..."
			}
			fmt.Printf("session=%s", sid)
		}
	}
}

// truncateTS converts an RFC3339Nano timestamp to HH:MM:SS.mmm for display.
func truncateTS(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.UTC().Format("15:04:05.000")
}

func init() {
	busSubscribeCmd.Flags().String("filter", "", "Filter by event type (e.g., Stop, MailSent, DecisionCreated)")
	busSubscribeCmd.Flags().String("stream", "", "Stream to subscribe to (hooks, decisions, oj, agents, mail, mutations, config, inbox)")
	busSubscribeCmd.Flags().Bool("all", false, "Subscribe to all streams")
	busSubscribeCmd.Flags().Bool("nats", false, "Use direct NATS connection instead of HTTP SSE")
	busSubscribeCmd.Flags().String("nats-url", "", "NATS server URL (implies --nats)")
	busSubscribeCmd.Flags().String("nats-token", "", "NATS auth token (default: BD_DAEMON_TOKEN env)")
	busCmd.AddCommand(busSubscribeCmd)
}
