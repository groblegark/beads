package configfile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const ConfigFileName = "metadata.json"

type Config struct {
	Database    string `json:"database"`
	JSONLExport string `json:"jsonl_export,omitempty"`
	Backend     string `json:"backend,omitempty"` // Always "dolt" (sqlite removed)

	// Dolt SQL server connection configuration.
	// bd-daemon connects to a dolt sql-server via TCP.
	DoltServerHost     string `json:"dolt_server_host,omitempty"`     // Default: 127.0.0.1
	DoltServerPort     int    `json:"dolt_server_port,omitempty"`     // Default: 3307
	DoltServerUser     string `json:"dolt_server_user,omitempty"`     // Default: root
	DoltServerPassword string `json:"dolt_server_password,omitempty"` // Or use BEADS_DOLT_PASSWORD env
	DoltDatabase       string `json:"dolt_database,omitempty"`        // SQL database name (default: beads)

	// Deletions configuration
	DeletionsRetentionDays int `json:"deletions_retention_days,omitempty"` // 0 means use default (3 days)

	// Stale closed issues check configuration
	// 0 = disabled (default), positive = threshold in days
	StaleClosedIssuesDays int `json:"stale_closed_issues_days,omitempty"`

	// Routing configuration
	// When false, disables prefix-based routing to multiple databases.
	// With single central database, routing is not needed.
	// nil/missing = enabled (for backwards compatibility), explicit false = disabled
	RoutingEnabled *bool `json:"routing_enabled,omitempty"`

	// Deprecated fields kept for JSON backwards compatibility when reading old configs.
	DoltRemoteURL string `json:"dolt_remote_url,omitempty"` // Never referenced in code
	LastBdVersion string `json:"last_bd_version,omitempty"` // Superseded by .local_version
}

func DefaultConfig() *Config {
	return &Config{
		Database:    "beads.db",
		JSONLExport: "issues.jsonl", // Canonical name (bd-6xd)
	}
}

// RemoteDefaultConfig returns defaults for remote mode (BD_DAEMON_HOST set).
// The daemon owns the database, so the CLI only needs backend type and
// sensible defaults for any code that inspects config. (bd-baoqj)
func RemoteDefaultConfig() *Config {
	return &Config{
		Backend:     BackendDolt,
		Database:    "dolt",
		JSONLExport: "issues.jsonl",
	}
}

func ConfigPath(beadsDir string) string {
	return filepath.Join(beadsDir, ConfigFileName)
}

// Load reads metadata.json from beadsDir.
//
// In remote mode (BD_DAEMON_HOST set), returns a default Dolt config without
// touching the filesystem. Remote CLI clients are thin RPC wrappers — the
// daemon owns the database, so metadata.json is irrelevant. (bd-baoqj)
func Load(beadsDir string) (*Config, error) {
	// Remote mode: return defaults — no filesystem needed.
	if os.Getenv("BD_DAEMON_HOST") != "" {
		return RemoteDefaultConfig(), nil
	}

	configPath := ConfigPath(beadsDir)

	data, err := os.ReadFile(configPath) // #nosec G304 - controlled path from config
	if os.IsNotExist(err) {
		// Try legacy config.json location (migration path)
		legacyPath := filepath.Join(beadsDir, "config.json")
		data, err = os.ReadFile(legacyPath) // #nosec G304 - controlled path from config
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading legacy config: %w", err)
		}

		// Migrate: parse legacy config, save as metadata.json, remove old file
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("parsing legacy config: %w", err)
		}

		// Save to new location
		if err := cfg.Save(beadsDir); err != nil {
			return nil, fmt.Errorf("migrating config to metadata.json: %w", err)
		}

		// Remove legacy file (best effort)
		_ = os.Remove(legacyPath)

		return &cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Save(beadsDir string) error {
	// Remote mode: no local filesystem to write to. (bd-baoqj)
	if os.Getenv("BD_DAEMON_HOST") != "" {
		return nil
	}

	configPath := ConfigPath(beadsDir)

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

func (c *Config) DatabasePath(beadsDir string) string {
	// Dolt directory name (default: dolt)
	// Backward-compat: early configs wrote "beads.db" even when Backend=dolt.
	db := strings.TrimSpace(c.Database)
	if db == "" || db == "beads.db" {
		db = "dolt"
	}
	if filepath.IsAbs(db) {
		return db
	}
	return filepath.Join(beadsDir, db)
}

func (c *Config) JSONLPath(beadsDir string) string {
	if c.JSONLExport == "" {
		return filepath.Join(beadsDir, "issues.jsonl")
	}
	return filepath.Join(beadsDir, c.JSONLExport)
}

// DefaultDeletionsRetentionDays is the default retention period for deletion records.
const DefaultDeletionsRetentionDays = 3

// GetDeletionsRetentionDays returns the configured retention days, or the default if not set.
func (c *Config) GetDeletionsRetentionDays() int {
	if c.DeletionsRetentionDays <= 0 {
		return DefaultDeletionsRetentionDays
	}
	return c.DeletionsRetentionDays
}

// GetStaleClosedIssuesDays returns the configured threshold for stale closed issues.
// Returns 0 if disabled (the default), or a positive value if enabled.
func (c *Config) GetStaleClosedIssuesDays() int {
	if c.StaleClosedIssuesDays < 0 {
		return 0
	}
	return c.StaleClosedIssuesDays
}

// IsRoutingEnabled returns whether prefix-based routing is enabled.
// Returns true by default (for backwards compatibility), false if explicitly disabled.
func (c *Config) IsRoutingEnabled() bool {
	if c.RoutingEnabled == nil {
		return true // Default: enabled for backwards compatibility
	}
	return *c.RoutingEnabled
}

// Backend constants
const (
	BackendSQLite = "sqlite" // Deprecated: kept for config migration only
	BackendDolt   = "dolt"
)

// GetBackend returns the configured backend type, defaulting to Dolt.
func (c *Config) GetBackend() string {
	if c.Backend == "" {
		return BackendDolt
	}
	return c.Backend
}

// Default Dolt server settings
const (
	DefaultDoltServerHost = "127.0.0.1"
	DefaultDoltServerPort = 3307 // Use 3307 to avoid conflict with MySQL on 3306
	DefaultDoltServerUser = "root"
	DefaultDoltDatabase   = "beads"
)

// GetDoltServerHost returns the Dolt server host.
// Checks BEADS_DOLT_SERVER_HOST env var first, then config, then default.
func (c *Config) GetDoltServerHost() string {
	if h := os.Getenv("BEADS_DOLT_SERVER_HOST"); h != "" {
		return h
	}
	if c.DoltServerHost != "" {
		return c.DoltServerHost
	}
	return DefaultDoltServerHost
}

// GetDoltServerPort returns the Dolt server port.
// Checks BEADS_DOLT_SERVER_PORT env var first, then config, then default.
func (c *Config) GetDoltServerPort() int {
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil {
			return port
		}
	}
	if c.DoltServerPort > 0 {
		return c.DoltServerPort
	}
	return DefaultDoltServerPort
}

// GetDoltServerUser returns the Dolt server MySQL user.
// Checks BEADS_DOLT_SERVER_USER env var first, then config, then default.
func (c *Config) GetDoltServerUser() string {
	if u := os.Getenv("BEADS_DOLT_SERVER_USER"); u != "" {
		return u
	}
	if c.DoltServerUser != "" {
		return c.DoltServerUser
	}
	return DefaultDoltServerUser
}

// GetDoltDatabase returns the Dolt SQL database name.
// Checks BEADS_DOLT_SERVER_DATABASE env var first, then config, then default.
func (c *Config) GetDoltDatabase() string {
	if d := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); d != "" {
		return d
	}
	if c.DoltDatabase != "" {
		return c.DoltDatabase
	}
	return DefaultDoltDatabase
}
