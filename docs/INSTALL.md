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

### Install macFUSE

Install macFUSE from the official installer. You may need to approve the system extension in **System Settings → Privacy & Security**.

### Unmount

- `umount <mountpoint>`

## Common problems

### “permission denied” when mounting

- Check FUSE permissions (Linux), and ensure the mountpoint exists and is owned by your user.

### `fusermount exited with code 256` (or similar)

That value is the Unix wait status for **exit code 1** from `fusermount3` / `fusermount` — the helper refused the mount (busy mountpoint, stale FUSE session, non-empty directory, etc.). Try lazy unmount, then mount again:

```bash
fusermount3 -u -z /your/mountpoint
```

Use `fusermount -u -z` if `fusermount3` is not installed. Ensure the directory is empty and not already listed in `mount | grep fuse`.

### Finder shows errors / hangs

- FUSE filesystems can get hit with lots of metadata reads. Use caching options once available and try mounting with read-only if you only need browsing.

