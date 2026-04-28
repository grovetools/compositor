# compositor

A high-performance cell-buffer rendering engine and PTY embedder for Go TUI applications.

## Packages

**`compositor`** (base) — Zig-backed dirty-cell compositor for bubbletea. Parses ANSI output from `View()` into a cell buffer and flushes only changed cells. Zero external dependencies beyond bubbletea.

```go
import "github.com/grovetools/compositor"

p := tea.NewProgram(compositor.NewModel(myModel), tea.WithAltScreen())
```

**`compositor/ghostty`** — CGo bindings to [libghostty-vt](https://github.com/ghostty-org/ghostty) for native terminal emulation. Provides a full VT100/ANSI terminal emulator that parses PTY output in C memory, avoiding Go GC overhead.

**`compositor/ext`** — Zig extension layer that bridges the base compositor with Ghostty. Adds direct C-to-C PTY blitting (`BlitGhostty`), a background Zig input thread that bypasses the Go event loop for zero-latency typing, and a binary delta protocol for multi-attach frame broadcasting.

**`compositor/pty`** — PTY backend interface and local implementation (via `creack/pty`). Defines the `Backend` interface for PTY I/O abstraction.

## How It Works

See [docs/how-it-works.md](docs/how-it-works.md) for the base compositor internals (cell buffers, ANSI parsing, flush algorithm).

## Building from Source

```bash
make build    # Builds Zig libraries + verifies Go packages
make clean    # Removes build artifacts
```

The Zig build produces two static libraries:
- `libcompositor.a` — base ANSI compositor (no external deps)
- `libgrove-compositor-ext.a` — extension with Ghostty integration

Ghostty is built from source on first build via `make libghostty` (requires `zig` on PATH).
