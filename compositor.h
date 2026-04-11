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

// Lifecycle
Compositor* compositor_new(int width, int height);
void compositor_free(Compositor* c);
void compositor_resize(Compositor* c, int width, int height);

// Content
void compositor_blit_ansi(Compositor* c, int x, int y, int w, int h,
                          const char* str, size_t len);

// Cursor
void compositor_set_cursor(Compositor* c, int x, int y, int style, bool visible);

// Output
void compositor_flush(Compositor* c, int fd);

#ifdef __cplusplus
}
#endif

#endif // GROVE_COMPOSITOR_H
