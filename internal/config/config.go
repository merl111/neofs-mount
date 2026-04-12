package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mathias/neofs-mount/internal/mountutils"
	"github.com/pelletier/go-toml/v2"
)

type FileConfig struct {
	Endpoint   *string `toml:"endpoint,omitempty"`
	WalletKey  *string `toml:"wallet_key,omitempty"`
	Mountpoint *string `toml:"mountpoint,omitempty"`

	ReadOnly   *bool `toml:"read_only,omitempty"`
	AutoMount  *bool `toml:"auto_mount,omitempty"`
	RunAtLogin *bool `toml:"run_at_login,omitempty"`

	// Windows: pin mount under File Explorer → This PC (shell registration).
	ShowInExplorer *bool `toml:"show_in_explorer,omitempty"`

	Network     *string `toml:"network,omitempty"`      // "mainnet" or "testnet"
	RPCEndpoint *string `toml:"rpc_endpoint,omitempty"` // overrides default RPC URL

	CacheDir  *string `toml:"cache_dir,omitempty"`
	CacheSize *int64  `toml:"cache_size,omitempty"`

	// Windows CfAPI: seconds to reuse in-memory directory listings (placeholder fetch). Zero = default 5s.
	FetchDirCacheSeconds *int `toml:"fetch_dir_cache_seconds,omitempty"`
	// Windows CfAPI: max object size (MiB) to pull into disk cache on first FetchData. Zero = default 64 MiB.
	HydrationCacheMaxObjectMB *int64 `toml:"hydration_cache_max_object_mb,omitempty"`

	LogLevel *string `toml:"log_level,omitempty"`

	// AuditLog, when false, disables the append-only operations log.
	// When nil or true, logging is enabled (see ResolveAuditLogPath).
	AuditLog *bool `toml:"audit_log,omitempty"`
	// AuditLogPath overrides the default JSONL path (non-empty).
	AuditLogPath *string `toml:"audit_log_path,omitempty"`

	IgnoreContainerIDs []string `toml:"ignore_container_ids,omitempty"`
}

func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}

	switch runtime.GOOS {
	case "darwin":
		// macOS: keep it under the standard home directory application support area.
		return filepath.Join(home, "Library", "Application Support", "neofs-mount", "config.toml")
	case "windows":
		// Windows: use %APPDATA%\neofs-mount\config.toml
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "neofs-mount", "config.toml")
	default:
		// Linux: use XDG base dir if available.
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		return filepath.Join(xdg, "neofs-mount", "config.toml")
	}
}

// DefaultUploadHistoryPath is the JSON file the tray app uses for persisted upload history.
func DefaultUploadHistoryPath() string {
	return filepath.Join(filepath.Dir(DefaultConfigPath()), "upload-history.json")
}

// DefaultAuditLogPath returns the default append-only audit file next to neofs-mount.log
// (same directory as [mountutils.LogFilePath]).
func DefaultAuditLogPath() string {
	return filepath.Join(filepath.Dir(mountutils.LogFilePath()), "neofs-audit.jsonl")
}

// ResolveAuditLogPath returns the audit log file path, or empty string if logging is disabled.
func ResolveAuditLogPath(fc *FileConfig) string {
	if fc != nil && fc.AuditLog != nil && !*fc.AuditLog {
		return ""
	}
	if fc != nil && fc.AuditLogPath != nil && *fc.AuditLogPath != "" {
		return *fc.AuditLogPath
	}
	return DefaultAuditLogPath()
}

func Load(path string) (*FileConfig, error) {
	if _, err := EnsureDefault(path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc FileConfig
	if err := toml.Unmarshal(b, &fc); err != nil {
		return nil, err
	}
	// Default AutoMount to true if not present.
	if fc.AutoMount == nil {
		t := true
		fc.AutoMount = &t
	}
	// Default ShowInExplorer to true if not present (Windows Explorer pin).
	if fc.ShowInExplorer == nil {
		t := true
		fc.ShowInExplorer = &t
	}
	// Operations audit log defaults to enabled.
	if fc.AuditLog == nil {
		t := true
		fc.AuditLog = &t
	}
	return &fc, nil
}

func Save(path string, fc *FileConfig) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	b, err := toml.Marshal(fc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

// DefaultConfigTemplate is a starter config written on first run when no config exists.
// It intentionally includes placeholders so the user can fill values via the tray UI.
const DefaultConfigTemplate = `# neoFS-mount config (auto-created)
#
# Default location:
#   Linux:  $XDG_CONFIG_HOME/neofs-mount/config.toml (or $HOME/.config/neofs-mount/config.toml)
#   macOS:  $HOME/Library/Application Support/neofs-mount/config.toml
#   Windows: %APPDATA%\neofs-mount\config.toml

# NeoFS endpoint, e.g. "s03.neofs.devenv:8080"
endpoint = ""

# Either a path to a file containing WIF, or a raw WIF string directly.
wallet_key = ""

# Linux-only (FUSE) directory mountpoint. On macOS the default integration is File Provider (Finder).
mountpoint = "/tmp/neofs"

read_only = false
auto_mount = true
run_at_login = false

# Optional cache settings (used for reads and to stage writes before upload).
cache_dir = ""
cache_size = 1073741824 # 1GiB

# Log level: debug|info|warn|error
log_level = "info"

ignore_container_ids = []
`

// EnsureDefault creates a starter config file if it doesn't already exist.
// It returns created=true when it successfully created a new file.
func EnsureDefault(path string) (created bool, err error) {
	if path == "" {
		return false, errors.New("empty config path")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		return false, nil
	} else if !os.IsNotExist(statErr) {
		return false, statErr
	}

	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
		return false, mkErr
	}

	// Create atomically; don't clobber if another process wins the race.
	f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if openErr != nil {
		if os.IsExist(openErr) {
			return false, nil
		}
		return false, openErr
	}
	defer f.Close()

	if _, werr := f.WriteString(DefaultConfigTemplate); werr != nil {
		_ = os.Remove(path)
		return false, werr
	}
	return true, nil
}
