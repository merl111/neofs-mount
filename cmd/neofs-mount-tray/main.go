package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/mathias/neofs-mount/internal/config"
	"github.com/mathias/neofs-mount/internal/fs"
	"github.com/mathias/neofs-mount/internal/mountutils"
	"github.com/mathias/neofs-mount/internal/neofs"
	"github.com/mathias/neofs-mount/internal/uploads"
)

var (
	activeMount   *fs.MountedFS
	mountContext  context.Context
	mountCancel   context.CancelFunc
	uploadTracker = uploads.New()
)

func main() {
	a := app.NewWithID("org.neofs.mount")

	desk, ok := a.(desktop.App)
	if !ok {
		fmt.Fprintln(os.Stderr, "Error: Tray menu is not supported on this platform")
		os.Exit(1)
	}

	a.SetIcon(resourceLogoPng)

	var menu *fyne.Menu
	var mountItem *fyne.MenuItem

	toggleMount := func() {
		if activeMount == nil {
			// Try Mount
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				notifyError(a, fmt.Errorf("could not load config: %w", err))
				return
			}
			if cfg.Endpoint == nil || cfg.WalletKey == nil || cfg.Mountpoint == nil {
				notifyError(a, fmt.Errorf("missing essential config. please open Settings"))
				return
			}

			if err := mountutils.EnsureDir(*cfg.Mountpoint, 0o755); err != nil {
				notifyError(a, err)
				return
			}

			cacheDir := ""
			if cfg.CacheDir != nil {
				cacheDir = *cfg.CacheDir
			}
			if cacheDir == "" {
				cacheDir = os.TempDir() + "/neofs-mount-cache"
			}
			if err := mountutils.EnsureDir(cacheDir, 0o755); err != nil {
				notifyError(a, err)
				return
			}

			logLvl := "info"
			if cfg.LogLevel != nil {
				logLvl = *cfg.LogLevel
			}
			log := mountutils.NewLogger(logLvl)

			cacheSize := int64(1 << 30) // 1GB default
			if cfg.CacheSize != nil {
				cacheSize = *cfg.CacheSize
			}
			ro := false
			if cfg.ReadOnly != nil {
				ro = *cfg.ReadOnly
			}

			var mntErr error
			activeMount, mntErr = fs.Mount(fs.MountParams{
				Logger:             log,
				Endpoint:           *cfg.Endpoint,
				WalletKey:          *cfg.WalletKey,
				Mountpoint:         *cfg.Mountpoint,
				ReadOnly:           ro,
				CacheDir:           cacheDir,
				CacheSize:          cacheSize,
				IgnoreContainerIDs: cfg.IgnoreContainerIDs,
				UploadTracker:      uploadTracker,
			})
			if mntErr != nil {
				notifyError(a, mntErr)
				return
			}

			mountContext, mountCancel = context.WithCancel(context.Background())
			mountItem.Label = "Unmount"
			desk.SetSystemTrayIcon(resourceLogoPng)
			if menu != nil {
				desk.SetSystemTrayMenu(menu)
			}

			// Wait for it in background, clearing state if it crashes
			go func() {
				<-mountContext.Done()
				if activeMount != nil {
					_ = activeMount.Unmount()
					_ = activeMount.Shutdown(context.Background())
					activeMount = nil
				}
				mountItem.Label = "Mount"
				if menu != nil {
					desk.SetSystemTrayMenu(menu)
				}
			}()
		} else {
			// Try Unmount
			if mountCancel != nil {
				mountCancel()
			}
		}
	}

	mountItem = fyne.NewMenuItem("Mount", toggleMount)

	var balanceItem *fyne.MenuItem
	settingsItem := fyne.NewMenuItem("Settings", func() {
		openSettingsWindow(a, desk, balanceItem, menu)
	})

	quitItem := fyne.NewMenuItem("Quit", func() {
		if mountCancel != nil {
			mountCancel()
			time.Sleep(100 * time.Millisecond) // brief wait for unmount
		}
		a.Quit()
	})

	topUpItem := fyne.NewMenuItem("Top Up", func() {
		openTopUpWindow(a)
	})

	balanceItem = fyne.NewMenuItem("Balance: ...", nil)
	balanceItem.Disabled = true

	uploadsItem := fyne.NewMenuItem("Uploads…", func() {
		openUploadsWindow(a)
	})

	menu = fyne.NewMenu("neoFS-mount",
		balanceItem,
		fyne.NewMenuItemSeparator(),
		mountItem,
		fyne.NewMenuItemSeparator(),
		uploadsItem,
		topUpItem,
		fyne.NewMenuItemSeparator(),
		settingsItem,
		fyne.NewMenuItemSeparator(),
		quitItem,
	)
	desk.SetSystemTrayMenu(menu)

	go updateBalance(desk, balanceItem, menu)

	if cfg, err := config.Load(config.DefaultConfigPath()); err == nil {
		if cfg.AutoMount != nil && *cfg.AutoMount {
			go toggleMount()
		}
	}

	a.Run()
}

func notifyError(a fyne.App, err error) {
	w := a.NewWindow("neoFS-mount Error")
	dialog.ShowError(err, w)
	w.Show()
}

func openUploadsWindow(a fyne.App) {
	w := a.NewWindow("Active Uploads")
	w.Resize(fyne.NewSize(520, 300))

	emptyLabel := widget.NewLabel("No active uploads.")
	emptyLabel.Alignment = fyne.TextAlignCenter

	content := container.NewVBox()

	formatBytes := func(b int64) string {
		switch {
		case b >= 1<<30:
			return fmt.Sprintf("%.2f GB", float64(b)/float64(1<<30))
		case b >= 1<<20:
			return fmt.Sprintf("%.2f MB", float64(b)/float64(1<<20))
		case b >= 1<<10:
			return fmt.Sprintf("%.2f KB", float64(b)/float64(1<<10))
		default:
			return fmt.Sprintf("%d B", b)
		}
	}

	refresh := func() {
		fyne.Do(func() {
			content.Objects = nil
			entries := uploadTracker.List()
			if len(entries) == 0 {
				content.Add(emptyLabel)
			} else {
				for _, e := range entries {
					e := e
					name := filepath.Base(e.Path)
					sent := e.Sent()
					total := e.TotalBytes
					elapsed := time.Since(e.Started).Round(time.Second)

					pct := float64(0)
					if total > 0 {
						pct = float64(sent) / float64(total)
					}

					bar := widget.NewProgressBar()
					bar.SetValue(pct)

					label := widget.NewLabel(fmt.Sprintf("%s  •  %s / %s  •  %s elapsed",
						name, formatBytes(sent), formatBytes(total), elapsed))
					label.TextStyle = fyne.TextStyle{Monospace: true}

					content.Add(container.NewVBox(label, bar))
				}
			}
			content.Refresh()
		})
	}

	refresh()
	w.SetContent(container.NewPadded(content))
	w.Show()

	// Refresh every 2 seconds while the window is visible.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if w == nil {
				return
			}
			refresh()
		}
	}()
}

func openSettingsWindow(a fyne.App, desk desktop.App, balanceItem *fyne.MenuItem, menu *fyne.Menu) {
	w := a.NewWindow("neoFS-mount Settings")

	cfgPath := config.DefaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		cfg = &config.FileConfig{}
	}

	strVal := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}

	endpointEntry := widget.NewEntry()
	endpointEntry.SetText(strVal(cfg.Endpoint))

	walletKeyEntry := widget.NewEntry()
	walletKeyEntry.SetText(strVal(cfg.WalletKey))

	mountpointEntry := widget.NewEntry()
	mountpointEntry.SetText(strVal(cfg.Mountpoint))

	cacheDirEntry := widget.NewEntry()
	cacheDirEntry.SetText(strVal(cfg.CacheDir))

	cacheSizeEntry := widget.NewEntry()
	if cfg.CacheSize != nil {
		cacheSizeEntry.SetText(fmt.Sprintf("%d", *cfg.CacheSize))
	} else {
		cacheSizeEntry.SetText("")
	}

	readOnlyCheck := widget.NewCheck("Mount as Read-Only", nil)
	if cfg.ReadOnly != nil && *cfg.ReadOnly {
		readOnlyCheck.SetChecked(true)
	}

	autoMountCheck := widget.NewCheck("Auto-Mount on Start", nil)
	if cfg.AutoMount != nil && *cfg.AutoMount {
		autoMountCheck.SetChecked(true)
	}

	runAtLoginCheck := widget.NewCheck("Run at Login", nil)
	if cfg.RunAtLogin != nil && *cfg.RunAtLogin {
		runAtLoginCheck.SetChecked(true)
	}

	networkSelect := widget.NewSelect([]string{"mainnet", "testnet"}, nil)
	if cfg.Network != nil && *cfg.Network == "testnet" {
		networkSelect.SetSelected("testnet")
	} else {
		networkSelect.SetSelected("mainnet")
	}

	rpcEntry := widget.NewEntry()
	rpcEntry.SetText(strVal(cfg.RPCEndpoint))

	logLevelSelect := widget.NewSelect([]string{"debug", "info", "warn", "error"}, nil)
	if cfg.LogLevel != nil {
		logLevelSelect.SetSelected(*cfg.LogLevel)
	} else {
		logLevelSelect.SetSelected("info")
	}

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "Network", Widget: networkSelect},
			{Text: "Endpoint", Widget: endpointEntry, HintText: "e.g. s03.neofs.devenv:8080"},
			{Text: "Wallet Key", Widget: walletKeyEntry, HintText: "WIF string or path to .key file"},
			{Text: "Override RPC", Widget: rpcEntry, HintText: "Leave blank for defaults"},
			{Text: "Mountpoint", Widget: mountpointEntry, HintText: "Directory to mount NeoFS on"},
			{Text: "Cache Dir", Widget: cacheDirEntry, HintText: "Local path for temporary uploads"},
			{Text: "Cache Size", Widget: cacheSizeEntry, HintText: "Max cache size in bytes"},
			{Text: "Log Level", Widget: logLevelSelect},
			{Text: "Read Only", Widget: readOnlyCheck},
			{Text: "Auto Mount", Widget: autoMountCheck},
			{Text: "Run at Login", Widget: runAtLoginCheck},
		},
		OnSubmit: func() {
			ep := endpointEntry.Text
			if ep != "" { cfg.Endpoint = &ep } else { cfg.Endpoint = nil }

			wk := walletKeyEntry.Text
			if wk != "" { cfg.WalletKey = &wk } else { cfg.WalletKey = nil }

			mp := mountpointEntry.Text
			if mp != "" { cfg.Mountpoint = &mp } else { cfg.Mountpoint = nil }

			cd := cacheDirEntry.Text
			if cd != "" { cfg.CacheDir = &cd } else { cfg.CacheDir = nil }

			sz, _ := strconv.ParseInt(cacheSizeEntry.Text, 10, 64)
			if sz > 0 { cfg.CacheSize = &sz } else { cfg.CacheSize = nil }

			ll := logLevelSelect.Selected
			cfg.LogLevel = &ll

			ro := readOnlyCheck.Checked
			cfg.ReadOnly = &ro

			am := autoMountCheck.Checked
			cfg.AutoMount = &am

			ral := runAtLoginCheck.Checked
			cfg.RunAtLogin = &ral

			nw := networkSelect.Selected
			cfg.Network = &nw

			rpcE := rpcEntry.Text
			if rpcE != "" { cfg.RPCEndpoint = &rpcE } else { cfg.RPCEndpoint = nil }

			if err := toggleRunAtLogin(ral); err != nil {
				dialog.ShowError(fmt.Errorf("Failed to configure OS startup: %w", err), w)
			}

			if err := config.Save(cfgPath, cfg); err != nil {
				dialog.ShowError(err, w)
			} else {
				dialog.ShowInformation("Success", "Configuration saved successfully.", w)
			}
		},
		SubmitText: "Save",
	}

	w.SetContent(container.NewVBox(
		widget.NewLabelWithStyle("Configuration", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		form,
	))
	w.Resize(fyne.NewSize(500, 520))
	w.Show()
}

func openTopUpWindow(a fyne.App) {
	w := a.NewWindow("Top Up NeoFS Balance")

	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil || cfg.WalletKey == nil {
		dialog.ShowError(fmt.Errorf("Wallet Key is not configured. Please check Settings."), w)
		w.Show()
		return
	}

	nw := "mainnet"
	if cfg.Network != nil {
		nw = *cfg.Network
	}
	rpcUrl := "https://mainnet1.neo.coz.io:443"
	if nw == "testnet" {
		rpcUrl = "https://testnet1.neo.coz.io:443"
	}
	if cfg.RPCEndpoint != nil && *cfg.RPCEndpoint != "" {
		rpcUrl = *cfg.RPCEndpoint
	}

	amountEntry := widget.NewEntry()
	amountEntry.SetPlaceHolder("Amount in GAS (e.g. 1.5)")

	var submitBtn *widget.Button
	submitBtn = widget.NewButton("Deposit", func() {
		amount, err := strconv.ParseFloat(amountEntry.Text, 64)
		if err != nil || amount <= 0 {
			dialog.ShowError(fmt.Errorf("Please enter a valid amount"), w)
			return
		}

		submitBtn.Disable()
		submitBtn.SetText("Sending (waiting for confirmation)...")

		go func() {
			tx, err := neofs.TopUpGAS(context.Background(), *cfg.WalletKey, nw, rpcUrl, amount)

			if err != nil {
				dialog.ShowError(fmt.Errorf("Top Up failed: %v", err), w)
			} else {
				dialog.ShowInformation("Success", fmt.Sprintf("Successfully deposited %v GAS.\nTxHash: %s", amount, tx), w)
			}
		}()
	})

	titleLabel := widget.NewLabelWithStyle("NeoFS GAS Top-Up", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	infoCard := widget.NewCard("", "Current Connection", widget.NewLabel(fmt.Sprintf("Network: %s\nRPC Node: %s", nw, rpcUrl)))

	content := container.NewVBox(
		titleLabel,
		infoCard,
		widget.NewLabel("Amount of GAS to transfer:"),
		amountEntry,
		layout.NewSpacer(),
		submitBtn,
	)

	w.SetContent(container.NewPadded(content))
	w.Resize(fyne.NewSize(500, 320))
	w.Show()
}

func updateBalance(desk desktop.App, item *fyne.MenuItem, menu *fyne.Menu) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		cfg, err := config.Load(config.DefaultConfigPath())
		if err != nil || cfg.Endpoint == nil || cfg.WalletKey == nil {
			item.Label = "Balance: Not Configured"
			desk.SetSystemTrayMenu(menu)
		} else {
			neo, err := neofs.New(context.Background(), neofs.Params{
				Endpoint:  *cfg.Endpoint,
				WalletKey: *cfg.WalletKey,
			})
			if err != nil {
				item.Label = "Balance: Error"
				desk.SetSystemTrayMenu(menu)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				val, prec, err := neo.Balance(ctx)
				if err != nil {
					item.Label = "Balance: Failed"
				} else {
					f := float64(val) / math.Pow10(int(prec))
					item.Label = fmt.Sprintf("Balance: %.4f GAS", f)
				}
				desk.SetSystemTrayMenu(menu)
				cancel()
				neo.Close()
			}
		}

		<-ticker.C
	}
}

func toggleRunAtLogin(enable bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	if runtime.GOOS == "linux" {
		autostartDir := filepath.Join(home, ".config", "autostart")
		os.MkdirAll(autostartDir, 0755)
		deskFile := filepath.Join(autostartDir, "neofsmounttray.desktop")
		if enable {
			content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=neoFS-mount Tray
Exec=%s
Hidden=false
NoDisplay=false
X-GNOME-Autostart-enabled=true
`, exe)
			return os.WriteFile(deskFile, []byte(content), 0644)
		} else {
			_ = os.Remove(deskFile)
			return nil
		}
	} else if runtime.GOOS == "darwin" {
		agentsDir := filepath.Join(home, "Library", "LaunchAgents")
		os.MkdirAll(agentsDir, 0755)
		plistFile := filepath.Join(agentsDir, "org.neofs.mount.plist")
		if enable {
			content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
   <key>Label</key>
   <string>org.neofs.mount</string>
   <key>ProgramArguments</key>
   <array><string>%s</string></array>
   <key>RunAtLoad</key>
   <true/>
</dict>
</plist>`, exe)
			return os.WriteFile(plistFile, []byte(content), 0644)
		} else {
			_ = os.Remove(plistFile)
			return nil
		}
	}
	return nil
}
