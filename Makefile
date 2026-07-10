BINARY := hermod
BIN_DIR := bin

# Platforms cross-compiled by `make release`, as GOOS/GOARCH pairs. Override on
# the command line, e.g. `make release PLATFORMS="linux/amd64 darwin/arm64"`.
PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# Everything a build depends on; a release binary older than any of these is stale.
SOURCES := go.mod go.sum $(shell find . -name '*.go' -not -path './$(BIN_DIR)/*')

.DEFAULT_GOAL := build

.PHONY: build
build: $(BIN_DIR)/$(BINARY) ## Compile the hermod binary into ./bin

$(BIN_DIR)/$(BINARY): $(SOURCES)
	go build -o $@ .

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: lint
lint: fmt-check vet ## Run all static checks (gofmt + go vet)

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format the code in place
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-clean
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: install
install: ## Install to $GOBIN for local use
	go install .

.PHONY: release
release: $(foreach p,$(PLATFORMS),$(BIN_DIR)/$(BINARY)-$(subst /,-,$(p))) ## Cross-compile a static binary for every platform in PLATFORMS

# Real file targets (bin/hermod-<os>-<arch>): Make skips any binary already newer
# than every source, so an unchanged tree rebuilds nothing. `%` is e.g. linux-amd64.
$(BIN_DIR)/$(BINARY)-%: os = $(word 1,$(subst -, ,$*))
$(BIN_DIR)/$(BINARY)-%: arch = $(word 2,$(subst -, ,$*))
$(BIN_DIR)/$(BINARY)-%: $(SOURCES)
	CGO_ENABLED=0 GOOS=$(os) GOARCH=$(arch) go build -o $@ .

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

.PHONY: help
help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
