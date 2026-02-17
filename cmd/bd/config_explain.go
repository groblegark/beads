package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// categoryDoc describes a config bead category for the explain command.
type categoryDoc struct {
	Name        string
	Description string
	Fields      []fieldDoc
	Example     string
}

// fieldDoc describes a single field within a config bead category.
type fieldDoc struct {
	Key         string
	Type        string
	Description string
}

// configExplainCmd prints documentation for a config bead category.
var configExplainCmd = &cobra.Command{
	Use:   "explain [category]",
	Short: "Show documentation for a config bead category",
	Long: `Print human/agent-readable documentation for a config bead category.

Shows what each field does, its type, and how to update it. Works offline
(no daemon connection needed) — schema is embedded in the binary.

Without arguments, lists all known categories.

Examples:
  bd config explain                  # List all categories
  bd config explain claude-hooks     # Show claude-hooks fields
  bd config explain mcp              # Show MCP config fields`,
	Args: cobra.MaximumNArgs(1),
	Run:  runConfigExplain,
}

func init() {
	configCmd.AddCommand(configExplainCmd)
}

func runConfigExplain(cmd *cobra.Command, args []string) {
	if len(args) == 0 {
		listCategories()
		return
	}

	cat := strings.ToLower(args[0])
	doc, ok := categoryDocs[cat]
	if !ok {
		fmt.Fprintf(os.Stderr, "Unknown category: %s\n\nKnown categories:\n", cat)
		listCategories()
		os.Exit(1)
	}

	printCategoryDoc(doc)
}

func listCategories() {
	keys := make([]string, 0, len(categoryDocs))
	for k := range categoryDocs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Println("Config bead categories:")
	fmt.Println()
	for _, k := range keys {
		fmt.Printf("  %-20s %s\n", k, categoryDocs[k].Description)
	}
	fmt.Println()
	fmt.Println("Usage: bd config explain <category>")
	fmt.Println()
	fmt.Println("To see existing config beads: bd config list-beads")
	fmt.Println("To view a config bead:        bd config show-bead <id>")
}

func printCategoryDoc(doc categoryDoc) {
	fmt.Printf("# %s\n\n", doc.Name)
	fmt.Printf("%s\n\n", doc.Description)

	if len(doc.Fields) > 0 {
		fmt.Println("## Fields")
		fmt.Println()
		for _, f := range doc.Fields {
			fmt.Printf("  %-40s (%s)\n", f.Key, f.Type)
			fmt.Printf("    %s\n\n", f.Description)
		}
	}

	if doc.Example != "" {
		fmt.Println("## Example")
		fmt.Println()
		fmt.Println(doc.Example)
		fmt.Println()
	}

	fmt.Println("## Commands")
	fmt.Println()
	fmt.Printf("  List beads:   bd config list-beads --category %s\n", doc.Name)
	fmt.Printf("  View merged:  bd config show-bead %s --merged\n", doc.Name)
	fmt.Printf("  Update:       bd config set-bead --category %s --scope global --data '{...}'\n", doc.Name)
}

// categoryDocs contains embedded documentation for all config bead categories.
var categoryDocs = map[string]categoryDoc{
	"claude-hooks": {
		Name:        "claude-hooks",
		Description: "Claude Code hook settings (stop prompts, session behavior)",
		Fields: []fieldDoc{
			{Key: "stop_decision.enabled", Type: "bool", Description: "Whether the stop hook fires when Claude tries to stop."},
			{Key: "stop_decision.require_agent_decision", Type: "bool", Description: "If true, agent must create a decision point before stopping."},
			{Key: "stop_decision.agent_decision_prompt", Type: "string", Description: "Prompt shown to the agent when the stop hook fires."},
			{Key: "stop_decision.agent_context_prompt", Type: "string", Description: "Additional context injected into the stop prompt."},
			{Key: "stop_decision.agent_close_old_prompt", Type: "string", Description: "Prompt for closing stale in-progress issues."},
			{Key: "stop_decision.timeout", Type: "duration", Description: "How long to wait for a decision response (e.g. '5m')."},
			{Key: "stop_decision.poll_interval", Type: "duration", Description: "How often to poll for a decision response."},
			{Key: "hooks", Type: "object", Description: "Hook definitions merged by specificity (arrays APPEND, scalars OVERRIDE)."},
		},
		Example: `  {
    "stop_decision": {
      "enabled": true,
      "require_agent_decision": true,
      "agent_decision_prompt": "Present wrap-up options..."
    }
  }`,
	},
	"mcp": {
		Name:        "mcp",
		Description: "MCP (Model Context Protocol) server configuration",
		Fields: []fieldDoc{
			{Key: "mcpServers", Type: "object", Description: "Map of MCP server names to their configuration."},
			{Key: "mcpServers.<name>.command", Type: "string", Description: "Command to start the MCP server."},
			{Key: "mcpServers.<name>.args", Type: "[]string", Description: "Arguments passed to the MCP server command."},
			{Key: "mcpServers.<name>.env", Type: "object", Description: "Environment variables for the MCP server process."},
		},
		Example: `  {
    "mcpServers": {
      "beads": {
        "command": "bd",
        "args": ["mcp", "serve"],
        "env": {"BD_DAEMON_HOST": "http://localhost:9876"}
      }
    }
  }`,
	},
	"identity": {
		Name:        "identity",
		Description: "Town identity and naming configuration",
		Fields: []fieldDoc{
			{Key: "town_name", Type: "string", Description: "The canonical name of this Gas Town instance."},
			{Key: "display_name", Type: "string", Description: "Human-readable display name."},
		},
	},
	"rig-registry": {
		Name:        "rig-registry",
		Description: "Rig registration and discovery",
		Fields: []fieldDoc{
			{Key: "rigs", Type: "[]object", Description: "Array of registered rigs with name, type, and connection info."},
			{Key: "rigs[].name", Type: "string", Description: "Rig name (e.g. 'gastown', 'polecats')."},
			{Key: "rigs[].type", Type: "string", Description: "Rig type (e.g. 'local', 'k8s')."},
			{Key: "rigs[].url", Type: "string", Description: "Connection URL for the rig."},
		},
	},
	"agent-preset": {
		Name:        "agent-preset",
		Description: "Agent presets defining default config for agent types",
		Fields: []fieldDoc{
			{Key: "agents", Type: "object", Description: "Map of agent name to preset configuration."},
			{Key: "agents.<name>.model", Type: "string", Description: "Default AI model for this agent."},
			{Key: "agents.<name>.max_tokens", Type: "int", Description: "Max token budget per turn."},
			{Key: "agents.<name>.tools", Type: "[]string", Description: "Allowed tool names."},
		},
	},
	"role-definition": {
		Name:        "role-definition",
		Description: "Role definitions (polecat, crew, mayor, deacon, witness)",
		Fields: []fieldDoc{
			{Key: "role", Type: "string", Description: "Role name (polecat, crew, mayor, deacon, witness)."},
			{Key: "description", Type: "string", Description: "Human-readable description of the role."},
			{Key: "capabilities", Type: "[]string", Description: "List of capabilities this role has."},
			{Key: "constraints", Type: "object", Description: "Resource constraints (cpu, memory, timeout)."},
		},
	},
	"slack-routing": {
		Name:        "slack-routing",
		Description: "Slack notification routing rules",
		Fields: []fieldDoc{
			{Key: "default_channel", Type: "string", Description: "Default Slack channel for notifications."},
			{Key: "routes", Type: "[]object", Description: "Routing rules mapping events to channels."},
			{Key: "routes[].event", Type: "string", Description: "Event type to match (e.g. 'decision', 'error')."},
			{Key: "routes[].channel", Type: "string", Description: "Target Slack channel."},
		},
	},
	"accounts": {
		Name:        "accounts",
		Description: "Service account configuration for external integrations",
		Fields: []fieldDoc{
			{Key: "accounts", Type: "[]object", Description: "Array of account configurations."},
			{Key: "accounts[].name", Type: "string", Description: "Account identifier."},
			{Key: "accounts[].provider", Type: "string", Description: "Auth provider (e.g. 'anthropic', 'openai')."},
			{Key: "accounts[].type", Type: "string", Description: "Account type (e.g. 'oauth', 'api-key')."},
		},
	},
	"daemon": {
		Name:        "daemon",
		Description: "Daemon operational settings (patrol, health checks)",
		Fields: []fieldDoc{
			{Key: "patrol.enabled", Type: "bool", Description: "Whether daemon patrol (health monitoring) is active."},
			{Key: "patrol.interval", Type: "duration", Description: "How often to run patrol checks."},
			{Key: "health_check.timeout", Type: "duration", Description: "Timeout for individual health checks."},
		},
	},
	"messaging": {
		Name:        "messaging",
		Description: "Inter-agent messaging and notification settings",
		Fields: []fieldDoc{
			{Key: "nats.url", Type: "string", Description: "NATS server URL for pub/sub messaging."},
			{Key: "nats.subject_prefix", Type: "string", Description: "Subject prefix for NATS messages."},
			{Key: "inbox.poll_interval", Type: "duration", Description: "How often agents check their inbox."},
		},
	},
	"escalation": {
		Name:        "escalation",
		Description: "Escalation policies for unhandled events",
		Fields: []fieldDoc{
			{Key: "default_handler", Type: "string", Description: "Default handler for unescalated events (e.g. 'slack', 'log')."},
			{Key: "policies", Type: "[]object", Description: "Escalation policy rules."},
			{Key: "policies[].event", Type: "string", Description: "Event type to match."},
			{Key: "policies[].handler", Type: "string", Description: "Handler for this event type."},
			{Key: "policies[].timeout", Type: "duration", Description: "Time before escalating to next level."},
		},
	},
	"formula": {
		Name:        "formula",
		Description: "Formula definitions for automated workflows",
		Fields: []fieldDoc{
			{Key: "name", Type: "string", Description: "Formula name."},
			{Key: "steps", Type: "[]object", Description: "Ordered list of workflow steps."},
			{Key: "steps[].action", Type: "string", Description: "Action to execute (e.g. 'create-bead', 'assign', 'notify')."},
			{Key: "steps[].params", Type: "object", Description: "Parameters for this step."},
		},
	},
}
