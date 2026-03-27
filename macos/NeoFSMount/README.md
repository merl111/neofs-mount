# NeoFS Mount (macOS File Provider)

Native host app (`org.neofs.mount`) and **File Provider** extension that link the Go archive from `../GoBridge/`.

## Build

1. Install **Go** (e.g. Homebrew) and **Xcode**.
2. Open `NeoFSMount.xcodeproj` and build the **NeoFSMount** scheme (⌘B).

The **Build Go static library** run script compiles `macos/GoBridge` with `CGO_ENABLED=1` before linking Swift.

## Run

- Build and run the app. Use **Register File Provider domain**, then look for **NeoFS** in Finder’s sidebar / Locations.
- **Connect NeoFS** reads `~/Library/Application Support/neofs-mount/config.toml` (same as the Fyne tray) and calls `NeoFsFpInit`.

## Signing & entitlements

CI uses `CODE_SIGN_IDENTITY=-` for ad-hoc signing. For distribution, set a **Development Team**, enable the **File Provider** (or **File Provider Testing**) capability, and match the **App Group** `group.org.neofs.mount` in the Apple Developer portal.

The Fyne tray’s **Mount** action runs `open -gn -b org.neofs.mount` to launch this app when the `.app` is installed.
