//go:build windows

package explorerpin

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// Explorer context-menu verbs under HKCU\Software\Classes\*\shell\...
const (
	fileAttrsShellVerb  = `Software\Classes\*\shell\NeoFSMountFileAttrs`
	fileDeleteShellVerb = `Software\Classes\*\shell\NeoFSMountFileDelete`

	// CLSIDs implemented by win/neofs_shellcmd/neofs_shellcmd.dll (IExplorerCommand).
	clsidNeoFSAttrsVerb  = "{7E4F8C21-0A3D-4B91-BE6D-8C2A9F55D401}"
	clsidNeoFSDeleteVerb = "{8F5A9D32-1B4E-4CA2-CF7E-9D3B0A66E512}"

	shellExtensionConfigKey = `Software\neofs-mount\ShellExtension`
	shellExtensionTrayValue = "TrayExe"
)

func registerFileShellVerb(regPath, menuTitle, trayExe, cliFlag string) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, regPath, registry.SET_VALUE)
	if err != nil {
		return
	}
	_ = k.DeleteValue("ExplorerCommandHandler")
	_ = k.SetStringValue("", menuTitle)
	_ = k.Close()

	cmdK, _, err := registry.CreateKey(registry.CURRENT_USER, regPath+`\command`, registry.SET_VALUE)
	if err != nil {
		return
	}
	cmdLine := fmt.Sprintf("\"%s\" %s \"%%1\"", trayExe, cliFlag)
	_ = cmdK.SetStringValue("", cmdLine)
	_ = cmdK.Close()
}

func deleteVerbCommandSubkey(verbPath string) {
	_ = registry.DeleteKey(registry.CURRENT_USER, verbPath+`\command`)
}

// writeShellExtensionTrayExe records the tray path for neofs_shellcmd.dll (HKCU).
func writeShellExtensionTrayExe(trayExe string) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, shellExtensionConfigKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	_ = k.SetStringValue(shellExtensionTrayValue, trayExe)
	_ = k.Close()
}

func registerInProcServer32(clsid, dllPath string) {
	base := `Software\Classes\CLSID\` + clsid
	k, _, err := registry.CreateKey(registry.CURRENT_USER, base, registry.SET_VALUE)
	if err != nil {
		return
	}
	_ = k.Close()
	ip, _, err := registry.CreateKey(registry.CURRENT_USER, base+`\InProcServer32`, registry.SET_VALUE)
	if err != nil {
		return
	}
	_ = ip.SetStringValue("", dllPath)
	_ = ip.SetStringValue("ThreadingModel", "Apartment")
	_ = ip.Close()
}

func unregisterInProcServer32(clsid string) {
	base := `Software\Classes\CLSID\` + clsid
	_ = registry.DeleteKey(registry.CURRENT_USER, base+`\InProcServer32`)
	_ = registry.DeleteKey(registry.CURRENT_USER, base)
}

// registerExplorerCommandVerb registers a *shell* verb that uses ExplorerCommandHandler (Windows 11 primary menu).
func registerExplorerCommandVerb(verbPath, menuTitle, clsid string) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, verbPath, registry.SET_VALUE)
	if err != nil {
		return
	}
	_ = k.SetStringValue("", menuTitle)
	_ = k.SetStringValue("ExplorerCommandHandler", clsid)
	_ = k.Close()
	deleteVerbCommandSubkey(verbPath)
}

// RegisterNeoFSContextMenuVerbs adds Explorer entries for NeoFS mount paths.
// When neofs-shellcmd.dll sits next to the tray executable, verbs use IExplorerCommand so they
// appear on the Windows 11 default context menu; otherwise legacy static \command verbs are used
// (visible under "Show more options" on Windows 11).
func RegisterNeoFSContextMenuVerbs(trayExe string) {
	trayExe, _ = filepath.Abs(trayExe)
	writeShellExtensionTrayExe(trayExe)

	dllPath := filepath.Join(filepath.Dir(trayExe), "neofs-shellcmd.dll")
	if st, err := os.Stat(dllPath); err == nil && !st.IsDir() {
		registerInProcServer32(clsidNeoFSAttrsVerb, dllPath)
		registerInProcServer32(clsidNeoFSDeleteVerb, dllPath)
		registerExplorerCommandVerb(fileAttrsShellVerb, "NeoFS object details…", clsidNeoFSAttrsVerb)
		registerExplorerCommandVerb(fileDeleteShellVerb, "Delete from NeoFS container…", clsidNeoFSDeleteVerb)
		notifyAssocChange()
		return
	}

	unregisterNeoFSExplorerCommandRegistration()
	registerFileShellVerb(fileAttrsShellVerb, "NeoFS object details…", trayExe, "-neofs-attrs")
	registerFileShellVerb(fileDeleteShellVerb, "Delete from NeoFS container…", trayExe, "-neofs-delete")
	notifyAssocChange()
}

func unregisterNeoFSExplorerCommandRegistration() {
	unregisterInProcServer32(clsidNeoFSAttrsVerb)
	unregisterInProcServer32(clsidNeoFSDeleteVerb)
}

// UnregisterNeoFSContextMenuVerbs removes shell keys created for NeoFS file verbs (HKCU).
func UnregisterNeoFSContextMenuVerbs() {
	_ = registry.DeleteKey(registry.CURRENT_USER, fileAttrsShellVerb+`\command`)
	_ = registry.DeleteKey(registry.CURRENT_USER, fileAttrsShellVerb)
	_ = registry.DeleteKey(registry.CURRENT_USER, fileDeleteShellVerb+`\command`)
	_ = registry.DeleteKey(registry.CURRENT_USER, fileDeleteShellVerb)
	unregisterNeoFSExplorerCommandRegistration()
	if k, err := registry.OpenKey(registry.CURRENT_USER, shellExtensionConfigKey, registry.SET_VALUE); err == nil {
		_ = k.DeleteValue(shellExtensionTrayValue)
		_ = k.Close()
	}
	notifyAssocChange()
}

// RegisterFileAttrsShellVerb registers only the object-details verb (legacy \command).
func RegisterFileAttrsShellVerb(trayExe string) {
	registerFileShellVerb(fileAttrsShellVerb, "NeoFS object details…", trayExe, "-neofs-attrs")
}
