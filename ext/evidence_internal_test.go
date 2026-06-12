package ext

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// devNull opens os.DevNull for writing.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestVerificationVTMatchesAfterFlush feeds the verification VT exactly what
// flush writes and asserts grid==front, then injects a byte stream the
// compositor never flushed and asserts the divergence is detected with the
// right coordinates.
func TestVerificationVTMatchesAfterFlush(t *testing.T) {
	t.Setenv("GROVE_TTY_SHADOW", "1")
	t.Setenv("GROVE_TTY_CAPTURE", "")

	c := New(20, 5, 4)
	defer c.Free()
	if verifyTerm == nil {
		t.Fatal("verification VT not initialized despite GROVE_TTY_SHADOW=1")
	}

	c.BlitANSI(0, 0, 20, 5, "HELLO")
	c.Flush(int(devNull(t).Fd())) // record func feeds the verification VT

	if _, _, _, _, mismatch := c.compareFront(verifyTerm.UnsafePointer()); mismatch {
		t.Fatalf("front and verification VT diverged after a clean flush")
	}

	// Divergence injection: bytes reach the VT but were never flushed by the
	// compositor (models dropped/foreign writes: front no longer equals what
	// a conformant emulator displays).
	recordOutput([]byte("\x1b[2;3HXYZ"))
	row, col, frontCp, termCp, mismatch := c.compareFront(verifyTerm.UnsafePointer())
	if !mismatch {
		t.Fatal("expected divergence after injecting unflushed bytes into the verification VT")
	}
	if row != 1 || col != 2 {
		t.Errorf("expected first mismatch at row=1 col=2, got row=%d col=%d", row, col)
	}
	if termCp != 'X' {
		t.Errorf("expected VT codepoint 'X' at mismatch, got %q", rune(termCp))
	}
	_ = frontCp
}

// TestCaptureRingRecordsFlushFrames asserts the capture ring stores the
// resize header record plus the exact frame bytes flush wrote, and that
// dumpCapture writes a parseable file.
func TestCaptureRingRecordsFlushFrames(t *testing.T) {
	t.Setenv("GROVE_TTY_CAPTURE", "1")
	t.Setenv("GROVE_TTY_SHADOW", "")
	t.Setenv("GROVE_COMPOSITOR_CLASSIC", "") // frames must carry 2026 brackets

	c := New(10, 3, 4)
	defer c.Free()

	c.BlitANSI(0, 0, 10, 3, "AB")
	c.Flush(int(devNull(t).Fd()))

	dir := t.TempDir()
	dumpCapture(dir)

	data, err := os.ReadFile(filepath.Join(dir, "tty-capture.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:5]) != "GTRC1" {
		t.Fatalf("bad magic: %q", data[:5])
	}

	// Parse records: expect resize(10,3) first, then ≥1 bytes record whose
	// payload contains the flushed frame (bracketed, containing 'A').
	off := 5
	var kinds []byte
	var firstResize []byte
	var output []byte
	for off < len(data) {
		if off+13 > len(data) {
			t.Fatalf("truncated record header at %d", off)
		}
		kind := data[off+8]
		plen := int(binary.LittleEndian.Uint32(data[off+9 : off+13]))
		payload := data[off+13 : off+13+plen]
		kinds = append(kinds, kind)
		if kind == recKindResize && firstResize == nil {
			firstResize = payload
		}
		if kind == recKindBytes {
			output = append(output, payload...)
		}
		off += 13 + plen
	}
	if len(kinds) == 0 || kinds[0] != recKindResize {
		t.Fatalf("expected first record to be resize, got kinds=%v", kinds)
	}
	if w := binary.LittleEndian.Uint32(firstResize[0:4]); w != 10 {
		t.Errorf("resize record width = %d, want 10", w)
	}
	if h := binary.LittleEndian.Uint32(firstResize[4:8]); h != 3 {
		t.Errorf("resize record height = %d, want 3", h)
	}
	if len(output) == 0 {
		t.Fatal("no output bytes recorded for a non-empty flush")
	}
	frame := string(output)
	if !strings.Contains(frame, "A") || !strings.Contains(frame, "\x1b[?2026h") || !strings.Contains(frame, "\x1b[?2026l") {
		t.Errorf("recorded frame missing content or 2026 brackets: %q", frame)
	}
}

// TestShortWriteInvalidatesFront fills a non-blocking pipe so the flush
// write cannot complete, and asserts the front buffer was invalidated: the
// next flush to a working fd re-emits the entire screen.
func TestShortWriteInvalidatesFront(t *testing.T) {
	c := New(8, 4, 4)
	defer c.Free()

	// Baseline: paint + flush everything once.
	c.BlitANSI(0, 0, 8, 4, "hi")
	c.Flush(int(devNull(t).Fd()))
	base := c.GetStats().DirtyCellsFlushed

	// Build a full non-blocking pipe. NOTE: os.File.Fd() switches the fd
	// back to blocking mode — grab it once, THEN set non-blocking, and use
	// the raw int from here on.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	wfd := int(w.Fd())
	if err := syscall.SetNonblock(wfd, true); err != nil {
		t.Fatal(err)
	}
	junk := make([]byte, 65536)
	for {
		if _, err := syscall.Write(wfd, junk); err != nil {
			break // EAGAIN: pipe full
		}
	}

	// Dirty one cell and flush into the full pipe: the checked write loop
	// retries EAGAIN with 1ms sleeps, then gives up and invalidates front.
	c.BlitANSI(0, 0, 8, 4, "yo")
	start := time.Now()
	c.Flush(wfd)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("flush blocked too long on full pipe: %v", elapsed)
	}

	// Recovery: flushing to a good fd must repaint the FULL screen (8*4
	// cells), proving front was invalidated rather than silently desynced.
	c.Flush(int(devNull(t).Fd()))
	delta := c.GetStats().DirtyCellsFlushed - base
	if delta < 8*4 {
		t.Errorf("expected full-screen repaint (>=32 dirty cells) after failed write, got %d", delta)
	}
}
