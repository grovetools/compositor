package ghostty

import (
	"encoding/binary"
	"fmt"
	"os"
	"testing"
)

// Round-trip: bytes recorded by GROVE_PTY_CAPTURE, dumped and replayed into
// a fresh terminal, must reproduce the original grid exactly. This is the
// contract the mangling investigation relies on — the ptyin capture is a
// faithful record of everything the grid parsed.
func TestInputCaptureRoundTrip(t *testing.T) {
	t.Setenv("GROVE_PTY_CAPTURE", "1")

	term, err := New(40, 10)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer term.Close()

	term.WriteVT([]byte("hello \x1b[1mworld\x1b[0m"))
	term.Resize(30, 8)
	term.WriteVT([]byte("\r\nsecond line \x1b[5Cjumped"))

	dir := t.TempDir()
	term.dumpInputCapture(dir, 0xabc)

	data, err := os.ReadFile(fmt.Sprintf("%s/ptyin-%016x.bin", dir, uintptr(0xabc)))
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if string(data[:5]) != "GTRC1" {
		t.Fatalf("bad magic %q", data[:5])
	}

	// Replay records into a fresh terminal (same parse loop as treemux
	// replay): kind 1 resizes, kind 0 bytes.
	replay, err := New(40, 10)
	if err != nil {
		t.Fatalf("New replay: %v", err)
	}
	defer replay.Close()

	off := 5
	nRecs := 0
	for off < len(data) {
		if off+13 > len(data) {
			t.Fatalf("truncated record header at %d", off)
		}
		kind := data[off+8]
		plen := int(binary.LittleEndian.Uint32(data[off+9 : off+13]))
		payload := data[off+13 : off+13+plen]
		switch kind {
		case inputRecKindResize:
			cols := int(binary.LittleEndian.Uint32(payload[0:4]))
			rows := int(binary.LittleEndian.Uint32(payload[4:8]))
			replay.Resize(cols, rows)
		case inputRecKindBytes:
			replay.WriteVT(payload)
		default:
			t.Fatalf("unknown record kind %d", kind)
		}
		off += 13 + plen
		nRecs++
	}
	// initial-size resize + write + resize + write
	if nRecs != 4 {
		t.Fatalf("got %d records, want 4", nRecs)
	}

	if got, want := replay.FormatScreenPlain(), term.FormatScreenPlain(); got != want {
		t.Fatalf("replayed grid differs:\n--- replay ---\n%s\n--- original ---\n%s", got, want)
	}
}

// The ring must evict oldest records under cap pressure rather than grow.
func TestInputCaptureRingEvicts(t *testing.T) {
	term := &Terminal{inCap: 64}
	for i := 0; i < 100; i++ {
		term.appendInputLocked(inputRecKindBytes, []byte("0123456789"))
	}
	if term.inBytes > 64+23 { // one record may straddle the cap
		t.Fatalf("ring grew to %d bytes, cap 64", term.inBytes)
	}
	if len(term.inRecs) == 0 {
		t.Fatal("ring empty after eviction")
	}
}
