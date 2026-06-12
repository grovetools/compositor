package ghostty

/*
#include <ghostty/vt.h>
#include <stdlib.h>
*/
import "C"

import (
	"sync"
	"unsafe"
)

// Mouse event actions — mirrors GhosttyMouseAction.
const (
	MouseActionPress   = int(C.GHOSTTY_MOUSE_ACTION_PRESS)
	MouseActionRelease = int(C.GHOSTTY_MOUSE_ACTION_RELEASE)
	MouseActionMotion  = int(C.GHOSTTY_MOUSE_ACTION_MOTION)
)

// Mouse button identities — mirrors GhosttyMouseButton. Wheel up/down/left/
// right are buttons four through seven per xterm convention; the encoder
// translates them to the tracking protocol's wheel codes.
const (
	MouseButtonNone       = int(C.GHOSTTY_MOUSE_BUTTON_UNKNOWN)
	MouseButtonLeft       = int(C.GHOSTTY_MOUSE_BUTTON_LEFT)
	MouseButtonRight      = int(C.GHOSTTY_MOUSE_BUTTON_RIGHT)
	MouseButtonMiddle     = int(C.GHOSTTY_MOUSE_BUTTON_MIDDLE)
	MouseButtonWheelUp    = int(C.GHOSTTY_MOUSE_BUTTON_FOUR)
	MouseButtonWheelDown  = int(C.GHOSTTY_MOUSE_BUTTON_FIVE)
	MouseButtonWheelLeft  = int(C.GHOSTTY_MOUSE_BUTTON_SIX)
	MouseButtonWheelRight = int(C.GHOSTTY_MOUSE_BUTTON_SEVEN)
	MouseButtonBackward   = int(C.GHOSTTY_MOUSE_BUTTON_EIGHT)
	MouseButtonForward    = int(C.GHOSTTY_MOUSE_BUTTON_NINE)
)

// mouseEncState holds the lazily-created libghostty mouse encoder for a
// Terminal. Kept out of the hot Terminal struct; created on first
// EncodeMouseEvent call (i.e. only for panes whose app enables tracking).
type mouseEncState struct {
	encoder C.GhosttyMouseEncoder
	event   C.GhosttyMouseEvent
}

var (
	mouseEncMu  sync.Mutex
	mouseEncMap = map[*Terminal]*mouseEncState{}
)

// IsMouseTrackingActive returns true if the child process has enabled any
// mouse tracking mode (DEC 9/1000/1002/1003). Input routing uses this to
// decide whether mouse events should be encoded and forwarded to the PTY
// (the app owns the mouse) or handled host-side (selection/copy mode).
func (t *Terminal) IsMouseTrackingActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal == nil {
		return false
	}
	var tracking C.bool
	res := C.ghostty_terminal_get(t.terminal, C.GHOSTTY_TERMINAL_DATA_MOUSE_TRACKING, unsafe.Pointer(&tracking))
	return res == C.GHOSTTY_SUCCESS && bool(tracking)
}

// IsAlternateScreenActive returns true when the child process is on the
// alternate screen (full-screen apps: claude fullscreen mode, less, vim).
// Used to redirect copy-mode entry to the app's own scrollback UI when
// there is no host-side scrollback to browse.
func (t *Terminal) IsAlternateScreenActive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal == nil {
		return false
	}
	var screen C.GhosttyTerminalScreen
	res := C.ghostty_terminal_get(t.terminal, C.GHOSTTY_TERMINAL_DATA_ACTIVE_SCREEN, unsafe.Pointer(&screen))
	return res == C.GHOSTTY_SUCCESS && screen == C.GHOSTTY_TERMINAL_SCREEN_ALTERNATE
}

// EncodeMouseEvent encodes a mouse event into the escape sequence the child
// app expects under its current tracking mode and output format (X10/UTF-8/
// SGR/urxvt), reading both from live terminal state. Returns nil when the
// app isn't tracking the mouse or the event produces no output under the
// active mode (e.g. motion without a button in button-event mode).
//
// x,y are 0-based cell coordinates relative to the pane. anyButtonPressed
// reflects the caller's drag state and gates motion reporting in
// button-event mode.
func (t *Terminal) EncodeMouseEvent(action, button, mods, x, y int, anyButtonPressed bool) []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.terminal == nil {
		return nil
	}

	mouseEncMu.Lock()
	st := mouseEncMap[t]
	if st == nil {
		st = &mouseEncState{}
		if C.ghostty_mouse_encoder_new(nil, &st.encoder) != C.GHOSTTY_SUCCESS {
			mouseEncMu.Unlock()
			return nil
		}
		if C.ghostty_mouse_event_new(nil, &st.event) != C.GHOSTTY_SUCCESS {
			C.ghostty_mouse_encoder_free(st.encoder)
			mouseEncMu.Unlock()
			return nil
		}
		dedup := C.bool(true)
		C.ghostty_mouse_encoder_setopt(st.encoder, C.GHOSTTY_MOUSE_ENCODER_OPT_TRACK_LAST_CELL, unsafe.Pointer(&dedup))
		mouseEncMap[t] = st
	}
	mouseEncMu.Unlock()

	// Pull tracking mode + output format from live terminal state, then
	// describe the geometry. The terminal is text-mode with 1x1-pixel
	// cells (see New's ghostty_terminal_resize call), so cell coords are
	// pixel coords; +0.5 lands in the cell center.
	C.ghostty_mouse_encoder_setopt_from_terminal(st.encoder, t.terminal)
	size := C.GhosttyMouseEncoderSize{
		size:          C.size_t(unsafe.Sizeof(C.GhosttyMouseEncoderSize{})),
		screen_width:  C.uint32_t(t.cols),
		screen_height: C.uint32_t(t.rows),
		cell_width:    1,
		cell_height:   1,
	}
	C.ghostty_mouse_encoder_setopt(st.encoder, C.GHOSTTY_MOUSE_ENCODER_OPT_SIZE, unsafe.Pointer(&size))
	pressed := C.bool(anyButtonPressed)
	C.ghostty_mouse_encoder_setopt(st.encoder, C.GHOSTTY_MOUSE_ENCODER_OPT_ANY_BUTTON_PRESSED, unsafe.Pointer(&pressed))

	C.ghostty_mouse_event_set_action(st.event, C.GhosttyMouseAction(action))
	if button == MouseButtonNone {
		C.ghostty_mouse_event_clear_button(st.event)
	} else {
		C.ghostty_mouse_event_set_button(st.event, C.GhosttyMouseButton(button))
	}
	C.ghostty_mouse_event_set_mods(st.event, C.GhosttyMods(mods))
	C.ghostty_mouse_event_set_position(st.event, C.GhosttyMousePosition{
		x: C.float(float32(x) + 0.5),
		y: C.float(float32(y) + 0.5),
	})

	var buf [64]C.char
	var n C.size_t
	res := C.ghostty_mouse_encoder_encode(st.encoder, st.event, &buf[0], C.size_t(len(buf)), &n)
	if res != C.GHOSTTY_SUCCESS || n == 0 {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(&buf[0]), C.int(n))
}

// freeMouseEncoder releases the lazily-created mouse encoder state for t.
// Called from Terminal.Close.
func freeMouseEncoder(t *Terminal) {
	mouseEncMu.Lock()
	st := mouseEncMap[t]
	delete(mouseEncMap, t)
	mouseEncMu.Unlock()
	if st != nil {
		C.ghostty_mouse_event_free(st.event)
		C.ghostty_mouse_encoder_free(st.encoder)
	}
}
