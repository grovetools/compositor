package ghostty

import (
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"time"
)

// ============================================================================
// Pane-input recording (GROVE_PTY_CAPTURE)
// ============================================================================
//
// Input-side mirror of the ext package's GROVE_TTY_CAPTURE: an in-memory
// ring per terminal of the exact byte stream fed to WriteVT (i.e. what the
// pane's PTY delivered and the grid parsed), plus resize records. Dumped as
// ptyin-<ptr>.bin alongside grid-<ptr>.txt in state dumps.
//
// This closes the last gap in the rendering-evidence chain: when a pane's
// grid contains corrupted rows, replaying its ptyin capture through a fresh
// headless VT (`treemux replay ptyin-*.bin`) shows whether the application's
// own byte stream produces that grid — separating "the app emitted this"
// from "our ingestion corrupted it".
//
// File format is identical to tty-capture.bin (the replay CLI reads both):
//   magic "GTRC1", then records: [8B unix-nanos LE][1B kind][4B len LE][payload]
//   kind 0 = bytes; kind 1 = resize, payload [4B cols LE][4B rows LE].
//
// Value "1" uses the default per-terminal cap; an integer value sets the
// cap in MB. Rings hang off the Terminal and are guarded by t.mu, which
// WriteVT and Resize already hold.

const inputCaptureDefaultCap = 16 << 20

const (
	inputRecKindBytes  = 0
	inputRecKindResize = 1
)

var inputCaptureMagic = []byte("GTRC1")

// inputCaptureCapFromEnv returns the configured ring cap in bytes, or 0
// when input capture is disabled.
func inputCaptureCapFromEnv() int {
	v := os.Getenv("GROVE_PTY_CAPTURE")
	if v == "" {
		return 0
	}
	if mb, err := strconv.Atoi(v); err == nil && mb > 1 {
		return mb << 20
	}
	return inputCaptureDefaultCap
}

// appendInputLocked encodes one record into the terminal's input ring,
// evicting oldest records to stay under the cap. Caller holds t.mu.
func (t *Terminal) appendInputLocked(kind byte, payload []byte) {
	rec := make([]byte, 13+len(payload))
	binary.LittleEndian.PutUint64(rec[0:8], uint64(time.Now().UnixNano()))
	rec[8] = kind
	binary.LittleEndian.PutUint32(rec[9:13], uint32(len(payload)))
	copy(rec[13:], payload)
	t.inRecs = append(t.inRecs, rec)
	t.inBytes += len(rec)
	for t.inBytes > t.inCap && len(t.inRecs) > 1 {
		t.inBytes -= len(t.inRecs[0])
		t.inRecs = t.inRecs[1:]
	}
}

func inputResizePayload(cols, rows int) []byte {
	p := make([]byte, 8)
	binary.LittleEndian.PutUint32(p[0:4], uint32(cols))
	binary.LittleEndian.PutUint32(p[4:8], uint32(rows))
	return p
}

// dumpInputCapture writes the terminal's input ring to
// dir/ptyin-<ptr>.bin. No-op when input capture is disabled or empty.
func (t *Terminal) dumpInputCapture(dir string, ptr uintptr) {
	t.mu.Lock()
	recs := t.inRecs
	t.mu.Unlock()
	if len(recs) == 0 {
		return
	}
	f, err := os.Create(fmt.Sprintf("%s/ptyin-%016x.bin", dir, ptr))
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Write(inputCaptureMagic); err != nil {
		return
	}
	for _, rec := range recs {
		if _, err := f.Write(rec); err != nil {
			return
		}
	}
}
