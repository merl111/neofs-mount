.PHONY: build build-windows cfapi-implib tidy test lint
.PHONY: build-macos-app build-darwin-app install-macos build-macos-pkg

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
	@# Create a universal tray binary and wrap it in a .app bundle so macOS
	@# doesn't open Terminal when the user launches it.
	lipo -create -output $(DIST_DIR)/neofs-mount-tray-darwin-universal \
		$(DIST_DIR)/neofs-mount-tray-darwin-amd64 \
		$(DIST_DIR)/neofs-mount-tray-darwin-arm64
	rm -rf "$(DIST_DIR)/NeoFS Tray.app"
	mkdir -p "$(DIST_DIR)/NeoFS Tray.app/Contents/MacOS"
	mkdir -p "$(DIST_DIR)/NeoFS Tray.app/Contents/Resources"
	cp $(DIST_DIR)/neofs-mount-tray-darwin-universal "$(DIST_DIR)/NeoFS Tray.app/Contents/MacOS/NeoFS Tray"
	@printf '<?xml version="1.0" encoding="UTF-8"?>\n\
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n\
<plist version="1.0">\n<dict>\n\
\t<key>CFBundleExecutable</key>\n\t<string>NeoFS Tray</string>\n\
\t<key>CFBundleIdentifier</key>\n\t<string>org.neofs.mount.tray</string>\n\
\t<key>CFBundleName</key>\n\t<string>NeoFS Tray</string>\n\
\t<key>CFBundleDisplayName</key>\n\t<string>NeoFS</string>\n\
\t<key>CFBundlePackageType</key>\n\t<string>APPL</string>\n\
\t<key>CFBundleVersion</key>\n\t<string>$(VERSION)</string>\n\
\t<key>CFBundleShortVersionString</key>\n\t<string>$(VERSION)</string>\n\
\t<key>CFBundleIconFile</key>\n\t<string>icon</string>\n\
\t<key>LSUIElement</key>\n\t<true/>\n\
\t<key>NSHighResolutionCapable</key>\n\t<true/>\n\
</dict>\n</plist>\n' > "$(DIST_DIR)/NeoFS Tray.app/Contents/Info.plist"
	@# Generate .icns from the app icon PNGs in the Xcode asset catalog
	@ICONSET=$$(mktemp -d)/icon.iconset && mkdir -p "$$ICONSET" && \
	ICONS=macos/NeoFSMount/NeoFSMount/Assets.xcassets/AppIcon.appiconset && \
	cp "$$ICONS/icon_16x16.png"   "$$ICONSET/icon_16x16.png" && \
	cp "$$ICONS/icon_32x32.png"   "$$ICONSET/icon_16x16@2x.png" && \
	cp "$$ICONS/icon_32x32.png"   "$$ICONSET/icon_32x32.png" && \
	cp "$$ICONS/icon_64x64.png"   "$$ICONSET/icon_32x32@2x.png" && \
	cp "$$ICONS/icon_128x128.png" "$$ICONSET/icon_128x128.png" && \
	cp "$$ICONS/icon_256x256.png" "$$ICONSET/icon_128x128@2x.png" && \
	cp "$$ICONS/icon_256x256.png" "$$ICONSET/icon_256x256.png" && \
	cp "$$ICONS/icon_512x512.png" "$$ICONSET/icon_256x256@2x.png" && \
	cp "$$ICONS/icon_512x512.png" "$$ICONSET/icon_512x512.png" && \
	cp "$$ICONS/icon_1024x1024.png" "$$ICONSET/icon_512x512@2x.png" && \
	iconutil -c icns -o "$(DIST_DIR)/NeoFS Tray.app/Contents/Resources/icon.icns" "$$ICONSET" && \
	rm -rf "$$(dirname $$ICONSET)" || true
	@echo "Built $(DIST_DIR)/NeoFS Tray.app"

# Build the native macOS File Provider host app (.app) via Xcode.
# Outputs NeoFS.app into dist/NeoFS.app (and zips it as NeoFS.app.zip).
#
# The Xcode project already has DEVELOPMENT_TEAM and provisioning profiles
# configured. Pass DEVELOPMENT_TEAM=... only to override the project default.
#
# Requirements:
#   - Xcode with a logged-in Apple Developer account
#   - Provisioning profiles installed (Xcode manages these automatically)
XCODE_PROJECT := macos/NeoFSMount/NeoFSMount.xcodeproj
XCODE_SCHEME := NeoFSMount
XCODE_CONFIGURATION ?= Debug
APP_NAME := NeoFS

build-macos-app:
	$(call mkdir_p,$(DIST_DIR))
	xcodebuild -project $(XCODE_PROJECT) \
		-scheme $(XCODE_SCHEME) \
		-configuration $(XCODE_CONFIGURATION) \
		-destination 'platform=macOS' \
		-allowProvisioningUpdates \
		$(if $(DEVELOPMENT_TEAM),DEVELOPMENT_TEAM="$(DEVELOPMENT_TEAM)") \
		build
	rm -rf $(DIST_DIR)/$(APP_NAME).app
	@PRODUCTS_DIR=$$(xcodebuild -project $(XCODE_PROJECT) -scheme $(XCODE_SCHEME) \
		-configuration $(XCODE_CONFIGURATION) -showBuildSettings 2>/dev/null \
		| grep ' BUILT_PRODUCTS_DIR' | head -1 | awk '{print $$3}'); \
	cp -R "$$PRODUCTS_DIR/$(APP_NAME).app" "$(DIST_DIR)/$(APP_NAME).app"
	cd $(DIST_DIR) && rm -f $(APP_NAME).app.zip && /usr/bin/zip -qry $(APP_NAME).app.zip $(APP_NAME).app
	@echo "Built $(DIST_DIR)/$(APP_NAME).app"

# Full macOS build: Go CLI + tray binaries (universal) + Xcode File Provider .app
build-darwin-app: build-darwin build-macos-app

# Install the .app into /Applications, register with LaunchServices, and enable
# the File Provider extension. Rebuilds if needed.
install-macos: build-darwin-app
	osascript -e 'do shell script "rm -rf /Applications/$(APP_NAME).app && cp -R $(CURDIR)/$(DIST_DIR)/$(APP_NAME).app /Applications/$(APP_NAME).app && rm -rf \"/Applications/NeoFS Tray.app\" && cp -R \"$(CURDIR)/$(DIST_DIR)/NeoFS Tray.app\" \"/Applications/NeoFS Tray.app\"" with administrator privileges'
	/System/Library/Frameworks/CoreServices.framework/Versions/Current/Frameworks/LaunchServices.framework/Versions/Current/Support/lsregister -f -R -trusted /Applications/$(APP_NAME).app
	/System/Library/Frameworks/CoreServices.framework/Versions/Current/Frameworks/LaunchServices.framework/Versions/Current/Support/lsregister -f -R -trusted "/Applications/NeoFS Tray.app"
	pluginkit -e use -i org.neofs.mount.FileProvider
	@echo "Installed to /Applications — run 'open /Applications/NeoFS Tray.app' to start"

# Build a .pkg installer that installs both apps into /Applications and then
# enables the File Provider extension. The postinstall also launches NeoFS.app
# once so it can register the domain.
build-macos-pkg: build-darwin-app
	$(call mkdir_p,$(DIST_DIR)/pkgroot/Applications)
	rm -rf "$(DIST_DIR)/pkgroot/Applications/NeoFS.app" "$(DIST_DIR)/pkgroot/Applications/NeoFS Tray.app"
	cp -R "$(DIST_DIR)/NeoFS.app" "$(DIST_DIR)/pkgroot/Applications/NeoFS.app"
	cp -R "$(DIST_DIR)/NeoFS Tray.app" "$(DIST_DIR)/pkgroot/Applications/NeoFS Tray.app"
	rm -rf "$(DIST_DIR)/pkgscripts" && mkdir -p "$(DIST_DIR)/pkgscripts"
	@printf '#!/bin/sh\nset -e\n\nAPP1=\"/Applications/NeoFS.app\"\nAPP2=\"/Applications/NeoFS Tray.app\"\n\n# Refresh LaunchServices registration\nif [ -x \"/System/Library/Frameworks/CoreServices.framework/Versions/Current/Frameworks/LaunchServices.framework/Versions/Current/Support/lsregister\" ]; then\n  /System/Library/Frameworks/CoreServices.framework/Versions/Current/Frameworks/LaunchServices.framework/Versions/Current/Support/lsregister -f -R -trusted \"$APP1\" || true\n  /System/Library/Frameworks/CoreServices.framework/Versions/Current/Frameworks/LaunchServices.framework/Versions/Current/Support/lsregister -f -R -trusted \"$APP2\" || true\nfi\n\n# Enable the File Provider extension\n/usr/bin/pluginkit -e use -i org.neofs.mount.FileProvider || true\n\n# Launch NeoFS once to ensure the domain is registered\n/usr/bin/open -gj \"$APP1\" || true\n\nexit 0\n' > "$(DIST_DIR)/pkgscripts/postinstall"
	chmod +x "$(DIST_DIR)/pkgscripts/postinstall"
	rm -f "$(DIST_DIR)/NeoFS-$(VERSION).pkg"
	pkgbuild --root "$(DIST_DIR)/pkgroot" \
		--identifier org.neofs.mount.pkg \
		--version "$(VERSION)" \
		--install-location / \
		--scripts "$(DIST_DIR)/pkgscripts" \
		"$(DIST_DIR)/NeoFS-$(VERSION).pkg"
	@echo "Built $(DIST_DIR)/NeoFS-$(VERSION).pkg"

build-all: build-linux build-darwin

dist: build-all
	cd $(DIST_DIR) && sha256sum neofs-mount-* > SHA256SUMS

tidy:
	go mod tidy

test:
	go test ./...

# Cross-compile CLI for Darwin from Linux CI (pure Go; tray needs macOS + CGO for Fyne).
ci-darwin-cli:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o /dev/null ./cmd/neofs-mount
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o /dev/null ./cmd/neofs-mount

lint:
	go vet ./...

