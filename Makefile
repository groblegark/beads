# Makefile for beads project

.PHONY: all build test bench-dolt bench-dolt-quick clean install help \
       image-build image-push

# Default target
all: build

BINARY := bd
BUILD_DIR := .
INSTALL_DIR := $(HOME)/.local/bin

# On macOS, set CGO flags for Homebrew's keg-only icu4c
ifeq ($(shell uname),Darwin)
  ICU_PREFIX := $(shell brew --prefix icu4c 2>/dev/null)
  ifneq ($(ICU_PREFIX),)
    export CGO_CFLAGS   += -I$(ICU_PREFIX)/include
    export CGO_CXXFLAGS += -I$(ICU_PREFIX)/include
    export CGO_LDFLAGS  += -L$(ICU_PREFIX)/lib
  endif
endif

# Build the bd binary
build:
	@echo "Building bd..."
	go build -ldflags="-X main.Build=$$(git rev-parse --short HEAD)" -o $(BUILD_DIR)/$(BINARY) ./cmd/bd
ifeq ($(shell uname),Darwin)
	@codesign -s - -f $(BUILD_DIR)/$(BINARY) 2>/dev/null || true
	@echo "Signed $(BINARY) for macOS"
endif

# Run all tests (skips known broken tests listed in .test-skip)
test:
	@echo "Running tests..."
	@TEST_COVER=1 ./scripts/test.sh

# Run Dolt performance benchmarks
# Requires: Dolt installed (brew install dolt or from https://doltdb.com)
bench-dolt:
	@echo "Running Dolt performance benchmarks..."
	@echo "This measures bootstrap time, CRUD operations, and query performance."
	@echo ""
	@if ! command -v dolt >/dev/null 2>&1; then \
		echo "Error: Dolt not installed. Install with: brew install dolt"; \
		exit 1; \
	fi
	go test -bench=. -benchmem -benchtime=1s -run=^$$ ./internal/storage/dolt/ -timeout=30m
	@echo ""
	@echo "Dolt benchmark complete."

# Run quick Dolt benchmarks
bench-dolt-quick:
	@echo "Running quick Dolt benchmarks..."
	@if ! command -v dolt >/dev/null 2>&1; then \
		echo "Error: Dolt not installed. Install with: brew install dolt"; \
		exit 1; \
	fi
	go test -bench=. -benchmem -benchtime=100ms -run=^$$ ./internal/storage/dolt/ -timeout=15m

# Install bd to ~/.local/bin (builds, signs on macOS, and copies)
install: build
	@mkdir -p $(INSTALL_DIR)
	@rm -f $(INSTALL_DIR)/$(BINARY)
	@cp $(BUILD_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"

# Clean build artifacts and benchmark profiles
clean:
	@echo "Cleaning..."
	rm -f bd
	rm -f beads-perf-*.prof

# OCI image settings (built via RWX native — no Dockerfiles)
IMAGE_REGISTRY ?= ghcr.io
IMAGE_REPO     ?= groblegark/beads
IMAGE_TAG      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Build OCI image via RWX native
image-build:
	@echo "Building OCI image via RWX (target: image-bd)..."
	rwx image build -f .rwx/image.yml --target image-bd \
		--tag $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG) \
		--tag $(IMAGE_REGISTRY)/$(IMAGE_REPO):latest
	@echo "Built $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG)"

# Push OCI image to registry via RWX
image-push: image-build
	@echo "Pushing $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG)..."
	rwx image build -f .rwx/image.yml --target image-bd \
		--push-to $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG) \
		--push-to $(IMAGE_REGISTRY)/$(IMAGE_REPO):latest
	@echo "Pushed $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(IMAGE_TAG)"

# Show help
help:
	@echo "Beads Makefile targets:"
	@echo "  make build           - Build the bd binary"
	@echo "  make test            - Run all tests"
	@echo "  make bench-dolt      - Run Dolt performance benchmarks"
	@echo "  make bench-dolt-quick - Run quick Dolt benchmarks"
	@echo "  make install         - Install bd to ~/.local/bin (with codesign on macOS)"
	@echo "  make clean           - Remove build artifacts"
	@echo "  make image-build     - Build OCI image via RWX"
	@echo "  make image-push      - Build and push OCI image via RWX"
	@echo "  make help            - Show this help message"
