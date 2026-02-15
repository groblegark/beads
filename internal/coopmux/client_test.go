package coopmux

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReauthInitiate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/credentials/reauth" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("missing or wrong auth header: %s", r.Header.Get("Authorization"))
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["account"] != "my-account" {
			t.Errorf("expected account=my-account, got %q", body["account"])
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ReauthSession{
			Account: "my-account",
			AuthURL: "https://auth.example.com/device?code=ABCD",
			State:   "state-123",
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, WithToken("test-token"), WithHTTPClient(server.Client()))
	session, err := c.ReauthInitiate(context.Background(), "my-account")
	if err != nil {
		t.Fatalf("ReauthInitiate: %v", err)
	}
	if session.Account != "my-account" {
		t.Errorf("Account = %q, want %q", session.Account, "my-account")
	}
	if session.AuthURL != "https://auth.example.com/device?code=ABCD" {
		t.Errorf("AuthURL = %q", session.AuthURL)
	}
	if session.State != "state-123" {
		t.Errorf("State = %q, want %q", session.State, "state-123")
	}
}

func TestReauthExchange_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/credentials/exchange" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["state"] != "state-123" {
			t.Errorf("expected state=state-123, got %q", body["state"])
		}
		if body["code"] != "auth-code-xyz" {
			t.Errorf("expected code=auth-code-xyz, got %q", body["code"])
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewClient(server.URL, WithHTTPClient(server.Client()))
	err := c.ReauthExchange(context.Background(), "state-123", "auth-code-xyz")
	if err != nil {
		t.Fatalf("ReauthExchange: %v", err)
	}
}

func TestReauthExchange_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid state token"))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithHTTPClient(server.Client()))
	err := c.ReauthExchange(context.Background(), "bad-state", "code")
	if err == nil {
		t.Fatal("expected error for HTTP 400")
	}
	cerr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	if cerr.StatusCode != 400 {
		t.Errorf("StatusCode = %d, want 400", cerr.StatusCode)
	}
	if cerr.Body != "invalid state token" {
		t.Errorf("Body = %q", cerr.Body)
	}
}

func TestHealth_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithHTTPClient(server.Client()))
	err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestHealth_Unreachable(t *testing.T) {
	c := NewClient("http://127.0.0.1:1")
	err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestReauthInitiate_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	c := NewClient(server.URL, WithHTTPClient(server.Client()))
	_, err := c.ReauthInitiate(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
}

func TestWithToken_SetsAuthHeader(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewClient(server.URL, WithToken("my-secret"), WithHTTPClient(server.Client()))
	c.Health(context.Background())
	if gotHeader != "Bearer my-secret" {
		t.Errorf("Authorization header = %q, want %q", gotHeader, "Bearer my-secret")
	}
}

func TestNoToken_NoAuthHeader(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewClient(server.URL, WithHTTPClient(server.Client()))
	c.Health(context.Background())
	if gotHeader != "" {
		t.Errorf("Authorization header should be empty, got %q", gotHeader)
	}
}
