package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage/factory"
)

// kvPrefix matches the prefix used in cmd/bd/kv.go
const kvPrefix = "kv."

// CheckKVSyncStatus checks if KV data exists and reports sync status.
// KV data syncs via Dolt (the only storage backend).
func CheckKVSyncStatus(path string) DoctorCheck {
	_, beadsDir := getBackendAndBeadsDir(path)

	ctx := context.Background()
	store, err := factory.NewFromConfigWithOptions(ctx, beadsDir, factory.Options{AllowWithRemoteDaemon: true})
	if err != nil {
		return DoctorCheck{
			Name:     "KV Store Sync",
			Status:   StatusOK,
			Message:  "N/A (unable to open database)",
			Category: CategoryData,
		}
	}
	defer func() { _ = store.Close() }()

	// Get all config and count kv.* entries
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		return DoctorCheck{
			Name:     "KV Store Sync",
			Status:   StatusOK,
			Message:  "N/A (unable to read config)",
			Category: CategoryData,
		}
	}

	kvCount := 0
	for k := range allConfig {
		if strings.HasPrefix(k, kvPrefix) {
			kvCount++
		}
	}

	// No KV data - nothing to check
	if kvCount == 0 {
		return DoctorCheck{
			Name:     "KV Store Sync",
			Status:   StatusOK,
			Message:  "No KV data stored",
			Category: CategoryData,
		}
	}

	// KV data syncs via Dolt (dolt-native is the only mode)
	return DoctorCheck{
		Name:     "KV Store Sync",
		Status:   StatusOK,
		Message:  formatKVCount(kvCount) + " (syncs via Dolt)",
		Category: CategoryData,
	}
}

func formatKVCount(count int) string {
	if count == 1 {
		return "1 KV pair stored"
	}
	return fmt.Sprintf("%d KV pairs stored", count)
}
