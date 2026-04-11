// ansi.zig — ANSI escape sequence parser.
//
// Lightweight state machine that converts ANSI-styled strings (from
// lipgloss/bubbletea View() output) into cells in the compositor's
// back buffer. Handles SGR, UTF-8, color passthrough, and runewidth.
//
// TODO: Extract from terminal/internal/compositor/ansi.zig
