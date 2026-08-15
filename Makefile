# LoopWorker Makefile
# ==================== Configuration ====================
APP_NAME    := loopworker
CMD_DIR     := ./cmd/loopworker
BUILD_DIR   := ./bin
GO          := go
GOFLAGS     := -v
LDFLAGS     := -s -w
WEB_DIR     := web/canvas
EMBED_DIR   := pkg/api/dist
NODE        := node
NPM         := npm

# Binary suffix: .exe on Windows (Git Bash exposes OS=Windows_NT), none elsewhere.
ifeq ($(OS),Windows_NT)
BIN_SUFFIX  := .exe
else
BIN_SUFFIX  :=
endif

# ==================== Build ====================
.PHONY: build build-all clean

# build depends on web-build so the embedded Canvas UI is always up to date.
build: web-build
	@echo "==> Building $(APP_NAME)..."
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME)$(BIN_SUFFIX) $(CMD_DIR)

build-all: build

clean:
	@echo "==> Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	rm -f *.exe coverage coverage.out coverage.html

# ==================== Test ====================
.PHONY: test test-short test-cover test-race test-integration test-e2e test-all bench

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

test-e2e: test-integration
	@echo "==> e2e suite currently maps to integration tests..."

test-all: fmt-check vet test test-race tidy-check
	@echo "==> All tests passed!"

bench:
	@echo "==> Running benchmarks..."
	$(GO) test -bench=. -benchmem ./...

# ==================== Quality ====================
.PHONY: lint vet fmt fmt-check check

lint:
	@echo "==> Running linter..."
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed, skipping"

vet:
	@echo "==> Running go vet..."
	$(GO) vet ./...

fmt:
	@echo "==> Formatting code..."
	$(GO) fmt ./...

# Format gate: fails if any source file is not gofmt-formatted.
fmt-check:
	@echo "==> Checking formatting..."
	@files="$$(gofmt -l .)"; \
	if [ -n "$$files" ]; then \
		echo "gofmt needed on:"; \
		echo "$$files"; \
		exit 1; \
	fi; \
	echo "all files are gofmt-formatted"

# go.mod tidiness gate: fails when 'go mod tidy' would change anything.
tidy-check:
	@echo "==> Checking go.mod is tidy..."
	@diff="$$($(GO) mod tidy -diff 2>&1)"; \
	if [ -n "$$diff" ]; then \
		echo "go.mod is NOT tidy; run 'make tidy'"; \
		echo "$$diff"; \
		exit 1; \
	fi; \
	echo "go.mod is tidy"

check: fmt vet test tidy-check
	@echo "==> All checks passed!"

# ==================== Dependencies ====================
.PHONY: deps deps-update tidy install

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

install:
	@echo "==> Installing binaries to GOPATH/bin..."
	$(GO) install ./cmd/...

# ==================== Web (Canvas UI) ====================
.PHONY: web-install web-dev web-build

web-install:
	@echo "==> Installing web dependencies..."
	cd $(WEB_DIR) && $(NPM) install

web-dev:
	@echo "==> Starting web dev server..."
	cd $(WEB_DIR) && $(NPM) run dev

# Builds the Canvas UI and copies the real artifacts into pkg/api/dist (embed source).
# Fallback: when Node/npm is unavailable, reuse the committed artifacts as long as
# they exist and are not the placeholder page; otherwise fail loudly (no silent placeholders).
web-build:
	@echo "==> Building web assets..."
	@if command -v $(NODE) >/dev/null 2>&1 && command -v $(NPM) >/dev/null 2>&1; then \
		cd $(WEB_DIR) && $(NPM) ci --no-audit --no-fund && $(NPM) run build; \
		mkdir -p $(EMBED_DIR); \
		cp -r $(WEB_DIR)/dist/. $(EMBED_DIR)/; \
		if ! grep -q "Placeholder" $(EMBED_DIR)/index.html; then \
			echo "==> Web assets built and copied to $(EMBED_DIR)/"; \
		else \
			echo "ERROR: built $(EMBED_DIR)/index.html looks like a placeholder; refusing to continue"; \
			exit 1; \
		fi \
	elif [ -f $(EMBED_DIR)/index.html ] && ! grep -q "Placeholder" $(EMBED_DIR)/index.html; then \
		echo "==> WARNING: Node/npm unavailable; using committed artifacts from $(EMBED_DIR)/"; \
	else \
		echo "ERROR: Node/npm unavailable and $(EMBED_DIR)/index.html is missing or a placeholder."; \
		echo "Install Node.js (or restore committed artifacts) and retry."; \
		exit 1; \
	fi

# ==================== Run ====================
.PHONY: run

run: build
	@echo "==> Starting $(APP_NAME)..."
	$(BUILD_DIR)/$(APP_NAME) -port 19527

# ==================== Help ====================
.PHONY: help

help:
	@echo "LoopWorker Build System"
	@echo "======================"
	@echo ""
	@echo "  make build            Build the main binary (runs web-build first)"
	@echo "  make clean            Remove build artifacts"
	@echo "  make test             Run all tests"
	@echo "  make test-short       Run short tests"
	@echo "  make test-cover       Run tests with coverage report"
	@echo "  make test-race        Run tests with race detector"
	@echo "  make test-integration Run integration tests"
	@echo "  make test-e2e         Run e2e suite (maps to integration tests)"
	@echo "  make test-all         Run fmt-check + vet + test + race + tidy-check"
	@echo "  make bench            Run benchmarks"
	@echo "  make lint             Run linter (if golangci-lint is installed)"
	@echo "  make vet              Run go vet"
	@echo "  make fmt              Format code"
	@echo "  make fmt-check        Fail if any file is not gofmt-formatted"
	@echo "  make check            Run fmt + vet + test + tidy-check"
	@echo "  make deps             Download dependencies"
	@echo "  make tidy             Tidy go modules"
	@echo "  make install          Install binaries to GOPATH/bin"
	@echo "  make web-install      Install web dependencies"
	@echo "  make web-dev          Start web dev server"
	@echo "  make web-build        Build web assets and copy to pkg/api/dist"
	@echo "  make run              Build and run server"
	@echo "  make help             Show this help"
