package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/coopmux"
)

var (
	muxURL   string
	muxToken string
)

func newMuxClient() (*coopmux.Client, error) {
	url := muxURL
	if url == "" {
		url = os.Getenv("COOPMUX_URL")
	}
	if url == "" {
		return nil, fmt.Errorf("coopmux URL not set: use --mux-url flag or COOPMUX_URL env var")
	}

	token := muxToken
	if token == "" {
		token = os.Getenv("COOPMUX_TOKEN")
	}

	var opts []coopmux.Option
	if token != "" {
		opts = append(opts, coopmux.WithToken(token))
	}
	return coopmux.NewClient(url, opts...), nil
}

var muxCmd = &cobra.Command{
	Use:     "mux",
	GroupID: "advanced",
	Short:   "Interact with a remote coopmux credential multiplexer",
	Long: `Interact with a remote coopmux service for credential lifecycle management.

Coopmux handles OAuth credential refresh, reauth, and distribution across
agent pods. Use these commands to check status and trigger reauth flows
from your local machine.

Configuration:
  --mux-url   Coopmux service URL (or COOPMUX_URL env var)
  --token     Bearer auth token (or COOPMUX_TOKEN env var)`,
}

var muxStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check coopmux service health",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := newMuxClient()
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := client.Health(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "✗ Coopmux unreachable: %v\n", err)
			return err
		}

		fmt.Println("✓ Coopmux is healthy")
		return nil
	},
}

var muxReauthCmd = &cobra.Command{
	Use:   "reauth <account>",
	Short: "Initiate credential re-authentication for an account",
	Long: `Start an OAuth re-authentication flow for the specified account.

This initiates the reauth flow on the coopmux service, prints the auth URL
for you to visit in a browser, and optionally waits for you to paste the
authorization code to complete the exchange.

Example:
  bd mux reauth my-account
  bd mux reauth my-account --no-wait   # Just print the URL, don't wait for code`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		account := args[0]
		noWait, _ := cmd.Flags().GetBool("no-wait")

		client, err := newMuxClient()
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		fmt.Fprintf(os.Stderr, "Initiating reauth for account %q...\n", account)

		session, err := client.ReauthInitiate(ctx, account)
		if err != nil {
			return fmt.Errorf("reauth initiate failed: %w", err)
		}

		fmt.Println()
		fmt.Println("Re-authentication required:")
		fmt.Printf("  Account: %s\n", session.Account)
		fmt.Printf("  Auth URL: %s\n", session.AuthURL)
		fmt.Println()
		fmt.Println("1. Open the URL above in your browser")
		fmt.Println("2. Sign in and authorize")
		fmt.Println("3. Copy the authorization code shown")

		if noWait {
			fmt.Printf("\nState token: %s\n", session.State)
			fmt.Println("Run 'bd mux exchange' with the state and code to complete.")
			return nil
		}

		fmt.Println()
		fmt.Print("Paste authorization code: ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return fmt.Errorf("no input received")
		}
		code := strings.TrimSpace(scanner.Text())
		if code == "" {
			return fmt.Errorf("empty authorization code")
		}

		exchCtx, exchCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer exchCancel()

		if err := client.ReauthExchange(exchCtx, session.State, code); err != nil {
			return fmt.Errorf("reauth exchange failed: %w", err)
		}

		fmt.Printf("\n✓ Re-authentication successful for account %q\n", account)
		return nil
	},
}

var muxExchangeCmd = &cobra.Command{
	Use:   "exchange <state> <code>",
	Short: "Complete a reauth flow with state token and authorization code",
	Long: `Submit an authorization code to complete a previously initiated reauth flow.

Use this after 'bd mux reauth --no-wait' to complete the exchange separately.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		state := args[0]
		code := args[1]

		client, err := newMuxClient()
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := client.ReauthExchange(ctx, state, code); err != nil {
			return fmt.Errorf("reauth exchange failed: %w", err)
		}

		fmt.Println("✓ Re-authentication exchange successful")
		return nil
	},
}

func init() {
	muxCmd.PersistentFlags().StringVar(&muxURL, "mux-url", "", "Coopmux service URL (env: COOPMUX_URL)")
	muxCmd.PersistentFlags().StringVar(&muxToken, "token", "", "Bearer auth token (env: COOPMUX_TOKEN)")

	muxReauthCmd.Flags().Bool("no-wait", false, "Print auth URL and exit without waiting for code")

	muxCmd.AddCommand(muxStatusCmd)
	muxCmd.AddCommand(muxReauthCmd)
	muxCmd.AddCommand(muxExchangeCmd)
	rootCmd.AddCommand(muxCmd)
}
