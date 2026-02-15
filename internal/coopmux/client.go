// Package coopmux provides a Go HTTP client for the coopmux credential
// multiplexer API. Coopmux is a service that handles OAuth credential lifecycle
// (refresh, reauth, distribution) across multiple coop agent pods.
//
// Usage:
//
//	c := coopmux.NewClient("http://coopmux:9800", coopmux.WithToken("secret"))
//	session, err := c.ReauthInitiate(ctx, "my-account")
//	// user visits session.AuthURL and gets authorization code
//	err = c.ReauthExchange(ctx, session.State, code)
package coopmux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is an HTTP client for a coopmux credential multiplexer instance.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithToken sets the Bearer auth token for coopmux API requests.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// NewClient creates a coopmux client for the service at baseURL
// (e.g. "http://coopmux:9800").
func NewClient(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ReauthSession is returned by ReauthInitiate with the OAuth flow details.
type ReauthSession struct {
	Account string `json:"account"`
	AuthURL string `json:"auth_url"`
	State   string `json:"state"`
}

// ReauthInitiate starts an OAuth reauth flow for the given account.
// Returns the auth URL the user must visit and the state token needed
// for ReauthExchange.
func (c *Client) ReauthInitiate(ctx context.Context, account string) (*ReauthSession, error) {
	var session ReauthSession
	err := c.postJSON(ctx, "/api/v1/credentials/reauth", map[string]string{
		"account": account,
	}, &session)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// ReauthExchange completes the OAuth reauth flow by submitting the
// authorization code obtained after the user visits the auth URL.
func (c *Client) ReauthExchange(ctx context.Context, state, code string) error {
	return c.postJSON(ctx, "/api/v1/credentials/exchange", map[string]string{
		"state": state,
		"code":  code,
	}, nil)
}

// Health checks if the coopmux service is reachable.
func (c *Client) Health(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("coopmux: health check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return c.parseError(resp)
	}
	return nil
}

// --- HTTP helpers ---

// Error is returned when the coopmux API returns an error status code.
type Error struct {
	StatusCode int
	Body       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("coopmux: HTTP %d: %s", e.StatusCode, e.Body)
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

func (c *Client) postJSON(ctx context.Context, path string, body interface{}, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("coopmux: marshal: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := c.newRequest(ctx, http.MethodPost, path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("coopmux: POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return c.parseError(resp)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("coopmux: POST %s: decode: %w", path, err)
		}
	}
	return nil
}

func (c *Client) parseError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return &Error{
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(body)),
	}
}
