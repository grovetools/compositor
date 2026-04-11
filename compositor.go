// Package compositor provides a high-performance cell-buffer rendering
// backend for bubbletea applications. It replaces bubbletea's string-concat
// rendering pipeline with a native Zig compositor that maintains a screen-sized
// cell buffer, parses ANSI output from View() into cells, and flushes only
// dirty cells to the terminal.
//
// Basic usage:
//
//	p := tea.NewProgram(compositor.NewModel(myModel), tea.WithAltScreen())
//
// The compositor is transparent to the child model — it intercepts View()
// output, parses it into cells, and handles rendering. The child model
// doesn't need to know the compositor exists.
package compositor
