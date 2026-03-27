.PHONY: build build-windows cfapi-implib tidy test lint

BIN_DIR := bin
DIST_DIR := dist
VERSION ?= dev
ifeq ($(OS),Windows_NT)
BUILD_TIME := $(shell powershell -NoProfile -Command "[DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')")
else
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
endif
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)"

# Windows cmd.exe has no "mkdir -p" and errors if the directory exists; sh -c "mkdir -p" works on Unix/Git-Bash.
ifeq ($(OS),Windows_NT)
mkdir_p = @powershell -NoProfile -Command "New-Item -ItemType Directory -Force -Path \"$(subst \,/,$(1))\" | Out-Null"
else
mkdir_p = @mkdir -p "$(1)"
endif

# MinGW import library for Windows CfApi (see internal/cfapi/cfapi.go). Regenerate with: make cfapi-implib
CFAPI_DIR := internal/cfapi
CFAPI_IMPLIB := $(CFAPI_DIR)/libcldapi.a

cfapi-implib: $(CFAPI_IMPLIB)

$(CFAPI_IMPLIB): $(CFAPI_DIR)/cldapi.def
	dlltool -d $(CFAPI_DIR)/cldapi.def -l $(CFAPI_IMPLIB) -D cldapi.dll

# IExplorerCommand in-process server so NeoFS verbs appear on the Windows 11 primary context menu.
# Requires MinGW g++ on PATH when building on Windows; if absent, registration falls back to legacy verbs.
ifeq ($(OS),Windows_NT)
$(BIN_DIR)/neofs-shellcmd.dll: win/neofs_shellcmd/neofs_shellcmd.cpp
	$(call mkdir_p,$(BIN_DIR))
	powershell -NoProfile -Command "if (Get-Command g++ -ErrorAction SilentlyContinue) { g++ -shared -O2 -static-libgcc -static-libstdc++ -o '$(BIN_DIR)/neofs-shellcmd.dll' 'win/neofs_shellcmd/neofs_shellcmd.cpp' -luuid -lole32 -lshell32 -lshlwapi } else { Write-Host 'Skipping neofs-shellcmd.dll (no g++ in PATH; Win11 menu uses Show more options only).'; if (Test-Path '$(BIN_DIR)/neofs-shellcmd.dll') { Remove-Item -Force '$(BIN_DIR)/neofs-shellcmd.dll' } }"
endif

# Windows amd64: run on a Windows host (or MSYS2) with gcc on PATH. Uses bundled libcldapi.a so -lcldapi / SDK paths are not required.
# Windows + MinGW CGO: DWARF/debug in the PE can trigger "This app can't run on your PC".
# -s -w strips Go symbol/DWARF; CGO_CFLAGS avoids GCC debug; -trimpath drops host paths from the binary.
# -fno-asynchronous-unwind-tables shrinks unwind metadata some bad PE loaders choke on.
WIN_CGO_CFLAGS := -O2 -g0 -fno-asynchronous-unwind-tables
WIN_TRAY_GO := -trimpath -ldflags '-s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME) -H windowsgui'
WIN_CLI_GO  := -trimpath -ldflags '-s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)'
# PE icon COFF objects must not live under cmd/...: Go links *.syso for every GOOS, which breaks Linux and fyne-cross (lld: unknown file type).
WIN_PE_RSRC_TRAY := win/pe-rsrc/neofs-mount-tray.syso
WIN_PE_RSRC_CLI := win/pe-rsrc/neofs-mount.syso

ifeq ($(OS),Windows_NT)
build-windows: $(CFAPI_IMPLIB) $(BIN_DIR)/neofs-shellcmd.dll
	$(call mkdir_p,$(BIN_DIR))
	powershell -NoProfile -Command "Copy-Item -Force '$(subst /,\,$(WIN_PE_RSRC_TRAY))' 'cmd\neofs-mount-tray\rsrc.syso'; Copy-Item -Force '$(subst /,\,$(WIN_PE_RSRC_CLI))' 'cmd\neofs-mount\rsrc.syso'"
	powershell -NoProfile -Command "$$env:CGO_ENABLED='1'; $$env:GOOS='windows'; $$env:GOARCH='amd64'; $$env:CGO_CFLAGS='$(WIN_CGO_CFLAGS)'; go build $(WIN_TRAY_GO) -o '$(BIN_DIR)/neofs-mount-tray.exe' './cmd/neofs-mount-tray'"
	powershell -NoProfile -Command "$$env:CGO_ENABLED='1'; $$env:GOOS='windows'; $$env:GOARCH='amd64'; $$env:CGO_CFLAGS='$(WIN_CGO_CFLAGS)'; go build $(WIN_CLI_GO) -o '$(BIN_DIR)/neofs-mount.exe' './cmd/neofs-mount'"
else
build-windows: $(CFAPI_IMPLIB)
	$(call mkdir_p,$(BIN_DIR))
	cp $(WIN_PE_RSRC_TRAY) cmd/neofs-mount-tray/rsrc.syso
	cp $(WIN_PE_RSRC_CLI) cmd/neofs-mount/rsrc.syso
	CGO_CFLAGS=$(WIN_CGO_CFLAGS) CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build $(WIN_TRAY_GO) -o $(BIN_DIR)/neofs-mount-tray.exe ./cmd/neofs-mount-tray
	CGO_CFLAGS=$(WIN_CGO_CFLAGS) CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build $(WIN_CLI_GO) -o $(BIN_DIR)/neofs-mount.exe ./cmd/neofs-mount
endif

build:
	$(call mkdir_p,$(BIN_DIR))
	go build $(LDFLAGS) -o $(BIN_DIR)/neofs-mount ./cmd/neofs-mount
	go build $(LDFLAGS) -o $(BIN_DIR)/neofs-mount-tray ./cmd/neofs-mount-tray

APPIMAGETOOL := $(BIN_DIR)/appimagetool

$(APPIMAGETOOL):
	$(call mkdir_p,$(BIN_DIR))
	curl -L -o $(APPIMAGETOOL) https://github.com/AppImage/AppImageKit/releases/download/continuous/appimagetool-x86_64.AppImage
	chmod +x $(APPIMAGETOOL)

build-linux-appimage: $(APPIMAGETOOL)
	$(call mkdir_p,$(DIST_DIR))
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(BIN_DIR)/neofs-mount-tray ./cmd/neofs-mount-tray
	$(call mkdir_p,$(BIN_DIR)/AppDir/usr/bin)
	cp $(BIN_DIR)/neofs-mount-tray $(BIN_DIR)/AppDir/usr/bin/
	ln -s usr/bin/neofs-mount-tray $(BIN_DIR)/AppDir/AppRun
	echo "[Desktop Entry]" > $(BIN_DIR)/AppDir/neofs-mount-tray.desktop
	echo "Name=neofs-mount-tray" >> $(BIN_DIR)/AppDir/neofs-mount-tray.desktop
	echo "Exec=neofs-mount-tray" >> $(BIN_DIR)/AppDir/neofs-mount-tray.desktop
	echo "Icon=logo-tray" >> $(BIN_DIR)/AppDir/neofs-mount-tray.desktop
	echo "Type=Application" >> $(BIN_DIR)/AppDir/neofs-mount-tray.desktop
	echo "Categories=Utility;" >> $(BIN_DIR)/AppDir/neofs-mount-tray.desktop
	cp logo-tray.png $(BIN_DIR)/AppDir/logo-tray.png
	cd $(BIN_DIR) && ./appimagetool --appimage-extract-and-run AppDir neofs-mount-tray-linux-amd64.AppImage
	mv $(BIN_DIR)/neofs-mount-tray*.AppImage $(DIST_DIR)/neofs-mount-tray-linux-amd64.AppImage || true
	rm -rf $(BIN_DIR)/AppDir

build-linux: build-linux-appimage
	$(call mkdir_p,$(DIST_DIR))
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-linux-amd64 ./cmd/neofs-mount
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-tray-linux-amd64 ./cmd/neofs-mount-tray
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-linux-arm64 ./cmd/neofs-mount
	go run github.com/fyne-io/fyne-cross@latest linux -arch=arm64 -app-id com.mathias.neofsmount -env GOTOOLCHAIN=auto ./cmd/neofs-mount-tray
	cp fyne-cross/bin/linux-arm64/neofs-mount-tray $(DIST_DIR)/neofs-mount-tray-linux-arm64

build-darwin:
	$(call mkdir_p,$(DIST_DIR))
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-darwin-amd64 ./cmd/neofs-mount
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-tray-darwin-amd64 ./cmd/neofs-mount-tray
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-darwin-arm64 ./cmd/neofs-mount
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-tray-darwin-arm64 ./cmd/neofs-mount-tray

build-all: build-linux build-darwin

dist: build-all
	cd $(DIST_DIR) && sha256sum neofs-mount-* > SHA256SUMS

tidy:
	go mod tidy

test:
	go test ./...

lint:
	go vet ./...

