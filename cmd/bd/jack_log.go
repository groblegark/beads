package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/sensitive"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

const maxJackChanges = 500
const maxChangeFieldSize = 5 * 1024 // 5KB

var validJackActions = map[string]bool{
	"edit":   true,
	"exec":   true,
	"patch":  true,
	"delete": true,
	"create": true,
}

// jackLogCmd records a change made while a jack is active.
var jackLogCmd = &cobra.Command{
	Use:   "log <jack-id>",
	Short: "Record a change made while the jack is active",
	Long: `Append a structured change record to the jack's change log.

Every modification made while a jack is raised should be recorded so that
the revert plan can be verified and the blast radius is documented.

Actions:
  edit    — modified a file, config, or resource in place
  exec    — ran a command (record with --cmd)
  patch   — applied a patch or diff
  delete  — removed a resource
  create  — created a new resource

Examples:
  # Record a config edit
  bd jack log bd-jack-abc --action=edit \
    --target="config.yaml" \
    --before="log_level: info" \
    --after="log_level: debug"

  # Record a command execution
  bd jack log bd-jack-abc --action=exec \
    --cmd="kubectl rollout restart deployment/bd-daemon"

  # Record a file deletion
  bd jack log bd-jack-abc --action=delete \
    --target="/etc/nginx/sites-enabled/old-config"`,
	Args: cobra.ExactArgs(1),
	Run:  runJackLog,
}

func init() {
	jackLogCmd.Flags().String("action", "", "What was done: edit, exec, patch, delete, create (required)")
	jackLogCmd.Flags().String("target", "", "Specific resource affected (defaults to jack target)")
	jackLogCmd.Flags().String("before", "", "State before change")
	jackLogCmd.Flags().String("after", "", "State after change")
	jackLogCmd.Flags().String("cmd", "", "Command that was executed")
	jackLogCmd.Flags().Bool("sensitive", false, "Acknowledge that values may contain secrets")
	jackLogCmd.ValidArgsFunction = issueIDCompletion
	jackCmd.AddCommand(jackLogCmd)
}

func runJackLog(cmd *cobra.Command, args []string) {
	CheckReadonly("jack log")
	requireDaemon("jack log")

	jackID := args[0]
	action, _ := cmd.Flags().GetString("action")
	target, _ := cmd.Flags().GetString("target")
	before, _ := cmd.Flags().GetString("before")
	after, _ := cmd.Flags().GetString("after")
	cmdStr, _ := cmd.Flags().GetString("cmd")
	sensitiveFlag, _ := cmd.Flags().GetBool("sensitive")

	// Validate action
	if action == "" {
		FatalErrorRespectJSON("--action is required (edit, exec, patch, delete, create)")
	}
	if !validJackActions[action] {
		FatalErrorRespectJSON("invalid action %q — must be one of: edit, exec, patch, delete, create", action)
	}

	// Truncate before/after to 5KB
	if len(before) > maxChangeFieldSize {
		before = before[:maxChangeFieldSize-3] + "..."
	}
	if len(after) > maxChangeFieldSize {
		after = after[:maxChangeFieldSize-3] + "..."
	}

	// Check for secrets in before/after
	hasSensitive := false
	if field := sensitive.ScanFields(before, after); field != "" {
		if !sensitiveFlag {
			pat := sensitive.MatchedPattern(before)
			if pat == "" {
				pat = sensitive.MatchedPattern(after)
			}
			FatalErrorRespectJSON(
				"%s appears to contain secrets (matched pattern: %s)\n"+
					"Use --sensitive to acknowledge and proceed, or redact the value",
				field, pat)
		}
		hasSensitive = true
		// Redact secret values in storage — originals are never persisted
		before, _ = sensitive.Redact(before)
		after, _ = sensitive.Redact(after)
	}

	// Fetch the jack to validate it exists, is a jack type, and is active
	showResp, err := daemonClient.Show(&rpc.ShowArgs{ID: jackID})
	if err != nil {
		FatalErrorRespectJSON("jack not found: %v — run 'bd jack list' to see active jacks", err)
	}
	if !showResp.Success {
		FatalErrorRespectJSON("jack not found: %s — run 'bd jack list' to see active jacks", showResp.Error)
	}

	var details types.IssueDetails
	if err := json.Unmarshal(showResp.Data, &details); err != nil {
		FatalErrorRespectJSON("error parsing jack: %v", err)
	}
	issue := &details.Issue

	if issue.IssueType != types.TypeJack {
		FatalErrorRespectJSON("%s is not a jack (type=%s) — use 'bd comment' for regular issues", jackID, issue.IssueType)
	}
	if issue.Status == types.StatusClosed {
		FatalErrorRespectJSON("jack %s is already closed — cannot log changes to a closed jack", jackID)
	}

	// Parse existing metadata to get jack_changes array
	metadata := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
			FatalErrorRespectJSON("error parsing jack metadata: %v", err)
		}
	}

	// Get existing changes array
	var changes []interface{}
	if existing, ok := metadata["jack_changes"]; ok {
		if arr, ok := existing.([]interface{}); ok {
			changes = arr
		}
	}

	// Enforce max 500 changes
	if len(changes) >= maxJackChanges {
		FatalErrorRespectJSON("jack %s has %d changes (max %d) — close this jack and raise a new one",
			jackID, len(changes), maxJackChanges)
	}

	// Default target to jack target if not specified
	if target == "" {
		if jackTarget, ok := metadata["jack_target"].(string); ok {
			target = jackTarget
		} else {
			target = "unknown"
		}
	}

	// Build the change record
	change := rpc.JackChange{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Action:    action,
		Target:    target,
		Before:    before,
		After:     after,
		Cmd:       cmdStr,
		Agent:     actor,
		Sensitive: hasSensitive,
	}

	// Append to changes
	changes = append(changes, change)
	metadata["jack_changes"] = changes

	// Marshal updated metadata
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		FatalErrorRespectJSON("error marshaling updated metadata: %v", err)
	}

	// Update the jack with new metadata
	rawMsg := json.RawMessage(metadataJSON)
	updateArgs := &rpc.UpdateArgs{
		ID:       jackID,
		Metadata: &rawMsg,
	}

	updateResp, err := daemonClient.Update(updateArgs)
	if err != nil {
		FatalErrorRespectJSON("error updating jack: %v", err)
	}
	if !updateResp.Success {
		FatalErrorRespectJSON("error updating jack: %s", updateResp.Error)
	}

	// Output
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"jack_id":      jackID,
			"change_index": len(changes) - 1,
			"action":       action,
			"target":       target,
			"timestamp":    change.Timestamp,
			"total":        len(changes),
			"sensitive":    hasSensitive,
		})
		return
	}

	fmt.Printf("%s Change recorded on jack %s\n\n", ui.RenderPass("✓"), ui.RenderID(jackID))
	fmt.Printf("  Action:    %s\n", action)
	fmt.Printf("  Target:    %s\n", target)
	if hasSensitive {
		fmt.Printf("  Sensitive: values redacted in storage (use 'bd jack show %s --reveal' to view)\n", jackID)
	}
	if before != "" {
		display := before
		if len(display) > 80 {
			display = display[:77] + "..."
		}
		fmt.Printf("  Before:    %s\n", display)
	}
	if after != "" {
		display := after
		if len(display) > 80 {
			display = display[:77] + "..."
		}
		fmt.Printf("  After:     %s\n", display)
	}
	if cmdStr != "" {
		fmt.Printf("  Command:   %s\n", cmdStr)
	}
	fmt.Printf("  Changes:   %d of %d max\n", len(changes), maxJackChanges)
	if len(changes) > maxJackChanges*4/5 {
		fmt.Printf("\n  %s Change log is %.0f%% full — consider closing this jack\n",
			ui.RenderWarn("!"), float64(len(changes))/float64(maxJackChanges)*100)
	}

	// Show remaining capacity hint
	remaining := strings.Builder{}
	remaining.WriteString(fmt.Sprintf("\n  Log more:    bd jack log %s --action=edit --target=...\n", jackID))
	remaining.WriteString(fmt.Sprintf("  Lower jack:  bd jack off %s --reason=\"...\"\n", jackID))
	fmt.Print(remaining.String())
}
