//go:build !windows

package main

import (
	"fmt"
	"path/filepath"

	"github.com/mathias/neofs-mount/internal/config"
)

func buildNeoFSAttrsReport(targetPath string) (string, bool) {
	cfgPath := config.DefaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Sprintf("Error: could not load config from %s:\n%v", cfgPath, err), false
	}
	if cfg.Mountpoint == nil || *cfg.Mountpoint == "" {
		return "Error: mountpoint is not set in config.", false
	}
	if cfg.Endpoint == nil || cfg.WalletKey == nil {
		return "Error: endpoint or wallet key missing in config.", false
	}

	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}
	absMount, err := filepath.Abs(*cfg.Mountpoint)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}
	if !pathHasPrefixFold(absTarget, absMount) {
		return fmt.Sprintf("Not under NeoFS mount.\n\nMount: %s\nPath:  %s", absMount, absTarget), false
	}

	return pathBasedNeoFSAttrsReport(absTarget, absMount, cfg)
}
