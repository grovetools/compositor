package ext

/*
#cgo CFLAGS: -I${SRCDIR} -I${SRCDIR}/../lib/ghostty/include
#cgo LDFLAGS: -L${SRCDIR}/../zig/zig-out/lib -lgrove-compositor-ext -L${SRCDIR}/../lib/ghostty/lib -lghostty-vt -lc++ -framework CoreFoundation
#include "compositor_ext.h"
#include "input_bridge.h"
*/
import "C"
import (
	"io"
	"os"
	"strconv"
	"strings"
	"unsafe"

	comp "github.com/grovetools/compositor"
	grovelogging "github.com/grovetools/core/logging"
)

func init() {
	// Wire the base compositor's log callback to grove's unified logger.
	compLog := grovelogging.NewUnifiedLogger("treemux.compositor")
	comp.SetLogFunc(func(level int, msg string) {
		switch level {
		case 0, 1: // trace, debug
			compLog.Debug(msg).StructuredOnly().Emit()
		case 2: // info
			compLog.Info(msg).StructuredOnly().Emit()
		case 3: // warn
			compLog.Warn(msg).StructuredOnly().Emit()
		default: // error
			compLog.Error(msg).StructuredOnly().Emit()
		}
	})
}

// Stats holds compositor performance metrics from both base and extensions.
type Stats struct {
	FramesRendered    uint64
	DirtyCellsFlushed uint64
	BytesWritten      uint64
	BlitGhosttyTimeUs uint64
	BlitANSITimeUs    uint64
	FlushTimeUs       uint64
}

// LogLevelFromEnv reads GROVE_LOG_LEVEL and returns the corresponding int.
func LogLevelFromEnv() int {
	val := strings.ToLower(os.Getenv("GROVE_LOG_LEVEL"))
	switch val {
	case "trace":
		return 0
	case "debug":
		return 1
	case "info":
		return 2
	case "warn", "warning":
		return 3
	case "error":
		return 4
	default:
		if n, err := strconv.Atoi(val); err == nil && n >= 0 && n <= 4 {
			return n
		}
		return 2 // default to info
	}
}

// inputWriter receives intercepted keystrokes from the Zig input thread.
var inputWriter *io.PipeWriter

// SetInputWriter installs the pipe writer for the Zig input callback.
func SetInputWriter(pw *io.PipeWriter) {
	inputWriter = pw
}

//export compositorOnInput
func compositorOnInput(data *C.uint8_t, length C.size_t) {
	if inputWriter != nil {
		buf := C.GoBytes(unsafe.Pointer(data), C.int(length))
		_, _ = inputWriter.Write(buf)
	}
}

// Compositor wraps the base compositor with terminal-specific extensions
// (ghostty blit, input routing, delta protocol).
type Compositor struct {
	*comp.Compositor // embedded base compositor
}

// New creates a compositor with the given screen dimensions and initializes
// the extension state (ghostty cache, diff buffers, input state).
func New(width, height, logLevel int) *Compositor {
	C.ext_init()
	return &Compositor{
		Compositor: comp.New(width, height, logLevel),
	}
}

// Free releases both extension and base compositor resources.
func (c *Compositor) Free() {
	C.ext_free()
	if c.Compositor != nil {
		c.Compositor.Free()
	}
}

// Resize updates the compositor's screen dimensions.
// Package-level function to match the existing terminal API.
func Resize(c *Compositor, width, height int) {
	if c.Compositor != nil {
		c.Compositor.Resize(width, height)
	}
}

// GetStats returns merged stats from base + extension.
func (c *Compositor) GetStats() Stats {
	if c.Compositor == nil {
		return Stats{}
	}
	base := c.Compositor.GetStats()
	ext := C.ext_get_stats()
	return Stats{
		FramesRendered:    base.FramesRendered,
		DirtyCellsFlushed: base.DirtyCellsFlushed,
		BytesWritten:      base.BytesWritten,
		BlitGhosttyTimeUs: uint64(ext.blit_ghostty_time_us),
		BlitANSITimeUs:    base.BlitANSITimeUs,
		FlushTimeUs:       base.FlushTimeUs,
	}
}

// BlitGhostty reads cell data from a ghostty terminal's render state
// and writes it into the back buffer at the given pane coordinates.
func (c *Compositor) BlitGhostty(termPtr unsafe.Pointer, x, y, w, h int) {
	if c.Compositor != nil && termPtr != nil {
		C.ext_blit_ghostty(c.Pointer(), termPtr, C.int(x), C.int(y), C.int(w), C.int(h))
	}
}

// UnregisterTerminal removes cached render state for a terminal.
func (c *Compositor) UnregisterTerminal(termPtr unsafe.Pointer) {
	if termPtr != nil {
		C.ext_unregister_terminal(termPtr)
	}
}

// SetSelection configures a visual selection overlay for the given terminal.
// When active, cells in the range (sc,sr)→(ec,er) are rendered with inverted
// video during BlitGhostty. Coordinates are in viewport-local space (0-indexed).
func (c *Compositor) SetSelection(termPtr unsafe.Pointer, active bool, sc, sr, ec, er int) {
	if termPtr != nil {
		C.ext_set_selection(termPtr, C.bool(active), C.int(sc), C.int(sr), C.int(ec), C.int(er))
	}
}

// HighlightRange represents a single search match range in viewport-local coordinates.
type HighlightRange struct {
	StartX, StartY, EndX, EndY int
}

// SetSearchHighlights sets multiple search highlight ranges for the given terminal.
// Each range is rendered with inverted video during BlitGhostty.
// Pass an empty slice to clear highlights.
func (c *Compositor) SetSearchHighlights(termPtr unsafe.Pointer, matches []HighlightRange) {
	if termPtr == nil {
		return
	}
	if len(matches) == 0 {
		C.ext_set_search_highlights(termPtr, nil, 0)
		return
	}
	// Convert Go slice to C array of CompositorHighlight.
	cMatches := make([]C.CompositorHighlight, len(matches))
	for i, m := range matches {
		cMatches[i] = C.CompositorHighlight{
			start_x: C.int(m.StartX),
			start_y: C.int(m.StartY),
			end_x:   C.int(m.EndX),
			end_y:   C.int(m.EndY),
		}
	}
	C.ext_set_search_highlights(termPtr, &cMatches[0], C.size_t(len(cMatches)))
}

// CursorInfo holds cursor position, style, and visibility from a ghostty terminal.
type CursorInfo struct {
	X, Y    int
	Style   int // DECSCUSR: 0=default, 2=block, 4=underline, 6=bar
	Visible bool
}

// GetCursor reads cursor info from the cached RenderState for the given terminal.
func (c *Compositor) GetCursor(termPtr unsafe.Pointer) CursorInfo {
	if termPtr == nil {
		return CursorInfo{}
	}
	var cur C.CompositorCursor
	C.ext_get_cursor(termPtr, &cur)
	return CursorInfo{
		X:       int(cur.x),
		Y:       int(cur.y),
		Style:   int(cur.style),
		Visible: bool(cur.visible),
	}
}

// GetDirtyPayload extracts a binary diff of cells that changed since the
// last call. Returns nil if nothing changed.
func (c *Compositor) GetDirtyPayload() []byte {
	if c.Compositor == nil {
		return nil
	}
	var ptr *C.uint8_t
	var length C.size_t
	C.ext_get_dirty_payload(c.Pointer(), &ptr, &length)
	if length == 0 {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(ptr), C.int(length))
}

// ApplyDiff writes a binary diff payload into the compositor's back buffer.
func (c *Compositor) ApplyDiff(payload []byte) {
	if c.Compositor != nil && len(payload) > 0 {
		C.ext_apply_diff(c.Pointer(), (*C.uint8_t)(unsafe.Pointer(&payload[0])), C.size_t(len(payload)))
	}
}

// RequestFullSync zeroes the broadcast_front buffer so the next
// GetDirtyPayload call returns a full-screen diff.
func (c *Compositor) RequestFullSync() {
	if c.Compositor != nil {
		C.ext_request_full_sync(c.Pointer())
	}
}

// StartInputThread spawns the Zig background thread that reads raw stdin.
func (c *Compositor) StartInputThread() {
	C.ext_start_input_thread(C.input_bridge_get_cb())
}

// StopInputThread signals the input thread to exit and waits for it to join.
func (c *Compositor) StopInputThread() {
	C.ext_stop_input_thread()
}

// SetActivePTY tells the Zig input router which PTY fd to write to.
func (c *Compositor) SetActivePTY(fd int) {
	C.ext_set_active_pty(C.int(fd))
}

// SetPassthrough enables or disables passthrough mode.
func (c *Compositor) SetPassthrough(passthrough bool) {
	C.ext_set_passthrough(C.bool(passthrough))
}

// SetLeaderSequence configures the byte sequence that triggers leader chord entry.
func (c *Compositor) SetLeaderSequence(seq []byte) {
	if len(seq) > 0 {
		C.ext_set_leader_sequence((*C.uint8_t)(unsafe.Pointer(&seq[0])), C.size_t(len(seq)))
	}
}

// AddInterceptSequence registers a byte sequence that should always be
// forwarded to Go (e.g. global hotkeys like Alt+L, F9, Ctrl+F).
func (c *Compositor) AddInterceptSequence(seq []byte) {
	if len(seq) > 0 {
		C.ext_add_intercept_sequence((*C.uint8_t)(unsafe.Pointer(&seq[0])), C.size_t(len(seq)))
	}
}
