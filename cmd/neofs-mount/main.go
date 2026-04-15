package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/mathias/neofs-mount/internal/config"
	"github.com/mathias/neofs-mount/internal/fs"
	"github.com/mathias/neofs-mount/internal/mountutils"
)

var version = "dev"
var buildTime = "unknown"

type appConfig struct {
	endpoint   string
	walletKey  string
	mountpoint string

	readOnly   bool
	traceReads bool

	cacheDir  string
	cacheSize int64

	logLevel string

	printVersion bool
	help         bool
	printInfo    bool

	configPath string

	ignoreContainerIDs []string

	auditLogPath    string
	auditFromConfig bool
}

func main() {
	cfg := appConfig{}

	// Track which flags user explicitly set (affects how we merge config + CLI).
	explicit := map[string]bool{}

	// Custom usage to show a compact, copy/paste-friendly help message.
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nExamples:")
		fmt.Fprintln(os.Stderr, "  ./bin/neofs-mount \\")
		fmt.Fprintln(os.Stderr, "    --endpoint s03.neofs.devenv:8080 \\")
		fmt.Fprintln(os.Stderr, "    --wallet-key /path/to/wallet.key \\")
		fmt.Fprintln(os.Stderr, "    --mountpoint /tmp/neofs \\")
		fmt.Fprintln(os.Stderr, "    --cache-dir /tmp/neofs-cache")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "Config:\n  default: %s\n  override: --config <path>\n", config.DefaultConfigPath())
	}

	flag.StringVar(&cfg.configPath, "config", config.DefaultConfigPath(), "Path to config file (TOML)")
	flag.StringVar(&cfg.endpoint, "endpoint", "", "NeoFS endpoint host:port")
	flag.StringVar(&cfg.walletKey, "wallet-key", "", "NeoFS wallet key: WIF string or path to file containing WIF")
	flag.StringVar(&cfg.mountpoint, "mountpoint", "", "Mountpoint directory")
	flag.BoolVar(&cfg.readOnly, "read-only", false, "Mount read-only")
	flag.BoolVar(&cfg.traceReads, "trace-reads", false, "Log detailed Linux read-path timing for profiling")
	flag.StringVar(&cfg.cacheDir, "cache-dir", "", "Cache directory (default: OS temp dir)")
	flag.Int64Var(&cfg.cacheSize, "cache-size", 1<<30, "Cache size in bytes (default: 1GiB)")
	flag.StringVar(&cfg.logLevel, "log-level", "info", "Log level: debug|info|warn|error")
	flag.BoolVar(&cfg.printVersion, "version", false, "Print version and exit")
	flag.BoolVar(&cfg.help, "help", false, "Show help")
	flag.BoolVar(&cfg.help, "h", false, "Show help")
	flag.BoolVar(&cfg.printInfo, "info", false, "Print mount info and exit")
	flag.Parse()

	flag.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
	})

	if cfg.printVersion {
		fmt.Printf("%s (built %s)\n", version, buildTime)
		return
	}
	if cfg.help {
		flag.Usage()
		return
	}
	if cfg.printInfo {
		fmt.Printf("neofs-mount %s (built %s)\n", version, buildTime)
		fmt.Println("Backend: NeoFS object storage")
		fmt.Println("Mount: FUSE filesystem")
		fmt.Println("Layout: / lists containers; /<container>/<path> maps to object FilePath prefixes")
		fmt.Println("Writes: upload-on-close (best-effort overwrite via copy+delete)")
		fmt.Println("Examples:")
		flag.Usage()
		return
	}

	if err := mergeConfig(&cfg, explicit); err != nil {
		logErr(err)
		os.Exit(2)
	}

	log := mountutils.NewLogger(cfg.logLevel)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, log, cfg); err != nil {
		// Provide usage for common validation errors so "missing --endpoint" isn't just a log line.
		if errors.Is(err, errUsage) {
			flag.Usage()
		}
		log.Error("fatal", "err", err)
		os.Exit(2)
	}
}

var errUsage = errors.New("usage")

func run(ctx context.Context, log *slog.Logger, cfg appConfig) error {
	if cfg.endpoint == "" {
		return fmt.Errorf("%w: --endpoint is required", errUsage)
	}
	if cfg.walletKey == "" {
		return fmt.Errorf("%w: --wallet-key is required", errUsage)
	}
	if cfg.mountpoint == "" {
		return fmt.Errorf("%w: --mountpoint is required", errUsage)
	}

	mp, err := filepath.Abs(cfg.mountpoint)
	if err != nil {
		return fmt.Errorf("resolve mountpoint: %w", err)
	}
	if err := mountutils.EnsureDir(mp, 0o755); err != nil {
		return fmt.Errorf("mountpoint: %w", err)
	}

	cacheDir := cfg.cacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "neofs-mount-cache")
	}
	if err := mountutils.EnsureDir(cacheDir, 0o755); err != nil {
		return fmt.Errorf("cache dir: %w", err)
	}

	log.Info("starting", "os", runtime.GOOS, "arch", runtime.GOARCH)
	log.Info("mounting", "mountpoint", mp, "read_only", cfg.readOnly, "cache_dir", cacheDir, "trace_reads", cfg.traceReads)

	auditPath := config.DefaultAuditLogPath()
	if cfg.auditFromConfig {
		auditPath = cfg.auditLogPath
	}

	mnt, err := fs.Mount(fs.MountParams{
		Logger:             log,
		Endpoint:           cfg.endpoint,
		WalletKey:          cfg.walletKey,
		Mountpoint:         mp,
		ReadOnly:           cfg.readOnly,
		TraceReads:         cfg.traceReads,
		CacheDir:           cacheDir,
		CacheSize:          cfg.cacheSize,
		IgnoreContainerIDs: cfg.ignoreContainerIDs,
		AuditLogPath:       auditPath,
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = mnt.Unmount()
	}()

	<-ctx.Done()

	log.Info("shutting down", "reason", ctx.Err())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return mnt.Shutdown(shutdownCtx)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

func ensureDir(path string, perm os.FileMode) error {
	st, err := os.Stat(path)
	if err == nil {
		if st.IsDir() {
			return nil
		}
		return fmt.Errorf("path exists but is not a directory: %s", path)
	}
	// If a previous FUSE mount crashed, Linux can return ENOTCONN here.
	// Try to unmount and re-check.
	if isNotConn(err) {
		unmErr := tryUnmount(path)
		if st2, err2 := os.Stat(path); err2 == nil && st2.IsDir() {
			return nil
		}
		// If it still fails, surface a helpful error.
		help := staleUnmountHelp(path)
		if unmErr != nil {
			return fmt.Errorf("mountpoint is in a stale FUSE state (transport endpoint is not connected): %s (auto-unmount failed: %v)\n%s", path, unmErr, help)
		}
		return fmt.Errorf("mountpoint is in a stale FUSE state (transport endpoint is not connected): %s\n%s", path, help)
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat: %w", err)
	}
	if err := os.MkdirAll(path, perm); err != nil {
		// In case of a race or strange filesystem behavior, re-check.
		if st2, err2 := os.Stat(path); err2 == nil && st2.IsDir() {
			return nil
		}
		return fmt.Errorf("mkdir: %w", err)
	}
	return nil
}

func isNotConn(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return errors.Is(pe.Err, syscall.ENOTCONN)
	}
	return errors.Is(err, syscall.ENOTCONN)
}

func mergeConfig(cfg *appConfig, explicit map[string]bool) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if cfg.configPath == "" {
		return nil
	}

	_, statErr := os.Stat(cfg.configPath)
	if statErr != nil {
		// If user explicitly provided --config and it doesn't exist, fail loudly.
		if explicit["config"] {
			if os.IsNotExist(statErr) {
				return fmt.Errorf("config: file not found: %s", cfg.configPath)
			}
			return fmt.Errorf("config: stat %s: %w", cfg.configPath, statErr)
		}
		// Default config path is optional.
		return nil
	}

	fc, err := config.Load(cfg.configPath)
	if err != nil {
		return fmt.Errorf("config: load %s: %w", cfg.configPath, err)
	}

	// Helper: set only if CLI flag wasn't explicit and config field is present.
	if !explicit["endpoint"] && fc.Endpoint != nil {
		cfg.endpoint = *fc.Endpoint
	}
	if !explicit["wallet-key"] && fc.WalletKey != nil {
		cfg.walletKey = *fc.WalletKey
	}
	if !explicit["mountpoint"] && fc.Mountpoint != nil {
		cfg.mountpoint = *fc.Mountpoint
	}
	if !explicit["read-only"] && fc.ReadOnly != nil {
		cfg.readOnly = *fc.ReadOnly
	}
	if !explicit["cache-dir"] && fc.CacheDir != nil {
		cfg.cacheDir = *fc.CacheDir
	}
	if !explicit["cache-size"] && fc.CacheSize != nil {
		cfg.cacheSize = *fc.CacheSize
	}
	if !explicit["log-level"] && fc.LogLevel != nil {
		cfg.logLevel = *fc.LogLevel
	}
	if len(cfg.ignoreContainerIDs) == 0 && len(fc.IgnoreContainerIDs) > 0 {
		// Only from config for now (we don't expose a CLI flag yet).
		cfg.ignoreContainerIDs = fc.IgnoreContainerIDs
	}

	cfg.auditLogPath = config.ResolveAuditLogPath(fc)
	cfg.auditFromConfig = true

	return nil
}

func logErr(err error) {
	// Avoid introducing ordering dependencies on logger; this is only for config errors.
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
}
