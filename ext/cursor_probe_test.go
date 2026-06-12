package ext_test

import (
	"testing"
	"unsafe"

	ext "github.com/grovetools/compositor/ext"
	"github.com/grovetools/compositor/ghostty"
)

func TestCursorVisibilityTracksDECTCEM(t *testing.T) {
	term, err := ghostty.New(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	c := ext.New(80, 24, 4)
	defer c.Free()
	ptr := unsafe.Pointer(term.UnsafePointer())

	term.WriteVT([]byte("hello\x1b[5;10H")) // position cursor
	c.BlitGhostty(ptr, 0, 0, 80, 24)        // updates render state
	ci := c.GetCursor(ptr)
	t.Logf("shown: visible=%v x=%d y=%d style=%d", ci.Visible, ci.X, ci.Y, ci.Style)
	if !ci.Visible {
		t.Errorf("cursor should be visible by default")
	}

	term.WriteVT([]byte("\x1b[?25l")) // DECTCEM hide
	c.BlitGhostty(ptr, 0, 0, 80, 24)
	ci = c.GetCursor(ptr)
	t.Logf("after ?25l: visible=%v x=%d y=%d", ci.Visible, ci.X, ci.Y)
	if ci.Visible {
		t.Errorf("BUG CONFIRMED: cursor reports visible after DECTCEM hide")
	}
}
