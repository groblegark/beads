package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// pluralRole returns the plural form of a role name.
func pluralRole(role string) string {
	switch {
	case strings.HasSuffix(role, "ss"):
		return role + "es" // witness → witnesses
	case strings.HasSuffix(role, "y"):
		return role[:len(role)-1] + "ies" // refinery → refineries
	case strings.HasSuffix(role, "s"):
		return role + "es"
	default:
		return role + "s"
	}
}

// roleAgentInfo holds filtered agent info for display.
type roleAgentInfo struct {
	ID         string     `json:"id"`
	Rig        string     `json:"rig"`
	State      string     `json:"agent_state"`
	PodName    string     `json:"pod_name,omitempty"`
	PodStatus  string     `json:"pod_status,omitempty"`
	Assignee   string     `json:"assignee,omitempty"`
	LastActive *time.Time `json:"last_activity,omitempty"`
	Status     string     `json:"status"` // issue status (open/closed)
}

// buildRoleAgentCommands creates list/stop/start subcommands for a given role type.
// roleType is "refinery" or "witness".
func buildRoleAgentCommands(roleType string) (*cobra.Command, func()) {
	var listRig string
	var listAll bool
	var stopForce bool
	var stopReason string
	var startReason string

	parentCmd := &cobra.Command{
		Use:   roleType,
		Short: fmt.Sprintf("Manage %s agents across rigs", roleType),
		Long: fmt.Sprintf(`List, stop, and start %s agents across all rigs.

%s agents are identified by the role_type:%s label on agent beads.
Stopping a %s sets agent_state=stopping, which triggers the K8s controller
to delete the pod. Starting (restarting) sets agent_state=spawning, which
triggers the controller to create a new pod.

Examples:
  bd %s list                    # List all %s across rigs
  bd %s list --rig gastown      # List only gastown %s
  bd %s stop gastown            # Stop the gastown %s
  bd %s start gastown           # Start (respawn) the gastown %s`,
			roleType,
			strings.ToUpper(roleType[:1])+roleType[1:], roleType,
			roleType,
			roleType, pluralRole(roleType),
			roleType, roleType,
			roleType, roleType,
			roleType, roleType),
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: fmt.Sprintf("List %s agents across rigs", roleType),
		Long: fmt.Sprintf(`List all %s agents, showing their rig, state, pod status, and last activity.

By default only shows open (active) agents. Use --all to include closed ones.

Examples:
  bd %s list                    # All active %s
  bd %s list --rig gastown      # Only gastown rig
  bd %s list --all              # Include stopped/closed
  bd %s list --json             # JSON output`, roleType,
			roleType, pluralRole(roleType),
			roleType,
			roleType,
			roleType),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoleList(roleType, listRig, listAll)
		},
	}

	stopCmd := &cobra.Command{
		Use:   "stop <rig>",
		Short: fmt.Sprintf("Stop a %s agent", roleType),
		Long: fmt.Sprintf(`Gracefully stop a %s by setting agent_state=stopping.

The K8s controller watches for the stopping state and deletes the pod.
A SIGTERM is also sent to the coop sidecar if available.

The <rig> argument is the rig name (e.g., "gastown", "beads"). The full
agent ID is resolved automatically (e.g., gt-gastown-%s).

Examples:
  bd %s stop gastown             # Stop gastown %s
  bd %s stop gastown --force     # Skip coop signal`, roleType,
			roleType,
			roleType, roleType,
			roleType),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoleStop(roleType, args[0], stopForce, stopReason)
		},
	}

	startCmd := &cobra.Command{
		Use:   "start <rig>",
		Short: fmt.Sprintf("Start (respawn) a %s agent", roleType),
		Long: fmt.Sprintf(`Start or restart a %s by setting agent_state=spawning.

The K8s controller watches for the spawning state and creates a new pod.
If a pod already exists, it is deleted first. Pod metadata is cleared.

The <rig> argument is the rig name (e.g., "gastown", "beads"). The full
agent ID is resolved automatically (e.g., gt-gastown-%s).

Examples:
  bd %s start gastown            # Start/restart gastown %s`, roleType,
			roleType,
			roleType, roleType),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoleStart(roleType, args[0], startReason)
		},
	}

	// Return a setup function that registers flags and subcommands.
	// This is called from init() in the respective command file.
	setup := func() {
		listCmd.Flags().StringVar(&listRig, "rig", "", "Filter by rig name")
		listCmd.Flags().BoolVar(&listAll, "all", false, "Include closed/stopped agents")

		stopCmd.Flags().BoolVar(&stopForce, "force", false, "Skip coop signal, only set state")
		stopCmd.Flags().StringVar(&stopReason, "reason", "", "Reason for stopping")

		startCmd.Flags().StringVar(&startReason, "reason", "", "Reason for starting")

		parentCmd.AddCommand(listCmd)
		parentCmd.AddCommand(stopCmd)
		parentCmd.AddCommand(startCmd)
		rootCmd.AddCommand(parentCmd)
	}

	return parentCmd, setup
}

// runRoleList lists agents with the given role_type.
func runRoleList(roleType, rigFilter string, includeAll bool) error {
	if daemonClient == nil {
		return fmt.Errorf("%s list requires the daemon (set BD_DAEMON_HOST)", roleType)
	}

	// Query agent beads by type + role label. Using IssueType avoids a label AND
	// query bug where "gt:agent"+"role:X" returns empty for closed beads.
	rolePrefix := "role:"
	labels := []string{rolePrefix + roleType}
	status := "open"
	if includeAll {
		status = ""
	}

	resp, err := daemonClient.List(&rpc.ListArgs{
		IssueType: "agent",
		Labels:    labels,
		Status:    status,
	})
	if err != nil {
		return fmt.Errorf("failed to list %s agents: %w", roleType, err)
	}

	var issues []*types.Issue
	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	// Filter by rig if specified.
	var agents []roleAgentInfo
	for _, issue := range issues {
		if rigFilter != "" && issue.Rig != rigFilter {
			continue
		}
		agents = append(agents, roleAgentInfo{
			ID:         issue.ID,
			Rig:        issue.Rig,
			State:      string(issue.AgentState),
			PodName:    issue.PodName,
			PodStatus:  issue.PodStatus,
			Assignee:   issue.Assignee,
			LastActive: issue.LastActivity,
			Status:     string(issue.Status),
		})
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(agents)
	}

	if len(agents) == 0 {
		if rigFilter != "" {
			fmt.Printf("No %s agents found for rig %q\n", roleType, rigFilter)
		} else {
			fmt.Printf("No %s agents found\n", roleType)
		}
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tRIG\tSTATE\tPOD\tLAST ACTIVE\n")
	for _, a := range agents {
		state := a.State
		if state == "" {
			state = "-"
		}
		pod := a.PodStatus
		if pod == "" {
			pod = "-"
		}
		lastActive := "-"
		if a.LastActive != nil {
			lastActive = friendlyDuration(time.Since(*a.LastActive))
		}

		// Color-code state.
		switch state {
		case "running", "working", "idle":
			state = ui.RenderPass(state)
		case "stopping", "stopped", "dead":
			state = ui.RenderFail(state)
		case "spawning":
			state = ui.RenderWarn(state)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			a.ID, a.Rig, state, pod, lastActive)
	}
	w.Flush()
	return nil
}

// runRoleStop stops an agent of the given role_type for a rig.
func runRoleStop(roleType, rigArg string, force bool, reason string) error {
	CheckReadonly(roleType + " stop")

	if daemonClient == nil {
		return fmt.Errorf("%s stop requires the daemon (set BD_DAEMON_HOST)", roleType)
	}

	// Resolve: try "gt-<rig>-<roleType>" pattern first via ResolveID.
	agentID, err := resolveRoleAgent(roleType, rigArg)
	if err != nil {
		return err
	}

	result, err := daemonClient.AgentStop(&rpc.AgentStopArgs{
		AgentID: agentID,
		Reason:  reason,
		Force:   force,
	})
	if err != nil {
		return fmt.Errorf("failed to stop %s: %w", roleType, err)
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	coopStatus := "no"
	if result.CoopSignal {
		coopStatus = "yes"
	}
	fmt.Printf("%s %s state=%s coop_signal=%s\n",
		ui.RenderPass("✓"), result.AgentID, result.AgentState, coopStatus)
	return nil
}

// runRoleStart starts (respawns) an agent of the given role_type for a rig.
// If the bead is closed it is reopened first so the controller will reconcile a pod.
func runRoleStart(roleType, rigArg string, reason string) error {
	CheckReadonly(roleType + " start")

	if daemonClient == nil {
		return fmt.Errorf("%s start requires the daemon (set BD_DAEMON_HOST)", roleType)
	}

	// Resolve across all statuses — the bead may be closed.
	agentID, err := resolveRoleAgentAnyStatus(roleType, rigArg)
	if err != nil {
		return err
	}

	// If the bead is closed, reopen it first — the controller only reconciles open beads.
	showResp, err := daemonClient.Show(&rpc.ShowArgs{ID: agentID})
	if err == nil {
		var issue types.Issue
		if json.Unmarshal(showResp.Data, &issue) == nil && issue.Status == types.StatusClosed {
			openStatus := string(types.StatusOpen)
			if _, err := daemonClient.UpdateWithComment(&rpc.UpdateWithCommentArgs{
				UpdateArgs: rpc.UpdateArgs{
					ID:     agentID,
					Status: &openStatus,
				},
				CommentText:   reason,
				CommentAuthor: actor,
			}); err != nil {
				return fmt.Errorf("failed to reopen %s bead %s: %w", roleType, agentID, err)
			}
		}
	}

	result, err := daemonClient.AgentRestart(&rpc.AgentRestartArgs{
		AgentID: agentID,
		Reason:  reason,
	})
	if err != nil {
		return fmt.Errorf("failed to start %s: %w", roleType, err)
	}

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s %s state=%s\n", ui.RenderPass("✓"), result.AgentID, result.AgentState)
	return nil
}

// resolveRoleAgent resolves a rig name to a full agent ID for the given role.
// Tries the canonical pattern "gt-<rig>-<role>" first, falls back to label search.
func resolveRoleAgent(roleType, rigArg string) (string, error) {
	// Try canonical ID pattern: gt-<rig>-<role>
	candidateID := fmt.Sprintf("gt-%s-%s", rigArg, roleType)
	resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: candidateID})
	if err == nil {
		var agentID string
		if err := json.Unmarshal(resp.Data, &agentID); err == nil {
			return agentID, nil
		}
	}

	// Fallback: search open beads by type + role label to find the agent for this rig+role.
	// (Avoid "gt:agent"+"role:X" AND query which has a bug with closed beads.)
	roleLabel := strings.Join([]string{"role", roleType}, ":")
	listResp, err := daemonClient.List(&rpc.ListArgs{
		IssueType: "agent",
		Labels:    []string{roleLabel},
		Status:    "open",
	})
	if err != nil {
		return "", fmt.Errorf("failed to find %s agent for rig %q: %w", roleType, rigArg, err)
	}

	var issues []*types.Issue
	if err := json.Unmarshal(listResp.Data, &issues); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}

	for _, issue := range issues {
		if issue.Rig == rigArg {
			return issue.ID, nil
		}
	}

	return "", fmt.Errorf("no %s agent found for rig %q", roleType, rigArg)
}

// resolveRoleAgentAnyStatus is like resolveRoleAgent but searches all statuses
// (including closed). Used by start, which may need to reopen a stopped agent.
func resolveRoleAgentAnyStatus(roleType, rigArg string) (string, error) {
	// Try canonical ID pattern first.
	candidateID := fmt.Sprintf("gt-%s-%s", rigArg, roleType)
	resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: candidateID})
	if err == nil {
		var agentID string
		if err := json.Unmarshal(resp.Data, &agentID); err == nil {
			return agentID, nil
		}
	}

	// Fallback: search all statuses using type + role label.
	roleLabel := strings.Join([]string{"role", roleType}, ":")
	listResp, err := daemonClient.List(&rpc.ListArgs{
		IssueType: "agent",
		Labels:    []string{roleLabel},
	})
	if err != nil {
		return "", fmt.Errorf("failed to find %s agent for rig %q: %w", roleType, rigArg, err)
	}

	var issues []*types.Issue
	if err := json.Unmarshal(listResp.Data, &issues); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}

	for _, issue := range issues {
		if issue.Rig == rigArg {
			return issue.ID, nil
		}
	}

	return "", fmt.Errorf("no %s agent found for rig %q", roleType, rigArg)
}

// friendlyDuration formats a duration as a human-readable "Xm ago" or "Xh ago" string.
func friendlyDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
