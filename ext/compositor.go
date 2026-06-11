package ext

/*
#cgo CFLAGS: -I${SRCDIR} -I${SRCDIR}/../lib/ghostty/include
#cgo LDFLAGS: -L${SRCDIR}/../zig/zig-out/lib -lgrove-compositor-ext -L${SRCDIR}/../lib/ghostty/lib -lghostty-vt -lc++ -framework CoreFoundation
#include "compositor_ext.h"
#include "input_bridge.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	comp "github.com/grovetools/compositor"
	"github.com/grovetools/compositor/ghostty"
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

// Clear fills the back buffer with default blank cells.
func (c *Compositor) Clear() {
	if c.Compositor != nil {
		C.ext_clear(c.Pointer())
	}
}

// BlitGhostty reads cell data from a ghostty terminal's render state
// and writes it into the back buffer at the given pane coordinates.
//
// The terminal's mutex is held for the duration of the C call: the blit
// iterates the ghostty cell grid, which is mutated concurrently by WriteVT
// on the PTY reader goroutine. Without the lock the blit can observe a
// half-applied VT write (torn cells interleaving two text generations).
func (c *Compositor) BlitGhostty(termPtr unsafe.Pointer, x, y, w, h int) {
	if c.Compositor == nil || termPtr == nil {
		return
	}
	if !ghostty.LockByPointer(termPtr) {
		return // unknown or closed terminal
	}
	C.ext_blit_ghostty(c.Pointer(), termPtr, C.int(x), C.int(y), C.int(w), C.int(h))
	ghostty.UnlockByPointer(termPtr)
}

// ttyMu serializes all byte writes to the terminal fd. Two writers share
// the output tty: the Zig compositor (compositor_flush does a raw write(2)
// from Flush below) and bubbletea's renderer goroutine (cursor hide/show
// and chrome frames via its output writer). Without a shared lock the
// kernel interleaves their escape sequences mid-stream, which both mangles
// the screen and desyncs the compositor's front buffer from the physical
// terminal (a mangled-in-transit frame never paints, but the front buffer
// was already updated, so the diff skips those cells until a full
// invalidate). Host apps must route every other tty writer through
// SerializedWriter (or hold TTYLock) so a whole compositor frame is atomic.
var ttyMu sync.Mutex

// TTYLock returns the mutex that serializes writes to the terminal fd.
// Hold it for any direct tty write that may race with Flush.
func TTYLock() *sync.Mutex {
	return &ttyMu
}

// dumpStateLimiter throttles dump state checks to once per N flushes.
var (
	dumpStateCounter int
	dumpStateLimiter = 10 // Check every 10 flushes
)

// checkAndDumpState checks for the trigger file and dumps state if present.
// This is called infrequently (every N flushes) to avoid filesystem overhead.
func (c *Compositor) checkAndDumpState() {
	if ttyAuditor == nil || c.Compositor == nil {
		return // GROVE_TTY_AUDIT not enabled or no compositor
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	triggerFile := home + "/.local/state/grove/tty-dump-now"
	_, err = os.Stat(triggerFile)
	if os.IsNotExist(err) {
		return // Trigger file not present
	}

	// Delete the trigger file first (best-effort)
	_ = os.Remove(triggerFile)

	c.dumpState()
}

// DumpStateNow captures the compositor buffers and ghostty grids
// immediately, for hosts that want to snapshot state at a meaningful
// moment (e.g. treemux dumps right before the C-g e full-relayout
// "heal", preserving the pre-heal front/back buffers). No-op unless
// GROVE_TTY_AUDIT is enabled. Takes the TTY mutex so the dump is
// atomic with respect to blits and flushes.
func (c *Compositor) DumpStateNow() {
	if ttyAuditor == nil || c.Compositor == nil {
		return
	}
	ttyMu.Lock()
	defer ttyMu.Unlock()
	c.dumpState()
}

// dumpState writes the buffer/grid dump. Callers hold ttyMu (Flush path
// already does; DumpStateNow acquires it).
func (c *Compositor) dumpState() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	ts := time.Now().UnixNano()
	dumpDir := fmt.Sprintf("%s/.local/state/grove/tty-dump-%d", home, ts)

	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		return
	}

	// Call ext_dump_state to dump compositor buffers
	cDumpDir := C.CString(dumpDir)
	defer C.free(unsafe.Pointer(cDumpDir))
	C.ext_dump_state(c.Pointer(), cDumpDir)

	// Dump all ghostty grids registered with the extension
	C.ext_dump_all_grids(cDumpDir)
}

// Flush writes only changed cells to the given file descriptor, holding
// the TTY write mutex for the entire C compositor_flush call so the frame
// is atomic with respect to other serialized writers (see ttyMu).
// This shadows the embedded base Compositor.Flush.
func (c *Compositor) Flush(fd int) {
	if c.Compositor == nil {
		return
	}

	// Audit instrumentation: log the Flush event
	startTime := int64(0)
	if ttyAuditor != nil {
		startTime = ttyAuditor.GetNanoTime()
	}

	ttyMu.Lock()
	c.Compositor.Flush(fd)

	// Check for state dump trigger (infrequently to minimize overhead)
	if ttyAuditor != nil {
		dumpStateCounter++
		if dumpStateCounter >= dumpStateLimiter {
			dumpStateCounter = 0
			c.checkAndDumpState()
		}
	}

	ttyMu.Unlock()

	if ttyAuditor != nil {
		duration := ttyAuditor.GetNanoTime() - startTime
		ttyAuditor.LogFlush(duration)
	}
}

// SerializedWriter wraps w so each Write holds the shared TTY mutex,
// serializing it against compositor Flush frames. Pass the result to
// tea.WithOutput so bubbletea's renderer cannot interleave escape
// sequences with the Zig compositor's raw write(2).
//
// The returned writer also exposes Read, Close, and Fd by delegating to w
// when supported: bubbletea type-asserts its output to term.File
// (io.ReadWriteCloser + Fd) to detect the TTY, query its size, and
// restore termios state — a plain io.Writer would silently disable
// window-size handling.
func SerializedWriter(w io.Writer) io.Writer {
	return &serializedTTY{w: w}
}

type serializedTTY struct {
	w io.Writer
}

func (s *serializedTTY) Write(p []byte) (int, error) {
	ttyMu.Lock()
	defer ttyMu.Unlock()

	// Audit instrumentation when GROVE_TTY_AUDIT is enabled
	if ttyAuditor != nil {
		ttyAuditor.LogWrite(p, "writer")
	}

	return s.w.Write(p)
}

func (s *serializedTTY) Read(p []byte) (int, error) {
	if r, ok := s.w.(io.Reader); ok {
		return r.Read(p)
	}
	return 0, io.EOF
}

func (s *serializedTTY) Close() error {
	if c, ok := s.w.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (s *serializedTTY) Fd() uintptr {
	if f, ok := s.w.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return ^uintptr(0) // invalid fd: term.IsTerminal fails cleanly
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

// ============================================================================
// TTY Write Audit Instrumentation (GROVE_TTY_AUDIT)
// ============================================================================

var (
	// ttyAuditor is initialized once by InitTTYAudit if GROVE_TTY_AUDIT is set.
	// Check ttyAuditor != nil in hot paths instead of a separate flag.
	ttyAuditor *TTYAuditor
)

func init() {
	// Initialize TTY auditor from environment on package load.
	// This runs before InitTTYAudit() is called, but the env var check is fast.
	auditPath := os.Getenv("GROVE_TTY_AUDIT")
	if auditPath == "" {
		return
	}

	if auditPath == "1" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		auditPath = home + "/.local/state/grove/tty-audit.log"
	}

	ttyAuditor = &TTYAuditor{
		path:                    auditPath,
		lastWriterEndedMidSeq:   false,
	}
}

// TTYAuditor logs TTY write events to a side file for race condition diagnosis.
type TTYAuditor struct {
	path                    string
	file                    *os.File
	fileMu                  sync.Mutex
	lastWriterEndedMidSeq   bool
	lastWriterID            string
	escapeState             int // 0=normal, 1=ESC seen, 2=CSI/OSC seen
}

// escapeSequenceState returns 0 if chunk ends in normal state,
// 1 if it ends with ESC, 2 if it ends mid-CSI/OSC.
func (a *TTYAuditor) analyzeEscapeState(data []byte) int {
	state := a.escapeState
	for _, b := range data {
		switch state {
		case 0: // Normal
			if b == 0x1b { // ESC
				state = 1
			}
		case 1: // ESC seen
			if b == '[' {
				state = 2 // CSI
			} else if b == ']' {
				state = 2 // OSC
			} else {
				state = 0 // ESC + other (complete)
			}
		case 2: // CSI/OSC in progress
			// CSI ends with letter (A-Z, a-z, etc)
			// OSC ends with BEL (0x07) or ST (ESC \)
			if (b >= 0x40 && b <= 0x7e) || b == 0x07 {
				state = 0
			}
			if b == 0x1b { // Potential ST start
				state = 1
			}
		}
	}
	a.escapeState = state
	return state
}

// LogWrite logs a writer chunk event.
func (a *TTYAuditor) LogWrite(data []byte, writerID string) {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()

	if a.file == nil {
		var err error
		a.file, err = os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
	}

	ns := time.Now().UnixNano()

	// Truncate data preview to first 32 bytes
	preview := data
	if len(preview) > 32 {
		preview = preview[:32]
	}
	previewStr := strings.Replace(fmt.Sprintf("%q", preview), "\\x", "\\x", -1)

	// Check escape state
	endedMidSeq := a.analyzeEscapeState(data)
	raceFlag := ""
	if a.lastWriterEndedMidSeq && a.lastWriterID != writerID {
		raceFlag = " RACE"
	}

	fmt.Fprintf(a.file, "%d writer:%s bytes:%d endedMid:%d%s data:%s\n",
		ns, writerID, len(data), endedMidSeq, raceFlag, previewStr)

	a.lastWriterEndedMidSeq = (endedMidSeq != 0)
	a.lastWriterID = writerID
}

// LogFlush logs a Flush event with lock hold duration.
func (a *TTYAuditor) LogFlush(durationNs int64) {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()

	if a.file == nil {
		var err error
		a.file, err = os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
	}

	ns := time.Now().UnixNano()
	fmt.Fprintf(a.file, "%d flush duration:%d\n", ns, durationNs)

	// Clear the mid-sequence flag on flush boundaries
	a.lastWriterEndedMidSeq = false
}

// GetNanoTime returns current nanosecond timestamp.
func (a *TTYAuditor) GetNanoTime() int64 {
	return time.Now().UnixNano()
}
