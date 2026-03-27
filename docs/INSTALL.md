# Install & troubleshooting

## Linux

### Install FUSE

- Arch/Manjaro: `sudo pacman -S fuse3`
- Ubuntu/Debian: `sudo apt-get install -y fuse3`

Ensure your user can mount FUSE (often via the `fuse` group) depending on distro configuration.

### GNOME Files (Nautilus) context menu

NeoFS Mount can add the same actions as on Windows (**NeoFS object details** and **Delete from NeoFS container**) to the Nautilus **Scripts** submenu:

1. Start the tray app once (or run `make build` / your usual install). On Linux it writes two small shell scripts under `~/.local/share/nautilus/scripts/` (or `$XDG_DATA_HOME/nautilus/scripts/`) pointing at your `neofs-mount-tray` binary.
2. Fully quit Nautilus if it was already running (`nautilus -q`), then open a folder again so it rescans the scripts directory.
3. Right‑click a file or folder **inside your NeoFS mount**, then choose **Scripts → NeoFS object details** or **Scripts → Delete from NeoFS container**.

Scripts only run for paths under your configured mountpoint; the tray does not need to be running for those invocations (each run starts a short‑lived UI for attrs or delete).

Other file managers (Dolphin, Thunar, etc.) do not use this location; you can still run `neofs-mount-tray -neofs-attrs /path/under/mount` or `-neofs-delete …` from a terminal or your own launcher.

### Unmount

- `fusermount3 -u <mountpoint>`

## macOS

### Native path: File Provider (recommended)

NeoFS containers are exposed through Apple’s **File Provider** framework (Finder integration), not kernel FUSE:

1. Build **`NeoFSMount.app`** from [`macos/NeoFSMount`](../macos/NeoFSMount/README.md) with Xcode (Go + CGO required for the embedded static library).
2. Install the app (bundle identifier **`org.neofs.mount`**). Use the same config as the tray: **`~/Library/Application Support/neofs-mount/config.toml`**.
3. In the app, register the **File Provider** domain, then open **Finder** and look for **NeoFS** under Locations / sidebar.

The Fyne tray’s **Mount** action runs `open -gn -b org.neofs.mount` to launch this app when it is installed. **Unmount** in the tray does not tear down File Provider domains; disconnect or quit from the host app as appropriate.

Override the bundle id with env **`NEOFS_FP_BUNDLE_ID`** if you use a custom signing identity.

### Optional: macFUSE (legacy POSIX mount)

If you maintain a **custom FUSE build** of neoFS-mount (not shipped in the default `linux`-only FUSE tag set), you could still use macFUSE for a directory mount—this is **not** the default integration anymore.

### Unmount

- **File Provider:** disconnect via the neoFS Mount app / Finder (not `umount` on a mountpoint).
- **Legacy FUSE only:** `umount -f <mountpoint>`

## Common problems

### “permission denied” when mounting

- **Linux:** Check FUSE permissions and that the mountpoint exists and is owned by your user.
- **macOS (File Provider):** Ensure the app is signed with appropriate **File Provider** / **App Group** entitlements for distribution builds.

### `fusermount exited with code 256` (or similar) — Linux

That value is the Unix wait status for **exit code 1** from `fusermount3` / `fusermount` — the helper refused the mount (busy mountpoint, stale FUSE session, non-empty directory, etc.). Try lazy unmount, then mount again:

```bash
fusermount3 -u -z /your/mountpoint
```

Use `fusermount -u -z` if `fusermount3` is not installed. Ensure the directory is empty and not already listed in `mount | grep fuse`.

### Finder shows errors / hangs

- **Linux FUSE:** The kernel can issue many metadata reads; try read-only if you only need browsing.
- **macOS File Provider:** Enumeration and fetch are implemented incrementally; ensure the NeoFS endpoint and wallet in `config.toml` are valid (**Connect NeoFS** in the host app calls the Go bridge).
