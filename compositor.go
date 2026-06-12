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

/*
#cgo CFLAGS: -I${SRCDIR}
#cgo LDFLAGS: -L${SRCDIR}/zig/zig-out/lib -lcompositor
#include "compositor.h"
*/
import "C"
import (
	"fmt"
	"os"
	"unsafe"
)

// Log level constants matching the Zig side.
const (
	LogTrace = 0
	LogDebug = 1
	LogInfo  = 2
	LogWarn  = 3
	LogError = 4
)

// SetLogFunc installs a callback that receives all Zig-side log messages.
// Set this before creating a Compositor. If nil, messages at info+ are
// printed to stderr. Level values use the Log* constants above.
func SetLogFunc(fn func(level int, msg string)) {
	logFunc = fn
}

var logFunc func(level int, msg string)

// groveCompositorLog is the CGo export called from Zig. It delegates to the
// consumer-provided callback, or falls back to stderr.
//
//export groveCompositorLog
func groveCompositorLog(level C.int, msg *C.char, length C.size_t) {
	goMsg := C.GoStringN(msg, C.int(length))
	if logFunc != nil {
		logFunc(int(level), goMsg)
		return
	}
	if int(level) >= LogInfo {
		fmt.Fprintf(os.Stderr, "[compositor] %s\n", goMsg)
	}
}

// SetRecordFunc installs a callback that receives every byte buffer the
// compositor flush writes to the terminal fd (complete frames, brackets
// included). Used by the rendering-debug instrumentation in the ext package
// (output recording ring + verification VT). Pass nil to disable.
//
// The callback runs on the flushing goroutine while the host's TTY lock is
// held — it must be fast and must never write to the tty or take a lock
// that can wait on a tty writer.
func SetRecordFunc(fn func([]byte)) {
	recordFunc = fn
	C.compositor_set_recording(C.bool(fn != nil))
}

var recordFunc func([]byte)

// groveCompositorRecord is the CGo export called from the Zig flush with
// the frame it is about to write. Gated Zig-side by compositor_set_recording
// so disabled runs pay zero cgo-callback cost.
//
//export groveCompositorRecord
func groveCompositorRecord(data *C.uint8_t, length C.size_t) {
	if recordFunc != nil && length > 0 {
		recordFunc(C.GoBytes(unsafe.Pointer(data), C.int(length)))
	}
}

// Stats holds compositor performance metrics read from the Zig side.
type Stats struct {
	FramesRendered    uint64
	DirtyCellsFlushed uint64
	BytesWritten      uint64
	BlitANSITimeUs    uint64
	FlushTimeUs       uint64
}

// Compositor manages a screen-sized cell buffer backed by a Zig rendering
// engine. It parses ANSI strings into cells and flushes only dirty cells
// to the terminal fd, eliminating flicker and reducing write overhead.
type Compositor struct {
	ptr *C.Compositor
}

// New creates a compositor with the given screen dimensions.
// logLevel is passed to the Zig side (0=trace, 1=debug, 2=info, 3=warn, 4=error).
func New(width, height, logLevel int) *Compositor {
	c := &Compositor{
		ptr: C.compositor_new(C.int(width), C.int(height), C.int(logLevel)),
	}
	// GROVE_COMPOSITOR_CLASSIC=1 reverts rendering to pre-2026 behavior
	// (no outbound frame bracketing, no periodic self-heal) — escape
	// hatch while the synchronized-output interaction with the host
	// terminal is being tuned.
	if os.Getenv("GROVE_COMPOSITOR_CLASSIC") != "" && c.ptr != nil {
		C.compositor_set_classic(c.ptr, C.bool(true))
	}
	return c
}

// Free releases all compositor resources.
func (c *Compositor) Free() {
	if c.ptr != nil {
		C.compositor_free(c.ptr)
		c.ptr = nil
	}
}

// Resize updates the compositor's screen dimensions.
func (c *Compositor) Resize(width, height int) {
	if c.ptr != nil {
		C.compositor_resize(c.ptr, C.int(width), C.int(height))
	}
}

// BlitANSI parses an ANSI-escaped string (e.g. from lipgloss/bubbletea View())
// and writes the decoded cells into the back buffer at the given coordinates.
func (c *Compositor) BlitANSI(x, y, w, h int, str string) {
	if c.ptr != nil && len(str) > 0 {
		ptr := unsafe.StringData(str)
		C.compositor_blit_ansi(c.ptr, C.int(x), C.int(y), C.int(w), C.int(h), (*C.char)(unsafe.Pointer(ptr)), C.size_t(len(str)))
	}
}

// SetCursor sets the cursor position, style and visibility for the next flush.
func (c *Compositor) SetCursor(x, y, style int, visible bool) {
	if c.ptr != nil {
		C.compositor_set_cursor(c.ptr, C.int(x), C.int(y), C.int(style), C.bool(visible))
	}
}

// Flush writes only changed cells to the given file descriptor.
func (c *Compositor) Flush(fd int) {
	if c.ptr != nil {
		C.compositor_flush(c.ptr, C.int(fd))
	}
}

// CopyFrontToBack restores the rect's last-flushed content (front buffer)
// into the back buffer, making the rect diff-neutral for the next flush.
// Sentinel (never-flushed) cells are skipped. Hosts use this to keep a
// deferred pane's previous frame on screen after the chrome blit pre-cleared
// its interior.
func (c *Compositor) CopyFrontToBack(x, y, w, h int) {
	if c.ptr != nil {
		C.compositor_copy_front_to_back(c.ptr, C.int(x), C.int(y), C.int(w), C.int(h))
	}
}

// GetStats returns a snapshot of the compositor's cumulative performance metrics.
func (c *Compositor) GetStats() Stats {
	if c.ptr == nil {
		return Stats{}
	}
	s := C.compositor_get_stats(c.ptr)
	return Stats{
		FramesRendered:    uint64(s.frames_rendered),
		DirtyCellsFlushed: uint64(s.dirty_cells_flushed),
		BytesWritten:      uint64(s.bytes_written),
		BlitANSITimeUs:    uint64(s.blit_ansi_time_us),
		FlushTimeUs:       uint64(s.flush_time_us),
	}
}

// Pointer returns the raw C pointer to the underlying Zig Compositor struct.
// This is used by terminal-specific extensions (ghostty blit, input routing,
// diff protocol) that need direct access to the cell buffer.
func (c *Compositor) Pointer() unsafe.Pointer {
	if c.ptr == nil {
		return nil
	}
	return unsafe.Pointer(C.compositor_pointer(c.ptr))
}
