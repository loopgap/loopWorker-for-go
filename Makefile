# LoopWorker Makefile

# Build information
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
GO_VERSION ?= $(shell go version | cut -d' ' -f3)

# Build flags
LDFLAGS = -ldflags "\
	-X loopworker/version.Version=$(VERSION) \
	-X loopworker/version.GitCommit=$(GIT_COMMIT) \
	-X loopworker/version.BuildDate=$(BUILD_DATE) \
	-X loopworker/version.GoVersion=$(GO_VERSION)"

# Binary names
BINARIES = loopworker loopctl loopdebug loopwatch loopbench loopsim

# Default target
.PHONY: all
all: build

# Build all binaries
.PHONY: build
build:
	@echo "Building LoopWorker $(VERSION)..."
	@for cmd in $(BINARIES); do \
		echo "  Building $$cmd..."; \
		go build $(LDFLAGS) -o bin/$$cmd ./cmd/$$cmd/; \
	done
	@echo "Build complete!"

# Build for current platform
.PHONY: build-current
build-current:
	@echo "Building for current platform..."
	@for cmd in $(BINARIES); do \
		go build $(LDFLAGS) -o bin/$$cmd ./cmd/$$cmd/; \
	done

# Cross-compile for Linux
.PHONY: build-linux
build-linux:
	@echo "Building for Linux..."
	@for cmd in $(BINARIES); do \
		GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o bin/$$cmd-linux-amd64 ./cmd/$$cmd/; \
	done

# Cross-compile for Windows
.PHONY: build-windows
build-windows:
	@echo "Building for Windows..."
	@for cmd in $(BINARIES); do \
		GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o bin/$$cmd-windows-amd64.exe ./cmd/$$cmd/; \
	done

# Cross-compile for macOS
.PHONY: build-darwin
build-darwin:
	@echo "Building for macOS..."
	@for cmd in $(BINARIES); do \
		GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o bin/$$cmd-darwin-amd64 ./cmd/$$cmd/; \
	done

# Build all platforms
.PHONY: build-all
build-all: build-linux build-windows build-darwin
	@echo "Cross-compilation complete!"

# Run tests
.PHONY: test
test:
	@echo "Running tests..."
	go test -count=1 ./...

# Run tests with race detection
.PHONY: test-race
test-race:
	@echo "Running tests with race detection..."
	go test -race -count=1 ./...

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Format code
.PHONY: fmt
fmt:
	@echo "Formatting code..."
	gofmt -w .

# Run linter
.PHONY: lint
lint:
	@echo "Running linter..."
	go vet ./...

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning..."
	rm -rf bin/
	rm -f coverage.out coverage.html

# Install binaries
.PHONY: install
install: build
	@echo "Installing binaries..."
	@for cmd in $(BINARIES); do \
		cp bin/$$cmd $(GOPATH)/bin/; \
	done
	@echo "Installation complete!"

# Docker build
.PHONY: docker
docker:
	@echo "Building Docker image..."
	docker build -t loopworker:$(VERSION) .

# Run server
.PHONY: run
run: build
	@echo "Starting LoopWorker..."
	./bin/loopworker

# Show version
.PHONY: version
version:
	@echo "Version: $(VERSION)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Date: $(BUILD_DATE)"
	@echo "Go Version: $(GO_VERSION)"

# Help
.PHONY: help
help:
	@echo "LoopWorker Build System"
	@echo ""
	@echo "Targets:"
	@echo "  build         Build all binaries"
	@echo "  build-linux   Cross-compile for Linux"
	@echo "  build-windows Cross-compile for Windows"
	@echo "  build-darwin  Cross-compile for macOS"
	@echo "  build-all     Cross-compile for all platforms"
	@echo "  test          Run tests"
	@echo "  test-race     Run tests with race detection"
	@echo "  test-coverage Run tests with coverage report"
	@echo "  fmt           Format code"
	@echo "  lint          Run linter"
	@echo "  clean         Clean build artifacts"
	@echo "  install       Install binaries to GOPATH/bin"
	@echo "  docker        Build Docker image"
	@echo "  run           Build and run server"
	@echo "  version       Show version information"
	@echo "  help          Show this help"
