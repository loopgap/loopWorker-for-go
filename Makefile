# LoopWorker Makefile
# ==================== Configuration ====================
APP_NAME    := loopworker
CMD_DIR     := ./cmd/loopworker
BUILD_DIR   := ./bin
GO          := go
GOFLAGS     := -v
LDFLAGS     := -s -w

# ==================== Build ====================
.PHONY: build build-all clean

build:
	@echo "==> Building $(APP_NAME)..."
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)$(if $(filter windows,$(OS)),.exe,) $(CMD_DIR)

build-all: build

clean:
	@echo "==> Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	rm -f *.exe coverage coverage.out coverage.html

# ==================== Test ====================
.PHONY: test test-short test-cover test-race test-integration bench

test:
	@echo "==> Running tests..."
	$(GO) test $(GOFLAGS) ./...

test-short:
	@echo "==> Running short tests..."
	$(GO) test -short $(GOFLAGS) ./...

test-cover:
	@echo "==> Running tests with coverage..."
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	$(GO) tool cover -func=coverage.out

test-race:
	@echo "==> Running tests with race detector..."
	$(GO) test -race $(GOFLAGS) ./...

test-integration:
	@echo "==> Running integration tests..."
	$(GO) test $(GOFLAGS) -tags=integration ./integration/...

bench:
	@echo "==> Running benchmarks..."
	$(GO) test -bench=. -benchmem ./...

# ==================== Quality ====================
.PHONY: lint vet fmt check

lint:
	@echo "==> Running linter..."
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed, skipping"

vet:
	@echo "==> Running go vet..."
	$(GO) vet ./...

fmt:
	@echo "==> Formatting code..."
	$(GO) fmt ./...

check: fmt vet test
	@echo "==> All checks passed!"

# ==================== Dependencies ====================
.PHONY: deps deps-update tidy

deps:
	@echo "==> Downloading dependencies..."
	$(GO) mod download

deps-update:
	@echo "==> Updating dependencies..."
	$(GO) get -u ./...
	$(GO) mod tidy

tidy:
	@echo "==> Tidying modules..."
	$(GO) mod tidy

# ==================== Web (Canvas UI) ====================
.PHONY: web-install web-dev web-build

web-install:
	@echo "==> Installing web dependencies..."
	cd web/canvas && npm install

web-dev:
	@echo "==> Starting web dev server..."
	cd web/canvas && npm run dev

web-build:
	@echo "==> Building web assets..."
	cd web/canvas && npm run build

# ==================== Run ====================
.PHONY: run run-tui

run: build
	@echo "==> Starting $(APP_NAME)..."
	$(BUILD_DIR)/$(APP_NAME) -port 19527

run-tui: build
	@echo "==> Starting $(APP_NAME) in TUI mode..."
	$(BUILD_DIR)/$(APP_NAME) -tui

# ==================== Help ====================
.PHONY: help

help:
	@echo "LoopWorker Build System"
	@echo "======================"
	@echo ""
	@echo "  make build          Build the main binary"
	@echo "  make clean          Remove build artifacts"
	@echo "  make test           Run all tests"
	@echo "  make test-short     Run short tests"
	@echo "  make test-cover     Run tests with coverage report"
	@echo "  make test-race      Run tests with race detector"
	@echo "  make test-integration Run integration tests"
	@echo "  make bench          Run benchmarks"
	@echo "  make lint           Run linter"
	@echo "  make vet            Run go vet"
	@echo "  make fmt            Format code"
	@echo "  make check          Run fmt + vet + test"
	@echo "  make deps           Download dependencies"
	@echo "  make tidy           Tidy go modules"
	@echo "  make web-install    Install web dependencies"
	@echo "  make web-dev        Start web dev server"
	@echo "  make web-build      Build web assets"
	@echo "  make run            Build and run server"
	@echo "  make run-tui        Build and run in TUI mode"
	@echo "  make help           Show this help"
