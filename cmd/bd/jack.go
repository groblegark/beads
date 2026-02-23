package main

import (
	"github.com/spf13/cobra"
)

// jackCmd is the parent command for jack (infrastructure modification) operations.
var jackCmd = &cobra.Command{
	Use:     "jack",
	GroupID: "issues",
	Short:   "Manage infrastructure jacks (temporary modification primitives)",
	Long: `Jacks are temporary infrastructure modification primitives using the automotive
jack metaphor: you put infrastructure "on the jacks" to perform maintenance,
then take it "off the jacks" when done.

A jack is created BEFORE making changes (the safety check), records what you're
about to do, and enforces time-to-live so modifications don't drift silently.

Commands:
  on        Raise a jack — declare intent and begin modification
  off       Lower a jack — revert changes and close
  list      Show active jacks (default) or all with filtering
  show      Detailed jack view with changes, revert plan, and deps
  check     Find expired jacks (for periodic sweeps)
  extend    Request more time for an active jack
  log       Record a change made while the jack is active

Every jack requires a revert plan (how to undo) and has a TTL (default 1h).
Expired jacks trigger alerts via 'bd jack check'.

Examples:
  bd jack on pod/my-app --reason="Add debug logging" --revert-plan="kubectl rollout undo"
  bd jack log <id> --action="patched deployment" --target="deploy/my-app"
  bd jack off <id> --reason="Debug complete, reverted"
  bd jack list
  bd jack list --expired
  bd jack check --auto-escalate
  bd jack extend <id> --ttl=2h --reason="Still investigating"
  bd jack show <id>`,
}

func init() {
	rootCmd.AddCommand(jackCmd)
}
