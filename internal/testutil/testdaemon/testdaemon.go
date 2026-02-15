//go:build !windows

// Package testdaemon provides a test helper that starts an in-process daemon
// HTTP RPC server backed by an ephemeral Dolt database. Tests get a daemon URL
// they can use directly or set as BD_DAEMON_HOST. Teardown is automatic via
// t.Cleanup.
//
// Usage:
//
//	func TestSomething(t *testing.T) {
//	    d := testdaemon.Start(t)
//	    resp, err := http.Get(d.URL + "/health")
//	    // ... use d.URL for HTTP RPC calls
//	}
package testdaemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil/teststore"
)

// Daemon holds the running test daemon and its resources.
type Daemon struct {
	// URL is the HTTP base URL for the daemon (e.g., "http://127.0.0.1:54321").
	URL string

	// SocketPath is the Unix domain socket path for the RPC server.
	SocketPath string

	// Store is the underlying Dolt storage instance.
	Store storage.Storage

	// Server is the RPC server (useful for direct method calls in tests).
	Server *rpc.Server
}

// Start creates an isolated Dolt store, starts an in-process RPC server with
// HTTP enabled on an ephemeral port, waits for it to be ready, and registers
// cleanup via t.Cleanup. Returns a Daemon with the live URL.
//
// If the `dolt` binary is not in PATH the test is skipped.
func Start(t testing.TB) *Daemon {
	t.Helper()
	return StartWithStore(t, teststore.New(t))
}

// StartWithStore is like Start but uses the provided store instead of creating
// a new one. This is useful when you need to pre-populate data before starting
// the daemon.
func StartWithStore(t testing.TB, store storage.Storage) *Daemon {
	t.Helper()

	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "bd.sock")
	dbPath := store.Path()

	server := rpc.NewServer(socketPath, store, tmpDir, dbPath)
	server.SetHTTPAddr("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Wait for server to be ready or fail.
	select {
	case <-server.WaitReady():
		// Server is listening.
	case err := <-errChan:
		cancel()
		t.Fatalf("testdaemon: server failed to start: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("testdaemon: timeout waiting for server to start")
	}

	httpServer := server.HTTPServer()
	if httpServer == nil {
		cancel()
		t.Fatal("testdaemon: HTTP server not active after start")
	}

	url := fmt.Sprintf("http://%s", httpServer.Addr())

	t.Cleanup(func() {
		cancel()
		_ = server.Stop()
		// Wait for Start() goroutine to finish to avoid goroutine leaks.
		select {
		case <-errChan:
		case <-time.After(5 * time.Second):
			fmt.Fprintf(os.Stderr, "testdaemon: warning: server did not stop within 5s\n")
		}
	})

	return &Daemon{
		URL:        url,
		SocketPath: socketPath,
		Store:      store,
		Server:     server,
	}
}
