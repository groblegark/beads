package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/chat"
	"github.com/steveyegge/beads/internal/daemon"
	"github.com/steveyegge/beads/internal/eventbus"
	"github.com/steveyegge/beads/internal/rpc"
)

var chatCmd = &cobra.Command{
	Use:     "chat",
	GroupID: "advanced",
	Short:   "Bidirectional Slack-Agent chat (bd-viux)",
	Long: `Chat commands for bidirectional messaging between Slack users and agents.

Commands:
  bd chat send <session> <message>   Send a message to an agent session
  bd chat listen <session>           Listen for inbound messages (blocking)
  bd chat reply <session> <message>  Send a reply from an agent to Slack
  bd chat status <session>           Show chat session status`,
}

// connectChatNATS establishes a NATS+JetStream connection using the same
// discovery pattern as bd bus subscribe.
func connectChatNATS() (*nats.Conn, nats.JetStreamContext, error) {
	var natsURL string
	var natsToken string

	if daemonClient != nil {
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

	natsToken = os.Getenv("BD_DAEMON_TOKEN")

	connectOpts := []nats.Option{
		nats.Name("bd-chat"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(5),
	}
	if natsToken != "" {
		connectOpts = append(connectOpts, nats.Token(natsToken))
	}

	nc, err := nats.Connect(natsURL, connectOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to NATS at %s: %w", natsURL, err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("JetStream context: %w", err)
	}

	return nc, js, nil
}

// --- bd chat send ---

var chatSendCmd = &cobra.Command{
	Use:   "send <session-tag> <message>",
	Short: "Send a message to an agent session (Slack user → agent)",
	Long: `Publish a chat message to the inbound channel for an agent session.
The agent must be listening with 'bd chat listen' to receive the message.

Examples:
  bd chat send sess-abc123 "What's the status of the deployment?"
  bd chat send my-agent "Please review PR #42"`,
	Args: cobra.ExactArgs(2),
	RunE: runChatSend,
}

func runChatSend(cmd *cobra.Command, args []string) error {
	sessionTag := args[0]
	content := args[1]
	sender, _ := cmd.Flags().GetString("sender")
	senderID, _ := cmd.Flags().GetString("sender-id")
	channelID, _ := cmd.Flags().GetString("channel")
	threadTS, _ := cmd.Flags().GetString("thread")

	if sender == "" {
		sender = os.Getenv("USER")
	}

	// Prefer daemon RPC when available.
	if daemonClient != nil {
		result, err := daemonClient.ChatSend(&rpc.ChatSendArgs{
			SessionTag: sessionTag,
			Content:    content,
			Sender:     sender,
			SenderID:   senderID,
			Direction:  "in",
			ChannelID:  channelID,
			ThreadTS:   threadTS,
		})
		if err == nil {
			if jsonOutput {
				out, _ := json.Marshal(result)
				fmt.Println(string(out))
			} else {
				fmt.Fprintf(os.Stderr, "Sent to %s via daemon (seq=%d)\n", sessionTag, result.Seq)
			}
			return nil
		}
		fmt.Fprintf(os.Stderr, "daemon RPC failed, falling back to direct NATS: %v\n", err)
	}

	// Fallback: direct NATS connection.
	nc, js, err := connectChatNATS()
	if err != nil {
		return err
	}
	defer nc.Close()

	registry := chat.NewRegistry()
	broker := chat.NewBroker(js, registry)

	msg := &eventbus.ChatMessagePayload{
		SessionTag: sessionTag,
		Sender:     sender,
		SenderID:   senderID,
		Content:    content,
		ChannelID:  channelID,
		ThreadTS:   threadTS,
	}

	ctx, cancel := context.WithTimeout(rootCtx, 10*time.Second)
	defer cancel()

	if err := broker.Send(ctx, msg, "in"); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	if jsonOutput {
		out, _ := json.Marshal(msg)
		fmt.Println(string(out))
	} else {
		fmt.Fprintf(os.Stderr, "Sent to %s (seq=%d)\n", sessionTag, msg.Seq)
	}
	return nil
}

// --- bd chat listen ---

var chatListenCmd = &cobra.Command{
	Use:   "listen <session-tag>",
	Short: "Listen for inbound chat messages (blocking)",
	Long: `Subscribe to inbound chat messages for an agent session and block until
a message arrives. Designed to run as a Claude Code background task.

When a Slack user sends a message to the agent's thread, this command
returns the message as JSON on stdout and exits with code 0.

Examples:
  bd chat listen sess-abc123                    # Wait for one message
  bd chat listen sess-abc123 --timeout 5m       # Wait up to 5 minutes
  bd chat listen sess-abc123 --drain            # Return all queued + next`,
	Args: cobra.ExactArgs(1),
	RunE: runChatListen,
}

func runChatListen(cmd *cobra.Command, args []string) error {
	sessionTag := args[0]
	timeoutStr, _ := cmd.Flags().GetString("timeout")
	drain, _ := cmd.Flags().GetBool("drain")

	timeout := 30 * time.Minute // Default: long timeout for background task use.
	if timeoutStr != "" {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return fmt.Errorf("invalid timeout %q: %w", timeoutStr, err)
		}
		timeout = d
	}

	// Note: chat listen intentionally uses direct NATS even when daemon is
	// available, because the RPC timeout cap (5m) is too short for the typical
	// 30m background task pattern. Direct NATS subscription is more appropriate
	// for long-running listeners.
	nc, js, err := connectChatNATS()
	if err != nil {
		return err
	}
	defer nc.Close()

	registry := chat.NewRegistry()
	broker := chat.NewBroker(js, registry)

	ctx, cancel := context.WithTimeout(rootCtx, timeout)
	defer cancel()

	// Handle SIGINT/SIGTERM for graceful exit.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	fmt.Fprintf(os.Stderr, "Listening for messages on session %s (timeout: %s)...\n", sessionTag, timeout)

	if drain {
		messages, err := broker.ListenAll(ctx, sessionTag)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "Timed out waiting for messages.")
				os.Exit(1)
			}
			return fmt.Errorf("listen: %w", err)
		}
		out, _ := json.Marshal(messages)
		fmt.Println(string(out))
	} else {
		msg, err := broker.Listen(ctx, sessionTag)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "Timed out waiting for messages.")
				os.Exit(1)
			}
			return fmt.Errorf("listen: %w", err)
		}
		out, _ := json.Marshal(msg)
		fmt.Println(string(out))
	}

	return nil
}

// --- bd chat reply ---

var chatReplyCmd = &cobra.Command{
	Use:   "reply <session-tag> <message>",
	Short: "Send a reply from an agent to Slack",
	Long: `Publish a chat message from an agent back to the Slack thread.
The Slack bot subscribes to outbound messages and posts them as thread replies.

Examples:
  bd chat reply sess-abc123 "Deployment is at 80%, ETA 5 minutes"
  bd chat reply my-agent "Done! PR merged and deployed."`,
	Args: cobra.ExactArgs(2),
	RunE: runChatReply,
}

func runChatReply(cmd *cobra.Command, args []string) error {
	sessionTag := args[0]
	content := args[1]
	agentName, _ := cmd.Flags().GetString("agent")

	if agentName == "" {
		agentName = os.Getenv("GT_AGENT_ID")
		if agentName == "" {
			agentName = "agent"
		}
	}

	// Prefer daemon RPC when available.
	if daemonClient != nil {
		result, err := daemonClient.ChatSend(&rpc.ChatSendArgs{
			SessionTag: sessionTag,
			Content:    content,
			Sender:     agentName,
			Direction:  "out",
		})
		if err == nil {
			if jsonOutput {
				out, _ := json.Marshal(result)
				fmt.Println(string(out))
			} else {
				fmt.Fprintf(os.Stderr, "Reply sent on %s via daemon (seq=%d)\n", sessionTag, result.Seq)
			}
			return nil
		}
		fmt.Fprintf(os.Stderr, "daemon RPC failed, falling back to direct NATS: %v\n", err)
	}

	// Fallback: direct NATS connection.
	nc, js, err := connectChatNATS()
	if err != nil {
		return err
	}
	defer nc.Close()

	registry := chat.NewRegistry()
	broker := chat.NewBroker(js, registry)

	msg := &eventbus.ChatMessagePayload{
		SessionTag: sessionTag,
		Sender:     agentName,
		Content:    content,
	}

	ctx, cancel := context.WithTimeout(rootCtx, 10*time.Second)
	defer cancel()

	if err := broker.Send(ctx, msg, "out"); err != nil {
		return fmt.Errorf("reply: %w", err)
	}

	if jsonOutput {
		out, _ := json.Marshal(msg)
		fmt.Println(string(out))
	} else {
		fmt.Fprintf(os.Stderr, "Reply sent on %s (seq=%d)\n", sessionTag, msg.Seq)
	}
	return nil
}

// --- bd chat status ---

var chatStatusCmd = &cobra.Command{
	Use:   "status [session-tag]",
	Short: "Show chat session status",
	Long: `Query the status of a chat session or list all active sessions.

Examples:
  bd chat status sess-abc123   # Show specific session
  bd chat status               # List all sessions (requires daemon)`,
	Args: cobra.MaximumNArgs(1),
	RunE: runChatStatus,
}

func runChatStatus(cmd *cobra.Command, args []string) error {
	if daemonClient == nil {
		return fmt.Errorf("chat status requires a running daemon (start with 'bd daemon start')")
	}

	var sessionTag string
	if len(args) > 0 {
		sessionTag = args[0]
	}

	result, err := daemonClient.ChatStatus(&rpc.ChatStatusArgs{
		SessionTag: sessionTag,
	})
	if err != nil {
		return err
	}

	if jsonOutput {
		out, _ := json.Marshal(result)
		fmt.Println(string(out))
		return nil
	}

	if len(result.Sessions) == 0 {
		if sessionTag != "" {
			fmt.Fprintf(os.Stderr, "No session found for %q\n", sessionTag)
		} else {
			fmt.Fprintln(os.Stderr, "No active chat sessions.")
		}
		return nil
	}

	for _, s := range result.Sessions {
		fmt.Printf("Session: %s\n", s.SessionTag)
		fmt.Printf("  Status:    %s\n", s.Status)
		if s.AgentName != "" {
			fmt.Printf("  Agent:     %s\n", s.AgentName)
		}
		if s.ChannelID != "" {
			fmt.Printf("  Channel:   %s\n", s.ChannelID)
		}
		if s.ThreadTS != "" {
			fmt.Printf("  Thread:    %s\n", s.ThreadTS)
		}
		if s.CreatedAt != "" {
			fmt.Printf("  Created:   %s\n", s.CreatedAt)
		}
		if s.UpdatedAt != "" {
			fmt.Printf("  Updated:   %s\n", s.UpdatedAt)
		}
		fmt.Println()
	}
	return nil
}

func init() {
	// send flags
	chatSendCmd.Flags().String("sender", "", "Sender display name (default: $USER)")
	chatSendCmd.Flags().String("sender-id", "", "Slack user ID of sender")
	chatSendCmd.Flags().String("channel", "", "Slack channel ID")
	chatSendCmd.Flags().String("thread", "", "Slack thread timestamp")

	// listen flags
	chatListenCmd.Flags().String("timeout", "", "Max wait duration (e.g., 5m, 1h). Default: 30m")
	chatListenCmd.Flags().Bool("drain", false, "Drain all queued messages before waiting for new ones")

	// reply flags
	chatReplyCmd.Flags().String("agent", "", "Agent name (default: $GT_AGENT_ID or 'agent')")

	chatCmd.AddCommand(chatSendCmd)
	chatCmd.AddCommand(chatListenCmd)
	chatCmd.AddCommand(chatReplyCmd)
	chatCmd.AddCommand(chatStatusCmd)
	rootCmd.AddCommand(chatCmd)
}
