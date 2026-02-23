package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

const (
	// maxExtensionsPerJack is the maximum number of times a jack can be extended.
	maxExtensionsPerJack = 5
	// maxSingleExtension is the maximum TTL for a single extension.
	maxSingleExtension = 24 * time.Hour
	// maxCumulativeTTL is the maximum total TTL across all extensions.
	maxCumulativeTTL = 7 * 24 * time.Hour
)

// jackExtendCmd extends the TTL on an active jack.
var jackExtendCmd = &cobra.Command{
	Use:   "extend <jack-id>",
	Short: "Extend a jack's TTL — request more time",
	Long: `Extend the time-to-live on an active infrastructure jack.

Enforces limits:
  - Maximum single extension: 24 hours
  - Maximum extensions per jack: 5
  - Maximum cumulative TTL: 7 days

Examples:
  bd jack extend bd-jack-abc --ttl=1h
  bd jack extend bd-jack-abc --ttl=2h --reason="Still debugging NATS timeout"
  bd jack extend bd-jack-abc --ttl=30m --reason="Need more time for verification"`,
	Args: cobra.ExactArgs(1),
	Run:  runJackExtend,
}

func init() {
	jackExtendCmd.Flags().Duration("ttl", 0, "New TTL from now (required)")
	jackExtendCmd.Flags().StringP("reason", "r", "", "Why more time is needed")
	jackExtendCmd.ValidArgsFunction = issueIDCompletion

	jackCmd.AddCommand(jackExtendCmd)
}

func runJackExtend(cmd *cobra.Command, args []string) {
	CheckReadonly("jack extend")
	requireDaemon("jack extend")

	jackID := args[0]
	ttl, _ := cmd.Flags().GetDuration("ttl")
	reason, _ := cmd.Flags().GetString("reason")

	// Validate TTL
	if ttl <= 0 {
		fmt.Fprintf(os.Stderr, "Error: --ttl is required and must be positive (e.g., --ttl=1h, --ttl=30m)\n")
		os.Exit(1)
	}
	if ttl > maxSingleExtension {
		FatalErrorRespectJSON("maximum single extension is %s, got %s", maxSingleExtension, ttl)
	}

	// Fetch the jack to validate
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
		FatalErrorRespectJSON("%s is not a jack (type=%s) — use 'bd update' for regular issues", jackID, issue.IssueType)
	}
	if issue.Status == types.StatusClosed {
		FatalErrorRespectJSON("jack %s is already closed — cannot extend a closed jack", jackID)
	}

	// Parse existing metadata
	metadata := make(map[string]interface{})
	if len(issue.Metadata) > 0 {
		if err := json.Unmarshal(issue.Metadata, &metadata); err != nil {
			FatalErrorRespectJSON("error parsing jack metadata: %v", err)
		}
	}

	// Count existing extensions
	extensionCount := 0
	if ec, ok := metadata["jack_extension_count"]; ok {
		if ecf, ok := ec.(float64); ok {
			extensionCount = int(ecf)
		}
	}
	if extensionCount >= maxExtensionsPerJack {
		FatalErrorRespectJSON("jack %s has been extended %d times (max %d) — close this jack and raise a new one",
			jackID, extensionCount, maxExtensionsPerJack)
	}

	// Calculate cumulative TTL
	originalTTL := time.Hour // default
	if ttlStr, ok := metadata["jack_ttl"].(string); ok {
		if parsed, err := time.ParseDuration(ttlStr); err == nil {
			originalTTL = parsed
		}
	}
	cumulativeTTL := originalTTL
	if cumStr, ok := metadata["jack_cumulative_ttl"].(string); ok {
		if parsed, err := time.ParseDuration(cumStr); err == nil {
			cumulativeTTL = parsed
		}
	}
	newCumulativeTTL := cumulativeTTL + ttl
	if newCumulativeTTL > maxCumulativeTTL {
		FatalErrorRespectJSON("cumulative TTL would be %s (max %s) — close this jack and raise a new one",
			newCumulativeTTL, maxCumulativeTTL)
	}

	// Compute new expiry
	now := time.Now()
	newExpiresAt := now.Add(ttl)

	// Update metadata
	metadata["jack_expires_at"] = newExpiresAt.Format(time.RFC3339)
	metadata["jack_cumulative_ttl"] = newCumulativeTTL.String()
	metadata["jack_extension_count"] = extensionCount + 1

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

	// Add a comment recording the extension
	commentText := fmt.Sprintf("Extended TTL by %s (expires %s)", ttl, newExpiresAt.Format("2006-01-02 15:04 MST"))
	if reason != "" {
		commentText += fmt.Sprintf("\nReason: %s", reason)
	}
	commentText += fmt.Sprintf("\nExtension %d/%d, cumulative TTL: %s/%s",
		extensionCount+1, maxExtensionsPerJack, newCumulativeTTL, maxCumulativeTTL)

	commentArgs := &rpc.CommentAddArgs{
		ID:   jackID,
		Text: commentText,
	}
	if actor != "" {
		commentArgs.Author = actor
	}
	_, _ = daemonClient.AddComment(commentArgs) // best-effort

	// Output
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"id":              jackID,
			"new_expires_at":  newExpiresAt.Format(time.RFC3339),
			"extension_count": extensionCount + 1,
			"cumulative_ttl":  newCumulativeTTL.String(),
			"ttl_added":       ttl.String(),
		})
		return
	}

	fmt.Printf("%s Extended jack: %s\n\n", ui.RenderPass("✓"), ui.RenderID(jackID))
	fmt.Printf("  TTL added:       %s\n", ttl)
	fmt.Printf("  New expiry:      %s\n", newExpiresAt.Format("2006-01-02 15:04 MST"))
	fmt.Printf("  Extensions:      %d/%d\n", extensionCount+1, maxExtensionsPerJack)
	fmt.Printf("  Cumulative TTL:  %s/%s\n", newCumulativeTTL, maxCumulativeTTL)
	if reason != "" {
		fmt.Printf("  Reason:          %s\n", reason)
	}
}
