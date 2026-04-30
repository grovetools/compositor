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

#ifdef __cplusplus
}
#endif

#endif // GROVE_COMPOSITOR_EXT_H
