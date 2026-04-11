// compositor.h — C API contract for the Zig compositor.
//
// This header defines the FFI boundary between Go (via CGo) and the
// Zig rendering engine. All functions use C ABI calling conventions.

#ifndef GROVE_COMPOSITOR_H
#define GROVE_COMPOSITOR_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct Compositor Compositor;

// Log levels (passed to compositor_new).
#define COMPOSITOR_LOG_TRACE 0
#define COMPOSITOR_LOG_DEBUG 1
#define COMPOSITOR_LOG_INFO  2
#define COMPOSITOR_LOG_WARN  3
#define COMPOSITOR_LOG_ERROR 4

// Lifecycle
Compositor* compositor_new(int width, int height, int log_level);
void compositor_free(Compositor* c);
void compositor_resize(Compositor* c, int width, int height);

// Content
void compositor_blit_ansi(Compositor* c, int x, int y, int w, int h,
                          const char* str, size_t len);

// Cursor
void compositor_set_cursor(Compositor* c, int x, int y, int style, bool visible);

// Output
void compositor_flush(Compositor* c, int fd);

// Stats
typedef struct {
    uint64_t frames_rendered;
    uint64_t dirty_cells_flushed;
    uint64_t bytes_written;
    uint64_t blit_ansi_time_us;
    uint64_t flush_time_us;
} CompositorStats;

const CompositorStats* compositor_get_stats(Compositor* c);

// Extension support — returns a raw pointer to the Compositor struct
// for use by terminal-specific extensions (ghostty blit, input, diff).
void* compositor_pointer(Compositor* c);

#ifdef __cplusplus
}
#endif

#endif // GROVE_COMPOSITOR_H
