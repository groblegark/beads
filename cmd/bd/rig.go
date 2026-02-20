package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
)

// rigCmd is the parent command for rig operations (bd-jzx1m).
var rigCmd = &cobra.Command{
	Use:   "rig",
	Short: "Manage repository registrations",
	Long: `Manage rig registrations — lightweight repository metadata.

A rig associates a name with a remote URL, local path, default branch, and
issue prefix. Rigs are used to link beads to repositories and detect branch
conflicts at the roster level.

Commands:
  register   Register or update a rig
  list       List all registered rigs
  show       Show rig details
  remove     Remove a rig registration`,
}

var rigRegisterCmd = &cobra.Command{
	Use:   "register <name>",
	Short: "Register or update a rig",
	Long: `Register a new rig or update an existing one.

A rig is a lightweight repository registration that associates a name with
a remote URL, optional local path, default branch, and issue prefix.

Examples:
  bd rig register beads --url https://github.com/groblegark/beads.git --branch main --prefix bd
  bd rig register gastown --url https://github.com/groblegark/gastown.git --branch main --prefix gt
  bd rig register coop --url https://github.com/groblegark/coop.git`,
	Args: cobra.ExactArgs(1),
	Run:  runRigRegister,
}

var rigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all registered rigs",
	Run:   runRigList,
}

var rigShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show rig details",
	Args:  cobra.ExactArgs(1),
	Run:   runRigShow,
}

var rigRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a rig registration",
	Args:  cobra.ExactArgs(1),
	Run:   runRigRemove,
}

func runRigRegister(cmd *cobra.Command, args []string) {
	CheckReadonly("rig register")

	name := args[0]
	url, _ := cmd.Flags().GetString("url")
	localPath, _ := cmd.Flags().GetString("path")
	branch, _ := cmd.Flags().GetString("branch")
	prefix, _ := cmd.Flags().GetString("prefix")

	if url == "" {
		fmt.Fprintf(os.Stderr, "Error: --url is required\n")
		os.Exit(1)
	}

	result, err := daemonClient.RigRegister(&rpc.RigRegisterArgs{
		Name:          name,
		RemoteURL:     url,
		LocalPath:     localPath,
		DefaultBranch: branch,
		Prefix:        prefix,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("Registered rig %q\n", result.Name)
	fmt.Printf("  URL:    %s\n", result.RemoteURL)
	if result.LocalPath != "" {
		fmt.Printf("  Path:   %s\n", result.LocalPath)
	}
	if result.DefaultBranch != "" {
		fmt.Printf("  Branch: %s\n", result.DefaultBranch)
	}
	if result.Prefix != "" {
		fmt.Printf("  Prefix: %s\n", result.Prefix)
	}
}

func runRigList(cmd *cobra.Command, args []string) {
	result, err := daemonClient.RigList()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	if len(result.Rigs) == 0 {
		fmt.Println("No rigs registered. Use 'bd rig register' to add one.")
		return
	}

	for _, rig := range result.Rigs {
		prefix := ""
		if rig.Prefix != "" {
			prefix = fmt.Sprintf(" [%s]", rig.Prefix)
		}
		branch := ""
		if rig.DefaultBranch != "" {
			branch = fmt.Sprintf(" (%s)", rig.DefaultBranch)
		}
		fmt.Printf("  %s%s  %s%s\n", rig.Name, prefix, rig.RemoteURL, branch)
	}
}

func runRigShow(cmd *cobra.Command, args []string) {
	result, err := daemonClient.RigShow(&rpc.RigShowArgs{Name: args[0]})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	fmt.Printf("Name:      %s\n", result.Name)
	fmt.Printf("URL:       %s\n", result.RemoteURL)
	if result.LocalPath != "" {
		fmt.Printf("Path:      %s\n", result.LocalPath)
	}
	if result.DefaultBranch != "" {
		fmt.Printf("Branch:    %s\n", result.DefaultBranch)
	}
	if result.Prefix != "" {
		fmt.Printf("Prefix:    %s\n", result.Prefix)
	}
	if result.CreatedAt != "" {
		fmt.Printf("Created:   %s\n", result.CreatedAt)
	}
	if result.UpdatedAt != "" {
		fmt.Printf("Updated:   %s\n", result.UpdatedAt)
	}
}

func runRigRemove(cmd *cobra.Command, args []string) {
	CheckReadonly("rig remove")

	err := daemonClient.RigRemove(&rpc.RigRemoveArgs{Name: args[0]})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		outputJSON(map[string]string{"removed": args[0]})
		return
	}

	fmt.Printf("Removed rig %q\n", args[0])
}

func init() {
	rigRegisterCmd.Flags().String("url", "", "Remote repository URL (required)")
	rigRegisterCmd.Flags().String("path", "", "Local checkout path (optional)")
	rigRegisterCmd.Flags().String("branch", "", "Default branch name (e.g., main)")
	rigRegisterCmd.Flags().String("prefix", "", "Issue ID prefix (e.g., bd, gt)")

	rigCmd.AddCommand(rigRegisterCmd)
	rigCmd.AddCommand(rigListCmd)
	rigCmd.AddCommand(rigShowCmd)
	rigCmd.AddCommand(rigRemoveCmd)
	rootCmd.AddCommand(rigCmd)
}

