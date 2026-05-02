package ghostty

/*
#cgo CFLAGS: -I${SRCDIR}/../lib/ghostty/include
#cgo LDFLAGS: -L${SRCDIR}/../lib/ghostty/lib -lghostty-vt -lc++ -framework CoreFoundation

#include <ghostty/vt.h>
#include <ghostty/vt/sys.h>
#include <stdlib.h>
#include <string.h>

// Defined in effects.c
extern void ghostty_setup_effects(GhosttyTerminal term, void* userdata);

// Silence libghostty logging (page_list info messages corrupt terminal).
static inline void silence_ghostty_logs(void) {
    ghostty_sys_set(GHOSTTY_SYS_OPT_LOG, NULL);
}

// Helper: CGo can't set union fields directly.
static GhosttyTerminalScrollViewport scroll_viewport_delta(intptr_t delta) {
    GhosttyTerminalScrollViewport sv;
    sv.tag = GHOSTTY_SCROLL_VIEWPORT_DELTA;
    sv.value.delta = delta;
    return sv;
}
static GhosttyTerminalScrollViewport scroll_viewport_bottom(void) {
    GhosttyTerminalScrollViewport sv;
    sv.tag = GHOSTTY_SCROLL_VIEWPORT_BOTTOM;
    return sv;
}
// Helper: build a viewport GhosttyPoint (CGo can't access union fields).
static GhosttyPoint make_viewport_point(uint16_t x, uint32_t y) {
    GhosttyPoint p;
    memset(&p, 0, sizeof(p));
    p.tag = GHOSTTY_POINT_TAG_VIEWPORT;
    p.value.coordinate.x = x;
    p.value.coordinate.y = y;
    return p;
}
// Helper: build a screen-coordinate GhosttyPoint (scrollback + active area).
static GhosttyPoint make_screen_point(uint16_t x, uint32_t y) {
    GhosttyPoint p;
    memset(&p, 0, sizeof(p));
    p.tag = GHOSTTY_POINT_TAG_SCREEN;
    p.value.coordinate.x = x;
    p.value.coordinate.y = y;
    return p;
}
*/
import "C"
import (
	"fmt"
	"io"
	"sync"
	"unsafe"

	"github.com/mattn/go-runewidth"
)

func runeWidth(r rune) int {
	w := runewidth.RuneWidth(r)
	if w < 1 {
		return 1
	}
	return w
}

// registry maps C userdata pointers back to Terminal instances.
var (
	registryMu sync.Mutex
	registry   = map[uintptr]*Terminal{}
	nextID     uintptr
)

func registerTerminal(t *Terminal) uintptr {
	registryMu.Lock()
	defer registryMu.Unlock()
	nextID++
	registry[nextID] = t
	return nextID
}

func unregisterTerminal(id uintptr) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, id)
}

func lookupTerminal(id uintptr) *Terminal {
	registryMu.Lock()
	defer registryMu.Unlock()
	return registry[id]
}

//export goWritePTYCallback
func goWritePTYCallback(userdata unsafe.Pointer, data *C.uint8_t, length C.size_t) {
	id := uintptr(userdata)
	t := lookupTerminal(id)
	if t == nil || t.ptyWriter == nil || data == nil || length == 0 {
		return
	}
	buf := C.GoBytes(unsafe.Pointer(data), C.int(length))
	_, _ = t.ptyWriter.Write(buf)
}

func init() {
	C.silence_ghostty_logs()
}

// Terminal wraps a libghostty-vt terminal instance.
type Terminal struct {
	terminal   C.GhosttyTerminal
	formatter  C.GhosttyFormatter
	keyEncoder C.GhosttyKeyEncoder
	keyEvent   C.GhosttyKeyEvent
	cols, rows int
	mu         sync.Mutex
	ptyWriter  io.Writer // for writing responses back to PTY (local *os.File or remote WS)
	registryID uintptr   // ID in the global registry for C callbacks

	// Render state for copy-mode grid access (lazy-initialized).
	renderState   C.GhosttyRenderState
	rowIter       C.GhosttyRenderStateRowIterator
	rowCells      C.GhosttyRenderStateRowCells
	renderInited  bool
}

// New creates a new ghostty terminal with the given dimensions.
func New(cols, rows int) (*Terminal, error) {
	t := &Terminal{cols: cols, rows: rows}

	opts := C.GhosttyTerminalOptions{
		cols:           C.uint16_t(cols),
		rows:           C.uint16_t(rows),
		max_scrollback: 1000,
	}

	res := C.ghostty_terminal_new(nil, &t.terminal, opts)
	if res != C.GHOSTTY_SUCCESS {
		return nil, fmt.Errorf("ghostty_terminal_new failed: %d", res)
	}

	// Initial resize to set cell dimensions (use 1x1 pixel cells for text-mode)
	C.ghostty_terminal_resize(t.terminal, C.uint16_t(cols), C.uint16_t(rows), 1, 1)

	// Create key encoder
	res = C.ghostty_key_encoder_new(nil, &t.keyEncoder)
	if res != C.GHOSTTY_SUCCESS {
		C.ghostty_terminal_free(t.terminal)
		return nil, fmt.Errorf("ghostty_key_encoder_new failed: %d", res)
	}

	// Create key event
	res = C.ghostty_key_event_new(nil, &t.keyEvent)
	if res != C.GHOSTTY_SUCCESS {
		C.ghostty_key_encoder_free(t.keyEncoder)
		C.ghostty_terminal_free(t.terminal)
		return nil, fmt.Errorf("ghostty_key_event_new failed: %d", res)
	}

	// Create formatter with VT output format
	fmtOpts := C.GhosttyFormatterTerminalOptions{
		size:      C.size_t(unsafe.Sizeof(C.GhosttyFormatterTerminalOptions{})),
		emit:      C.GHOSTTY_FORMATTER_FORMAT_VT,
		selection: nil,
	}
	res = C.ghostty_formatter_terminal_new(nil, &t.formatter, t.terminal, fmtOpts)
	if res != C.GHOSTTY_SUCCESS {
		C.ghostty_key_event_free(t.keyEvent)
		C.ghostty_key_encoder_free(t.keyEncoder)
		C.ghostty_terminal_free(t.terminal)
		return nil, fmt.Errorf("ghostty_formatter_terminal_new failed: %d", res)
	}

	return t, nil
}

// SetWriter sets the writer for PTY responses and registers the WRITE_PTY
// effect callback. This enables the terminal to respond to device attribute
// queries, size queries, and other VT sequences that require writing back
// to the PTY. The writer can be a local *os.File or a remote WebSocket backend.
func (t *Terminal) SetWriter(w io.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ptyWriter = w
	t.registryID = registerTerminal(t)
	C.ghostty_setup_effects(t.terminal, unsafe.Pointer(t.registryID))
}

// WriteVT feeds raw PTY output into the terminal's VT parser.
func (t *Terminal) WriteVT(data []byte) {
	if len(data) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	C.ghostty_terminal_vt_write(t.terminal, (*C.uint8_t)(unsafe.Pointer(&data[0])), C.size_t(len(data)))
}

// FormatScreen returns the current terminal screen as a VT-encoded string.
// This includes ANSI escape sequences for colors, styles, cursor position, etc.
func (t *Terminal) FormatScreen() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	var outPtr *C.uint8_t
	var outLen C.size_t

	res := C.ghostty_formatter_format_alloc(t.formatter, nil, &outPtr, &outLen)
	if res != C.GHOSTTY_SUCCESS || outPtr == nil || outLen == 0 {
		return ""
	}

	// Copy to Go string and free the C buffer
	goStr := C.GoStringN((*C.char)(unsafe.Pointer(outPtr)), C.int(outLen))
	C.ghostty_free(nil, outPtr, outLen)

	return goStr
}

// ScrollViewport scrolls the terminal viewport by delta rows.
// Negative delta scrolls up (into scrollback), positive scrolls down.
func (t *Terminal) ScrollViewport(delta int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	C.ghostty_terminal_scroll_viewport(t.terminal, C.scroll_viewport_delta(C.intptr_t(delta)))
}

// ScrollToBottom scrolls the viewport back to the active area.
func (t *Terminal) ScrollToBottom() {
	t.mu.Lock()
	defer t.mu.Unlock()
	C.ghostty_terminal_scroll_viewport(t.terminal, C.scroll_viewport_bottom())
}

// FormatScreenPlain returns the current terminal screen as plain text
// (no ANSI escape sequences). Used for clipboard copy in copy mode.
func (t *Terminal) FormatScreenPlain() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Create a plain-text formatter for the current screen state.
	var plainFmt C.GhosttyFormatter
	fmtOpts := C.GhosttyFormatterTerminalOptions{
		size:      C.size_t(unsafe.Sizeof(C.GhosttyFormatterTerminalOptions{})),
		emit:      C.GHOSTTY_FORMATTER_FORMAT_PLAIN,
		trim:      C.bool(true),
		selection: nil,
	}
	res := C.ghostty_formatter_terminal_new(nil, &plainFmt, t.terminal, fmtOpts)
	if res != C.GHOSTTY_SUCCESS {
		return ""
	}
	defer C.ghostty_formatter_free(plainFmt)

	var outPtr *C.uint8_t
	var outLen C.size_t
	res = C.ghostty_formatter_format_alloc(plainFmt, nil, &outPtr, &outLen)
	if res != C.GHOSTTY_SUCCESS || outPtr == nil || outLen == 0 {
		return ""
	}
	goStr := C.GoStringN((*C.char)(unsafe.Pointer(outPtr)), C.int(outLen))
	C.ghostty_free(nil, outPtr, outLen)
	return goStr
}

// FormatSelectionPlain returns the plain text within the given selection range
// (viewport-local coordinates, 0-indexed). Uses ghostty's built-in selection
// support in the formatter to extract exactly the selected text.
func (t *Terminal) FormatSelectionPlain(startCol, startRow, endCol, endRow int) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Build GhosttyGridRef for start and end by looking up viewport points.
	var startRef C.GhosttyGridRef
	startRef.size = C.size_t(unsafe.Sizeof(startRef))
	startPoint := C.make_viewport_point(C.uint16_t(startCol), C.uint32_t(startRow))
	if C.ghostty_terminal_grid_ref(t.terminal, startPoint, &startRef) != C.GHOSTTY_SUCCESS {
		return ""
	}

	var endRef C.GhosttyGridRef
	endRef.size = C.size_t(unsafe.Sizeof(endRef))
	endPoint := C.make_viewport_point(C.uint16_t(endCol), C.uint32_t(endRow))
	if C.ghostty_terminal_grid_ref(t.terminal, endPoint, &endRef) != C.GHOSTTY_SUCCESS {
		return ""
	}

	// Build selection struct.
	var sel C.GhosttySelection
	sel.size = C.size_t(unsafe.Sizeof(sel))
	sel.start = startRef
	sel.end = endRef
	sel.rectangle = C.bool(false)

	// Create a plain-text formatter with the selection.
	var plainFmt C.GhosttyFormatter
	fmtOpts := C.GhosttyFormatterTerminalOptions{
		size:      C.size_t(unsafe.Sizeof(C.GhosttyFormatterTerminalOptions{})),
		emit:      C.GHOSTTY_FORMATTER_FORMAT_PLAIN,
		trim:      C.bool(true),
		selection: &sel,
	}
	res := C.ghostty_formatter_terminal_new(nil, &plainFmt, t.terminal, fmtOpts)
	if res != C.GHOSTTY_SUCCESS {
		return ""
	}
	defer C.ghostty_formatter_free(plainFmt)

	var outPtr *C.uint8_t
	var outLen C.size_t
	res = C.ghostty_formatter_format_alloc(plainFmt, nil, &outPtr, &outLen)
	if res != C.GHOSTTY_SUCCESS || outPtr == nil || outLen == 0 {
		return ""
	}
	goStr := C.GoStringN((*C.char)(unsafe.Pointer(outPtr)), C.int(outLen))
	C.ghostty_free(nil, outPtr, outLen)
	return goStr
}

// TotalRows returns the total number of rows in the terminal (scrollback + viewport).
func (t *Terminal) TotalRows() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	var total C.size_t
	res := C.ghostty_terminal_get(t.terminal, C.GHOSTTY_TERMINAL_DATA_TOTAL_ROWS, unsafe.Pointer(&total))
	if res != C.GHOSTTY_SUCCESS {
		return t.rows
	}
	return int(total)
}

// FormatScrollbackPlain returns the full scrollback + viewport as plain text.
// It creates a selection spanning the entire screen buffer (row 0 = top of
// scrollback, through to the last row of the active viewport) and formats it.
func (t *Terminal) FormatScrollbackPlain() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Get total rows to know the extent of the screen buffer.
	var total C.size_t
	if C.ghostty_terminal_get(t.terminal, C.GHOSTTY_TERMINAL_DATA_TOTAL_ROWS, unsafe.Pointer(&total)) != C.GHOSTTY_SUCCESS || total == 0 {
		return ""
	}

	// Build screen-coordinate points spanning the full buffer.
	startPoint := C.make_screen_point(0, 0)
	var startRef C.GhosttyGridRef
	startRef.size = C.size_t(unsafe.Sizeof(startRef))
	if C.ghostty_terminal_grid_ref(t.terminal, startPoint, &startRef) != C.GHOSTTY_SUCCESS {
		return ""
	}

	lastRow := C.uint32_t(total - 1)
	lastCol := C.uint16_t(t.cols - 1)
	endPoint := C.make_screen_point(lastCol, lastRow)
	var endRef C.GhosttyGridRef
	endRef.size = C.size_t(unsafe.Sizeof(endRef))
	if C.ghostty_terminal_grid_ref(t.terminal, endPoint, &endRef) != C.GHOSTTY_SUCCESS {
		return ""
	}

	// Build selection covering the full buffer.
	var sel C.GhosttySelection
	sel.size = C.size_t(unsafe.Sizeof(sel))
	sel.start = startRef
	sel.end = endRef
	sel.rectangle = C.bool(false)

	// Create a plain-text formatter with the selection.
	var plainFmt C.GhosttyFormatter
	fmtOpts := C.GhosttyFormatterTerminalOptions{
		size:      C.size_t(unsafe.Sizeof(C.GhosttyFormatterTerminalOptions{})),
		emit:      C.GHOSTTY_FORMATTER_FORMAT_PLAIN,
		trim:      C.bool(true),
		selection: &sel,
	}
	res := C.ghostty_formatter_terminal_new(nil, &plainFmt, t.terminal, fmtOpts)
	if res != C.GHOSTTY_SUCCESS {
		return ""
	}
	defer C.ghostty_formatter_free(plainFmt)

	var outPtr *C.uint8_t
	var outLen C.size_t
	res = C.ghostty_formatter_format_alloc(plainFmt, nil, &outPtr, &outLen)
	if res != C.GHOSTTY_SUCCESS || outPtr == nil || outLen == 0 {
		return ""
	}
	goStr := C.GoStringN((*C.char)(unsafe.Pointer(outPtr)), C.int(outLen))
	C.ghostty_free(nil, outPtr, outLen)
	return goStr
}

// Resize changes the terminal dimensions.
func (t *Terminal) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cols = cols
	t.rows = rows
	C.ghostty_terminal_resize(t.terminal, C.uint16_t(cols), C.uint16_t(rows), 1, 1)
}

// EncodeKey encodes a key event using the ghostty key encoder, respecting the
// terminal's current keyboard mode (legacy VT100 vs kitty keyboard protocol).
// The encoder syncs from the terminal's internal state on each call, so if the
// child process has negotiated kitty keyboard mode via CSI > flags u, the
// encoder automatically produces CSI-u sequences instead of legacy bytes.
//
// key is a GhosttyKey constant (use the Key* constants exported below).
// mods is a GhosttyMods bitmask (use the Mod* constants exported below).
// utf8Text is the unmodified character for the key (e.g. "i" for key_I),
// or "" for non-printable keys.
func (t *Terminal) EncodeKey(key int, mods int, utf8Text string) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Sync encoder options from the terminal's current state. This picks up
	// kitty keyboard mode, cursor key application mode, etc.
	C.ghostty_key_encoder_setopt_from_terminal(t.keyEncoder, t.terminal)

	// Populate the reusable key event.
	C.ghostty_key_event_set_action(t.keyEvent, C.GHOSTTY_KEY_ACTION_PRESS)
	C.ghostty_key_event_set_key(t.keyEvent, C.GhosttyKey(key))
	C.ghostty_key_event_set_mods(t.keyEvent, C.GhosttyMods(mods))
	C.ghostty_key_event_set_consumed_mods(t.keyEvent, 0)

	if utf8Text != "" {
		cstr := C.CString(utf8Text)
		C.ghostty_key_event_set_utf8(t.keyEvent, cstr, C.size_t(len(utf8Text)))
		C.free(unsafe.Pointer(cstr))
		// Set the unshifted codepoint — the kitty encoder uses this to
		// produce the correct Unicode value in CSI-u sequences.
		r := []rune(utf8Text)
		if len(r) > 0 {
			C.ghostty_key_event_set_unshifted_codepoint(t.keyEvent, C.uint32_t(r[0]))
		}
	} else {
		C.ghostty_key_event_set_utf8(t.keyEvent, nil, 0)
		C.ghostty_key_event_set_unshifted_codepoint(t.keyEvent, 0)
	}

	// Encode into a stack buffer. 128 bytes is generous for any escape sequence.
	var buf [128]C.char
	var written C.size_t
	res := C.ghostty_key_encoder_encode(t.keyEncoder, t.keyEvent, &buf[0], 128, &written)
	if res == C.GHOSTTY_SUCCESS && written > 0 {
		return C.GoBytes(unsafe.Pointer(&buf[0]), C.int(written))
	}
	if res == C.GHOSTTY_OUT_OF_SPACE && written > 0 {
		// Extremely unlikely, but handle oversized sequences.
		dynBuf := make([]C.char, written)
		res = C.ghostty_key_encoder_encode(t.keyEncoder, t.keyEvent, &dynBuf[0], written, &written)
		if res == C.GHOSTTY_SUCCESS && written > 0 {
			return C.GoBytes(unsafe.Pointer(&dynBuf[0]), C.int(written))
		}
	}
	return nil
}

// Ghostty key constants — mirrors GhosttyKey enum from ghostty/vt/key/event.h.
// Only the keys needed by the terminal's input routing are exported here.
const (
	KeyUnidentified = int(C.GHOSTTY_KEY_UNIDENTIFIED)
	KeyA            = int(C.GHOSTTY_KEY_A)
	KeyB            = int(C.GHOSTTY_KEY_B)
	KeyC            = int(C.GHOSTTY_KEY_C)
	KeyD            = int(C.GHOSTTY_KEY_D)
	KeyE            = int(C.GHOSTTY_KEY_E)
	KeyF            = int(C.GHOSTTY_KEY_F)
	KeyG            = int(C.GHOSTTY_KEY_G)
	KeyH            = int(C.GHOSTTY_KEY_H)
	KeyI            = int(C.GHOSTTY_KEY_I)
	KeyJ            = int(C.GHOSTTY_KEY_J)
	KeyK            = int(C.GHOSTTY_KEY_K)
	KeyL            = int(C.GHOSTTY_KEY_L)
	KeyM            = int(C.GHOSTTY_KEY_M)
	KeyN            = int(C.GHOSTTY_KEY_N)
	KeyO            = int(C.GHOSTTY_KEY_O)
	KeyP            = int(C.GHOSTTY_KEY_P)
	KeyQ            = int(C.GHOSTTY_KEY_Q)
	KeyR            = int(C.GHOSTTY_KEY_R)
	KeyS            = int(C.GHOSTTY_KEY_S)
	KeyT            = int(C.GHOSTTY_KEY_T)
	KeyU            = int(C.GHOSTTY_KEY_U)
	KeyV            = int(C.GHOSTTY_KEY_V)
	KeyW            = int(C.GHOSTTY_KEY_W)
	KeyX            = int(C.GHOSTTY_KEY_X)
	KeyY            = int(C.GHOSTTY_KEY_Y)
	KeyZ            = int(C.GHOSTTY_KEY_Z)
	KeyDigit0       = int(C.GHOSTTY_KEY_DIGIT_0)
	KeyDigit1       = int(C.GHOSTTY_KEY_DIGIT_1)
	KeyDigit2       = int(C.GHOSTTY_KEY_DIGIT_2)
	KeyDigit3       = int(C.GHOSTTY_KEY_DIGIT_3)
	KeyDigit4       = int(C.GHOSTTY_KEY_DIGIT_4)
	KeyDigit5       = int(C.GHOSTTY_KEY_DIGIT_5)
	KeyDigit6       = int(C.GHOSTTY_KEY_DIGIT_6)
	KeyDigit7       = int(C.GHOSTTY_KEY_DIGIT_7)
	KeyDigit8       = int(C.GHOSTTY_KEY_DIGIT_8)
	KeyDigit9       = int(C.GHOSTTY_KEY_DIGIT_9)
	KeyBackquote    = int(C.GHOSTTY_KEY_BACKQUOTE)
	KeyBackslash    = int(C.GHOSTTY_KEY_BACKSLASH)
	KeyBracketLeft  = int(C.GHOSTTY_KEY_BRACKET_LEFT)
	KeyBracketRight = int(C.GHOSTTY_KEY_BRACKET_RIGHT)
	KeyComma        = int(C.GHOSTTY_KEY_COMMA)
	KeyEqual        = int(C.GHOSTTY_KEY_EQUAL)
	KeyMinus        = int(C.GHOSTTY_KEY_MINUS)
	KeyPeriod       = int(C.GHOSTTY_KEY_PERIOD)
	KeyQuote        = int(C.GHOSTTY_KEY_QUOTE)
	KeySemicolon    = int(C.GHOSTTY_KEY_SEMICOLON)
	KeySlash        = int(C.GHOSTTY_KEY_SLASH)
	KeyBackspace    = int(C.GHOSTTY_KEY_BACKSPACE)
	KeyEnter        = int(C.GHOSTTY_KEY_ENTER)
	KeySpace        = int(C.GHOSTTY_KEY_SPACE)
	KeyTab          = int(C.GHOSTTY_KEY_TAB)
	KeyDelete       = int(C.GHOSTTY_KEY_DELETE)
	KeyEnd          = int(C.GHOSTTY_KEY_END)
	KeyHome         = int(C.GHOSTTY_KEY_HOME)
	KeyInsert       = int(C.GHOSTTY_KEY_INSERT)
	KeyPageDown     = int(C.GHOSTTY_KEY_PAGE_DOWN)
	KeyPageUp       = int(C.GHOSTTY_KEY_PAGE_UP)
	KeyArrowDown    = int(C.GHOSTTY_KEY_ARROW_DOWN)
	KeyArrowLeft    = int(C.GHOSTTY_KEY_ARROW_LEFT)
	KeyArrowRight   = int(C.GHOSTTY_KEY_ARROW_RIGHT)
	KeyArrowUp      = int(C.GHOSTTY_KEY_ARROW_UP)
	KeyEscape       = int(C.GHOSTTY_KEY_ESCAPE)
	KeyF1           = int(C.GHOSTTY_KEY_F1)
	KeyF2           = int(C.GHOSTTY_KEY_F2)
	KeyF3           = int(C.GHOSTTY_KEY_F3)
	KeyF4           = int(C.GHOSTTY_KEY_F4)
	KeyF5           = int(C.GHOSTTY_KEY_F5)
	KeyF6           = int(C.GHOSTTY_KEY_F6)
	KeyF7           = int(C.GHOSTTY_KEY_F7)
	KeyF8           = int(C.GHOSTTY_KEY_F8)
	KeyF9           = int(C.GHOSTTY_KEY_F9)
	KeyF10          = int(C.GHOSTTY_KEY_F10)
	KeyF11          = int(C.GHOSTTY_KEY_F11)
	KeyF12          = int(C.GHOSTTY_KEY_F12)

	// Modifier bitmask constants — mirrors GhosttyMods from event.h.
	ModShift = int(C.GHOSTTY_MODS_SHIFT)
	ModCtrl  = int(C.GHOSTTY_MODS_CTRL)
	ModAlt   = int(C.GHOSTTY_MODS_ALT)
	ModSuper = int(C.GHOSTTY_MODS_SUPER)
)

// IsKittyKeyboardModeActive returns true if the child process has requested
// kitty keyboard protocol mode (any flags set). This is checked by the input
// routing layer to decide whether ctrl+letter keys should be encoded via
// EncodeKey (CSI-u) instead of legacy single-byte control codes.
func (t *Terminal) IsKittyKeyboardModeActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal == nil {
		return false
	}
	var flags C.uint8_t
	res := C.ghostty_terminal_get(t.terminal, C.GHOSTTY_TERMINAL_DATA_KITTY_KEYBOARD_FLAGS, unsafe.Pointer(&flags))
	return res == C.GHOSTTY_SUCCESS && flags != 0
}

// UnsafePointer returns the raw C GhosttyTerminal handle for direct use
// by the compositor's blit path. The caller must hold the terminal's mutex
// or ensure exclusive access.
func (t *Terminal) UnsafePointer() unsafe.Pointer {
	return unsafe.Pointer(t.terminal)
}

// initRenderState lazily creates the render state objects for grid access.
// Must be called with t.mu held.
func (t *Terminal) initRenderState() {
	if t.renderInited {
		return
	}
	C.ghostty_render_state_new(nil, &t.renderState)
	C.ghostty_render_state_row_iterator_new(nil, &t.rowIter)
	C.ghostty_render_state_row_cells_new(nil, &t.rowCells)
	t.renderInited = true
}

// GetVisualRow returns the viewport row at index y as a []rune slice where
// each index corresponds to a visual column. Wide characters (width 2) are
// followed by a padding space so that len(result) == terminal width.
func (t *Terminal) GetVisualRow(y int) []rune {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.initRenderState()

	C.ghostty_render_state_update(t.renderState, t.terminal)
	C.ghostty_render_state_get(t.renderState, C.GHOSTTY_RENDER_STATE_DATA_ROW_ITERATOR,
		unsafe.Pointer(&t.rowIter))

	// Advance iterator to row y.
	for i := 0; i <= y; i++ {
		if !bool(C.ghostty_render_state_row_iterator_next(t.rowIter)) {
			return make([]rune, t.cols)
		}
	}

	C.ghostty_render_state_row_get(t.rowIter, C.GHOSTTY_RENDER_STATE_ROW_DATA_CELLS,
		unsafe.Pointer(&t.rowCells))

	row := make([]rune, 0, t.cols)
	for C.ghostty_render_state_row_cells_next(t.rowCells) {
		var graphemeLen C.uint32_t
		C.ghostty_render_state_row_cells_get(t.rowCells,
			C.GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_LEN, unsafe.Pointer(&graphemeLen))

		var cp rune = ' '
		if graphemeLen > 0 {
			var codepoints [16]C.uint32_t
			C.ghostty_render_state_row_cells_get(t.rowCells,
				C.GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_BUF, unsafe.Pointer(&codepoints[0]))
			cp = rune(codepoints[0])
		}

		row = append(row, cp)
		if runeWidth(cp) == 2 {
			row = append(row, ' ')
		}

		if len(row) >= t.cols {
			break
		}
	}

	// Pad to full width if needed.
	for len(row) < t.cols {
		row = append(row, ' ')
	}
	return row[:t.cols]
}

// Cols returns the terminal width.
func (t *Terminal) Cols() int {
	return t.cols
}

// Close frees all ghostty resources.
func (t *Terminal) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.renderInited {
		C.ghostty_render_state_row_cells_free(t.rowCells)
		C.ghostty_render_state_row_iterator_free(t.rowIter)
		C.ghostty_render_state_free(t.renderState)
		t.renderInited = false
	}
	if t.registryID != 0 {
		unregisterTerminal(t.registryID)
		t.registryID = 0
	}
	if t.formatter != nil {
		C.ghostty_formatter_free(t.formatter)
		t.formatter = nil
	}
	if t.keyEvent != nil {
		C.ghostty_key_event_free(t.keyEvent)
		t.keyEvent = nil
	}
	if t.keyEncoder != nil {
		C.ghostty_key_encoder_free(t.keyEncoder)
		t.keyEncoder = nil
	}
	if t.terminal != nil {
		C.ghostty_terminal_free(t.terminal)
		t.terminal = nil
	}
}

