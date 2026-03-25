.PHONY: build tidy test lint

BIN_DIR := bin
DIST_DIR := dist
VERSION ?= dev
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

build:
	mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/neofs-mount ./cmd/neofs-mount
	go build $(LDFLAGS) -o $(BIN_DIR)/neofs-mount-tray ./cmd/neofs-mount-tray

build-linux-amd64:
	mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-linux-amd64 ./cmd/neofs-mount
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-tray-linux-amd64 ./cmd/neofs-mount-tray

build-linux-arm64:
	mkdir -p $(DIST_DIR)
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-linux-arm64 ./cmd/neofs-mount
	go run github.com/fyne-io/fyne-cross@latest linux -arch=arm64 -app-id com.mathias.neofsmount ./cmd/neofs-mount-tray
	cp fyne-cross/bin/linux-arm64/neofs-mount-tray $(DIST_DIR)/neofs-mount-tray-linux-arm64

build-darwin-amd64:
	mkdir -p $(DIST_DIR)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-darwin-amd64 ./cmd/neofs-mount
	go run github.com/fyne-io/fyne-cross@latest darwin -arch=amd64 -app-id com.mathias.neofsmount ./cmd/neofs-mount-tray
	cp fyne-cross/bin/darwin-amd64/neofs-mount-tray $(DIST_DIR)/neofs-mount-tray-darwin-amd64

build-darwin-arm64:
	mkdir -p $(DIST_DIR)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/neofs-mount-darwin-arm64 ./cmd/neofs-mount
	go run github.com/fyne-io/fyne-cross@latest darwin -arch=arm64 -app-id com.mathias.neofsmount ./cmd/neofs-mount-tray
	cp fyne-cross/bin/darwin-arm64/neofs-mount-tray $(DIST_DIR)/neofs-mount-tray-darwin-arm64

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64

dist: build-all
	cd $(DIST_DIR) && sha256sum neofs-mount-* > SHA256SUMS

tidy:
	go mod tidy

test:
	go test ./...

lint:
	go vet ./...

