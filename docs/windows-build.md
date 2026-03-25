# Building neoFS-mount on Windows 10 / 11

This guide covers building the Windows version of `neofs-mount-tray`, which uses the native **Windows Cloud Files API (CfApi)** to present NeoFS containers as a local sync folder in Explorer — no third-party drivers required.

---

## Prerequisites

### 1. Go 1.22+

Download from <https://go.dev/dl/> and install. Verify:

```powershell
go version
```

### 2. TDM-GCC (MinGW-w64 C compiler for CGo)

CfApi bindings require a C compiler. [TDM-GCC](https://jmeubank.github.io/tdm-gcc/) is the easiest option:

1. Download **TDM64 installer** from <https://jmeubank.github.io/tdm-gcc/download/>
2. Run the installer — choose **"Create"** not "Manage"
3. Keep defaults (installs to `C:\TDM-GCC-64`)
4. Ensure `C:\TDM-GCC-64\bin` is in your `PATH` (the installer does this automatically)

Verify:

```powershell
gcc --version
```

### 3. Windows SDK (for `cldapi.lib`)

Install the **Windows 10 SDK** (build 17763 or later) via one of:

- **Visual Studio Installer** → Individual Components → Windows 10 SDK (any recent version)
- **Standalone**: <https://developer.microsoft.com/en-us/windows/downloads/windows-sdk/>

The library we need is `cldapi.lib`. After installation it lives at:

```
C:\Program Files (x86)\Windows Kits\10\Lib\<version>\um\x64\cldapi.lib
```

Tell the CGo linker where to find it by adding the SDK lib path to your environment:

```powershell
$sdkVer = (Get-Item "C:\Program Files (x86)\Windows Kits\10\Lib\*" | Sort-Object Name | Select-Object -Last 1).Name
$env:CGO_LDFLAGS = "-L`"C:\Program Files (x86)\Windows Kits\10\Lib\$sdkVer\um\x64`""
```

Or add it permanently via System Properties → Environment Variables.

---

## Clone the Repository

```powershell
git clone https://github.com/<your-org>/neofs-mount.git
cd neofs-mount
git checkout win   # the Windows CfApi branch
```

---

## Build

```powershell
$env:CGO_ENABLED = "1"
$env:GOOS        = "windows"
$env:GOARCH      = "amd64"

# Tray application (the main GUI)
go build -o bin\neofs-mount-tray.exe .\cmd\neofs-mount-tray

# Headless daemon (optional)
go build -o bin\neofs-mount.exe .\cmd\neofs-mount
```

Both binaries end up in `.\bin\`.

> **Tip**: If CGo can't find `cldapi.lib`, double-check `CGO_LDFLAGS` and that TDM-GCC is on your `PATH`.

---

## Run

```powershell
.\bin\neofs-mount-tray.exe
```

On first launch:
1. The tray icon appears in the system notification area
2. Click **Open…** → **Settings** and fill in your Endpoint, Wallet Key, and Mountpoint (any empty folder, e.g. `C:\neoFS`)
3. Click **Save Settings**
4. The app auto-mounts on startup (or click **Mount File System** on the Dashboard)

Windows Explorer will show the folder as a Cloud Sync root with placeholder files that hydrate on demand from NeoFS.

---

## Test

```powershell
$env:CGO_ENABLED = "1"

# Run all tests (CfApi tests auto-skip on non-Windows, but here they run)
go test .\internal\cfapi\... -v -timeout 60s

# Run only the fast unit tests (no network):
go test .\internal\cfapi\... -v -run "TestSplitPath"
```

### What the tests cover

| Test | Description |
|---|---|
| `TestRegisterUnregisterSyncRoot` | Registers and unregisters the CfApi sync root |
| `TestConnectDisconnect` | Full connect/disconnect session lifecycle |
| `TestCreatePlaceholders` | Creates placeholder files in a temp folder and asserts they exist on disk |
| `TestSplitPath` | Unit tests for the path-splitting helper (no Windows required) |

> `TestRegisterUnregisterSyncRoot` and `TestConnectDisconnect` require Windows 10 build 16299+ and will fail on older versions.

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `cgo: C compiler "gcc" not found` | Add TDM-GCC bin to PATH and reopen PowerShell |
| `undefined reference to CfRegisterSyncRoot` | Set `CGO_LDFLAGS` to point at the SDK um\x64 lib directory |
| `HRESULT 0x80070005` (Access Denied) | Folder is already registered; try a different temp path or unregister first via `CfUnregisterSyncRoot` |
| `HRESULT 0x80070057` (Invalid Argument) | Sync root path must exist before calling Register |
| Mount folder shows no files | Check that the NeoFS endpoint is reachable from Windows; try `telnet <endpoint> 8080` |
