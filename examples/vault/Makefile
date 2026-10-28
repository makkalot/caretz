GO ?= go
BINARY_DIR ?= bin
BINARY_PATH := $(BINARY_DIR)/vault
INSTALL_DIR ?= $(HOME)/bin
LD_FLAGS ?=
ARGS ?= --help

.PHONY: build run install

# Build the example to ./bin/vault.
build:
	@mkdir -p "$(BINARY_DIR)"
	$(GO) build -ldflags '$(LD_FLAGS)' -o "$(BINARY_PATH)" .

# Build and run; pass a command and flags with ARGS='serve -workspace ~/Notes'.
run: build
	"$(abspath $(BINARY_PATH))" $(ARGS)

# Install to ~/bin, or override INSTALL_DIR.
install: build
	@mkdir -p "$(INSTALL_DIR)"
	cp "$(BINARY_PATH)" "$(INSTALL_DIR)/vault"
	@codesign --force --deep -s - "$(INSTALL_DIR)/vault" 2>/dev/null || true
	@echo "Installed vault to $(INSTALL_DIR)/"
