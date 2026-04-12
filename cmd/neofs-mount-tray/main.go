package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	"github.com/mathias/neofs-mount/internal/explorerpin"
	"github.com/mathias/neofs-mount/internal/fs"
	"github.com/mathias/neofs-mount/internal/mountutils"
	"github.com/mathias/neofs-mount/internal/neofs"
	"github.com/mathias/neofs-mount/internal/uploads"
)

// version and buildTime are injected by release builds via -ldflags (see Makefile).
var version = "dev"
var buildTime = "unknown"

// ---------------------------------------------------------------------------
// Global state
// ---------------------------------------------------------------------------

var (
	activeMount   *fs.MountedFS
	mountContext  context.Context
	mountCancel   context.CancelFunc
	uploadTracker = uploads.New()
	uploadHistory = uploads.NewHistory(config.DefaultUploadHistoryPath(), 0)

	// live stats displayed in the dashboard
	statsMu      sync.RWMutex
	statsBalance string = "…"
	statsN3Addr  string = "…"
)

// Uploads tab: refreshed by uploadsRefreshLoop; avoids fragile widget tree walks.
var (
	uploadsActiveVBox      *fyne.Container
	uploadsHistoryList     *widget.List
	uploadsHistorySnapshot []uploads.HistoryItem
)

// applyExplorerSidebarPin updates the Windows Explorer nav pane entry using the tray PNG as an .ico on disk.
func applyExplorerSidebarPin(cfg *config.FileConfig) {
	if runtime.GOOS != "windows" || cfg == nil {
		return
	}
	show := true
	if cfg.ShowInExplorer != nil {
		show = *cfg.ShowInExplorer
	}
	if !show {
		_ = explorerpin.Unregister()
		return
	}
	if cfg.Mountpoint == nil || *cfg.Mountpoint == "" {
		_ = explorerpin.Unregister()
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = explorerpin.Register("NeoFS", *cfg.Mountpoint, exe, resourceLogoPng.StaticContent)
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	if done, code := tryNeoFSAttrsMode(); done {
		os.Exit(code)
	}

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
			mp := fs.MountParams{
				Logger:             log,
				Endpoint:           *cfg.Endpoint,
				WalletKey:          *cfg.WalletKey,
				Mountpoint:         *cfg.Mountpoint,
				ReadOnly:           ro,
				CacheDir:           cacheDir,
				CacheSize:          cacheSize,
				IgnoreContainerIDs: cfg.IgnoreContainerIDs,
				UploadTracker:      uploadTracker,
				UploadHistory:      uploadHistory,
				AuditLogPath:       config.ResolveAuditLogPath(cfg),
			}
			if cfg.FetchDirCacheSeconds != nil && *cfg.FetchDirCacheSeconds > 0 {
				mp.FetchDirCacheTTL = time.Duration(*cfg.FetchDirCacheSeconds) * time.Second
			}
			if cfg.HydrationCacheMaxObjectMB != nil && *cfg.HydrationCacheMaxObjectMB > 0 {
				mp.HydrationCacheMaxObjectBytes = *cfg.HydrationCacheMaxObjectMB << 20
			}
			activeMount, mntErr = fs.Mount(mp)
			if mntErr != nil {
				showError(a, mntErr)
				return
			}

			applyExplorerSidebarPin(cfg)

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

	// Run as soon as the event loop is live so the splash can paint immediately
	// (Show() before Run() would stay blank until a.Run()).
	a.Lifecycle().SetOnStarted(func() {
		showStartupSplash(a)
		if exe, err := os.Executable(); err == nil {
			switch runtime.GOOS {
			case "windows", "linux":
				explorerpin.RegisterNeoFSContextMenuVerbs(exe)
			}
		}
		go updateBalance(desk, balanceItem, menu)
		cfg, cfgErr := config.Load(config.DefaultConfigPath())
		if cfgErr == nil {
			applyExplorerSidebarPin(cfg)
			autoMount := true
			if cfg.AutoMount != nil {
				autoMount = *cfg.AutoMount
			}
			if autoMount && cfg.Endpoint != nil && cfg.WalletKey != nil && cfg.Mountpoint != nil {
				go toggleMount()
			}
		}
	})

	a.Run()
}

// ---------------------------------------------------------------------------
// Main unified window
// ---------------------------------------------------------------------------

func openMainWindow(a fyne.App, desk desktop.App, toggleMount func()) {
	w := a.NewWindow("NeoFS")
	w.SetIcon(resourceLogoPng) // Shows up in the taskbar/dock
	w.Resize(fyne.NewSize(800, 750))

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
	titleLabel := widget.NewLabelWithStyle("NeoFS", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
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
	go uploadsRefreshLoop(w)
	go dashRefreshLoop(dashPage, w)
}

// ---------------------------------------------------------------------------
// Dashboard page
// ---------------------------------------------------------------------------

type dashWidgets struct {
	mountStatus *widget.Label
	gasBalance  *widget.Label
	n3Addr      *widget.Label
	mountpoint  *widget.Label
	mountBtn    *widget.Button
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

func formatByteSize(b int64) string {
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

func formatUploadHistoryRow(it uploads.HistoryItem) string {
	ts := it.FinishedAt.Local().Format("2006-01-02 15:04:05")
	st := strings.ToUpper(it.Status)
	key := it.NeoKey
	if len(key) > 64 {
		key = "…" + key[len(key)-61:]
	}
	s := fmt.Sprintf("%s  %s  %s", ts, st, key)
	if it.Bytes > 0 {
		s += "  (" + formatByteSize(it.Bytes) + ")"
	}
	if it.Status != "ok" && it.Detail != "" {
		d := it.Detail
		if len(d) > 80 {
			d = d[:77] + "…"
		}
		s += " — " + d
	}
	return s
}

func buildUploadsPage() fyne.CanvasObject {
	uploadsActiveVBox = container.NewVBox()
	activeScroll := container.NewVScroll(uploadsActiveVBox)

	uploadsHistoryList = widget.NewList(
		func() int { return len(uploadsHistorySnapshot) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.Wrapping = fyne.TextWrapWord
			l.TextStyle = fyne.TextStyle{Monospace: true}
			return l
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			l := o.(*widget.Label)
			i := int(id)
			if i < 0 || i >= len(uploadsHistorySnapshot) {
				l.SetText("")
				return
			}
			l.SetText(formatUploadHistoryRow(uploadsHistorySnapshot[i]))
		},
	)
	uploadsHistoryList.HideSeparators = true
	historyScroll := container.NewVScroll(uploadsHistoryList)

	tabs := container.NewAppTabs(
		container.NewTabItem("Active", activeScroll),
		container.NewTabItem("History", historyScroll),
	)
	return container.NewPadded(tabs)
}

func uploadsRefreshLoop(w fyne.Window) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	for {
		<-tick.C
		fyne.Do(func() {
			uploadsHistorySnapshot = uploadHistory.List()
			if uploadsHistoryList != nil {
				uploadsHistoryList.Refresh()
			}
			if uploadsActiveVBox == nil {
				return
			}
			uploadsActiveVBox.Objects = nil
			entries := uploadTracker.List()
			if len(entries) == 0 {
				lbl := widget.NewLabel("No active uploads.")
				lbl.Alignment = fyne.TextAlignCenter
				uploadsActiveVBox.Add(lbl)
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
						filepath.Base(e.Path), formatByteSize(sent), formatByteSize(total), elapsed))
					lbl.TextStyle = fyne.TextStyle{Monospace: true}
					uploadsActiveVBox.Add(container.NewVBox(lbl, bar))
				}
			}
			uploadsActiveVBox.Refresh()
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

	var showInExplorerCheck *widget.Check
	if runtime.GOOS == "windows" {
		showInExplorerCheck = widget.NewCheck("Show mount in Explorer under This PC", nil)
		sie := true
		if cfg.ShowInExplorer != nil {
			sie = *cfg.ShowInExplorer
		}
		showInExplorerCheck.SetChecked(sie)
	}

	auditEnabledCheck := widget.NewCheck("Record NeoFS operations to audit log (JSON lines)", nil)
	if cfg.AuditLog != nil && !*cfg.AuditLog {
		auditEnabledCheck.SetChecked(false)
	} else {
		auditEnabledCheck.SetChecked(true)
	}
	auditPathEntry := widget.NewEntry()
	auditPathEntry.SetText(strVal(cfg.AuditLogPath))
	auditPathEntry.SetPlaceHolder("Default: same folder as neofs-mount.log")
	auditEffectiveLabel := widget.NewLabel("")
	updateAuditEffective := func() {
		var fc config.FileConfig
		al := auditEnabledCheck.Checked
		fc.AuditLog = &al
		if p := strings.TrimSpace(auditPathEntry.Text); p != "" {
			fc.AuditLogPath = &p
		}
		ep := config.ResolveAuditLogPath(&fc)
		if ep == "" {
			auditEffectiveLabel.SetText("Effective audit file: (disabled)")
		} else {
			auditEffectiveLabel.SetText("Effective audit file: " + ep)
		}
	}
	auditEnabledCheck.OnChanged = func(bool) { updateAuditEffective() }
	auditPathEntry.OnChanged = func(string) { updateAuditEffective() }
	updateAuditEffective()

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

	formItems := []*widget.FormItem{
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
	}
	if showInExplorerCheck != nil {
		formItems = append(formItems, &widget.FormItem{
			Text:     "Explorer",
			Widget:   showInExplorerCheck,
			HintText: "Uses the same image as the tray icon (saved as explorer-tray.ico under AppData).",
		})
	}
	formItems = append(formItems,
		&widget.FormItem{
			Text:     "Audit log",
			Widget:   auditEnabledCheck,
			HintText: "Append-only JSON lines of uploads, deletes, and other NeoFS operations.",
		},
		&widget.FormItem{
			Text:     "Audit file",
			Widget:   auditPathEntry,
			HintText: "Leave blank for default. Changing the path applies after remount.",
		},
	)
	form := widget.NewForm(formItems...)

	saveBtn := widget.NewButtonWithIcon("Save Settings", theme.DocumentSaveIcon(), func() {
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
		if showInExplorerCheck != nil {
			sie := showInExplorerCheck.Checked
			cfg.ShowInExplorer = &sie
		}
		nw := networkSelect.Selected
		cfg.Network = &nw
		rpcE := rpcEntry.Text
		if rpcE != "" {
			cfg.RPCEndpoint = &rpcE
		} else {
			cfg.RPCEndpoint = nil
		}

		al := auditEnabledCheck.Checked
		cfg.AuditLog = &al
		ap := strings.TrimSpace(auditPathEntry.Text)
		if ap != "" {
			cfg.AuditLogPath = &ap
		} else {
			cfg.AuditLogPath = nil
		}

		if err := toggleRunAtLogin(ral); err != nil {
			dialog.ShowError(fmt.Errorf("Failed to configure OS startup: %w", err), w)
		}
		if err := config.Save(cfgPath, cfg); err != nil {
			dialog.ShowError(err, w)
		} else {
			applyExplorerSidebarPin(cfg)
			dialog.ShowInformation("Saved", "Configuration saved.", w)
		}
	})
	saveBtn.Importance = widget.HighImportance

	openLogsBtn := widget.NewButtonWithIcon("Open Logs Directory", theme.FolderIcon(), func() {
		if err := mountutils.OpenLogDirectory(); err != nil {
			dialog.ShowError(fmt.Errorf("Failed to open log directory: %w", err), w)
		}
	})
	viewAuditBtn := widget.NewButtonWithIcon("View audit log", theme.DocumentIcon(), func() {
		showAuditLogWindow(a, w)
	})

	buildInfoLabel := widget.NewLabel(fmt.Sprintf("Version: %s  |  Built: %s", version, buildTime))
	buildInfoLabel.TextStyle = fyne.TextStyle{Italic: true}

	scroll := container.NewVScroll(container.NewVBox(
		widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		buildInfoLabel,
		widget.NewSeparator(),
		form,
		auditEffectiveLabel,
		container.NewPadded(saveBtn),
		widget.NewSeparator(),
		container.NewHBox(openLogsBtn, viewAuditBtn),
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

	refreshMenu := func() {
		defer func() {
			if r := recover(); r != nil {
				// Fyne systray can panic on menu reset if channels are closed during teardown.
			}
		}()
		desk.SetSystemTrayMenu(menu)
	}

	update := func() {
		cfg, err := config.Load(config.DefaultConfigPath())
		if err != nil || cfg.Endpoint == nil || cfg.WalletKey == nil {
			statsMu.Lock()
			statsBalance = "Not configured"
			statsN3Addr = "Not configured"
			statsMu.Unlock()
			item.Label = "Balance: —"
			refreshMenu()
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
			refreshMenu()
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
		refreshMenu()
	}

	// Give the systray time to fully initialize before the first menu refresh.
	time.Sleep(2 * time.Second)
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
	} else if runtime.GOOS == "windows" {
		if enable {
			cmd := exec.Command("reg", "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "neofs-mount-tray", "/t", "REG_SZ", "/d", exe, "/f")
			return cmd.Run()
		} else {
			cmd := exec.Command("reg", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "neofs-mount-tray", "/f")
			return cmd.Run()
		}
	}
	return nil
}
