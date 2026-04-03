# Homelab Horizon Build Makefile

BINARY_NAME=homelab-horizon
CMD_PATH=./cmd/homelab-horizon
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS=-ldflags "-s -w -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

# Default target
.PHONY: all
all: build

# Generate TypeScript types from Go structs (requires tygo)
.PHONY: generate
generate:
	~/go/bin/tygo generate

# Build frontend (React SPA)
.PHONY: ui
ui: generate
	cd ui && npm ci && npm run build

# Create stub ui/dist for Go-only builds (no npm required)
ui/dist/index.html:
	mkdir -p ui/dist
	echo '<!DOCTYPE html><html><body>Run <code>make ui</code> to build the frontend.</body></html>' > ui/dist/index.html

# Build for current platform (includes frontend)
.PHONY: build
build: ui
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY_NAME) $(CMD_PATH)

# Build Go only (uses stub frontend if ui/dist doesn't exist)
.PHONY: build-go
build-go: ui/dist/index.html
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY_NAME) $(CMD_PATH)

# Run backend + frontend dev servers together (Ctrl-C stops both)
.PHONY: run
run: ui/node_modules
	@trap 'kill 0' EXIT; \
	cd ui && npm run dev & \
	go run $(CMD_PATH) & \
	wait

# Run Go backend only (builds frontend first, serves at /app/)
.PHONY: run-backend
run-backend: ui
	go run $(CMD_PATH)

# Run Vite frontend dev server only (proxies API to :8080)
.PHONY: run-frontend
run-frontend: ui/node_modules
	cd ui && npm run dev

# Install frontend dependencies if needed
ui/node_modules: ui/package.json
	cd ui && npm install
	@touch ui/node_modules

# Build for all platforms
.PHONY: build-all
build-all: ui build-linux-amd64 build-linux-arm64 build-linux-arm

# Linux AMD64 (most servers, x86_64)
.PHONY: build-linux-amd64
build-linux-amd64: ui dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)

# Linux ARM64 (Raspberry Pi 4/5, modern ARM servers)
.PHONY: build-linux-arm64
build-linux-arm64: ui dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)

# Linux ARM (Raspberry Pi 2/3, older 32-bit ARM)
.PHONY: build-linux-arm
build-linux-arm: ui dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-armv7 $(CMD_PATH)

# Clean build artifacts
.PHONY: clean
clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/
	rm -rf ui/dist/

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
release: clean dist ui build-all
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

# Build Docker demo image (vanilla Ubuntu + binary + demo config)
.PHONY: docker
docker: build-linux-amd64
	docker build -t homelab-horizon:demo .

# Run Docker demo container
.PHONY: docker-run
docker-run: docker
	docker run --rm -p 8080:8080 --name hz-demo homelab-horizon:demo

.PHONY: help
help:
	@echo "Homelab Horizon Build Targets:"
	@echo ""
	@echo "  make              - Build for current platform (includes frontend)"
	@echo "  make ui           - Build frontend only (React SPA)"
	@echo "  make build-go     - Build Go only (stub frontend)"
	@echo "  make run          - Run backend + frontend dev servers together"
	@echo "  make run-backend  - Run Go backend only (:8080)"
	@echo "  make run-frontend - Run Vite frontend dev server only (:5173)"
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
	@echo ""
	@echo "  make docker       - Build Docker demo image"
	@echo "  make docker-run   - Build and run Docker demo on :8080"
