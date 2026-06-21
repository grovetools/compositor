#!/usr/bin/env bash
# zig-cache.sh — restore prebuilt zig artifacts from a shared content-addressed
# cache, or build them and populate the cache.
#
# A fresh ecosystem worktree otherwise re-clones ghostty (~250MB) and recompiles
# libghostty-vt.a / libcompositor.a from scratch — slow, and (before the grove
# build wave-ordering fix) the source of first-build link failures in CGO
# consumers. Because these artifacts are a pure function of their inputs (pinned
# ghostty ref, zig source content, zig version, build target), they can be built
# once and reused across every worktree on the machine.
#
# Design notes:
#   - COPY, not symlink/overlay: zig-out is a mutable build dir (`make clean`
#     removes it); symlinking it at a shared cache would let one worktree's clean
#     destroy another's artifacts. The libs are small (~11MB) so copies are cheap.
#     (The sync cluster harness overlay-mounts these read-only into Linux
#     containers — appropriate there because the mount is read-only and ephemeral;
#     not appropriate for a mutable local build dir.)
#   - Cache population is atomic (tmp dir + rename); concurrent worktrees racing
#     to populate the same key are safe — the loser discards its temp copy.
#   - Cache MISS behaviour is identical to a plain build, plus a populate step.
#   - `make clean` never touches the cache. Disable entirely with
#     GROVE_ZIG_CACHE=0. Prune with `make zig-cache-clean`.
#
# Usage: zig-cache.sh <build-make-target> <dest-dir> <relpath> [relpath...]
#   env: GROVE_ZIG_CACHE (1/0), CACHE_DIR (absolute cache entry directory)
#   <relpath> are paths relative to <dest-dir> that constitute the artifact set
#   (files or directories), e.g. "lib/libcompositor.a" or "include/ghostty".

set -euo pipefail

build_target="$1"
dest="$2"
shift 2
relpaths=("$@")

cache_enabled="${GROVE_ZIG_CACHE:-1}"
cache_dir="${CACHE_DIR:-}"

have_all() {
	local base="$1"
	local p
	for p in "${relpaths[@]}"; do
		[ -e "$base/$p" ] || return 1
	done
	return 0
}

copy_set() {
	# copy_set <src-base> <dst-base> — copy each relpath preserving structure
	local src="$1" dst="$2" p
	for p in "${relpaths[@]}"; do
		mkdir -p "$dst/$(dirname "$p")"
		cp -R "$src/$p" "$dst/$p"
	done
}

# --- Restore from cache ---------------------------------------------------
if [ "$cache_enabled" = "1" ] && [ -n "$cache_dir" ] && have_all "$cache_dir"; then
	echo "Restoring zig artifacts from cache: $cache_dir"
	copy_set "$cache_dir" "$dest"
	exit 0
fi

# --- Build (cache miss) ---------------------------------------------------
make "$build_target"

if ! have_all "$dest"; then
	echo "ERROR: build target '$build_target' did not produce expected artifacts in $dest" >&2
	exit 1
fi

# --- Populate cache -------------------------------------------------------
if [ "$cache_enabled" = "1" ] && [ -n "$cache_dir" ] && [ ! -e "$cache_dir" ]; then
	tmp="${cache_dir}.tmp.$$"
	rm -rf "$tmp"
	mkdir -p "$tmp"
	if copy_set "$dest" "$tmp"; then
		mkdir -p "$(dirname "$cache_dir")"
		# Atomic publish; if a concurrent worktree already published this key,
		# keep theirs and discard ours.
		if mv "$tmp" "$cache_dir" 2>/dev/null; then
			echo "Populated zig cache: $cache_dir"
		else
			rm -rf "$tmp"
		fi
	else
		rm -rf "$tmp"
	fi
fi
