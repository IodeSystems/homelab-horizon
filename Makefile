# Homelab Horizon Build Makefile

BINARY_NAME=homelab-horizon
CMD_PATH=./cmd/homelab-horizon
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

# Default target
.PHONY: all
all: build

# Build for current platform
.PHONY: build
build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY_NAME) $(CMD_PATH)

# Run locally
.PHONY: run
run:
	go run $(CMD_PATH)

# Build for all platforms
.PHONY: build-all
build-all: build-linux-amd64 build-linux-arm64 build-linux-arm

# Linux AMD64 (most servers, x86_64)
.PHONY: build-linux-amd64
build-linux-amd64: dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)

# Linux ARM64 (Raspberry Pi 4/5, modern ARM servers)
.PHONY: build-linux-arm64
build-linux-arm64: dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)

# Linux ARM (Raspberry Pi 2/3, older 32-bit ARM)
.PHONY: build-linux-arm
build-linux-arm: dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-armv7 $(CMD_PATH)

# Clean build artifacts
.PHONY: clean
clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/

# Run tests
.PHONY: test
test:
	go test -v ./...

# Run unit tests only
.PHONY: test-unit
test-unit:
	go test -v ./internal/...

# Run integration tests only
.PHONY: test-integration
test-integration:
	go test -v ./test/integration/...

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Check/lint
.PHONY: check
check:
	go vet ./...
	go fmt ./...

# Run all checks
.PHONY: test-all
test-all: test-unit test-integration check

# Create dist directory
dist:
	mkdir -p dist

# Build release archives
.PHONY: release
release: clean dist build-all
	@echo "Creating release archives..."
	cd dist && tar -czf $(BINARY_NAME)-linux-amd64.tar.gz $(BINARY_NAME)-linux-amd64
	cd dist && tar -czf $(BINARY_NAME)-linux-arm64.tar.gz $(BINARY_NAME)-linux-arm64
	cd dist && tar -czf $(BINARY_NAME)-linux-armv7.tar.gz $(BINARY_NAME)-linux-armv7
	@echo "Release archives created in dist/"
	@ls -la dist/*.tar.gz

# Install locally (requires sudo)
.PHONY: install
install: build
	sudo cp $(BINARY_NAME) /usr/local/bin/
	@echo "Installed to /usr/local/bin/$(BINARY_NAME)"

.PHONY: help
help:
	@echo "Homelab Horizon Build Targets:"
	@echo ""
	@echo "  make              - Build for current platform"
	@echo "  make run          - Run locally (go run)"
	@echo "  make build-all    - Build for all platforms"
	@echo "  make release      - Build all platforms and create .tar.gz archives"
	@echo ""
	@echo "  make build-linux-amd64  - Build for Linux x86_64"
	@echo "  make build-linux-arm64  - Build for Linux ARM64 (Raspberry Pi 4/5)"
	@echo "  make build-linux-arm    - Build for Linux ARMv7 (Raspberry Pi 2/3)"
	@echo ""
	@echo "  make install      - Install to /usr/local/bin (requires sudo)"
	@echo "  make clean        - Remove build artifacts"
	@echo "  make test         - Run tests"
	@echo "  make check        - Run go vet and fmt"
