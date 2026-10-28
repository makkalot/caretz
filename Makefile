# Project settings
BINARY_NAME := caretz
BINARY_DIR  := ./bin
BINARY_PATH := $(BINARY_DIR)/$(BINARY_NAME)
GOLANGCI_LINT_VERSION ?= v2.13.2
GOLANGCI_LINT_VERSION_NUMBER := $(patsubst v%,%,$(GOLANGCI_LINT_VERSION))
GOLANGCI_LINT := $(BINARY_DIR)/golangci-lint
GO_PACKAGE := ./cmd/caretz
BENCH_BINARY_PATH := $(BINARY_DIR)/bench
BENCH_MODULE := ./bench
BENCH_PACKAGE := ./cmd/bench
TEST_BINARY := $(BINARY_DIR)/caretz-test-linux
SANDBOX_TEST_BINARY := $(BINARY_DIR)/caretz-sandbox-test-linux
SANDBOX_TEST_IMAGE ?= golang:1.25-bookworm
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LD_FLAGS := -X github.com/makkalot/caretz/cli.Version=$(VERSION) -X github.com/makkalot/caretz/cli.Commit=$(COMMIT) -X github.com/makkalot/caretz/cli.BuildDate=$(BUILD_DATE)

# Default prompt for `make run`
PROMPT ?=

# Verbosity: unset=silent, V=v for info, V=vv for debug
V ?=
VFLAG := $(if $(V),-$(V),)

.PHONY: help setup tidy ci doccheck build build-bench run test test-bench test-bench-daily fmt vet golangci-lint lint site-build site-serve clean install submit \
	worktree worktree-from-branch worktree-from-remote \
	build-linux-amd64 build-linux-arm64 build-jetson build-all run-linux test-linux test-docker \
	test-sandbox-linux test-sandbox-darwin test-skills-e2e test-compaction-e2e

## help: Show this help message (default)
help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':' | sed 's/^/  /'

## worktree: Create a new feat/<name> branch in a sibling worktree
worktree:
	@bash scripts/worktree.sh new

## worktree-from-branch: Move the current feature branch into a sibling worktree
worktree-from-branch:
	@bash scripts/worktree.sh from-branch

## worktree-from-remote: Fetch a remote branch into a sibling worktree
worktree-from-remote:
	@bash scripts/worktree.sh from-remote

## setup: Download and tidy Go module dependencies and install lint tooling
setup: golangci-lint
	go mod download
	go mod tidy

## tidy: Tidy go.mod and go.sum for every Go module
tidy:
	@set -e; \
	for module in . ./bench; do \
		echo "Tidying $$module"; \
		(cd "$$module" && go mod tidy); \
	done

## ci: Run non-interactive build, test, lint, and site checks
ci: tidy lint build-all build-bench test test-bench site-build

## doccheck: Verify all doc code examples (Go/shell/CLI/JSON/skills/links/hugo)
doccheck: build
	.doccheck/run_all.sh

## build: Compile the binary to ./bin/caretz
build:
	@mkdir -p $(BINARY_DIR)
	go build -ldflags '$(LD_FLAGS)' -o $(BINARY_PATH) $(GO_PACKAGE)

## build-bench: Compile the benchmark runner to ./bin/bench
build-bench:
	@mkdir -p $(BINARY_DIR)
	cd $(BENCH_MODULE) && go build -o ../$(BENCH_BINARY_PATH) $(BENCH_PACKAGE)

## run: Build and run with PROMPT, e.g. make run PROMPT="hello" V=v
run: build
	@if [ -z "$(PROMPT)" ]; then \
		echo "Error: PROMPT is required. Usage: make run PROMPT=\"your prompt here\""; \
		exit 1; \
	fi
	$(BINARY_PATH) -p "$(PROMPT)" $(VFLAG)

run-chat: build
	$(BINARY_PATH) $(VFLAG)


## test: Run all tests
test:
	go test ./...

## test-bench: Run benchmark runner unit tests
test-bench:
	cd $(BENCH_MODULE) && go test ./...

## test-bench-daily: Check every daily-suite verifier against its reference solution
test-bench-daily:
	bash $(BENCH_MODULE)/verifiers/daily/selftest.sh

## fmt: Format Go source files in every module
fmt:
	git ls-files --cached --others --exclude-standard -z '*.go' | \
		xargs -0 sh -c 'for path do if [ -f "$$path" ]; then printf "%s\0" "$$path"; fi; done' sh | \
		xargs -0 gofmt -w

## vet: Run go vet for static analysis
vet:
	go vet ./...

## golangci-lint: Install the pinned golangci-lint binary into ./bin
golangci-lint:
	@mkdir -p $(BINARY_DIR)
	@if [ ! -x "$(GOLANGCI_LINT)" ] || ! "$(GOLANGCI_LINT)" version | grep -q "version $(GOLANGCI_LINT_VERSION_NUMBER)"; then \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)"; \
		curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(BINARY_DIR) $(GOLANGCI_LINT_VERSION); \
	fi

## lint: Run gofmt, go vet, and golangci-lint
lint: fmt vet golangci-lint
	$(GOLANGCI_LINT) run ./...

## site-build: Build the Hugo website into site/public
site-build:
	cd site && hugo --minify

## site-serve: Serve the Hugo website locally
site-serve:
	cd site && hugo server

## clean: Remove build artifacts
clean:
	rm -rf $(BINARY_DIR)

## install: Build for current arch and install to ~/bin
install: build
	@mkdir -p $(HOME)/bin
	cp $(BINARY_PATH) $(HOME)/bin/$(BINARY_NAME)
	@codesign --force --deep -s - $(HOME)/bin/$(BINARY_NAME) 2>/dev/null || true
	@echo "Installed $(BINARY_NAME) to $(HOME)/bin/"

## build-linux-amd64: Cross-compile for linux/amd64
build-linux-amd64:
	@mkdir -p $(BINARY_DIR)
	GOOS=linux GOARCH=amd64 go build -ldflags '$(LD_FLAGS)' -o $(BINARY_DIR)/caretz-linux-amd64 $(GO_PACKAGE)

## build-linux-arm64: Cross-compile for linux/arm64
build-linux-arm64:
	@mkdir -p $(BINARY_DIR)
	GOOS=linux GOARCH=arm64 go build -ldflags '$(LD_FLAGS)' -o $(BINARY_DIR)/caretz-linux-arm64 $(GO_PACKAGE)

## build-jetson: Cross-compile for NVIDIA Jetson devices (Linux ARM64)
build-jetson: build-linux-arm64

## build-all: Build caretz for the host, linux/amd64, and linux/arm64
build-all: build build-linux-amd64 build-linux-arm64

## run-linux: Build linux/amd64 binary and run with PROMPT, e.g. make run-linux PROMPT="hello" V=v
run-linux: build-linux-amd64
	@if [ -z "$(PROMPT)" ]; then \
		echo "Error: PROMPT is required. Usage: make run-linux PROMPT=\"your prompt here\""; \
		exit 1; \
	fi
	$(BINARY_DIR)/caretz-linux-amd64 -p "$(PROMPT)" $(VFLAG)

## test-linux: Build linux test binary
test-linux:
	@mkdir -p $(BINARY_DIR)
	GOOS=linux go test -c -o $(SANDBOX_TEST_BINARY) ./internal/sandbox

## test-sandbox-linux: Run all sandbox tests, including Linux integration, in a privileged disposable container
# Tests run as an unprivileged user: that is how caretz runs on a Linux host, and
# root inside a container would make the privilege-escalation checks meaningless.
test-sandbox-linux:
	docker run --privileged --rm \
		-v $(CURDIR):/workspace \
		-w /workspace \
		$(SANDBOX_TEST_IMAGE) \
		sh -c 'apt-get update -qq >/dev/null && apt-get install -y -qq bubblewrap curl git >/dev/null && \
			go mod download && chmod -R a+rX "$$(go env GOMODCACHE)" && \
			(getent passwd 1000 >/dev/null || useradd -M -u 1000 -s /bin/sh caretz-test) && \
			setpriv --reuid=1000 --regid=1000 --clear-groups env HOME=/tmp GOCACHE=/tmp/gocache GOFLAGS=-buildvcs=false \
			CARETZ_RUN_SANDBOX_INTEGRATION=1 go test ./internal/sandbox -count=1 -v'

## test-sandbox-darwin: Run all sandbox tests, including macOS integration, natively
test-sandbox-darwin:
	CARETZ_RUN_SANDBOX_INTEGRATION=1 go test ./internal/sandbox -count=1 -v

## test-docker: Alias for test-sandbox-linux
test-docker: test-sandbox-linux

## test-skills-e2e: Run end-to-end skills validation
test-skills-e2e: build
	bash scripts/test-skills-e2e.sh

## test-compaction-e2e: Run end-to-end compaction validation
test-compaction-e2e: build
	bash scripts/test-compaction-e2e.sh
