package slackbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/slack-go/slack"
)

// CoopCredWatcher subscribes to coop credential events on core NATS
// and posts reauth notifications to Slack. When a user replies with
// an authorization code, it completes the reauth flow via the coopmux API.
// It also handles "add account" commands to create new credential accounts.
type CoopCredWatcher struct {
	natsURL   string
	natsToken string
	coopmuxURL string // e.g. "http://gastown-next-coopmux:9800"
	authToken string // Bearer token for coopmux API
	bot       *Bot
	conn      *nats.Conn
	sub       *nats.Subscription

	// Track reauth thread messages so we can intercept code replies.
	reauthThreads   map[string]reauthInfo // thread_ts → reauth info
	completedThreads map[string]bool      // threads where reauth succeeded (don't re-recover)
	reauthThreadsMu sync.RWMutex

	// Track device code threads (no manual code paste needed — coopmux polls).
	deviceCodeThreads   map[string]string // thread_ts → account name
	deviceCodeThreadsMu sync.RWMutex
}

// reauthInfo tracks a pending reauth notification posted to Slack.
type reauthInfo struct {
	account   string
	channelID string
	state     string // OAuth state from coopmux reauth initiation
	postedAt  time.Time
}

// credentialEventPayload mirrors the NATS event published by coop.
type credentialEventPayload struct {
	EventType string  `json:"event_type"`
	Account   string  `json:"account"`
	Error     *string `json:"error,omitempty"`
	AuthURL   *string `json:"auth_url,omitempty"`
	UserCode  *string `json:"user_code,omitempty"`
	Ts        string  `json:"ts"`
}

// NewCoopCredWatcher creates a watcher that subscribes to coop credential
// events and forwards reauth URLs to Slack.
func NewCoopCredWatcher(natsURL, natsToken, coopmuxURL, authToken string, bot *Bot) *CoopCredWatcher {
	return &CoopCredWatcher{
		natsURL:           natsURL,
		natsToken:         natsToken,
		coopmuxURL:        strings.TrimRight(coopmuxURL, "/"),
		authToken:         authToken,
		bot:               bot,
		reauthThreads:     make(map[string]reauthInfo),
		completedThreads:  make(map[string]bool),
		deviceCodeThreads: make(map[string]string),
	}
}

// Run connects to NATS and subscribes to coop.events.credential.
// It reconnects with backoff on disconnect and blocks until ctx is canceled.
func (w *CoopCredWatcher) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := w.connect()
		if err != nil {
			log.Printf("slackbot/cred: connect error: %v (retry in %v)", err, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff = backoff * 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		backoff = time.Second

		select {
		case <-ctx.Done():
			w.Close()
			return ctx.Err()
		case <-w.waitDisconnect():
			log.Printf("slackbot/cred: disconnected, will reconnect")
			w.Close()
		}
	}
}

// connect establishes the NATS connection and subscribes to credential events.
func (w *CoopCredWatcher) connect() error {
	connectOpts := []nats.Option{
		nats.Name("beads-slack-bot-cred"),
		nats.RetryOnFailedConnect(false),
	}
	if w.natsToken != "" {
		connectOpts = append(connectOpts, nats.Token(w.natsToken))
	}

	nc, err := nats.Connect(w.natsURL, connectOpts...)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}

	// Subscribe to core NATS (not JetStream) — coop publishes credential
	// events on core NATS, not JetStream.
	sub, err := nc.Subscribe("coop.events.credential", w.handleMessage)
	if err != nil {
		nc.Close()
		return fmt.Errorf("subscribe coop.events.credential: %w", err)
	}

	w.conn = nc
	w.sub = sub
	log.Printf("slackbot/cred: connected to %s, subscribed to coop.events.credential", w.natsURL)
	return nil
}

// waitDisconnect returns a channel that closes when the NATS connection drops.
func (w *CoopCredWatcher) waitDisconnect() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		if w.conn == nil {
			close(ch)
			return
		}
		for w.conn.IsConnected() {
			time.Sleep(500 * time.Millisecond)
		}
		close(ch)
	}()
	return ch
}

const (
	// Don't send reauth notifications more often than this.
	reauthNotifyMinInterval = 5 * time.Minute
)

// handleMessage parses a credential event and dispatches reauth notifications.
func (w *CoopCredWatcher) handleMessage(msg *nats.Msg) {
	var payload credentialEventPayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		log.Printf("slackbot/cred: unmarshal error: %v", err)
		return
	}

	switch payload.EventType {
	case "reauth_required":
		log.Printf("slackbot/cred: reauth_required for %s", payload.Account)

		// Rate-limit: don't spam Slack with reauth notifications.
		w.reauthThreadsMu.RLock()
		tooRecent := false
		for _, info := range w.reauthThreads {
			if info.account == payload.Account && time.Since(info.postedAt) < reauthNotifyMinInterval {
				tooRecent = true
				break
			}
		}
		w.reauthThreadsMu.RUnlock()
		if tooRecent {
			return
		}

		// Device code flow: NATS payload has user_code + auth_url (verification URI).
		// User visits URL and enters code in browser. coopmux polls automatically.
		// No code paste needed in Slack.
		if payload.UserCode != nil && *payload.UserCode != "" {
			authURL := ""
			if payload.AuthURL != nil {
				authURL = *payload.AuthURL
			}
			go w.notifyDeviceCode(payload.Account, authURL, *payload.UserCode)
			return
		}

		// PKCE flow: initiate reauth via coopmux to get the auth URL and
		// state token needed for the exchange call.
		go w.pullReauthURL(payload.Account)
	case "refreshed":
		log.Printf("slackbot/cred: account %s refreshed", payload.Account)
		// Post completion in device code threads (coopmux auto-polled successfully).
		go w.notifyDeviceCodeComplete(payload.Account)
		// Don't clear reauth threads here — the credential seeder re-seeds
		// revoked credentials on pod restart, emitting a false "refreshed"
		// event that would wipe the thread tracking. Threads are cleaned up
		// when the code is successfully submitted (submitReauthCode).
	case "refresh_failed":
		errMsg := ""
		if payload.Error != nil {
			errMsg = *payload.Error
		}
		log.Printf("slackbot/cred: account %s refresh failed: %s", payload.Account, errMsg)

		// If we haven't already posted a reauth notification for this account,
		// pull the auth URL from the coopmux. This handles the race where the
		// coopmux emitted reauth_required on core NATS before we subscribed.
		w.reauthThreadsMu.RLock()
		recentlyNotified := false
		for _, info := range w.reauthThreads {
			if info.account == payload.Account && time.Since(info.postedAt) < reauthNotifyMinInterval {
				recentlyNotified = true
				break
			}
		}
		w.reauthThreadsMu.RUnlock()

		if !recentlyNotified {
			go w.pullReauthURL(payload.Account)
		}
	}
}

// notifyReauth posts the reauth URL to the default Slack channel.
// state is the OAuth state token from coopmux reauth initiation (may be empty
// for NATS-originated events; pullReauthURL will supply it).
func (w *CoopCredWatcher) notifyReauth(account, authURL, state string) {
	channelID := w.bot.channelID

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "Credential Re-authentication Required", false, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf(
				"Account *%s* needs re-authentication.\n\n"+
					"1. Open the link below in your browser\n"+
					"2. Sign in to Claude\n"+
					"3. Copy the authorization code shown\n"+
					"4. Reply to this thread with the code",
				account,
			), false, false),
			nil, nil,
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("<%s|Click here to authenticate>", authURL), false, false),
			nil, nil,
		),
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Account: `%s` | Reply with the code in this thread to complete re-authentication", account),
				false, false),
		),
	}

	_, ts, err := w.bot.client.PostMessage(channelID,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(fmt.Sprintf("Credential reauth required for %s", account), false),
	)
	if err != nil {
		log.Printf("slackbot/cred: failed to post reauth notification: %v", err)
		return
	}

	// Track this thread for code replies.
	w.reauthThreadsMu.Lock()
	w.reauthThreads[ts] = reauthInfo{
		account:   account,
		channelID: channelID,
		state:     state,
		postedAt:  time.Now(),
	}
	w.reauthThreadsMu.Unlock()

	log.Printf("slackbot/cred: posted reauth notification for %s (thread %s)", account, ts)
}

// HandleThreadReply checks if a thread reply is a reauth code submission.
// Returns true if the reply was handled as a reauth code.
func (w *CoopCredWatcher) HandleThreadReply(channelID, threadTS, text, userID string) bool {
	w.reauthThreadsMu.RLock()
	info, ok := w.reauthThreads[threadTS]
	completed := w.completedThreads[threadTS]
	w.reauthThreadsMu.RUnlock()

	if completed {
		return false
	}

	if !ok {
		// Thread not tracked (e.g., pod restarted since the reauth notification was posted).
		// Check if the parent message is a reauth notification from this bot.
		info, ok = w.recoverReauthThread(channelID, threadTS)
		if !ok {
			return false
		}
		log.Printf("slackbot/cred: recovered reauth thread %s for account %s", threadTS, info.account)
	}

	trimmed := strings.TrimSpace(text)
	code := extractAuthCode(trimmed)
	if code == "" {
		return false
	}

	// Submit the code to the coopmux.
	go w.submitReauthCode(info, code, channelID, threadTS, userID)
	return true
}

// submitReauthCode POSTs the authorization code to coopmux's complete endpoint.
func (w *CoopCredWatcher) submitReauthCode(info reauthInfo, code, channelID, threadTS, userID string) {
	if w.coopmuxURL == "" {
		w.bot.postThreadReply(channelID, threadTS,
			"Coopmux URL not configured (set COOPMUX_URL)")
		return
	}

	if info.state == "" {
		w.bot.postThreadReply(channelID, threadTS,
			"Missing OAuth state for this reauth session. Please start a new reauth flow.")
		return
	}

	reqBody := map[string]string{
		"state": info.state,
		"code":  code,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("slackbot/cred: marshal reauth code request: %v", err)
		return
	}

	log.Printf("slackbot/cred: submitting reauth code for %s (code_len=%d)", info.account, len(code))

	url := w.coopmuxURL + "/api/v1/credentials/exchange"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		log.Printf("slackbot/cred: create request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if w.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.authToken)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("slackbot/cred: coopmux request failed: %v", err)
		w.bot.postThreadReply(channelID, threadTS,
			fmt.Sprintf("Failed to submit code to coopmux: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		// Remove from tracked threads and mark completed so recoverReauthThread
		// doesn't re-add it for subsequent conversational replies.
		w.reauthThreadsMu.Lock()
		delete(w.reauthThreads, threadTS)
		w.completedThreads[threadTS] = true
		w.reauthThreadsMu.Unlock()

		log.Printf("slackbot/cred: reauth completed for account %s (submitted by %s)", info.account, userID)
		w.bot.postThreadReply(channelID, threadTS,
			fmt.Sprintf(":white_check_mark: Re-authentication successful for account *%s*.", info.account))
	} else {
		log.Printf("slackbot/cred: coopmux returned %d: %s", resp.StatusCode, string(respBody))
		detail := string(respBody)
		if len(detail) > 500 {
			detail = detail[:500] + "..."
		}
		w.bot.postThreadReply(channelID, threadTS,
			fmt.Sprintf("Re-authentication failed (HTTP %d):\n```\n%s\n```\nCheck the code and try again.", resp.StatusCode, detail))

		// Clear the stale thread so a fresh reauth notification can be posted.
		// The old auth URL and state are likely invalid (e.g. coopmux restarted
		// with a new PKCE session), so keeping the thread suppresses all future
		// reauth notifications for this account.
		w.reauthThreadsMu.Lock()
		delete(w.reauthThreads, threadTS)
		w.reauthThreadsMu.Unlock()
	}
}

// fetchReauthState calls coopmux's reauth endpoint to get a fresh OAuth state
// token for an account. Used when recovering reauth threads after pod restart.
func (w *CoopCredWatcher) fetchReauthState(account string) string {
	if w.coopmuxURL == "" {
		return ""
	}

	reqBody, _ := json.Marshal(map[string]string{"account": account})
	reqURL := w.coopmuxURL + "/api/v1/credentials/reauth"
	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(reqBody))
	if err != nil {
		log.Printf("slackbot/cred: create reauth state request: %v", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	if w.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.authToken)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("slackbot/cred: reauth state request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("slackbot/cred: reauth state returned %d: %s", resp.StatusCode, string(body))
		return ""
	}

	var session struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return ""
	}
	return session.State
}

// recoverReauthThread checks if a thread's parent message is a reauth notification
// posted by this bot. This handles the case where the pod restarted after posting
// the notification, losing the in-memory reauthThreads map.
func (w *CoopCredWatcher) recoverReauthThread(channelID, threadTS string) (reauthInfo, bool) {
	// Fetch the full thread (parent + replies) to check if reauth already completed.
	msgs, _, _, err := w.bot.client.GetConversationReplies(&slack.GetConversationRepliesParameters{
		ChannelID: channelID,
		Timestamp: threadTS,
		Inclusive: true,
	})
	if err != nil || len(msgs) == 0 {
		return reauthInfo{}, false
	}

	parent := msgs[0]

	// Must be from the bot.
	if parent.User != w.bot.botUserID && parent.BotID == "" {
		return reauthInfo{}, false
	}

	// Check if the message text contains our reauth marker.
	text := parent.Text
	if !strings.Contains(text, "Credential reauth required for ") {
		return reauthInfo{}, false
	}

	// Check if the bot already posted a success reply — reauth is done, don't re-track.
	for _, reply := range msgs[1:] {
		if (reply.User == w.bot.botUserID || reply.BotID != "") &&
			strings.Contains(reply.Text, "Re-authentication successful") {
			// Mark as completed so we don't fetch the thread again.
			w.reauthThreadsMu.Lock()
			w.completedThreads[threadTS] = true
			w.reauthThreadsMu.Unlock()
			return reauthInfo{}, false
		}
	}

	// Extract account name from "Credential reauth required for <account>"
	account := ""
	if idx := strings.Index(text, "Credential reauth required for "); idx >= 0 {
		account = strings.TrimSpace(text[idx+len("Credential reauth required for "):])
	}
	if account == "" {
		return reauthInfo{}, false
	}

	// Re-initiate reauth via coopmux to get a fresh state token.
	// The original state was lost when the pod restarted.
	state := w.fetchReauthState(account)
	if state == "" {
		log.Printf("slackbot/cred: recovered thread %s but could not get fresh state for %s", threadTS, account)
	}

	info := reauthInfo{
		account:   account,
		channelID: channelID,
		state:     state,
	}

	// Re-track this thread so subsequent replies don't need to fetch again.
	w.reauthThreadsMu.Lock()
	w.reauthThreads[threadTS] = info
	w.reauthThreadsMu.Unlock()

	return info, true
}

// extractAuthCode extracts the authorization code from user input.
// Handles these cases:
//   - Full callback URL: https://platform.claude.com/oauth/code/callback?code=ABC123
//   - Slack-formatted URL: <https://platform.claude.com/oauth/code/callback?code=ABC123>
//   - Raw code with state: UrVjjR...#nqIa2... (code#state from callback page)
//   - Raw code: UrVjjR...
//
// Returns "" if the text doesn't look like a valid authorization code
// (must be 20+ chars, alphanumeric/base64url only, no spaces).
func extractAuthCode(text string) string {
	// Strip Slack URL formatting: <url|display> or <url>
	if strings.HasPrefix(text, "<") {
		end := strings.Index(text, ">")
		if end > 0 {
			text = text[1:end]
		}
		// Remove display text after pipe
		if pipeIdx := strings.Index(text, "|"); pipeIdx > 0 {
			text = text[:pipeIdx]
		}
	}

	// If it looks like a URL with a code parameter, extract it.
	if strings.Contains(text, "code=") {
		parsed, err := url.Parse(text)
		if err == nil {
			if code := parsed.Query().Get("code"); code != "" {
				text = code
			}
		}
	}

	// Claude's OAuth callback includes the state after a # separator:
	//   code#state  or  code%23state
	// Strip the state suffix — the actual code is the part before #.
	if hashIdx := strings.Index(text, "#"); hashIdx > 0 {
		text = text[:hashIdx]
	}

	// Validate: auth codes are 20+ chars of alphanumeric/base64url (no spaces).
	// This prevents submitting conversational replies ("lol", "Ha", etc.) as codes.
	if !looksLikeAuthCode(text) {
		return ""
	}

	return text
}

// looksLikeAuthCode returns true if s looks like an OAuth authorization code:
// at least 20 characters, only alphanumeric, dash, underscore, plus, slash, equals.
func looksLikeAuthCode(s string) bool {
	if len(s) < 20 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '+' || c == '/' || c == '=') {
			return false
		}
	}
	return true
}

// pullReauthURL calls coopmux's reauth endpoint to initiate an OAuth flow
// and posts the auth URL to Slack. This handles the race where the coopmux
// emitted reauth_required before the slackbot subscribed.
func (w *CoopCredWatcher) pullReauthURL(account string) {
	if w.coopmuxURL == "" {
		log.Printf("slackbot/cred: cannot pull reauth URL — coopmux URL not configured")
		return
	}

	reqBody, _ := json.Marshal(map[string]string{"account": account})
	url := w.coopmuxURL + "/api/v1/credentials/reauth"
	req, err := http.NewRequest("POST", url, bytes.NewReader(reqBody))
	if err != nil {
		log.Printf("slackbot/cred: create reauth request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if w.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.authToken)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("slackbot/cred: coopmux reauth request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Printf("slackbot/cred: coopmux reauth returned %d: %s", resp.StatusCode, string(body))
		return
	}

	var session struct {
		Account string `json:"account"`
		AuthURL string `json:"auth_url"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		log.Printf("slackbot/cred: parse reauth response: %v", err)
		return
	}
	if session.AuthURL == "" {
		log.Printf("slackbot/cred: coopmux returned empty auth_url for %s", account)
		return
	}

	// Double-check we haven't been beaten by a concurrent notification.
	w.reauthThreadsMu.RLock()
	for _, info := range w.reauthThreads {
		if info.account == account && time.Since(info.postedAt) < reauthNotifyMinInterval {
			w.reauthThreadsMu.RUnlock()
			return
		}
	}
	w.reauthThreadsMu.RUnlock()

	log.Printf("slackbot/cred: pulled reauth URL for %s from coopmux (startup race recovery)", account)
	w.notifyReauth(account, session.AuthURL, session.State)
}

// HandleChannelMessage checks if a channel message is an "add account" command.
// Returns true if the message was handled.
func (w *CoopCredWatcher) HandleChannelMessage(channelID, text, userID string) bool {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)

	// Match "add account <name>" or "add account <name> <provider>".
	if !strings.HasPrefix(lower, "add account ") {
		return false
	}

	parts := strings.Fields(trimmed)
	if len(parts) < 3 {
		return false
	}

	accountName := parts[2]
	provider := "claude" // default
	if len(parts) >= 4 {
		provider = strings.ToLower(parts[3])
	}

	go w.addNewAccount(accountName, provider, channelID)
	return true
}

// addNewAccount calls coopmux's /api/v1/credentials/new to create a new
// credential account. The account starts in Expired state; coopmux's refresh
// loop will initiate device code auth and emit reauth_required on NATS,
// which triggers the device code notification flow.
func (w *CoopCredWatcher) addNewAccount(name, provider, channelID string) {
	if w.coopmuxURL == "" {
		w.bot.postThreadReply(channelID, "",
			"Coopmux URL not configured (set COOPMUX_URL)")
		return
	}

	reqBody, _ := json.Marshal(map[string]string{
		"name":     name,
		"provider": provider,
	})

	reqURL := w.coopmuxURL + "/api/v1/credentials/new"
	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(reqBody))
	if err != nil {
		log.Printf("slackbot/cred: create new account request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if w.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.authToken)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("slackbot/cred: new account request failed: %v", err)
		w.postChannelMessage(channelID,
			fmt.Sprintf("Failed to create account *%s*: %v", name, err))
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		detail := string(body)
		if len(detail) > 500 {
			detail = detail[:500]
		}
		log.Printf("slackbot/cred: new account returned %d: %s", resp.StatusCode, detail)
		w.postChannelMessage(channelID,
			fmt.Sprintf("Failed to create account *%s* (HTTP %d):\n```\n%s\n```", name, resp.StatusCode, detail))
		return
	}

	log.Printf("slackbot/cred: created new account %s (provider: %s)", name, provider)
	w.postChannelMessage(channelID,
		fmt.Sprintf(":new: Account *%s* (provider: %s) created. Waiting for authentication — a device code will appear shortly.", name, provider))
}

// notifyDeviceCode posts a device code notification to Slack.
// Device code flow: user visits the URL and enters the code in their browser.
// coopmux polls the token endpoint automatically — no code paste in Slack needed.
func (w *CoopCredWatcher) notifyDeviceCode(account, authURL, userCode string) {
	channelID := w.bot.channelID

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "Authentication Required", false, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf(
				"Account *%s* needs authentication.\n\n"+
					"1. Open the verification URL below\n"+
					"2. Enter the code shown\n"+
					"3. Sign in and authorize access\n\n"+
					"Authentication will complete automatically — no need to paste anything here.",
				account,
			), false, false),
			nil, nil,
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf(
				"*Verification URL:* <%s|Open in browser>\n*Code:* `%s`",
				authURL, userCode,
			), false, false),
			nil, nil,
		),
		slack.NewContextBlock("",
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Account: `%s` | Device code flow — coopmux polls automatically", account),
				false, false),
		),
	}

	_, ts, err := w.bot.client.PostMessage(channelID,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(fmt.Sprintf("Authentication required for %s — code: %s", account, userCode), false),
	)
	if err != nil {
		log.Printf("slackbot/cred: failed to post device code notification: %v", err)
		return
	}

	// Track this as a device code thread so we can post completion when refreshed.
	w.deviceCodeThreadsMu.Lock()
	w.deviceCodeThreads[ts] = account
	w.deviceCodeThreadsMu.Unlock()

	// Also track in reauthThreads for rate-limiting.
	w.reauthThreadsMu.Lock()
	w.reauthThreads[ts] = reauthInfo{
		account:   account,
		channelID: channelID,
		postedAt:  time.Now(),
	}
	w.reauthThreadsMu.Unlock()

	log.Printf("slackbot/cred: posted device code notification for %s (code: %s, thread %s)", account, userCode, ts)
}

// notifyDeviceCodeComplete posts a success message in device code threads
// when coopmux's background polling completes authentication.
func (w *CoopCredWatcher) notifyDeviceCodeComplete(account string) {
	w.deviceCodeThreadsMu.Lock()
	var threadsToComplete []string
	for ts, acct := range w.deviceCodeThreads {
		if acct == account {
			threadsToComplete = append(threadsToComplete, ts)
			delete(w.deviceCodeThreads, ts)
		}
	}
	w.deviceCodeThreadsMu.Unlock()

	if len(threadsToComplete) == 0 {
		return
	}

	channelID := w.bot.channelID
	for _, ts := range threadsToComplete {
		w.bot.postThreadReply(channelID, ts,
			fmt.Sprintf(":white_check_mark: Authentication successful for account *%s*. Credentials distributed to sessions.", account))

		// Clean up reauth tracking.
		w.reauthThreadsMu.Lock()
		delete(w.reauthThreads, ts)
		w.completedThreads[ts] = true
		w.reauthThreadsMu.Unlock()
	}

	log.Printf("slackbot/cred: device code auth completed for %s (%d threads notified)", account, len(threadsToComplete))
}

// postChannelMessage posts a message to a channel (not in a thread).
func (w *CoopCredWatcher) postChannelMessage(channelID, text string) {
	_, _, err := w.bot.client.PostMessage(channelID,
		slack.MsgOptionText(text, false),
	)
	if err != nil {
		log.Printf("slackbot/cred: failed to post channel message: %v", err)
	}
}

// Close drains the subscription and closes the NATS connection.
func (w *CoopCredWatcher) Close() {
	if w.sub != nil {
		_ = w.sub.Unsubscribe()
		w.sub = nil
	}
	if w.conn != nil {
		w.conn.Close()
		w.conn = nil
	}
}
