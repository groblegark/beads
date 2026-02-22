package configfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Database != "beads.db" {
		t.Errorf("Database = %q, want beads.db", cfg.Database)
	}

	// bd-6xd: issues.jsonl is the canonical name
	if cfg.JSONLExport != "issues.jsonl" {
		t.Errorf("JSONLExport = %q, want issues.jsonl", cfg.JSONLExport)
	}
}

func TestLoadSaveRoundtrip(t *testing.T) {
	t.Setenv("BD_DAEMON_HOST", "") // Ensure local mode for filesystem roundtrip
	t.Setenv("BD_DAEMON_HTTP_URL", "") // Ensure local mode (gt-6fe)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	cfg := DefaultConfig()

	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	loaded, err := Load(beadsDir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if loaded == nil {
		t.Fatal("Load() returned nil config")
	}

	if loaded.Database != cfg.Database {
		t.Errorf("Database = %q, want %q", loaded.Database, cfg.Database)
	}

	if loaded.JSONLExport != cfg.JSONLExport {
		t.Errorf("JSONLExport = %q, want %q", loaded.JSONLExport, cfg.JSONLExport)
	}
}

func TestLoadNonexistent(t *testing.T) {
	t.Setenv("BD_DAEMON_HOST", "")    // Ensure local mode for filesystem test
	t.Setenv("BD_DAEMON_HTTP_URL", "") // Ensure local mode (gt-6fe)
	tmpDir := t.TempDir()

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load() returned error for nonexistent config: %v", err)
	}

	if cfg != nil {
		t.Errorf("Load() = %v, want nil for nonexistent config", cfg)
	}
}

func TestDatabasePath_Dolt(t *testing.T) {
	beadsDir := "/home/user/project/.beads"

	t.Run("explicit dolt dir", func(t *testing.T) {
		cfg := &Config{Database: "dolt", Backend: BackendDolt}
		got := cfg.DatabasePath(beadsDir)
		want := filepath.Join(beadsDir, "dolt")
		if got != want {
			t.Errorf("DatabasePath() = %q, want %q", got, want)
		}
	})

	t.Run("backward compat: dolt backend with beads.db field", func(t *testing.T) {
		cfg := &Config{Database: "beads.db", Backend: BackendDolt}
		got := cfg.DatabasePath(beadsDir)
		want := filepath.Join(beadsDir, "dolt")
		if got != want {
			t.Errorf("DatabasePath() = %q, want %q", got, want)
		}
	})
}

func TestJSONLPath(t *testing.T) {
	beadsDir := "/home/user/project/.beads"

	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "default",
			cfg:  &Config{JSONLExport: "issues.jsonl"},
			want: filepath.Join(beadsDir, "issues.jsonl"),
		},
		{
			name: "custom",
			cfg:  &Config{JSONLExport: "custom.jsonl"},
			want: filepath.Join(beadsDir, "custom.jsonl"),
		},
		{
			name: "empty falls back to default",
			cfg:  &Config{JSONLExport: ""},
			want: filepath.Join(beadsDir, "issues.jsonl"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.JSONLPath(beadsDir)
			if got != tt.want {
				t.Errorf("JSONLPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigPath(t *testing.T) {
	beadsDir := "/home/user/project/.beads"
	got := ConfigPath(beadsDir)
	want := filepath.Join(beadsDir, "metadata.json")

	if got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestGetDeletionsRetentionDays(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want int
	}{
		{
			name: "zero uses default",
			cfg:  &Config{DeletionsRetentionDays: 0},
			want: DefaultDeletionsRetentionDays,
		},
		{
			name: "negative uses default",
			cfg:  &Config{DeletionsRetentionDays: -5},
			want: DefaultDeletionsRetentionDays,
		},
		{
			name: "custom value",
			cfg:  &Config{DeletionsRetentionDays: 14},
			want: 14,
		},
		{
			name: "minimum value 1",
			cfg:  &Config{DeletionsRetentionDays: 1},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.GetDeletionsRetentionDays()
			if got != tt.want {
				t.Errorf("GetDeletionsRetentionDays() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestDoltServerConfig tests the Dolt server connection configuration
func TestDoltServerConfig(t *testing.T) {
	t.Run("GetDoltServerHost", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  *Config
			want string
		}{
			{
				name: "empty defaults to 127.0.0.1",
				cfg:  &Config{},
				want: DefaultDoltServerHost,
			},
			{
				name: "custom host",
				cfg:  &Config{DoltServerHost: "192.168.1.100"},
				want: "192.168.1.100",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.cfg.GetDoltServerHost()
				if got != tt.want {
					t.Errorf("GetDoltServerHost() = %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("GetDoltServerPort", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  *Config
			want int
		}{
			{
				name: "zero defaults to 3307",
				cfg:  &Config{},
				want: DefaultDoltServerPort,
			},
			{
				name: "custom port",
				cfg:  &Config{DoltServerPort: 13306},
				want: 13306,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.cfg.GetDoltServerPort()
				if got != tt.want {
					t.Errorf("GetDoltServerPort() = %d, want %d", got, tt.want)
				}
			})
		}
	})

	t.Run("GetDoltServerUser", func(t *testing.T) {
		tests := []struct {
			name string
			cfg  *Config
			want string
		}{
			{
				name: "empty defaults to root",
				cfg:  &Config{},
				want: DefaultDoltServerUser,
			},
			{
				name: "custom user",
				cfg:  &Config{DoltServerUser: "beads"},
				want: "beads",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := tt.cfg.GetDoltServerUser()
				if got != tt.want {
					t.Errorf("GetDoltServerUser() = %q, want %q", got, tt.want)
				}
			})
		}
	})
}

// TestDoltServerConfigRoundtrip tests that server config survives save/load
func TestDoltServerConfigRoundtrip(t *testing.T) {
	t.Setenv("BD_DAEMON_HOST", "") // Ensure local mode for filesystem roundtrip
	t.Setenv("BD_DAEMON_HTTP_URL", "") // Ensure local mode (gt-6fe)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	cfg := &Config{
		Database:       "dolt",
		Backend:        BackendDolt,
		DoltServerHost: "192.168.1.50",
		DoltServerPort: 13306,
		DoltServerUser: "beads_admin",
	}

	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	loaded, err := Load(beadsDir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if loaded.GetDoltServerHost() != "192.168.1.50" {
		t.Errorf("GetDoltServerHost() = %q, want %q", loaded.GetDoltServerHost(), "192.168.1.50")
	}
	if loaded.GetDoltServerPort() != 13306 {
		t.Errorf("GetDoltServerPort() = %d, want %d", loaded.GetDoltServerPort(), 13306)
	}
	if loaded.GetDoltServerUser() != "beads_admin" {
		t.Errorf("GetDoltServerUser() = %q, want %q", loaded.GetDoltServerUser(), "beads_admin")
	}
}

// TestEnvVarOverrides tests env var overrides for getter methods
func TestEnvVarOverrides(t *testing.T) {
	t.Run("host env var overrides config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_HOST", "192.168.1.1")
		cfg := &Config{DoltServerHost: "10.0.0.1"}
		if got := cfg.GetDoltServerHost(); got != "192.168.1.1" {
			t.Errorf("GetDoltServerHost() = %q, want 192.168.1.1", got)
		}
	})

	t.Run("port env var overrides config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "3309")
		cfg := &Config{DoltServerPort: 3308}
		if got := cfg.GetDoltServerPort(); got != 3309 {
			t.Errorf("GetDoltServerPort() = %d, want 3309", got)
		}
	})

	t.Run("invalid port env var falls through to config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_PORT", "not-a-number")
		cfg := &Config{DoltServerPort: 3308}
		if got := cfg.GetDoltServerPort(); got != 3308 {
			t.Errorf("GetDoltServerPort() = %d, want 3308", got)
		}
	})

	t.Run("user env var overrides config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_USER", "envuser")
		cfg := &Config{DoltServerUser: "admin"}
		if got := cfg.GetDoltServerUser(); got != "envuser" {
			t.Errorf("GetDoltServerUser() = %q, want envuser", got)
		}
	})

	t.Run("database env var overrides config", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", "envdb")
		cfg := &Config{DoltDatabase: "mydb"}
		if got := cfg.GetDoltDatabase(); got != "envdb" {
			t.Errorf("GetDoltDatabase() = %q, want envdb", got)
		}
	})

	t.Run("database default", func(t *testing.T) {
		cfg := &Config{}
		if got := cfg.GetDoltDatabase(); got != DefaultDoltDatabase {
			t.Errorf("GetDoltDatabase() = %q, want %q", got, DefaultDoltDatabase)
		}
	})

	t.Run("database config value", func(t *testing.T) {
		cfg := &Config{DoltDatabase: "mydb"}
		if got := cfg.GetDoltDatabase(); got != "mydb" {
			t.Errorf("GetDoltDatabase() = %q, want mydb", got)
		}
	})
}

// TestLoadRemoteMode verifies that Load() returns defaults in remote mode
// (BD_DAEMON_HOST set) without touching the filesystem. (bd-baoqj)
func TestLoadRemoteMode(t *testing.T) {
	t.Setenv("BD_DAEMON_HOST", "https://daemon.example.com")

	// Load with a non-existent directory — should NOT fail because
	// remote mode skips filesystem entirely.
	cfg, err := Load("/nonexistent/path/.beads")
	if err != nil {
		t.Fatalf("Load() in remote mode returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load() in remote mode returned nil config")
	}
	if cfg.Backend != BackendDolt {
		t.Errorf("Backend = %q, want %q", cfg.Backend, BackendDolt)
	}
	if cfg.Database != "dolt" {
		t.Errorf("Database = %q, want \"dolt\"", cfg.Database)
	}
}

// TestSaveRemoteModeNoop verifies that Save() is a no-op in remote mode. (bd-baoqj)
func TestSaveRemoteModeNoop(t *testing.T) {
	t.Setenv("BD_DAEMON_HOST", "https://daemon.example.com")

	cfg := DefaultConfig()
	// Should succeed without error even though directory doesn't exist.
	if err := cfg.Save("/nonexistent/path/.beads"); err != nil {
		t.Fatalf("Save() in remote mode returned error: %v", err)
	}
}
