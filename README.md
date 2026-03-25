<p align="center">
  <img src="logo.png" alt="neoFS-mount Logo" width="300"/>
</p>

# neoFS-mount

Mount [NeoFS](https://github.com/nspcc-dev/neofs-node) containers as a local filesystem on Linux and macOS, complete with a native Fyne System Tray application.

## Features
- **Native Filesystem Mount:** Browse, open, and write to NeoFS containers locally using OS-native FUSE bounds.
- **Cross-Platform System Tray:** A lightweight `neofs-mount-tray` desktop app for toggling your mounts directly from your UI taskbar.
- **Live GAS Balance:** View your NeoFS account's GAS balance directly in the tray menu.
- **Direct Top Up:** Seamlessly deposit NEP-17 GAS from the Neo N3 blockchain straight into your NeoFS node by using the Tray connection window.
- **Startup Automation:** Enable `Run at Login` and `Auto-Mount` capabilities out-of-the-box.

## Quick Start

1. **Install FUSE**
   - Debian/Ubuntu: `sudo apt install fuse3`
   - Arch: `sudo pacman -S fuse3`
   - macOS: Install [macFUSE](https://osxfuse.github.io/)

2. **Run neoFS-mount GUI (Recommended)**
   ```bash
   ./bin/neofs-mount-tray
   ```
   Open the **Settings** menu inside the system tray to configure your `Endpoint`, `Wallet Key`, `Network`, and target `Mountpoint`.

3. **Run neoFS-mount CLI**
   ```bash
   ./bin/neofs-mount \
     --endpoint s03.neofs.devenv:8080 \
     --wallet-key /path/to/wallet.key \
     --mountpoint /tmp/neofs
   ```

4. **Unmount**
   - Linux: `fusermount3 -u /tmp/neofs`
   - macOS: `umount /tmp/neofs`
   *(Alternatively, simply click "Unmount" from the System Tray).*

## Building and Releasing
We use `make` alongside [Fyne Cross](https://github.com/fyne-io/fyne-cross) to natively compile isolated multi-arch CGO binaries for Mac & Linux using docker.

```bash
make build-all
```
Your compiled applications will be zipped and available seamlessly across architectures in the local `dist/` directory. Check the bundled GitHub Actions `.yml` workflow for automated CI/CD branch releases.

## Documentation
- [Configuration & Settings](docs/CONFIG.md)
- [Filesystem Semantics](docs/SEMANTICS.md)
- [Troubleshooting](docs/INSTALL.md)

