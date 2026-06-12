package ghostty

import (
	"bytes"
	"testing"
)

// TestEncodeMouseEvent verifies the encoder respects the app's tracking
// state and produces protocol-correct SGR sequences with 1-based cell
// coordinates (the terminal uses 1x1-pixel cells).
func TestEncodeMouseEvent(t *testing.T) {
	term, err := New(80, 24)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer term.Close()

	if term.IsMouseTrackingActive() {
		t.Fatal("tracking should be off for a fresh terminal")
	}
	if got := term.EncodeMouseEvent(MouseActionPress, MouseButtonLeft, 0, 5, 3, false); got != nil {
		t.Fatalf("expected nil without tracking, got %q", got)
	}

	// Enable button-event tracking + SGR format (what claude/vim do).
	term.WriteVT([]byte("\x1b[?1002h\x1b[?1006h"))
	if !term.IsMouseTrackingActive() {
		t.Fatal("tracking should be on after DECSET 1002")
	}

	// Left press at cell (5,3) -> SGR is 1-based: <0;6;4M
	got := term.EncodeMouseEvent(MouseActionPress, MouseButtonLeft, 0, 5, 3, false)
	if want := []byte("\x1b[<0;6;4M"); !bytes.Equal(got, want) {
		t.Errorf("press: got %q, want %q", got, want)
	}

	// Wheel up at (0,0) -> button code 64.
	got = term.EncodeMouseEvent(MouseActionPress, MouseButtonWheelUp, 0, 0, 0, false)
	if want := []byte("\x1b[<64;1;1M"); !bytes.Equal(got, want) {
		t.Errorf("wheel: got %q, want %q", got, want)
	}

	// Motion with no button held produces nothing in button-event mode.
	if got := term.EncodeMouseEvent(MouseActionMotion, MouseButtonNone, 0, 7, 7, false); got != nil {
		t.Errorf("hover motion in 1002 mode should encode nothing, got %q", got)
	}

	// Drag motion (left held) reports with motion flag: <32;9;9M
	got = term.EncodeMouseEvent(MouseActionMotion, MouseButtonLeft, 0, 8, 8, true)
	if want := []byte("\x1b[<32;9;9M"); !bytes.Equal(got, want) {
		t.Errorf("drag: got %q, want %q", got, want)
	}
}

// TestIsAlternateScreenActive verifies alt-screen detection across
// DECSET 1049 enter/leave.
func TestIsAlternateScreenActive(t *testing.T) {
	term, err := New(80, 24)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer term.Close()

	if term.IsAlternateScreenActive() {
		t.Fatal("fresh terminal should be on primary screen")
	}
	term.WriteVT([]byte("\x1b[?1049h"))
	if !term.IsAlternateScreenActive() {
		t.Fatal("should be on alternate screen after DECSET 1049")
	}
	term.WriteVT([]byte("\x1b[?1049l"))
	if term.IsAlternateScreenActive() {
		t.Fatal("should be back on primary screen after DECRST 1049")
	}
}
