# LoopWorker Makefile
#
# OS-AWARE: works on Linux/macOS/Git Bash. On native Windows (cmd.exe /
# PowerShell, no GNU coreutils) use .release/build.ps1 — it implements the same
# targets and produces the same .exe-suffixed binaries:
#
#   powershell -ExecutionPolicy Bypass -File .release\build.ps1 -Task build
#
# `make build` on Windows now emits bin\loopworker.exe (previously bin\loopworker,
# which is not executable from cmd/PowerShell and therefore useless to a customer).

GO        ?= go
GOOS      ?= $(shell $(GO) env GOOS)
GOARCH    ?= $(shell $(GO) env GOARCH)

# .exe on Windows, empty everywhere else.
ifeq ($(GOOS),windows)
EXE := .exe
else
EXE :=
endif

# Version metadata. `git describe` yields "dev" until the first tag exists —
# releases are cut by goreleaser (.goreleaser.yaml), which injects the real tag.
# Falls back to the in-tree beta version when no tag exists yet; release builds
# override this with the goreleaser tag (see .goreleaser.yaml).
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-beta)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)

# Import path of the version package = `<module>/version`. Kept in sync with
# go.mod by .release/tools/relcheck (CI job `release-config`).
MODULE     := $(shell $(GO) list -m 2>/dev/null)
MODULE     ?= loopworker

LDFLAGS = -ldflags "-s -w \
	-X $(MODULE)/version.Version=$(VERSION) \
	-X $(MODULE)/version.GitCommit=$(GIT_COMMIT) \
	-X $(MODULE)/version.BuildDate=$(BUILD_DATE)"

# Product binary (what a customer downloads) vs. developer CLIs (not shipped in
# release artifacts — see .release/SCOPE-PROPOSAL.md).
PRODUCT   := loopworker
DEV_CLIS  := loopctl
BINARIES  := $(PRODUCT) $(DEV_CLIS)

# Toolchain presence is checked at parse time so `make lint` fails with a clear
# message instead of a confusing shell error.
GOLANGCI  := $(shell command -v golangci-lint 2>/dev/null)
VULN      := $(shell command -v govulncheck 2>/dev/null)
GORELE    := $(shell command -v goreleaser 2>/dev/null)
COVERAGE_MIN ?= 80
COVERAGE_PKG_MIN ?= 60
SHIPPED_PKGS ?= ./cmd/loopworker,./cmd/loopctl

BIN_FILES := $(foreach b,$(BINARIES),bin/$(b)$(EXE))

.PHONY: all build build-product build-current build-linux build-windows build-darwin \
	build-all test test-race cover coverage-gate fmt fmt-check lint vet vuln check \
	tools-check clean boot-smoke docker docker-smoke run version \
	release-snapshot help

all: build

## build: every binary for the host platform, with the correct suffix
build: $(BIN_FILES)
	@echo "Build complete: $(VERSION) ($(GOOS)/$(GOARCH))"

bin/%$(EXE): cmd/%
	@$(GO) build $(LDFLAGS) -o bin/$*$(EXE) ./cmd/$*/

## build-product: only the shippable server binary
build-product:
	$(GO) build $(LDFLAGS) -o bin/$(PRODUCT)$(EXE) ./cmd/$(PRODUCT)/

## build-current: alias of build
build-current: build

## build-linux: cross-compile linux/amd64 + linux/arm64 (CGO off => static)
build-linux:
	@$(MAKE) --no-print-directory _cross GOOS=linux GOARCH=amd64
	@$(MAKE) --no-print-directory _cross GOOS=linux GOARCH=arm64

## build-windows: cross-compile windows/amd64 (produces .exe)
build-windows:
	@$(MAKE) --no-print-directory _cross GOOS=windows GOARCH=amd64

## build-darwin: cross-compile darwin/amd64 + darwin/arm64
build-darwin:
	@$(MAKE) --no-print-directory _cross GOOS=darwin GOARCH=amd64
	@$(MAKE) --no-print-directory _cross GOOS=darwin GOARCH=arm64

_cross:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build $(LDFLAGS) \
		-o bin/$(PRODUCT)-$(GOOS)-$(GOARCH)$(if $(filter windows,$(GOOS)),.exe,) ./cmd/$(PRODUCT)/

build-all: build-linux build-windows build-darwin

## test: unit + integration tests
test:
	$(GO) test -count=1 ./...

## test-race: race detector over ALL packages (pkg/server has a known race)
test-race:
	$(GO) test -race -count=1 ./...

## cover: coverage across every package, written to coverage.out
cover:
	CGO_ENABLED=1 $(GO) test -count=1 -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

## coverage-gate: fail if total coverage < $(COVERAGE_MIN)% (CI enforces 80)
coverage-gate: cover
	cd .release/tools && $(GO) run ./covergate -file ../../coverage.out -min $(COVERAGE_MIN)
	cd .release/tools && $(GO) run ./covergate -file ../../coverage.out -pkg "$(SHIPPED_PKGS)" -pkg-min $(COVERAGE_PKG_MIN)

## coverage-shipped: only the per-package floor for the binaries that ship.
## Does not need the whole-tree profile, so it is the fast local check.
coverage-shipped:
	cd .release/tools && $(GO) run ./covergate -file ../../coverage.out -pkg "$(SHIPPED_PKGS)" -pkg-min $(COVERAGE_PKG_MIN)

## fmt: gofmt the tree
fmt:
	gofmt -l -w .

## fmt-check: fail on unformatted files (list them first)
fmt-check:
	@bad=$$(gofmt -l . | grep -vE '^(tmp-audit|dist)[/\\]'); \
	if [ -n "$$bad" ]; then echo "NOT FORMATTED:"; echo "$$bad"; exit 1; fi; \
	echo "gofmt: clean"

## vet: go vet only
vet:
	$(GO) vet ./...

## lint: golangci-lint with .golangci.yaml (errcheck, govet, staticcheck,
## ineffassign, errorlint ...). go vet alone is NOT lint.
lint: tools-check
	$(GOLANGCI) run ./...

tools-check:
ifeq ($(GOLANGCI),)
	$(error golangci-lint not in PATH — install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8, or rely on CI job `quality`)
endif

## vuln: govulncheck against the official Go vulnerability database
vuln:
ifeq ($(VULN),)
	$(error govulncheck not in PATH — install: go install golang.org/x/vuln/cmd/govulncheck@latest)
endif
	govulncheck ./...

## check: the whole pre-push gate (same steps CI runs)
check: fmt-check vet lint test-race coverage-gate vuln

## release-snapshot: verify the release pipeline end to end without publishing
release-snapshot:
ifeq ($(GORELE),)
	$(error goreleaser not in PATH — install: go install github.com/goreleaser/goreleaser/v2@v2.9.0)
endif
	cd .release/tools && $(GO) run ./relcheck -root ../..
	$(GORELE) check
	$(GORELE) release --snapshot --clean --skip=publish

## clean: remove build output (Git Bash / POSIX; Windows native users: build.ps1 -Task clean)
clean:
	@echo "Cleaning..."
	-rm -rf bin dist coverage.out coverage.html
	@echo "Clean complete."

## install: install the product binary into $$(go env GOPATH)/bin
install: build-product
	$(GO) install -ldflags "-s -w -X $(MODULE)/version.Version=$(VERSION)" ./cmd/$(PRODUCT)/

## docker: build the container image (pinned builder matches go.mod)
docker:
	docker build -t loopworker:$(VERSION) .

## docker-smoke: boot the image, hit /api/v1/health, prove graceful SIGTERM
docker-smoke:
	bash .release/scripts/docker-smoke.sh loopworker:$(VERSION)

## boot-smoke: boot the product binary and require health 200, an anonymous
## write refused with 401, a task that reaches completed, and a clean exit
boot-smoke: build-product
	bash .release/scripts/boot-smoke.sh bin/$(PRODUCT)$(EXE)

## run: build and start the server
run: build-product
	./bin/$(PRODUCT)$(EXE)

## version: show build metadata
version:
	@echo "Version:    $(VERSION)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Date: $(BUILD_DATE)"
	@echo "Go:         $(shell $(GO) version)"
	@echo "Module:     $(MODULE)"
	@echo "Host:       $(GOOS)/$(GOARCH) exe-suffix='$(EXE)'"

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
