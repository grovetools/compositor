# Makefile for compositor (github.com/grovetools/compositor)

.PHONY: all build test clean zig zig-test help libghostty

all: build

# --- macOS deployment target ---
# Align the Zig-built static libraries and the Go/cgo link step on a single
# macOS version-min, otherwise ld warns: "object file built for newer macOS
# version (X) than being linked (Y)".
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
MACOS_MIN ?= 15.0
ZIG_TARGET_FLAG = -Dtarget=native-macos.$(MACOS_MIN)
export MACOSX_DEPLOYMENT_TARGET = $(MACOS_MIN)
endif

# --- libghostty-vt ---
# CGo bindings in ghostty/ depend on libghostty-vt.a + headers under
# lib/ghostty/. vendor/ is gitignored, so we (re)build the static library
# from upstream ghostty using `zig build -Demit-lib-vt=true` on first build.
GHOSTTY_REPO       = https://github.com/ghostty-org/ghostty.git
# Pinned upstream ref (tag "tip" as of 2026-06-08) so builds are reproducible.
GHOSTTY_REF        = 69095e298ab88bb0eb5ba541f4c505f2c22d07f5
VENDOR_DIR         = lib/ghostty
GHOSTTY_LIB        = $(VENDOR_DIR)/lib/libghostty-vt.a
GHOSTTY_HEADER     = $(VENDOR_DIR)/include/ghostty/vt.h
GHOSTTY_BUILD_DIR  = $(VENDOR_DIR)/src

libghostty: $(GHOSTTY_LIB)

$(GHOSTTY_LIB) $(GHOSTTY_HEADER):
	@echo "Building libghostty-vt (requires zig on PATH)..."
	@command -v zig >/dev/null || { echo "ERROR: zig not found on PATH (install via 'brew install zig')"; exit 1; }
	@mkdir -p $(VENDOR_DIR)/lib $(VENDOR_DIR)/include
	@if [ ! -d $(GHOSTTY_BUILD_DIR)/.git ]; then \
		rm -rf $(GHOSTTY_BUILD_DIR); \
		git clone --depth 1 $(GHOSTTY_REPO) $(GHOSTTY_BUILD_DIR); \
	fi
	@# Pin the checkout: a fresh shallow clone lands on upstream HEAD, which
	@# drifts. Detect a wrong ref and fetch+checkout the pinned commit.
	@cur=$$(git -C $(GHOSTTY_BUILD_DIR) rev-parse HEAD); \
	if [ "$$cur" != "$(GHOSTTY_REF)" ]; then \
		echo "ghostty checkout is at $$cur; pinning to $(GHOSTTY_REF)..."; \
		git -C $(GHOSTTY_BUILD_DIR) fetch --depth 1 origin $(GHOSTTY_REF); \
		git -C $(GHOSTTY_BUILD_DIR) checkout -f $(GHOSTTY_REF); \
	fi
	@# Suppress Zig std.log info messages (page_list) that corrupt terminal
	@# rendering. Written via temp file + mv because `sed -i` flags are not
	@# portable between BSD/macOS and GNU sed.
	@sed 's/break :options .{};/break :options .{ .log_level = .err };/' \
		$(GHOSTTY_BUILD_DIR)/src/lib_vt.zig > $(GHOSTTY_BUILD_DIR)/src/lib_vt.zig.tmp && \
		mv $(GHOSTTY_BUILD_DIR)/src/lib_vt.zig.tmp $(GHOSTTY_BUILD_DIR)/src/lib_vt.zig
	@# -Demit-xcframework=false: the xcframework step needs full Xcode (xcodebuild);
	@# the Command Line Tools stub passes ghostty's PATH check but can't run, and
	@# only the static lib + headers are consumed here. (Re-applies wt 436dbad,
	@# which was lost when 0642c14 was authored without it.)
	@cd $(GHOSTTY_BUILD_DIR) && zig build -Demit-lib-vt=true -Demit-xcframework=false -Doptimize=ReleaseFast
	@cp $(GHOSTTY_BUILD_DIR)/zig-out/lib/libghostty-vt.a $(VENDOR_DIR)/lib/
	@cp -R $(GHOSTTY_BUILD_DIR)/zig-out/include/ghostty $(VENDOR_DIR)/include/
	@echo "libghostty-vt installed in $(VENDOR_DIR)"

# --- Zig static libraries ---
ZIG_DIR = zig
ZIG_LIB = $(ZIG_DIR)/zig-out/lib/libcompositor.a
ZIG_EXT_LIB = $(ZIG_DIR)/zig-out/lib/libgrove-compositor-ext.a

zig: libghostty $(ZIG_LIB)

$(ZIG_LIB) $(ZIG_EXT_LIB): $(ZIG_DIR)/compositor.zig $(ZIG_DIR)/ansi.zig $(ZIG_DIR)/runewidth.zig $(ZIG_DIR)/ext.zig $(ZIG_DIR)/ext_input.zig $(ZIG_DIR)/build.zig
	@echo "Building libcompositor + libgrove-compositor-ext..."
	@cd $(ZIG_DIR) && zig build -Doptimize=ReleaseFast $(ZIG_TARGET_FLAG)

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
	@echo "  make build      - Build Zig libraries + Go packages"
	@echo "  make zig        - Build only the Zig static libraries"
	@echo "  make libghostty - Build libghostty-vt from source"
	@echo "  make test       - Run Zig tests and verify Go build"
	@echo "  make zig-test   - Run only Zig unit tests"
	@echo "  make clean      - Remove build artifacts"
