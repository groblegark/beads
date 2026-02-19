//go:build !windows

package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/testutil/teststore"
)

// TestAnalyticsBurndown tests the Burndown endpoint via HTTP (beads-cqpj)
func TestAnalyticsBurndown(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "analytics-burndown-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "bd.sock")
	store := teststore.New(t)

	server := NewServer(socketPath, store, tmpDir, filepath.Join(tmpDir, "beads.db"))
	server.SetHTTPAddr("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	select {
	case <-server.WaitReady():
	case err := <-errChan:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server")
	}

	httpAddr := server.HTTPServer().Addr()

	// Create a few test issues via RPC
	createIssue := func(title, issueType string) {
		body, _ := json.Marshal(map[string]interface{}{
			"title":      title,
			"issue_type": issueType,
			"priority":   2,
		})
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/Create",
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create issue: %v", err)
		}
		resp.Body.Close()
	}

	createIssue("Test task 1", "task")
	createIssue("Test bug 1", "bug")
	createIssue("Test feature 1", "feature")

	t.Run("burndown_default_params", func(t *testing.T) {
		body := bytes.NewBufferString(`{}`)
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/Burndown",
			"application/json", body)
		if err != nil {
			t.Fatalf("failed to POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
		}

		var result BurndownResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		if result.Interval != "day" {
			t.Errorf("expected interval=day, got %s", result.Interval)
		}
		if result.StartDate == "" || result.EndDate == "" {
			t.Error("expected non-empty date range")
		}
		// We should have at least today's data point with 3 created issues
		if len(result.DataPoints) == 0 {
			t.Error("expected at least one data point")
		}

		// Check that cumulative open count is correct
		totalCreated := 0
		for _, dp := range result.DataPoints {
			totalCreated += dp.CreatedCount
		}
		if totalCreated < 3 {
			t.Errorf("expected at least 3 total created, got %d", totalCreated)
		}
	})

	t.Run("burndown_with_date_range", func(t *testing.T) {
		today := time.Now().Format("2006-01-02")
		body, _ := json.Marshal(BurndownArgs{
			StartDate: today,
			EndDate:   today,
			Interval:  "day",
		})
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/Burndown",
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
		}

		var result BurndownResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		if result.StartDate != today {
			t.Errorf("expected start_date=%s, got %s", today, result.StartDate)
		}
	})

	if err := server.Stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
}

// TestAnalyticsVelocity tests the Velocity endpoint via HTTP (beads-cqpj)
func TestAnalyticsVelocity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "analytics-velocity-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "bd.sock")
	store := teststore.New(t)

	server := NewServer(socketPath, store, tmpDir, filepath.Join(tmpDir, "beads.db"))
	server.SetHTTPAddr("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	select {
	case <-server.WaitReady():
	case err := <-errChan:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server")
	}

	httpAddr := server.HTTPServer().Addr()

	t.Run("velocity_default_params", func(t *testing.T) {
		body := bytes.NewBufferString(`{}`)
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/Velocity",
			"application/json", body)
		if err != nil {
			t.Fatalf("failed to POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
		}

		var result VelocityResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		if result.Interval != "week" {
			t.Errorf("expected interval=week, got %s", result.Interval)
		}
	})

	if err := server.Stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
}

// TestAnalyticsCycleTime tests the CycleTime endpoint via HTTP (beads-cqpj)
func TestAnalyticsCycleTime(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "analytics-cycletime-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "bd.sock")
	store := teststore.New(t)

	server := NewServer(socketPath, store, tmpDir, filepath.Join(tmpDir, "beads.db"))
	server.SetHTTPAddr("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	select {
	case <-server.WaitReady():
	case err := <-errChan:
		t.Fatalf("server failed to start: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server")
	}

	httpAddr := server.HTTPServer().Addr()

	t.Run("cycle_time_default_params", func(t *testing.T) {
		body := bytes.NewBufferString(`{}`)
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/CycleTime",
			"application/json", body)
		if err != nil {
			t.Fatalf("failed to POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
		}

		var result CycleTimeResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		if result.GroupBy != "overall" {
			t.Errorf("expected group_by=overall, got %s", result.GroupBy)
		}
	})

	t.Run("cycle_time_group_by_type", func(t *testing.T) {
		body, _ := json.Marshal(CycleTimeArgs{GroupBy: "type"})
		resp, err := http.Post("http://"+httpAddr+"/bd.v1.BeadsService/CycleTime",
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to POST: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(bodyBytes))
		}

		var result CycleTimeResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		if result.GroupBy != "type" {
			t.Errorf("expected group_by=type, got %s", result.GroupBy)
		}
	})

	if err := server.Stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
}
