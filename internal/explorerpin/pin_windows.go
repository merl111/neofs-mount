//go:build windows

// Package explorerpin registers the NeoFS mount in File Explorer's navigation pane
// (same pattern as https://learn.microsoft.com/en-us/windows/win32/shell/integrate-cloud-storage).
package explorerpin

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Fixed CLSID for our shell folder (must not change or users get duplicate entries).
const navCLSID = `{E2B8F4A1-6C3D-4E50-9F2A-8C7D1E5B9A03}`

// Shell instance object — “function like other file folder structures” (MS docs step 6).
const instanceShellCLSID = `{0E5AAE11-A475-4c5b-AB00-C66DE400274E}`

const (
	shcneAssocChanged = 0x08000000
	shcnfIDList       = 0x0000
)

func notifyAssocChange() {
	shell := windows.NewLazySystemDLL("shell32.dll")
	p := shell.NewProc("SHChangeNotify")
	_, _, _ = p.Call(uintptr(shcneAssocChanged), uintptr(shcnfIDList), 0, 0)
}

func iconRefForPath(abs string) string {
	ref := abs + ",0"
	if strings.ContainsAny(abs, ` `) {
		ref = `"` + abs + `",0`
	}
	return ref
}

// Register pins the folder to the Explorer navigation pane (left tree).
// trayIconPNG should be the same bytes as the Fyne tray logo (PNG). They are written to
// %APPDATA%\neofs-mount\explorer-tray.ico so Explorer always has a real .ico path (PE
// embedded icons and runtime tray bitmaps are separate). If trayIconPNG is nil/empty or
// conversion fails, iconExe is used with ",0" as a fallback.
func Register(displayName, targetFolder, iconExe string, trayIconPNG []byte) error {
	targetFolder = filepath.Clean(targetFolder)
	if targetFolder == "" || targetFolder == "." {
		return fmt.Errorf("explorerpin: empty mount path")
	}
	iconExe, err := filepath.Abs(iconExe)
	if err != nil {
		return err
	}

	iconPath := iconExe
	if len(trayIconPNG) > 0 {
		if p, err := writeExplorerTrayICO(trayIconPNG); err == nil {
			iconPath = p
		}
	}
	iconRef := iconRefForPath(iconPath)

	base := `Software\Classes\CLSID\` + navCLSID

	// Older builds registered under MyComputer only (main pane); remove so Explorer doesn’t show duplicates.
	oldMyPC := `Software\Microsoft\Windows\CurrentVersion\Explorer\MyComputer\NameSpace\` + navCLSID
	_ = registry.DeleteKey(registry.CURRENT_USER, oldMyPC)

	k, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("explorerpin CLSID: %w", err)
	}
	_ = k.SetStringValue("", displayName)
	// Pinned + sort order — required for default visibility in the nav pane (MS steps 3–4).
	_ = k.SetDWordValue("System.IsPinnedToNameSpaceTree", 1)
	_ = k.SetDWordValue("SortOrderIndex", 0x42)
	_ = k.Close()

	di, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\DefaultIcon`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("explorerpin DefaultIcon: %w", err)
	}
	_ = di.SetStringValue("", iconRef)
	_ = di.Close()

	// In-process server: shell32 hosts the folder instance (MS step 5).
	ip, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\InProcServer32`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("explorerpin InProcServer32: %w", err)
	}
	if err := ip.SetExpandStringValue("", `%SystemRoot%\system32\shell32.dll`); err != nil {
		_ = ip.Close()
		return fmt.Errorf("explorerpin InProcServer32 value: %w", err)
	}
	_ = ip.Close()

	sf, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\ShellFolder`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("explorerpin ShellFolder: %w", err)
	}
	_ = sf.SetDWordValue("FolderValueFlags", 0x28)
	_ = sf.SetDWordValue("Attributes", 0xf080004d)
	_ = sf.Close()

	inst, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\Instance`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("explorerpin Instance: %w", err)
	}
	_ = inst.SetStringValue("CLSID", instanceShellCLSID)
	_ = inst.Close()

	bag, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\Instance\InitPropertyBag`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("explorerpin InitPropertyBag: %w", err)
	}
	_ = bag.SetDWordValue("Attributes", 0x11)
	_ = bag.SetStringValue("TargetFolderPath", targetFolder)
	// Shell folder instance often uses these for the nav pane icon (DefaultIcon alone is ignored).
	_ = bag.SetStringValue("IconPath", iconPath)
	_ = bag.SetDWordValue("IconIndex", 0)
	_ = bag.Close()

	// Nav pane root — child of Desktop namespace (MS step 11).
	deskNS := `Software\Microsoft\Windows\CurrentVersion\Explorer\Desktop\NameSpace\` + navCLSID
	ns, _, err := registry.CreateKey(registry.CURRENT_USER, deskNS, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("explorerpin Desktop NameSpace: %w", err)
	}
	_ = ns.SetStringValue("", displayName)
	_ = ns.Close()

	// Hide from the actual desktop (MS step 12).
	hidePath := `Software\Microsoft\Windows\CurrentVersion\Explorer\HideDesktopIcons\NewStartPanel`
	hk, err := registry.OpenKey(registry.CURRENT_USER, hidePath, registry.SET_VALUE)
	if err != nil {
		hk, _, err = registry.CreateKey(registry.CURRENT_USER, hidePath, registry.SET_VALUE)
	}
	if err == nil {
		_ = hk.SetDWordValue(navCLSID, 1)
		_ = hk.Close()
	}

	notifyAssocChange()
	return nil
}

// Unregister removes registry keys and values created by Register.
func Unregister() error {
	base := `Software\Classes\CLSID\` + navCLSID
	deskNS := `Software\Microsoft\Windows\CurrentVersion\Explorer\Desktop\NameSpace\` + navCLSID
	hidePath := `Software\Microsoft\Windows\CurrentVersion\Explorer\HideDesktopIcons\NewStartPanel`

	if hk, err := registry.OpenKey(registry.CURRENT_USER, hidePath, registry.SET_VALUE); err == nil {
		_ = hk.DeleteValue(navCLSID)
		_ = hk.Close()
	}

	_ = registry.DeleteKey(registry.CURRENT_USER, deskNS)
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Explorer\MyComputer\NameSpace\`+navCLSID)

	_ = registry.DeleteKey(registry.CURRENT_USER, base+`\Instance\InitPropertyBag`)
	_ = registry.DeleteKey(registry.CURRENT_USER, base+`\Instance`)
	_ = registry.DeleteKey(registry.CURRENT_USER, base+`\InProcServer32`)
	_ = registry.DeleteKey(registry.CURRENT_USER, base+`\ShellFolder`)
	_ = registry.DeleteKey(registry.CURRENT_USER, base+`\DefaultIcon`)
	_ = registry.DeleteKey(registry.CURRENT_USER, base)

	notifyAssocChange()
	return nil
}
