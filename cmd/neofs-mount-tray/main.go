package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mathias/neofs-mount/internal/config"
	"github.com/mathias/neofs-mount/internal/fs"
	"github.com/mathias/neofs-mount/internal/mountutils"
	"github.com/mathias/neofs-mount/internal/neofs"
	"github.com/mathias/neofs-mount/internal/uploads"
)

// ---------------------------------------------------------------------------
// Global state
// ---------------------------------------------------------------------------

var (
	activeMount   *fs.MountedFS
	mountContext  context.Context
	mountCancel   context.CancelFunc
	uploadTracker = uploads.New()

	// live stats displayed in the dashboard
	statsMu      sync.RWMutex
	statsBalance string = "…"
	statsN3Addr  string = "…"
)

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	a := app.NewWithID("org.neofs.mount")

	desk, ok := a.(desktop.App)
	if !ok {
		fmt.Fprintln(os.Stderr, "Error: Tray menu is not supported on this platform")
		os.Exit(1)
	}
	a.SetIcon(resourceLogoPng)
	a.Settings().SetTheme(&modernTheme{})

	var menu *fyne.Menu
	var mountItem *fyne.MenuItem

	toggleMount := func() {
		if activeMount == nil {
			cfgPath := config.DefaultConfigPath()
			cfg, err := config.Load(cfgPath)
			if err != nil {
				showError(a, fmt.Errorf("could not load config: %w", err))
				return
			}
			if cfg.Endpoint == nil || cfg.WalletKey == nil || cfg.Mountpoint == nil {
				showError(a, fmt.Errorf("missing essential config. please open Settings"))
				return
			}

			if err := mountutils.EnsureDir(*cfg.Mountpoint, 0o755); err != nil {
				showError(a, err)
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
				showError(a, err)
				return
			}

			logLvl := "info"
			if cfg.LogLevel != nil {
				logLvl = *cfg.LogLevel
			}
			log := mountutils.NewLogger(logLvl)

			cacheSize := int64(1 << 30)
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
				showError(a, mntErr)
				return
			}

			mountContext, mountCancel = context.WithCancel(context.Background())
			mountItem.Label = "Unmount"
			desk.SetSystemTrayIcon(resourceLogoPng)
			if menu != nil {
				desk.SetSystemTrayMenu(menu)
			}

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
			if mountCancel != nil {
				mountCancel()
			}
		}
	}

	mountItem = fyne.NewMenuItem("Mount", toggleMount)

	openApp := fyne.NewMenuItem("Open…", func() {
		openMainWindow(a, desk, toggleMount)
	})

	quitItem := fyne.NewMenuItem("Quit", func() {
		if mountCancel != nil {
			mountCancel()
			time.Sleep(100 * time.Millisecond)
		}
		a.Quit()
	})

	var balanceItem *fyne.MenuItem
	balanceItem = fyne.NewMenuItem("Balance: …", nil)
	balanceItem.Disabled = true

	menu = fyne.NewMenu("neoFS-mount",
		balanceItem,
		fyne.NewMenuItemSeparator(),
		mountItem,
		fyne.NewMenuItemSeparator(),
		openApp,
		fyne.NewMenuItemSeparator(),
		quitItem,
	)
	desk.SetSystemTrayMenu(menu)

	go updateBalance(desk, balanceItem, menu)

	// Auto-mount by default unless explicitly disabled.
	cfg, cfgErr := config.Load(config.DefaultConfigPath())
	autoMount := true // default ON
	if cfgErr == nil && cfg.AutoMount != nil {
		autoMount = *cfg.AutoMount
	}
	if autoMount {
		go toggleMount()
	}

	a.Run()
}

// ---------------------------------------------------------------------------
// Main unified window
// ---------------------------------------------------------------------------

func openMainWindow(a fyne.App, desk desktop.App, toggleMount func()) {
	w := a.NewWindow("neoFS Mount")
	w.SetIcon(resourceLogoPng) // Shows up in the taskbar/dock
	w.Resize(fyne.NewSize(720, 500))

	// Content area — swapped by sidebar buttons.
	contentArea := container.NewStack()

	// Pages
	dashPage := buildDashPage(a, toggleMount)
	uploadsPage := buildUploadsPage()
	settingsPage := buildSettingsPage(a, w, desk)
	topupPage := buildTopUpPage(a, w)

	show := func(page fyne.CanvasObject) {
		contentArea.Objects = []fyne.CanvasObject{page}
		contentArea.Refresh()
	}
	show(dashPage)

	// Sidebar nav items
	type navItem struct {
		label string
		icon  fyne.Resource
		page  fyne.CanvasObject
	}
	navItems := []navItem{
		{"Dashboard", theme.HomeIcon(), dashPage},
		{"Uploads", theme.UploadIcon(), uploadsPage},
		{"Settings", theme.SettingsIcon(), settingsPage},
		{"Top-Up", theme.ContentAddIcon(), topupPage},
	}

	var navBtns []*widget.Button
	selectNav := func(idx int) {
		show(navItems[idx].page)
		for i, b := range navBtns {
			if i == idx {
				b.Importance = widget.HighImportance
			} else {
				b.Importance = widget.MediumImportance
			}
			b.Refresh()
		}
	}
	sidebar := container.NewVBox()

	// Logo Header
	logoImg := canvas.NewImageFromResource(resourceLogoPng)
	logoImg.FillMode = canvas.ImageFillContain
	logoImg.SetMinSize(fyne.NewSize(64, 64))
	titleLabel := widget.NewLabelWithStyle("neoFS Mount", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	header := container.NewVBox(
		container.NewPadded(logoImg),
		titleLabel,
		widget.NewSeparator(),
	)
	sidebar.Add(header)

	for i, item := range navItems {
		i, item := i, item
		btn := widget.NewButtonWithIcon(item.label, item.icon, func() { selectNav(i) })
		btn.Alignment = widget.ButtonAlignLeading
		btn.Importance = widget.LowImportance
		btn.Importance = widget.MediumImportance
		navBtns = append(navBtns, btn)
		sidebar.Add(btn)
	}
	selectNav(0)
	sidebarScroll := container.NewVScroll(container.NewPadded(sidebar))
	sidebarScroll.SetMinSize(fyne.NewSize(180, 0))

	sep := canvas.NewRectangle(theme.SeparatorColor())
	sep.SetMinSize(fyne.NewSize(1, 0))

	split := container.NewBorder(nil, nil,
		container.NewHBox(sidebarScroll, sep),
		nil,
		container.NewPadded(contentArea),
	)

	w.SetContent(split)
	w.Show()

	// Start refresh loops for dynamic pages.
	go uploadsRefreshLoop(uploadsPage, w)
	go dashRefreshLoop(dashPage, w)
}

// ---------------------------------------------------------------------------
// Dashboard page
// ---------------------------------------------------------------------------

type dashWidgets struct {
	mountStatus  *widget.Label
	gasBalance   *widget.Label
	n3Addr       *widget.Label
	mountpoint   *widget.Label
	mountBtn     *widget.Button
}

var dash dashWidgets

func buildDashPage(a fyne.App, toggleMount func()) fyne.CanvasObject {
	dash.mountStatus = widget.NewLabelWithStyle("●  Unmounted", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	dash.gasBalance = widget.NewLabelWithStyle("…", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	dash.n3Addr = widget.NewLabelWithStyle("…", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	dash.mountpoint = widget.NewLabelWithStyle("…", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})

	dash.mountBtn = widget.NewButtonWithIcon("Mount File System", theme.MediaPlayIcon(), func() {
		go toggleMount()
	})
	dash.mountBtn.Importance = widget.HighImportance

	form := widget.NewForm(
		widget.NewFormItem("Status", dash.mountStatus),
		widget.NewFormItem("Mountpoint", dash.mountpoint),
		widget.NewFormItem("GAS Balance", dash.gasBalance),
		widget.NewFormItem("N3 Address", dash.n3Addr),
	)

	return container.NewVBox(
		widget.NewLabelWithStyle("Dashboard Overview", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		layout.NewSpacer(),
		container.NewHBox(layout.NewSpacer(), form, layout.NewSpacer()),
		layout.NewSpacer(),
		dash.mountBtn,
	)
}

func dashRefreshLoop(page fyne.CanvasObject, w fyne.Window) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		fyne.Do(func() {
			cfg, err := config.Load(config.DefaultConfigPath())

			// Mount status
			if activeMount != nil {
				dash.mountStatus.Text = "●  Mounted"
				dash.mountStatus.Importance = widget.HighImportance // Use accent color conceptually
				dash.mountBtn.SetText("Unmount File System")
				dash.mountBtn.SetIcon(theme.MediaStopIcon())
				dash.mountBtn.Importance = widget.DangerImportance
				if err == nil && cfg.Mountpoint != nil {
					dash.mountpoint.SetText(*cfg.Mountpoint)
				}
			} else {
				dash.mountStatus.Text = "○  Unmounted"
				dash.mountStatus.Importance = widget.MediumImportance
				dash.mountBtn.SetText("Mount File System")
				dash.mountBtn.SetIcon(theme.MediaPlayIcon())
				dash.mountBtn.Importance = widget.HighImportance
				dash.mountpoint.SetText("—")
			}

			// Balance & N3 address
			statsMu.RLock()
			dash.gasBalance.SetText(statsBalance)
			dash.n3Addr.SetText(statsN3Addr)
			statsMu.RUnlock()

			page.Refresh()
		})
		<-tick.C
	}
}

// ---------------------------------------------------------------------------
// Uploads page
// ---------------------------------------------------------------------------

var uploadsContent *container.AppTabs // reused in loop

func buildUploadsPage() fyne.CanvasObject {
	uploadsContent = container.NewAppTabs() // placeholder; rebuilt in loop
	uploadsContent.Hide()

	emptyLabel := widget.NewLabel("No active uploads.")
	emptyLabel.Alignment = fyne.TextAlignCenter

	box := container.NewVBox(emptyLabel)
	return container.NewPadded(box)
}

func uploadsRefreshLoop(page fyne.CanvasObject, w fyne.Window) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

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

	for {
		<-tick.C
		fyne.Do(func() {
			padded, ok := page.(*fyne.Container)
			if !ok {
				return
			}
			box, ok := padded.Objects[0].(*fyne.Container)
			if !ok {
				return
			}
			box.Objects = nil
			entries := uploadTracker.List()
			if len(entries) == 0 {
				lbl := widget.NewLabel("No active uploads.")
				lbl.Alignment = fyne.TextAlignCenter
				box.Add(lbl)
			} else {
				for _, e := range entries {
					e := e
					sent := e.Sent()
					total := e.TotalBytes
					elapsed := time.Since(e.Started).Round(time.Second)
					pct := float64(0)
					if total > 0 {
						pct = float64(sent) / float64(total)
					}
					bar := widget.NewProgressBar()
					bar.SetValue(pct)
					lbl := widget.NewLabel(fmt.Sprintf("%s  •  %s / %s  •  %s elapsed",
						filepath.Base(e.Path), formatBytes(sent), formatBytes(total), elapsed))
					lbl.TextStyle = fyne.TextStyle{Monospace: true}
					box.Add(container.NewVBox(lbl, bar))
				}
			}
			box.Refresh()
		})
	}
}

// ---------------------------------------------------------------------------
// Settings page
// ---------------------------------------------------------------------------

func buildSettingsPage(a fyne.App, w fyne.Window, desk desktop.App) fyne.CanvasObject {
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

	walletKeyEntry := widget.NewPasswordEntry()
	walletKeyEntry.SetText(strVal(cfg.WalletKey))

	mountpointEntry := widget.NewEntry()
	mountpointEntry.SetText(strVal(cfg.Mountpoint))

	cacheDirEntry := widget.NewEntry()
	cacheDirEntry.SetText(strVal(cfg.CacheDir))

	cacheSizeEntry := widget.NewEntry()
	if cfg.CacheSize != nil {
		cacheSizeEntry.SetText(fmt.Sprintf("%d", *cfg.CacheSize))
	}

	readOnlyCheck := widget.NewCheck("Mount as Read-Only", nil)
	if cfg.ReadOnly != nil && *cfg.ReadOnly {
		readOnlyCheck.SetChecked(true)
	}

	autoMountCheck := widget.NewCheck("Auto-Mount on Start", nil)
	// Default ON — show checked unless explicitly false.
	autoMount := true
	if cfg.AutoMount != nil {
		autoMount = *cfg.AutoMount
	}
	autoMountCheck.SetChecked(autoMount)

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
			if ep != "" {
				cfg.Endpoint = &ep
			} else {
				cfg.Endpoint = nil
			}
			wk := walletKeyEntry.Text
			if wk != "" {
				cfg.WalletKey = &wk
			} else {
				cfg.WalletKey = nil
			}
			mp := mountpointEntry.Text
			if mp != "" {
				cfg.Mountpoint = &mp
			} else {
				cfg.Mountpoint = nil
			}
			cd := cacheDirEntry.Text
			if cd != "" {
				cfg.CacheDir = &cd
			} else {
				cfg.CacheDir = nil
			}
			sz, _ := strconv.ParseInt(cacheSizeEntry.Text, 10, 64)
			if sz > 0 {
				cfg.CacheSize = &sz
			} else {
				cfg.CacheSize = nil
			}
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
			if rpcE != "" {
				cfg.RPCEndpoint = &rpcE
			} else {
				cfg.RPCEndpoint = nil
			}

			if err := toggleRunAtLogin(ral); err != nil {
				dialog.ShowError(fmt.Errorf("Failed to configure OS startup: %w", err), w)
			}
			if err := config.Save(cfgPath, cfg); err != nil {
				dialog.ShowError(err, w)
			} else {
				dialog.ShowInformation("Saved", "Configuration saved.", w)
			}
		},
		SubmitText: "Save",
	}

	scroll := container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		form,
	))
	return scroll
}

// ---------------------------------------------------------------------------
// Top-Up page
// ---------------------------------------------------------------------------

func buildTopUpPage(a fyne.App, w fyne.Window) fyne.CanvasObject {
	cfg, err := config.Load(config.DefaultConfigPath())

	nw := "mainnet"
	if err == nil && cfg.Network != nil {
		nw = *cfg.Network
	}
	rpcUrl := "https://mainnet1.neo.coz.io:443"
	if nw == "testnet" {
		rpcUrl = "https://testnet1.neo.coz.io:443"
	}
	if err == nil && cfg.RPCEndpoint != nil && *cfg.RPCEndpoint != "" {
		rpcUrl = *cfg.RPCEndpoint
	}

	amountEntry := widget.NewEntry()
	amountEntry.SetPlaceHolder("Amount in GAS (e.g. 1.5)")

	statusLabel := widget.NewLabel("")

	var submitBtn *widget.Button
	submitBtn = widget.NewButton("Deposit", func() {
		if err != nil || cfg.WalletKey == nil {
			dialog.ShowError(fmt.Errorf("Wallet Key is not configured. Please check Settings."), w)
			return
		}
		amount, err := strconv.ParseFloat(amountEntry.Text, 64)
		if err != nil || amount <= 0 {
			dialog.ShowError(fmt.Errorf("Please enter a valid amount"), w)
			return
		}

		submitBtn.Disable()
		submitBtn.SetText("Sending…")
		statusLabel.SetText("Waiting for confirmation…")

		go func() {
			tx, txErr := neofs.TopUpGAS(context.Background(), *cfg.WalletKey, nw, rpcUrl, amount)
			fyne.Do(func() {
				submitBtn.Enable()
				submitBtn.SetText("Deposit")
				if txErr != nil {
					statusLabel.SetText("Failed: " + txErr.Error())
				} else {
					statusLabel.SetText(fmt.Sprintf("✓ Deposited %.4f GAS\nTx: %s", amount, tx))
				}
			})
		}()
	})
	submitBtn.Importance = widget.HighImportance

	infoCard := widget.NewCard("", "Connection",
		widget.NewLabel(fmt.Sprintf("Network: %s\nRPC: %s", nw, rpcUrl)))

	return container.NewVBox(
		widget.NewLabelWithStyle("Top-Up GAS Balance", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		infoCard,
		widget.NewForm(widget.NewFormItem("Amount (GAS)", amountEntry)),
		submitBtn,
		statusLabel,
	)
}

// ---------------------------------------------------------------------------
// Balance poller (background) — also derives N3 address
// ---------------------------------------------------------------------------

func updateBalance(desk desktop.App, item *fyne.MenuItem, menu *fyne.Menu) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	update := func() {
		cfg, err := config.Load(config.DefaultConfigPath())
		if err != nil || cfg.Endpoint == nil || cfg.WalletKey == nil {
			statsMu.Lock()
			statsBalance = "Not configured"
			statsN3Addr = "Not configured"
			statsMu.Unlock()
			item.Label = "Balance: —"
			desk.SetSystemTrayMenu(menu)
			return
		}

		// Derive N3 address from WIF.
		if addr, err := neofs.AddressFromWIF(*cfg.WalletKey); err == nil {
			statsMu.Lock()
			statsN3Addr = addr
			statsMu.Unlock()
		}

		neo, err := neofs.New(context.Background(), neofs.Params{
			Endpoint:  *cfg.Endpoint,
			WalletKey: *cfg.WalletKey,
		})
		if err != nil {
			statsMu.Lock()
			statsBalance = "Error"
			statsMu.Unlock()
			item.Label = "Balance: Error"
			desk.SetSystemTrayMenu(menu)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		val, prec, err := neo.Balance(ctx)
		cancel()
		_ = neo.Close()

		if err != nil {
			statsMu.Lock()
			statsBalance = "Failed"
			statsMu.Unlock()
			item.Label = "Balance: —"
		} else {
			f := float64(val) / math.Pow10(int(prec))
			label := fmt.Sprintf("%.4f GAS", f)
			statsMu.Lock()
			statsBalance = label
			statsMu.Unlock()
			item.Label = "Balance: " + label
		}
		desk.SetSystemTrayMenu(menu)
	}

	update()
	for range ticker.C {
		update()
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func showError(a fyne.App, err error) {
	w := a.NewWindow("neoFS-mount Error")
	dialog.ShowError(err, w)
	w.Show()
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
		}
		_ = os.Remove(deskFile)
		return nil
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
		}
		_ = os.Remove(plistFile)
		return nil
	}
	return nil
}
