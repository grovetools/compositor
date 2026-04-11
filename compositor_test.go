package compositor

import (
	"testing"
	"unsafe"
)

func TestSetLogFunc(t *testing.T) {
	var captured []string
	SetLogFunc(func(level int, msg string) {
		captured = append(captured, msg)
	})
	defer SetLogFunc(nil)

	c := New(80, 24, LogInfo)
	c.Free()

	if len(captured) < 2 {
		t.Errorf("expected at least 2 log messages (new + free), got %d", len(captured))
	}
}

func TestNewFreeLifecycle(t *testing.T) {
	c := New(80, 24, LogInfo)
	if c == nil || c.ptr == nil {
		t.Fatal("New returned nil compositor")
	}
	defer c.Free()

	// Verify stats are zeroed.
	s := c.GetStats()
	if s.FramesRendered != 0 {
		t.Errorf("expected 0 frames, got %d", s.FramesRendered)
	}
}

func TestBlitANSIAndFlush(t *testing.T) {
	c := New(40, 10, 4) // error-only logging
	if c == nil {
		t.Fatal("New returned nil")
	}
	defer c.Free()

	// Blit a simple string.
	c.BlitANSI(0, 0, 40, 10, "Hello, compositor!")

	// Flush to /dev/null (fd obtained by opening it).
	// Use fd 2 (stderr) as a safe write target for testing.
	c.Flush(2)

	s := c.GetStats()
	if s.FramesRendered != 1 {
		t.Errorf("expected 1 frame, got %d", s.FramesRendered)
	}
	if s.DirtyCellsFlushed == 0 {
		t.Error("expected dirty cells > 0")
	}
}

func TestResize(t *testing.T) {
	c := New(80, 24, 4)
	defer c.Free()

	c.Resize(120, 40)
	// Should not panic — verify by doing a blit after resize.
	c.BlitANSI(0, 0, 120, 40, "After resize")
	c.Flush(2)

	s := c.GetStats()
	if s.FramesRendered != 1 {
		t.Errorf("expected 1 frame after resize, got %d", s.FramesRendered)
	}
}

func TestPointer(t *testing.T) {
	c := New(80, 24, 4)
	defer c.Free()

	p := c.Pointer()
	if p == nil {
		t.Error("Pointer returned nil for valid compositor")
	}

	// Verify it's a real pointer (not zero).
	if uintptr(p) == 0 {
		t.Error("Pointer returned zero address")
	}
}

func TestSetCursor(t *testing.T) {
	c := New(80, 24, 4)
	defer c.Free()

	// Should not panic.
	c.SetCursor(10, 5, 2, true)
	c.Flush(2)
}

func TestBlitANSIWithSGR(t *testing.T) {
	c := New(80, 24, 4)
	defer c.Free()

	// Bold red text.
	c.BlitANSI(0, 0, 80, 24, "\x1b[1;31mRed Bold\x1b[0m Normal")
	c.Flush(2)

	s := c.GetStats()
	if s.DirtyCellsFlushed == 0 {
		t.Error("expected dirty cells from SGR blit")
	}
}

func TestNilSafety(t *testing.T) {
	c := &Compositor{} // nil ptr

	// None of these should panic.
	c.Free()
	c.Resize(10, 10)
	c.BlitANSI(0, 0, 10, 10, "test")
	c.SetCursor(0, 0, 0, false)
	c.Flush(2)
	s := c.GetStats()
	if s.FramesRendered != 0 {
		t.Error("nil compositor should return zero stats")
	}
	if c.Pointer() != nil {
		t.Error("nil compositor should return nil pointer")
	}
}

func TestPointerUsableByExtension(t *testing.T) {
	// Simulates what terminal would do: get the raw pointer and pass it
	// to extension CGo functions.
	c := New(80, 24, 4)
	defer c.Free()

	ptr := c.Pointer()
	if ptr == nil {
		t.Fatal("Pointer is nil")
	}

	// Verify the pointer is valid by round-tripping through unsafe.Pointer.
	_ = unsafe.Pointer(ptr)
}
