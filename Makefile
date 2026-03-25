.PHONY: build tidy test lint

BIN_DIR := bin
DIST_DIR := dist
VERSION ?= dev
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

build:
	mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/neofs-mount ./cmd/neofs-mount
	go build $(LDFLAGS) -o $(BIN_DIR)/neofs-mount-tray ./cmd/neofs-mount-tray

APPIMAGETOOL := $(BIN_DIR)/appimagetool

$(APPIMAGETOOL):
	mkdir -p $(BIN_DIR)
	curl -L -o $(APPIMAGETOOL) https://github.com/AppImage/AppImageKit/releases/download/continuous/appimagetool-x86_64.AppImage
	chmod +x $(APPIMAGETOOL)

build-linux-appimage: $(APPIMAGETOOL)
	mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(BIN_DIR)/neofs-mount-tray ./cmd/neofs-mount-tray
	mkdir -p $(BIN_DIR)/AppDir/usr/bin
	cp $(BIN_DIR)/neofs-mount-tray $(BIN_DIR)/AppDir/usr/bin/
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
	mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-linux-amd64 ./cmd/neofs-mount
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-tray-linux-amd64 ./cmd/neofs-mount-tray
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-linux-arm64 ./cmd/neofs-mount
	go run github.com/fyne-io/fyne-cross@latest linux -arch=arm64 -app-id com.mathias.neofsmount -env GOTOOLCHAIN=auto ./cmd/neofs-mount-tray
	cp fyne-cross/bin/linux-arm64/neofs-mount-tray $(DIST_DIR)/neofs-mount-tray-linux-arm64

build-darwin:
	mkdir -p $(DIST_DIR)
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

