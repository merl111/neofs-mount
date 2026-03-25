# Install & troubleshooting

## Linux

### Install FUSE

- Arch/Manjaro: `sudo pacman -S fuse3`
- Ubuntu/Debian: `sudo apt-get install -y fuse3`

Ensure your user can mount FUSE (often via the `fuse` group) depending on distro configuration.

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

### Finder shows errors / hangs

- FUSE filesystems can get hit with lots of metadata reads. Use caching options once available and try mounting with read-only if you only need browsing.

