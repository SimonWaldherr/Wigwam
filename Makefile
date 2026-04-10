.PHONY: all build run test clean tidy fmt vet lint mods help

# Default binary name and output directory
BINARY   := wigwam
BUILD_DIR := .build
GO       := go
GOFLAGS  :=
PORT     ?= 8080

# Build metadata
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -X main.buildVersion=$(VERSION) -X main.buildCommit=$(COMMIT) -X main.buildDate=$(DATE)

all: build ## Build the server binary (default)

build: tidy ## Build the Wigwam server binary
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/$(BINARY) .

run: build ## Build and run the server
	$(BUILD_DIR)/$(BINARY) -port $(PORT)

test: ## Run all tests
	$(GO) test -race -count=1 ./...

test-cover: ## Run tests with coverage report
	$(GO) test -race -coverprofile=$(BUILD_DIR)/coverage.out ./...
	$(GO) tool cover -func=$(BUILD_DIR)/coverage.out

tidy: ## Run go mod tidy
	$(GO) mod tidy

fmt: ## Format all Go source files
	gofmt -s -w .

vet: ## Run go vet
	$(GO) vet ./...

lint: vet fmt ## Run vet + fmt

mods: ## Build all enabled mods as plugins
	@mkdir -p .mods-built
	@for f in mods-enabled/*.go; do \
		echo "[mods] building $$f ..."; \
		$(GO) build -buildmode=plugin -o .mods-built/$$(basename $$f).so $$f || true; \
	done

clean: ## Remove build artifacts and cached plugins
	rm -rf $(BUILD_DIR) .mods-built .sites-built

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
