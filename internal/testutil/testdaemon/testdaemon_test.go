//go:build !windows

package testdaemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestStart(t *testing.T) {
	d := Start(t)

	if d.URL == "" {
		t.Fatal("expected non-empty URL")
	}
	if d.Store == nil {
		t.Fatal("expected non-nil Store")
	}
	if d.Server == nil {
		t.Fatal("expected non-nil Server")
	}

	// Verify health endpoint is reachable.
	resp, err := http.Get(d.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var health map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}
	if health["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got %v", health["status"])
	}
}

func TestStartRPCEndpoint(t *testing.T) {
	d := Start(t)

	// List issues via Connect-RPC HTTP endpoint (should return empty list).
	body := bytes.NewBufferString(`{"status":"open"}`)
	resp, err := http.Post(d.URL+"/bd.v1.BeadsService/List", "application/json", body)
	if err != nil {
		t.Fatalf("POST List failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
	}
}

func TestClient(t *testing.T) {
	d := Start(t)
	client := d.Client(t)

	// Verify the client can reach the daemon.
	health, err := client.Health()
	if err != nil {
		t.Fatalf("Health RPC failed: %v", err)
	}
	if health.Status != "healthy" {
		t.Errorf("expected status 'healthy', got %q", health.Status)
	}
}
