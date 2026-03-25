package config

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/pelletier/go-toml/v2"
)

type FileConfig struct {
	Endpoint   *string `toml:"endpoint,omitempty"`
	WalletKey  *string `toml:"wallet_key,omitempty"`
	Mountpoint *string `toml:"mountpoint,omitempty"`

	ReadOnly   *bool `toml:"read_only,omitempty"`
	AutoMount  *bool `toml:"auto_mount,omitempty"`
	RunAtLogin *bool `toml:"run_at_login,omitempty"`

	Network     *string `toml:"network,omitempty"`      // "mainnet" or "testnet"
	RPCEndpoint *string `toml:"rpc_endpoint,omitempty"` // overrides default RPC URL

	CacheDir  *string `toml:"cache_dir,omitempty"`
	CacheSize *int64   `toml:"cache_size,omitempty"`

	LogLevel *string `toml:"log_level,omitempty"`

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
	default:
		// Linux: use XDG base dir if available.
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		return filepath.Join(xdg, "neofs-mount", "config.toml")
	}
}

func Load(path string) (*FileConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc FileConfig
	if err := toml.Unmarshal(b, &fc); err != nil {
		return nil, err
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
