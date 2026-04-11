# Makefile for compositor (github.com/grovetools/compositor)

.PHONY: all build test clean zig zig-test help

all: build

# --- Zig static library ---
ZIG_DIR = zig
ZIG_LIB = $(ZIG_DIR)/zig-out/lib/libcompositor.a

zig: $(ZIG_LIB)

$(ZIG_LIB): $(ZIG_DIR)/compositor.zig $(ZIG_DIR)/ansi.zig $(ZIG_DIR)/runewidth.zig $(ZIG_DIR)/build.zig
	@echo "Building libcompositor..."
	@cd $(ZIG_DIR) && zig build -Doptimize=ReleaseFast

# --- Go build (requires zig library) ---
build: zig
	@echo "Building Go package..."
	@go build ./...

# --- Tests ---
zig-test:
	@echo "Running Zig tests..."
	@cd $(ZIG_DIR) && zig build test

test: zig-test build
	@echo "All tests passed."

# --- Clean ---
clean:
	@echo "Cleaning..."
	@rm -rf $(ZIG_DIR)/.zig-cache $(ZIG_DIR)/zig-out
	@go clean

# --- Help ---
help:
	@echo "Available targets:"
	@echo "  make build    - Build Zig library + Go package"
	@echo "  make zig      - Build only the Zig static library"
	@echo "  make test     - Run Zig tests and verify Go build"
	@echo "  make zig-test - Run only Zig unit tests"
	@echo "  make clean    - Remove build artifacts"
