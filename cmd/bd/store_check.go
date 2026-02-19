package main

import "fmt"

// ensureStoreActive checks that the storage backend is initialized.
// In daemon-only mode, the store is set up by PersistentPreRun via
// daemon RPC. This function validates that initialization succeeded.
func ensureStoreActive() error {
	// If daemon is connected, commands use daemon RPC, not direct storage.
	if getDaemonClient() != nil {
		return nil
	}

	if getStore() != nil {
		return nil
	}

	return fmt.Errorf("no storage backend available; ensure the daemon is running with 'bd daemon start'")
}
