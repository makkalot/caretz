GO ?= go
BINARY_DIR ?= bin
BINARY_PATH := $(BINARY_DIR)/coderun
INSTALL_DIR ?= $(HOME)/bin
LD_FLAGS ?=
ARGS ?=

.PHONY: build run test install clean

# Build the example to ./bin/coderun.
build:
	@mkdir -p "$(BINARY_DIR)"
	$(GO) build -ldflags '$(LD_FLAGS)' -o "$(BINARY_PATH)" .

# Build and start the server; pass flags with ARGS='-model gpt-5'.
run: build
	"$(abspath $(BINARY_PATH))" $(ARGS)

# Run race tests and static analysis without calling a model.
test:
	$(GO) test -race ./...
	$(GO) vet ./...

# Install to ~/bin, or override INSTALL_DIR.
install: build
	@mkdir -p "$(INSTALL_DIR)"
	cp "$(BINARY_PATH)" "$(INSTALL_DIR)/coderun"
	@codesign --force --deep -s - "$(INSTALL_DIR)/coderun" 2>/dev/null || true
	@echo "Installed coderun to $(INSTALL_DIR)/"

# Remove the generated binary; keep project and session data.
clean:
	rm -f "$(BINARY_PATH)"
