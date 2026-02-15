package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/coop"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

var attachCoopPort int
var attachLocalPort int
var attachDirectURL string

var agentAttachCmd = &cobra.Command{
	Use:   "attach <agent>",
	Short: "Attach to a running agent's terminal via Coop WebSocket",
	Long: `Attach to a running agent's interactive terminal session.

Connects to the Coop sidecar running alongside the agent pod and streams
terminal I/O over WebSocket. Resolves the Coop URL in priority order:
  1. --url flag (direct override)
  2. coop_url from agent bead notes (ingress-routable, set by controller)
  3. Pod IP direct connection (same cluster network)
  4. kubectl port-forward (last resort fallback)

Detach with Ctrl+] (sends no signal to the agent).

Examples:
  bd agent attach gt-gastown-polecat-nux    # Auto-resolve via bead notes or pod info
  bd agent attach gt-mayor                  # Attach to mayor pod
  bd agent attach gt-emma --url http://localhost:3000  # Direct URL (skip pod lookup)`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentAttach,
}

func init() {
	agentAttachCmd.Flags().IntVar(&attachCoopPort, "coop-port", 3000, "Coop sidecar port on the pod")
	agentAttachCmd.Flags().IntVar(&attachLocalPort, "local-port", 0, "Local port for kubectl port-forward (0 = auto)")
	agentAttachCmd.Flags().StringVar(&attachDirectURL, "url", "", "Direct Coop URL (skip pod lookup and port-forward)")
	agentCmd.AddCommand(agentAttachCmd)
}

func runAgentAttach(cmd *cobra.Command, args []string) error {
	agentArg := args[0]
	ctx := rootCtx

	var coopURL string

	if attachDirectURL != "" {
		// Priority 1: Direct URL mode — skip pod lookup
		coopURL = attachDirectURL
	} else {
		// Look up agent pod info
		podInfo, err := resolveAgentPodInfo(ctx, agentArg)
		if err != nil {
			return err
		}

		if podInfo.PodName == "" {
			return fmt.Errorf("agent %s has no registered pod (use bd agent pod-list to check)", podInfo.AgentID)
		}
		if podInfo.PodStatus != "running" {
			return fmt.Errorf("agent %s pod is %s (must be running)", podInfo.AgentID, podInfo.PodStatus)
		}

		// Priority 2: coop_url from bead notes (ingress-routable, set by controller)
		if url := resolveCoopURLFromNotes(podInfo.AgentID); url != "" {
			fmt.Fprintf(os.Stderr, "Using coop_url from bead notes\n")
			coopURL = url
		} else if podInfo.PodIP != "" && isReachable(podInfo.PodIP, attachCoopPort) {
			// Priority 3: Pod IP direct connection (same cluster network)
			coopURL = fmt.Sprintf("http://%s:%d", podInfo.PodIP, attachCoopPort)
		} else {
			// Priority 4: kubectl port-forward (last resort fallback)
			localPort := attachLocalPort
			if localPort == 0 {
				localPort, err = findFreePort()
				if err != nil {
					return fmt.Errorf("finding free port: %w", err)
				}
			}

			pfCtx, pfCancel := context.WithCancel(ctx)
			defer pfCancel()

			namespace := podInfo.Rig
			if namespace == "" {
				namespace = "default"
			}

			fmt.Fprintf(os.Stderr, "Falling back to kubectl port-forward to %s (port %d → %d)...\n",
				podInfo.PodName, localPort, attachCoopPort)

			pfCmd := exec.CommandContext(pfCtx, "kubectl", "port-forward",
				"--namespace", namespace,
				"pod/"+podInfo.PodName,
				fmt.Sprintf("%d:%d", localPort, attachCoopPort),
			)
			pfCmd.Stderr = os.Stderr

			if err := pfCmd.Start(); err != nil {
				return fmt.Errorf("kubectl port-forward: %w", err)
			}
			defer func() {
				pfCancel()
				pfCmd.Wait()
			}()

			// Wait for port-forward to be ready
			if err := waitForPort(ctx, localPort, 10*time.Second); err != nil {
				return fmt.Errorf("port-forward not ready: %w", err)
			}

			coopURL = fmt.Sprintf("http://localhost:%d", localPort)
		}

		fmt.Fprintf(os.Stderr, "Connecting to %s at %s...\n", podInfo.AgentID, coopURL)
	}

	// Verify Coop sidecar is healthy before attaching
	client := coop.NewClient(coopURL)
	health, err := client.Health(ctx)
	if err != nil {
		return fmt.Errorf("coop sidecar unreachable at %s: %w", coopURL, err)
	}

	fmt.Fprintf(os.Stderr, "Connected (agent=%s, uptime=%ds, terminal=%dx%d)\n",
		health.AgentType, health.UptimeSec, health.Terminal.Cols, health.Terminal.Rows)
	fmt.Fprintf(os.Stderr, "Detach: Ctrl+]\n\n")

	// Attach
	opts := coop.DefaultAttachOptions()
	return client.Attach(ctx, opts)
}

// resolveAgentPodInfo looks up an agent's pod information via daemon RPC.
func resolveAgentPodInfo(ctx context.Context, agentArg string) (*rpc.AgentPodInfo, error) {
	// Try pod-list first for full pod info
	result, err := daemonClient.AgentPodList(&rpc.AgentPodListArgs{})
	if err != nil {
		return nil, fmt.Errorf("failed to list agent pods: %w", err)
	}
	for _, a := range result.Agents {
		if a.AgentID == agentArg {
			return &a, nil
		}
	}

	// Try resolving the ID (might be a prefix)
	resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: agentArg})
	if err != nil {
		return nil, fmt.Errorf("agent not found: %s", agentArg)
	}
	var resolvedID string
	if err := json.Unmarshal(resp.Data, &resolvedID); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	// Check again with resolved ID
	for _, a := range result.Agents {
		if a.AgentID == resolvedID {
			return &a, nil
		}
	}

	// Agent exists but has no pod
	return &rpc.AgentPodInfo{AgentID: resolvedID}, nil
}

// findFreePort returns an available port on localhost.
func findFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// waitForPort waits for a TCP port to become available.
func waitForPort(ctx context.Context, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("port %d not ready after %v", port, timeout)
}

// resolveCoopURLFromNotes looks up an agent bead's notes for a coop_url field.
// Returns empty string if not found or on any error.
func resolveCoopURLFromNotes(agentID string) string {
	resp, err := daemonClient.Show(&rpc.ShowArgs{ID: agentID})
	if err != nil {
		return ""
	}
	var issues []types.Issue
	if err := json.Unmarshal(resp.Data, &issues); err != nil || len(issues) == 0 {
		return ""
	}
	for _, line := range strings.Split(issues[0].Notes, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "coop_url" {
			url := strings.TrimSpace(parts[1])
			if url != "" {
				return url
			}
		}
	}
	return ""
}

// isReachable checks if a host:port is directly reachable with a short timeout.
func isReachable(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
