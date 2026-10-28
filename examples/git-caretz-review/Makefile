BINARY_DIR ?= bin
BINARY_PATH := $(BINARY_DIR)/git-caretz-review
INSTALL_DIR ?= $(HOME)/bin
LD_FLAGS ?=

.PHONY: build test install

# Build the example to ./bin/git-caretz-review.
build:
	@mkdir -p "$(BINARY_DIR)"
	go build -ldflags '$(LD_FLAGS)' -o "$(BINARY_PATH)" .

# Run tests and static analysis without calling a model.
test:
	go test ./...
	go vet ./...

# Install to ~/bin, or override INSTALL_DIR.
install: build
	@mkdir -p "$(INSTALL_DIR)"
	cp "$(BINARY_PATH)" "$(INSTALL_DIR)/git-caretz-review"
	@codesign --force --deep -s - "$(INSTALL_DIR)/git-caretz-review" 2>/dev/null || true
	@echo "Installed git-caretz-review to $(INSTALL_DIR)/"
