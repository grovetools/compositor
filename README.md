# compositor

A high-performance cell-buffer rendering backend for [bubbletea](https://github.com/charmbracelet/bubbletea) applications.

Replaces bubbletea's string-concat rendering with a native Zig compositor that:
- Maintains a screen-sized cell buffer (front + back)
- Parses ANSI output from `View()` into cells
- Diffs front/back buffers and writes only dirty cells
- Supports hardware cursor positioning and styling

## Usage

```go
import "github.com/grovetools/compositor"

// Wrap your bubbletea model — one line change:
p := tea.NewProgram(compositor.NewModel(myModel), tea.WithAltScreen())
```

The compositor is transparent to the child model. It intercepts `View()` output,
parses ANSI into cells, and handles rendering. The child model doesn't change.

## How It Works

See [docs/how-it-works.md](docs/how-it-works.md) for a detailed explanation of
cell buffers, ANSI parsing, the flush algorithm, and the frame cycle.

## Building from Source

Consumers don't need Zig — prebuilt static libraries are included for supported
platforms. To rebuild the Zig library (maintainers only):

```bash
cd zig && zig build -Doptimize=ReleaseFast
```
