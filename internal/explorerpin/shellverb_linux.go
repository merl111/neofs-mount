//go:build linux

package explorerpin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const nautilusScriptMarker = "neofs-mount-tray-nautilus:v1"

func nautilusScriptsDir() (string, error) {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "nautilus", "scripts"), nil
}

func shellSingleQuoted(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}

func writeNautilusScript(dir, name, kind, tray string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	var body strings.Builder
	fmt.Fprintf(&body, "#!/usr/bin/env sh\n")
	fmt.Fprintf(&body, "# %s:%s\n", nautilusScriptMarker, kind)
	fmt.Fprintf(&body, "TRAY=%s\n", shellSingleQuoted(tray))
	switch kind {
	case "attrs":
		fmt.Fprintf(&body, "for f in \"$@\"; do\n")
		fmt.Fprintf(&body, "  [ -e \"$f\" ] || continue\n")
		fmt.Fprintf(&body, "  \"$TRAY\" -neofs-attrs \"$f\" || exit $?\n")
		fmt.Fprintf(&body, "done\n")
	case "delete":
		fmt.Fprintf(&body, "for f in \"$@\"; do\n")
		fmt.Fprintf(&body, "  [ -e \"$f\" ] || continue\n")
		fmt.Fprintf(&body, "  \"$TRAY\" -neofs-delete \"$f\" || exit $?\n")
		fmt.Fprintf(&body, "done\n")
	default:
		return fmt.Errorf("unknown script kind %q", kind)
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o755); err != nil {
		return err
	}
	return nil
}

func isOurNautilusScript(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 512 {
		return false
	}
	return strings.Contains(string(b), nautilusScriptMarker)
}

// RegisterNeoFSContextMenuVerbs installs Nautilus “Scripts” menu entries (GNOME Files).
// Items appear under the right‑click Scripts submenu after a Nautilus restart.
func RegisterNeoFSContextMenuVerbs(trayExe string) {
	trayExe, err := filepath.Abs(trayExe)
	if err != nil {
		return
	}
	dir, err := nautilusScriptsDir()
	if err != nil {
		return
	}
	_ = writeNautilusScript(dir, "NeoFS object details", "attrs", trayExe)
	_ = writeNautilusScript(dir, "Delete from NeoFS container", "delete", trayExe)
}

// RegisterFileAttrsShellVerb is a no-op on Linux (use RegisterNeoFSContextMenuVerbs).
func RegisterFileAttrsShellVerb(_ string) {}

// UnregisterNeoFSContextMenuVerbs removes scripts written by RegisterNeoFSContextMenuVerbs.
func UnregisterNeoFSContextMenuVerbs() {
	dir, err := nautilusScriptsDir()
	if err != nil {
		return
	}
	for _, name := range []string{"NeoFS object details", "Delete from NeoFS container"} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		if isOurNautilusScript(p) {
			_ = os.Remove(p)
		}
	}
}
