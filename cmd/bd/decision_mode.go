package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// decisionModeCmd toggles decision checkpoint enforcement on or off.
var decisionModeCmd = &cobra.Command{
	Use:   "mode [enable|disable]",
	Short: "Toggle decision checkpoint enforcement",
	Long: `Toggle whether the stop hook enforces decision checkpoints.

When enabled (default), agents are paused at checkpoints and must present
a decision menu before stopping. When disabled, the stop hook allows
through without blocking — useful when a human is interacting directly.

Use --agent to set a per-agent override (takes precedence over global).
Without --agent, sets the global default for all agents.
Without arguments, shows the effective mode.

Examples:
  bd decision mode                          # Show effective mode
  bd decision mode enable                   # Enable globally
  bd decision mode disable                  # Disable globally
  bd decision mode disable --agent=bright-lark  # Disable for one agent
  bd decision mode enable --agent=bright-lark   # Re-enable for one agent
  bd decision mode clear --agent=bright-lark    # Remove per-agent override`,
	Args: cobra.MaximumNArgs(1),
	Run:  runDecisionMode,
}

func init() {
	decisionModeCmd.Flags().String("agent", "", "Set mode for a specific agent (overrides global)")
	decisionCmd.AddCommand(decisionModeCmd)
}

func runDecisionMode(cmd *cobra.Command, args []string) {
	requireDaemon("decision mode")

	agentFlag, _ := cmd.Flags().GetString("agent")

	// Load existing config bead metadata.
	metadata, err := loadStopDecisionConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// No args — show current mode.
	if len(args) == 0 {
		showDecisionMode(metadata, agentFlag)
		return
	}

	// Parse action argument.
	action := args[0]

	if agentFlag != "" {
		// Per-agent mode.
		switch action {
		case "enable":
			setAgentOverride(metadata, agentFlag, true)
		case "disable":
			setAgentOverride(metadata, agentFlag, false)
		case "clear":
			clearAgentOverride(metadata, agentFlag)
		default:
			fmt.Fprintf(os.Stderr, "Error: argument must be 'enable', 'disable', or 'clear', got %q\n", action)
			os.Exit(1)
		}
	} else {
		// Global mode.
		switch action {
		case "enable":
			sd := getStopDecisionMap(metadata)
			sd["enabled"] = true
		case "disable":
			sd := getStopDecisionMap(metadata)
			sd["enabled"] = false
		case "clear":
			fmt.Fprintf(os.Stderr, "Error: 'clear' only works with --agent (global mode uses enable/disable)\n")
			os.Exit(1)
		default:
			fmt.Fprintf(os.Stderr, "Error: argument must be 'enable' or 'disable', got %q\n", action)
			os.Exit(1)
		}
	}

	// Write back.
	if err := writeStopDecisionConfig(metadata); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Report result.
	effective := resolveDecisionEnabled(metadata, agentFlag)
	scope := "global"
	if agentFlag != "" {
		scope = agentFlag
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"enabled": effective,
			"mode":    modeLabel(effective),
			"scope":   scope,
			"updated": true,
		})
		return
	}

	label := fmt.Sprintf("Decision checkpoints (%s)", scope)
	if effective {
		fmt.Printf("%s %s %s\n", ui.RenderPass("✓"), label, ui.RenderAccent("enabled"))
	} else {
		fmt.Printf("%s %s %s\n", ui.RenderPass("✓"), label, ui.RenderWarn("disabled"))
	}
}

func showDecisionMode(metadata map[string]interface{}, agentName string) {
	globalEnabled := getStopDecisionEnabled(metadata)
	overrides := getAgentOverrides(metadata)

	if agentName != "" {
		// Show effective mode for a specific agent.
		effective := resolveDecisionEnabled(metadata, agentName)
		_, hasOverride := overrides[agentName]

		if jsonOutput {
			result := map[string]interface{}{
				"enabled":        effective,
				"mode":           modeLabel(effective),
				"scope":          agentName,
				"global_enabled": globalEnabled,
				"has_override":   hasOverride,
			}
			outputJSON(result)
			return
		}

		if effective {
			fmt.Printf("%s Decision checkpoints (%s): %s", ui.RenderPass("●"), agentName, ui.RenderAccent("enabled"))
		} else {
			fmt.Printf("%s Decision checkpoints (%s): %s", ui.RenderWarn("○"), agentName, ui.RenderWarn("disabled"))
		}
		if hasOverride {
			fmt.Print(" (agent override)")
		} else {
			fmt.Print(" (from global)")
		}
		fmt.Println()
		return
	}

	// Show global mode + any overrides.
	if jsonOutput {
		result := map[string]interface{}{
			"enabled":    globalEnabled,
			"mode":       modeLabel(globalEnabled),
			"scope":      "global",
			"overrides":  overrides,
		}
		outputJSON(result)
		return
	}

	if globalEnabled {
		fmt.Printf("%s Decision checkpoints (global): %s\n", ui.RenderPass("●"), ui.RenderAccent("enabled"))
	} else {
		fmt.Printf("%s Decision checkpoints (global): %s\n", ui.RenderWarn("○"), ui.RenderWarn("disabled"))
	}

	if len(overrides) > 0 {
		fmt.Println("\n  Agent overrides:")
		for agent, enabled := range overrides {
			if enabled {
				fmt.Printf("    %s %s: %s\n", ui.RenderPass("●"), agent, ui.RenderAccent("enabled"))
			} else {
				fmt.Printf("    %s %s: %s\n", ui.RenderWarn("○"), agent, ui.RenderWarn("disabled"))
			}
		}
	}
}

// resolveDecisionEnabled returns the effective enabled state for an agent.
// Per-agent override takes precedence over global.
func resolveDecisionEnabled(metadata map[string]interface{}, agentName string) bool {
	if agentName != "" {
		overrides := getAgentOverrides(metadata)
		if enabled, ok := overrides[agentName]; ok {
			return enabled
		}
	}
	return getStopDecisionEnabled(metadata)
}

func setAgentOverride(metadata map[string]interface{}, agentName string, enabled bool) {
	sd := getStopDecisionMap(metadata)
	overrides, _ := sd["agent_overrides"].(map[string]interface{})
	if overrides == nil {
		overrides = map[string]interface{}{}
	}
	overrides[agentName] = enabled
	sd["agent_overrides"] = overrides
}

func clearAgentOverride(metadata map[string]interface{}, agentName string) {
	sd := getStopDecisionMap(metadata)
	overrides, _ := sd["agent_overrides"].(map[string]interface{})
	if overrides == nil {
		return
	}
	delete(overrides, agentName)
	if len(overrides) == 0 {
		delete(sd, "agent_overrides")
	} else {
		sd["agent_overrides"] = overrides
	}
}

func writeStopDecisionConfig(metadata map[string]interface{}) error {
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}
	metaRaw := json.RawMessage(metaBytes)

	updateArgs := &rpc.UpdateArgs{
		ID:       "hq-cfg-claude-hooks-global",
		Metadata: &metaRaw,
	}
	_, err = daemonClient.Update(updateArgs)
	if err != nil {
		return fmt.Errorf("updating config bead: %w", err)
	}
	return nil
}

// loadStopDecisionConfig loads the metadata from the claude-hooks config bead.
func loadStopDecisionConfig() (map[string]interface{}, error) {
	listArgs := &rpc.ListArgs{
		IssueType: "config",
		Labels:    []string{"config:claude-hooks"},
		Status:    "open",
		Limit:     10,
	}
	resp, err := daemonClient.List(listArgs)
	if err != nil {
		return nil, fmt.Errorf("listing config beads: %w", err)
	}

	var issues []*types.IssueWithCounts
	if resp.Data == nil {
		return map[string]interface{}{}, nil
	}
	if err := json.Unmarshal(resp.Data, &issues); err != nil {
		return nil, fmt.Errorf("parsing config beads: %w", err)
	}

	// Find the global config bead.
	for _, iwc := range issues {
		if iwc.Issue.ID == "hq-cfg-claude-hooks-global" && iwc.Issue.Metadata != nil {
			var data map[string]interface{}
			if err := json.Unmarshal(iwc.Issue.Metadata, &data); err != nil {
				return nil, fmt.Errorf("parsing metadata: %w", err)
			}
			return data, nil
		}
	}

	return map[string]interface{}{}, nil
}

// getStopDecisionEnabled extracts the global enabled flag from config metadata.
func getStopDecisionEnabled(metadata map[string]interface{}) bool {
	sd := getStopDecisionMap(metadata)
	if enabled, ok := sd["enabled"].(bool); ok {
		return enabled
	}
	return true // default: enabled
}

// getAgentOverrides extracts the agent_overrides map from config metadata.
func getAgentOverrides(metadata map[string]interface{}) map[string]bool {
	sd := getStopDecisionMap(metadata)
	raw, _ := sd["agent_overrides"].(map[string]interface{})
	result := make(map[string]bool, len(raw))
	for k, v := range raw {
		if b, ok := v.(bool); ok {
			result[k] = b
		}
	}
	return result
}

// getStopDecisionMap extracts the stop_decision sub-map, creating it if absent.
func getStopDecisionMap(metadata map[string]interface{}) map[string]interface{} {
	if sd, ok := metadata["stop_decision"].(map[string]interface{}); ok {
		return sd
	}
	sd := map[string]interface{}{}
	metadata["stop_decision"] = sd
	return sd
}

func modeLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
