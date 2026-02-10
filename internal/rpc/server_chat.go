package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/steveyegge/beads/internal/chat"
	"github.com/steveyegge/beads/internal/eventbus"
)

// handleChatSend publishes a chat message to NATS JetStream via the event bus. (bd-viux)
func (s *Server) handleChatSend(req *Request) Response {
	var args ChatSendArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid arguments: %v", err)}
	}

	if args.SessionTag == "" {
		return Response{Success: false, Error: "session_tag is required"}
	}
	if args.Content == "" {
		return Response{Success: false, Error: "content is required"}
	}
	if args.Direction == "" {
		return Response{Success: false, Error: "direction is required (\"in\" or \"out\")"}
	}
	if args.Direction != "in" && args.Direction != "out" {
		return Response{Success: false, Error: fmt.Sprintf("direction must be \"in\" or \"out\", got %q", args.Direction)}
	}

	s.mu.RLock()
	bus := s.bus
	s.mu.RUnlock()

	if bus == nil || !bus.JetStreamEnabled() {
		return Response{Success: false, Error: "NATS JetStream not available (event bus not configured or NATS disabled)"}
	}

	payload := &eventbus.ChatMessagePayload{
		SessionTag: args.SessionTag,
		Sender:     args.Sender,
		SenderID:   args.SenderID,
		Content:    args.Content,
		ChannelID:  args.ChannelID,
		ThreadTS:   args.ThreadTS,
		Timestamp:  time.Now().UTC(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("marshal payload: %v", err)}
	}

	subject := eventbus.SubjectForChatSession(args.SessionTag, args.Direction)
	ack, err := bus.JetStreamPublish(subject, data)
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("publish to %s: %v", subject, err)}
	}

	var seq uint64
	if ack != nil {
		seq = ack.Sequence
	}

	fmt.Fprintf(os.Stderr, "chat_send: published to %s (seq=%d)\n", subject, seq)

	result, _ := json.Marshal(ChatSendResult{Seq: seq, Subject: subject})
	return Response{Success: true, Data: result}
}

// handleChatListen subscribes to inbound chat messages and waits for one to arrive. (bd-viux)
func (s *Server) handleChatListen(req *Request) Response {
	var args ChatListenArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid arguments: %v", err)}
	}

	if args.SessionTag == "" {
		return Response{Success: false, Error: "session_tag is required"}
	}

	s.mu.RLock()
	bus := s.bus
	s.mu.RUnlock()

	if bus == nil || !bus.JetStreamEnabled() {
		return Response{Success: false, Error: "NATS JetStream not available"}
	}

	// Default timeout: 30s for RPC (much shorter than CLI's 30m).
	timeout := 30 * time.Second
	if args.TimeoutMs > 0 {
		timeout = time.Duration(args.TimeoutMs) * time.Millisecond
		// Cap at 5 minutes to avoid blocking the server too long.
		if timeout > 5*time.Minute {
			timeout = 5 * time.Minute
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	subject := eventbus.SubjectForChatSession(args.SessionTag, "in")

	js := bus.JetStreamContext()
	if js == nil {
		return Response{Success: false, Error: "JetStream context unavailable"}
	}

	deliverOpt := nats.DeliverNew()
	if args.Drain {
		deliverOpt = nats.DeliverAll()
	}

	sub, err := js.SubscribeSync(subject, deliverOpt, nats.AckExplicit())
	if err != nil {
		return Response{Success: false, Error: fmt.Sprintf("subscribe to %s: %v", subject, err)}
	}
	defer sub.Unsubscribe()

	var messages []ChatMessageRPC

	for {
		if err := ctx.Err(); err != nil {
			if len(messages) > 0 {
				break // Return what we have on timeout.
			}
			return Response{Success: false, Error: "timeout waiting for chat message"}
		}

		msg, err := sub.NextMsg(1 * time.Second)
		if err != nil {
			if err == nats.ErrTimeout {
				if args.Drain && len(messages) > 0 {
					break // Drained all pending.
				}
				continue // Keep waiting.
			}
			return Response{Success: false, Error: fmt.Sprintf("next message: %v", err)}
		}

		var payload eventbus.ChatMessagePayload
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			msg.Nak()
			continue
		}

		meta, _ := msg.Metadata()
		var seq uint64
		if meta != nil {
			seq = meta.Sequence.Stream
		}

		messages = append(messages, ChatMessageRPC{
			SessionTag: payload.SessionTag,
			Sender:     payload.Sender,
			SenderID:   payload.SenderID,
			Content:    payload.Content,
			ChannelID:  payload.ChannelID,
			ThreadTS:   payload.ThreadTS,
			Timestamp:  payload.Timestamp.Format(time.RFC3339),
			Seq:        seq,
		})

		msg.Ack()

		if !args.Drain {
			break // Got one message, return immediately.
		}
	}

	fmt.Fprintf(os.Stderr, "chat_listen: received %d message(s) for session %s\n",
		len(messages), args.SessionTag)

	result, _ := json.Marshal(ChatListenResult{Messages: messages})
	return Response{Success: true, Data: result}
}

// handleChatStatus returns chat session information. (bd-viux)
func (s *Server) handleChatStatus(req *Request) Response {
	var args ChatStatusArgs
	if req.Args != nil {
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return Response{Success: false, Error: fmt.Sprintf("invalid arguments: %v", err)}
		}
	}

	s.mu.RLock()
	reg := s.chatRegistry
	s.mu.RUnlock()

	if reg == nil {
		return Response{Success: false, Error: "chat registry not configured"}
	}

	var sessions []ChatSessionInfo

	if args.SessionTag != "" {
		snap := reg.GetByTag(args.SessionTag)
		if snap == nil {
			return Response{Success: false, Error: fmt.Sprintf("session %q not found", args.SessionTag)}
		}
		sessions = append(sessions, chatSessionFromSnapshot(snap))
	} else {
		for _, snap := range reg.All() {
			sessions = append(sessions, chatSessionFromSnapshot(snap))
		}
	}

	result, _ := json.Marshal(ChatStatusResult{Sessions: sessions})
	return Response{Success: true, Data: result}
}

// handleChatSession creates or closes a chat session. (bd-viux)
func (s *Server) handleChatSession(req *Request) Response {
	var args ChatSessionArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{Success: false, Error: fmt.Sprintf("invalid arguments: %v", err)}
	}

	if args.SessionTag == "" {
		return Response{Success: false, Error: "session_tag is required"}
	}

	s.mu.RLock()
	reg := s.chatRegistry
	s.mu.RUnlock()

	if reg == nil {
		return Response{Success: false, Error: "chat registry not configured"}
	}

	switch args.Action {
	case "create":
		existing := reg.GetByTag(args.SessionTag)
		reg.Register(args.SessionTag, args.ChannelID, args.ThreadTS, args.AgentName)
		result, _ := json.Marshal(ChatSessionResult{
			SessionTag: args.SessionTag,
			Status:     "open",
			Created:    existing == nil,
		})
		fmt.Fprintf(os.Stderr, "chat_session: created session %s\n", args.SessionTag)
		return Response{Success: true, Data: result}

	case "close":
		closed := reg.Close(args.SessionTag)
		if !closed {
			return Response{Success: false, Error: fmt.Sprintf("session %q not found", args.SessionTag)}
		}
		result, _ := json.Marshal(ChatSessionResult{
			SessionTag: args.SessionTag,
			Status:     "closed",
		})
		fmt.Fprintf(os.Stderr, "chat_session: closed session %s\n", args.SessionTag)
		return Response{Success: true, Data: result}

	default:
		return Response{Success: false, Error: fmt.Sprintf("unknown action %q (use \"create\" or \"close\")", args.Action)}
	}
}

func chatSessionFromSnapshot(snap *chat.RPCSessionSnapshot) ChatSessionInfo {
	return ChatSessionInfo{
		SessionTag: snap.SessionTag,
		ChannelID:  snap.ChannelID,
		ThreadTS:   snap.ThreadTS,
		AgentName:  snap.AgentName,
		Status:     snap.Status,
		CreatedAt:  snap.CreatedAt,
		UpdatedAt:  snap.UpdatedAt,
	}
}
