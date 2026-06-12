// compositor.h — C API for terminal-specific compositor extensions.
//
// The base compositor API (new, free, resize, blit_ansi, flush, set_cursor,
// get_stats, pointer) comes from github.com/grovetools/compositor.
// This header covers the ghostty, diff, and input extensions.

#ifndef GROVE_COMPOSITOR_EXT_H
#define GROVE_COMPOSITOR_EXT_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

// Back buffer clear — wipe all cells to default spaces.
void ext_clear(void* c_ptr);

// Extension lifecycle (manages global state for ghostty cache, diff, input).
void ext_init(void);
void ext_free(void);

// Ghostty integration — c_ptr is the raw compositor pointer from Pointer().
void ext_blit_ghostty(void* c_ptr, void* term, int pane_x, int pane_y, int pane_w, int pane_h);
void ext_unregister_terminal(void* term);

// Selection overlay — highlights a range of cells with reverse video during blit.
void ext_set_selection(void* term, bool active, int sc, int sr, int ec, int er);

// Search highlights — highlight multiple disjoint ranges with reverse video.
typedef struct {
    int start_x;
    int start_y;
    int end_x;
    int end_y;
} CompositorHighlight;

void ext_set_search_highlights(void* term, const CompositorHighlight* matches, size_t count);

// Cursor reading from ghostty render state.
typedef struct {
    uint16_t x;
    uint16_t y;
    int32_t style;
    bool visible;
} CompositorCursor;

void ext_get_cursor(void* term, CompositorCursor* out);

// Delta protocol.
void ext_get_dirty_payload(void* c_ptr, const uint8_t** out_ptr, size_t* out_len);
void ext_apply_diff(void* c_ptr, const uint8_t* payload, size_t len);
void ext_request_full_sync(void* c_ptr);

// Extension stats.
typedef struct {
    uint64_t blit_ghostty_time_us;
} ExtStats;

const ExtStats* ext_get_stats(void);

// Input thread — callback type for forwarding bytes to Go.
typedef void (*CompositorInputCb)(const uint8_t* data, size_t len);

void ext_start_input_thread(CompositorInputCb cb);
void ext_stop_input_thread(void);
void ext_set_active_pty(int fd);
void ext_set_passthrough(bool passthrough);
void ext_set_leader_sequence(const uint8_t* bytes, size_t len);
void ext_add_intercept_sequence(const uint8_t* bytes, size_t len);

// Verification compare — codepoint-compare the given terminal's grid (the
// verification VT fed our own output stream) against the flush front buffer.
// Returns 0 on full match, 1 on mismatch with the first differing cell in
// the out params. The caller must guarantee the terminal is not being
// written concurrently (the verification VT is only touched under the
// host's TTY lock).
int ext_compare_front(void* c_ptr, void* term, int* out_row, int* out_col,
                      uint32_t* out_front_cp, uint32_t* out_term_cp);

// State dump for debugging — writes text representations of buffers and grids to dir_path.
// Called when GROVE_TTY_AUDIT is active and trigger file is detected.
// Returns 0 on success, nonzero on error.
int ext_dump_state(void* c_ptr, const char* dir_path);

// Dump all ghostty grids registered with the compositor extension.
// Called from Go side to serialize terminal grid state into the same dump dir.
void ext_dump_all_grids(const char* dir_path);

#ifdef __cplusplus
}
#endif

#endif // GROVE_COMPOSITOR_EXT_H
