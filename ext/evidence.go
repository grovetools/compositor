package ext

import (
	"encoding/binary"
	"os"
	"strconv"
	"sync"
	"time"

	comp "github.com/grovetools/compositor"
	"github.com/grovetools/compositor/ghostty"
	grovelogging "github.com/grovetools/core/logging"
)

// ============================================================================
// Rendering-evidence instrumentation (GROVE_TTY_CAPTURE / GROVE_TTY_SHADOW)
// ============================================================================
//
// The terminal is write-only: front-buffer vs glass divergence cannot be
// observed directly, which is why the mangling investigation kept relying on
// inference. These two instruments close the loop:
//
//   GROVE_TTY_CAPTURE — an in-memory ring of the exact byte stream written
//     to the host terminal (compositor flush frames via groveCompositorRecord
//     + host-app writes via SerializedWriter, both under ttyMu so the ring
//     preserves true write order). Dumped as tty-capture.bin alongside state
//     dumps; `treemux replay` turns it into a deterministic repro. Value "1"
//     uses the default 32MB cap; an integer value sets the cap in MB.
//
//   GROVE_TTY_SHADOW — a verification VT: a second libghostty-vt instance
//     fed the same byte stream. A conformant emulator's screen must equal
//     the flush front buffer, so shadow-grid != front is a real front-vs-
//     glass divergence the moment it happens, regardless of cause (diff
//     generation bug, dropped bytes, interfering writer). Compared every
//     verifyInterval flushes; the first mismatch is logged with coordinates
//     and triggers an automatic state dump WITHOUT healing.
//
// Capture file format (tty-capture.bin):
//   magic "GTRC1", then records: [8B unix-nanos LE][1B kind][4B len LE][payload]
//   kind 0 = output bytes; kind 1 = resize, payload [4B cols LE][4B rows LE].

const (
	captureDefaultCap = 32 << 20
	verifyInterval    = 60 // flushes between verification compares (~1s at 60Hz)

	recKindBytes  = 0
	recKindResize = 1
)

var captureMagic = []byte("GTRC1")

var (
	// evidenceMu is a leaf lock guarding the ring and verification state.
	// All writers already serialize on ttyMu; this exists so dump readers
	// and Resize stay safe without widening ttyMu.
	evidenceMu     sync.Mutex
	captureCap     int
	captureRecs    [][]byte
	captureBytes   int
	verifyTerm     *ghostty.Terminal
	verifyCount    int
	verifyDiverged bool

	evidenceLog = grovelogging.NewUnifiedLogger("treemux.compositor.evidence")
)

// initEvidence configures the instruments from the environment. Called from
// ext.New with the screen dimensions; safe to call again (tests, re-init) —
// it resets the ring and replaces the verification VT.
func initEvidence(width, height int) {
	evidenceMu.Lock()
	captureRecs = nil
	captureBytes = 0
	captureCap = 0
	verifyCount = 0
	verifyDiverged = false
	oldTerm := verifyTerm
	verifyTerm = nil

	if v := os.Getenv("GROVE_TTY_CAPTURE"); v != "" {
		captureCap = captureDefaultCap
		if mb, err := strconv.Atoi(v); err == nil && mb > 1 {
			captureCap = mb << 20
		}
		appendCaptureLocked(recKindResize, resizePayload(width, height))
	}
	captureEnabled := captureCap > 0
	evidenceMu.Unlock()

	if oldTerm != nil {
		oldTerm.Close()
	}

	shadowEnabled := false
	if os.Getenv("GROVE_TTY_SHADOW") != "" {
		t, err := ghostty.New(width, height)
		if err != nil {
			evidenceLog.Error("verification VT init failed: " + err.Error()).StructuredOnly().Emit()
		} else {
			evidenceMu.Lock()
			verifyTerm = t
			evidenceMu.Unlock()
			shadowEnabled = true
		}
	}

	if captureEnabled || shadowEnabled {
		comp.SetRecordFunc(recordOutput)
	} else {
		comp.SetRecordFunc(nil)
	}
}

// recordOutput receives every buffer written to the host tty: compositor
// flush frames (groveCompositorRecord → SetRecordFunc) and host-app writes
// (serializedTTY.Write calls it directly). Both call sites hold ttyMu, so
// ring order is true fd-write order and verification-VT feeding needs no
// extra synchronization against compares (also under ttyMu).
func recordOutput(p []byte) {
	if len(p) == 0 {
		return
	}
	evidenceMu.Lock()
	if captureCap > 0 {
		appendCaptureLocked(recKindBytes, p)
	}
	vt := verifyTerm
	evidenceMu.Unlock()
	if vt != nil {
		vt.WriteVT(p)
	}
}

// evidenceResize records a dimension change and resizes the verification VT.
func evidenceResize(width, height int) {
	evidenceMu.Lock()
	if captureCap > 0 {
		appendCaptureLocked(recKindResize, resizePayload(width, height))
	}
	vt := verifyTerm
	evidenceMu.Unlock()
	if vt != nil {
		vt.Resize(width, height)
	}
}

func resizePayload(width, height int) []byte {
	p := make([]byte, 8)
	binary.LittleEndian.PutUint32(p[0:4], uint32(width))
	binary.LittleEndian.PutUint32(p[4:8], uint32(height))
	return p
}

// appendCaptureLocked encodes one record into the ring, evicting oldest
// records to stay under captureCap. Caller holds evidenceMu.
func appendCaptureLocked(kind byte, payload []byte) {
	rec := make([]byte, 13+len(payload))
	binary.LittleEndian.PutUint64(rec[0:8], uint64(time.Now().UnixNano()))
	rec[8] = kind
	binary.LittleEndian.PutUint32(rec[9:13], uint32(len(payload)))
	copy(rec[13:], payload)
	captureRecs = append(captureRecs, rec)
	captureBytes += len(rec)
	for captureBytes > captureCap && len(captureRecs) > 1 {
		captureBytes -= len(captureRecs[0])
		captureRecs = captureRecs[1:]
	}
}

// dumpCapture writes the ring to dir/tty-capture.bin. No-op when capture is
// disabled or empty.
func dumpCapture(dir string) {
	evidenceMu.Lock()
	recs := captureRecs
	evidenceMu.Unlock()
	if len(recs) == 0 {
		return
	}
	f, err := os.Create(dir + "/tty-capture.bin")
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Write(captureMagic); err != nil {
		return
	}
	for _, rec := range recs {
		if _, err := f.Write(rec); err != nil {
			return
		}
	}
}

// verifyAfterFlush compares the verification VT against the front buffer
// every verifyInterval flushes. Called from Flush under ttyMu, after the
// base flush completed (front is post-diff, i.e. it models the glass).
// On the first mismatch of a divergence streak: log coordinates + cells and
// snapshot all state (dump dir includes the capture ring and both grids) —
// WITHOUT healing, so the live divergence stays observable.
func (c *Compositor) verifyAfterFlush() {
	evidenceMu.Lock()
	vt := verifyTerm
	evidenceMu.Unlock()
	if vt == nil {
		return
	}
	verifyCount++
	if verifyCount < verifyInterval {
		return
	}
	verifyCount = 0

	row, col, frontCp, termCp, mismatch := c.compareFront(vt.UnsafePointer())
	if !mismatch {
		verifyDiverged = false // re-arm after recovery (e.g. self-heal repaint)
		return
	}
	if verifyDiverged {
		return // already reported this streak
	}
	verifyDiverged = true
	evidenceLog.Error("verify: divergence front vs verification VT").
		Field("row", row).
		Field("col", col).
		Field("front_codepoint", frontCp).
		Field("vt_codepoint", termCp).
		StructuredOnly().Emit()
	c.dumpState()
}
