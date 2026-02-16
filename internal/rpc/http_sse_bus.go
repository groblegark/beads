//go:build !windows

package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/eventbus"
)

// handleSSEBusEvents handles GET /bus/events for streaming all event bus streams via SSE.
// The JSON envelope per event is defined by BusSSEEvent in http_client_sse.go.
// Query parameters:
//   - stream: comma-separated stream names (hooks,agents,etc.) or "all" (default: all)
//   - filter: event type filter (e.g., "Stop", "AgentStarted")
//   - since: unix timestamp in ms, replays events newer than this
//
// Events are sourced from JetStream. Returns 503 if JetStream is not available.
// Each SSE event uses the stream short name as the SSE event: field.
// (bd-95p0o / bd-ozs53)
func (h *HTTPServer) handleSSEBusEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth check
	if h.token != "" {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			h.writeError(w, http.StatusUnauthorized, "missing Authorization header")
			return
		}
		if !strings.HasPrefix(authHeader, "Bearer ") {
			h.writeError(w, http.StatusUnauthorized, "invalid Authorization header format")
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token != h.token {
			h.writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Parse stream parameter
	streamParam := r.URL.Query().Get("stream")
	streams := parseBusStreams(streamParam)
	if streams == nil {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown stream name in: %q (valid: %s, all)", streamParam, strings.Join(eventbus.StreamNames, ",")))
		return
	}

	// Parse filter (event type)
	filterType := r.URL.Query().Get("filter")

	// Parse since
	var sinceMs int64
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		var err error
		sinceMs, err = strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, "invalid 'since' parameter: must be unix ms")
			return
		}
	}

	// Require JetStream
	bus := h.rpcServer.GetBus()
	if bus == nil {
		h.writeError(w, http.StatusServiceUnavailable, "event bus not available")
		return
	}
	js := bus.JetStream()
	if js == nil {
		h.writeError(w, http.StatusServiceUnavailable, "JetStream not available")
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Build subscriptions for each requested stream
	var subs []*nats.Subscription
	defer func() {
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
	}()

	for _, streamName := range streams {
		prefix := eventbus.SubjectPrefixForStream(streamName)
		if prefix == "" {
			continue
		}

		// If filtering by event type, subscribe to specific subject; otherwise wildcard
		subject := prefix + ">"
		if filterType != "" {
			// For decision events, the subject has an extra scope segment
			if streamName == "decisions" {
				subject = prefix + "*." + filterType
			} else {
				subject = prefix + filterType
			}
		}

		var opts []nats.SubOpt
		opts = append(opts, nats.AckExplicit())
		if sinceMs > 0 {
			opts = append(opts, nats.StartTime(time.UnixMilli(sinceMs)))
		} else {
			opts = append(opts, nats.DeliverNew())
		}

		sub, err := js.SubscribeSync(subject, opts...)
		if err != nil {
			// Log but continue — some streams might not have events yet
			continue
		}
		subs = append(subs, sub)
	}

	if len(subs) == 0 {
		// Write an error comment and close — no subscriptions established
		fmt.Fprintf(w, ": error: no streams available\n\n")
		flusher.Flush()
		return
	}

	// Round-robin across subscriptions, relaying to SSE
	ctx := r.Context()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		default:
			gotMessage := false
			for _, sub := range subs {
				msg, err := sub.NextMsg(50 * time.Millisecond)
				if err != nil {
					continue
				}
				gotMessage = true

				streamName := eventbus.StreamForSubject(msg.Subject)
				evtType := eventbus.EventTypeFromSubject(msg.Subject)

				var seq uint64
				var ts time.Time
				if meta, err := msg.Metadata(); err == nil && meta != nil {
					seq = meta.Sequence.Stream
					ts = meta.Timestamp.UTC()
				} else {
					ts = time.Now().UTC()
				}

				evt := BusSSEEvent{
					Stream:  streamName,
					Type:    evtType,
					Subject: msg.Subject,
					Seq:     seq,
					TS:      ts.Format(time.RFC3339Nano),
					Payload: json.RawMessage(msg.Data),
				}

				data, err := json.Marshal(evt)
				if err != nil {
					_ = msg.Ack()
					continue
				}

				fmt.Fprintf(w, "id: %d\n", seq)
				fmt.Fprintf(w, "event: %s\n", streamName)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				_ = msg.Ack()
			}

			if !gotMessage {
				// No messages on any subscription — avoid busy-loop
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

// parseBusStreams parses the stream query parameter into a list of valid stream names.
// Returns nil if any stream name is invalid.
// Empty string or "all" returns all streams.
func parseBusStreams(param string) []string {
	if param == "" || param == "all" {
		return eventbus.StreamNames
	}

	parts := strings.Split(param, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if eventbus.SubjectPrefixForStream(p) == "" {
			return nil // invalid stream name
		}
		result = append(result, p)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
