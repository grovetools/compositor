package ext_test

import (
	"testing"
	"time"
	"unsafe"

	ext "github.com/grovetools/compositor/ext"
	"github.com/grovetools/compositor/ghostty"
)

// decodeDiffCodepoints maps (x,y) -> codepoint for every record in a
// GetDirtyPayload diff (GRV1 format: 12-byte header, 16-byte records).
func decodeDiffCodepoints(t *testing.T, payload []byte) map[[2]int]rune {
	t.Helper()
	out := map[[2]int]rune{}
	if payload == nil {
		return out
	}
	if len(payload) < 12 || string(payload[:4]) != "GRV1" {
		t.Fatalf("bad diff header: %q", payload[:min(len(payload), 12)])
	}
	count := int(payload[8]) | int(payload[9])<<8 | int(payload[10])<<16 | int(payload[11])<<24
	off := 12
	for i := 0; i < count; i++ {
		rec := payload[off : off+16]
		x := int(rec[0]) | int(rec[1])<<8
		y := int(rec[2]) | int(rec[3])<<8
		cp := rune(uint32(rec[4]) | uint32(rec[5])<<8 | uint32(rec[6])<<16 | uint32(rec[7])<<24)
		out[[2]int{x, y}] = cp
		off += 16
	}
	return out
}

// TestDeferredBlitAfterChromeClearBlanksPane reproduces the per-update pane
// flash: the tuimux tick does a full-screen BlitANSI (which pre-clears the
// entire back buffer, including pane interiors) and then re-fills pane
// interiors via BlitGhostty. Since the settle/sync gates (5a8dd6f / 0d8a381)
// can skip the BlitGhostty, the pane interior cells stay BLANK in the back
// buffer for that tick, and the subsequent Flush paints blanks over live pane
// content on the physical screen — content returns one settle-period later:
// a visible flash on every PTY write burst, independent of classic mode.
func TestDeferredBlitAfterChromeClearBlanksPane(t *testing.T) {
	term, err := ghostty.New(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	ptr := unsafe.Pointer(term.UnsafePointer())

	c := ext.New(80, 24, 4)
	defer c.Free()

	// The chrome string a real tick blits over the whole screen (mux.go
	// RenderLayout result): non-empty, paints away from cell (0,0). The
	// blit pre-clears its entire bounding box before parsing.
	chrome := "\n\n\n\n\n\n\n\n\nchrome"

	// Tick 1: pane has settled content; blit succeeds.
	term.WriteVT([]byte("\x1b[1;1HHELLO"))
	time.Sleep(10 * time.Millisecond) // settleQuiet elapsed -> blit runs
	c.BlitANSI(0, 0, 80, 24, chrome)  // chrome pass (clears whole back buffer)
	c.BlitGhostty(ptr, 0, 0, 80, 24)
	cells := decodeDiffCodepoints(t, c.GetDirtyPayload())
	if cells[[2]int{0, 0}] != 'H' {
		t.Fatalf("setup: expected 'H' at (0,0) after settled blit, got %q", cells[[2]int{0, 0}])
	}

	// Tick 2: PTY writes mid-burst; chrome pass clears the back buffer,
	// then the gated BlitGhostty defers (PTY not settled). The pane region
	// is now blank in the back buffer — exactly what Flush would write to
	// the terminal this tick.
	term.WriteVT([]byte(" WORLD"))   // lastWrite = now -> not settled
	c.BlitANSI(0, 0, 80, 24, chrome) // chrome pass clears pane interior
	c.BlitGhostty(ptr, 0, 0, 80, 24)
	cells = decodeDiffCodepoints(t, c.GetDirtyPayload())
	if cp, ok := cells[[2]int{0, 0}]; !ok || cp != ' ' {
		t.Errorf("expected pane cell (0,0) to be blanked while blit deferred (the flash); got %q present=%v", cp, ok)
	}

	// Tick 3 (one settle period later): blit runs again, content returns —
	// the other half of the flash.
	time.Sleep(10 * time.Millisecond)
	c.BlitANSI(0, 0, 80, 24, chrome)
	c.BlitGhostty(ptr, 0, 0, 80, 24)
	cells = decodeDiffCodepoints(t, c.GetDirtyPayload())
	if cells[[2]int{0, 0}] != 'H' {
		t.Errorf("expected content restored at (0,0) after settle, got %q", cells[[2]int{0, 0}])
	}
}
