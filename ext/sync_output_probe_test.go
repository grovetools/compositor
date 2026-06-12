package ext_test

import (
	"testing"
	"unsafe"

	ext "github.com/grovetools/compositor/ext"
	"github.com/grovetools/compositor/ghostty"
)

// The grid always parses incoming bytes; the contract is that the BLIT
// must not sample a terminal mid synchronized-output frame (DEC 2026).
func TestSyncOutputGatesBlit(t *testing.T) {
	term, err := ghostty.New(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	ptr := unsafe.Pointer(term.UnsafePointer())

	if ghostty.SyncActiveByPointer(ptr) {
		t.Fatal("sync should be inactive initially")
	}
	term.WriteVT([]byte("\x1b[?2026h\x1b[1;1HPARTIAL"))
	if !ghostty.SyncActiveByPointer(ptr) {
		t.Error("BSU not detected — sync should be active")
	}
	term.WriteVT([]byte("more content\x1b[?2026l"))
	if ghostty.SyncActiveByPointer(ptr) {
		t.Error("ESU not detected — sync should have ended")
	}

	// Split sequence across two writes (chunk boundary).
	term.WriteVT([]byte("\x1b[?20"))
	term.WriteVT([]byte("26h"))
	if !ghostty.SyncActiveByPointer(ptr) {
		t.Error("chunk-split BSU not detected")
	}
	term.WriteVT([]byte("\x1b[?2026l"))

	c := ext.New(80, 24, 4)
	defer c.Free()
	// Smoke: blit while sync active must be a no-op (no crash, returns fast).
	term.WriteVT([]byte("\x1b[?2026h"))
	c.BlitGhostty(ptr, 0, 0, 80, 24)
	term.WriteVT([]byte("\x1b[?2026l"))
	c.BlitGhostty(ptr, 0, 0, 80, 24)
}
