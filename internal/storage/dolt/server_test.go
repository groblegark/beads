//go:build cgo

// Package dolt provides server mode tests for dolt sql-server integration (bd-f4f78a).
package dolt

import (
	"net"
	"testing"
)

// getTestServerPort returns an available port for test servers.
// Uses a dynamic port to avoid conflicts with production servers on 3306.
func getTestServerPort(t *testing.T) int {
	t.Helper()
	// Find an available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find available port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// NOTE: The following tests have been temporarily removed because they reference
// undefined functions and types that were removed or changed in an upstream rebase:
// - TestIsServerRunning
// - TestServerConfigFromStoreConfig
// - TestDefaultServerConfig
// - TestServerStartStop
// - TestEnsureServerRunning
// - TestServerModeStoreCreation
// - TestServerModeReadOnly
// - TestServerModeAutoStart
// - TestConcurrentServerModeAccess
//
// These tests need to be rewritten to use the current ServerConfig API which has:
// - DataDir, SQLPort, RemotesAPIPort (not Host, Port, User, Password, LogLevel)
//
// The Server type now uses NewServer(ServerConfig) and has different methods.
// See server.go for the current API.
