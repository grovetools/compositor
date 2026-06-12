const std = @import("std");
pub const ansi = @import("ansi.zig");
pub const runewidth = @import("runewidth.zig");

// --- Logging ---

// Log levels matching compositor.h defines.
pub const LogLevel = enum(c_int) {
    trace = 0,
    debug = 1,
    info = 2,
    warn = 3,
    err = 4,
};

// Callback into Go's groveCompositorLog export.
extern fn groveCompositorLog(level: c_int, msg: [*]const u8, length: usize) void;

pub fn compositorLog(min_level: LogLevel, level: LogLevel, comptime fmt_str: []const u8, args: anytype) void {
    if (@intFromEnum(level) < @intFromEnum(min_level)) return;
    var buf: [512]u8 = undefined;
    const msg = std.fmt.bufPrint(&buf, fmt_str, args) catch &buf;
    groveCompositorLog(@intFromEnum(level), msg.ptr, msg.len);
}

// --- Stats ---

const CompositorStats = extern struct {
    frames_rendered: u64 = 0,
    dirty_cells_flushed: u64 = 0,
    bytes_written: u64 = 0,
    blit_ansi_time_us: u64 = 0,
    flush_time_us: u64 = 0,
};

// --- Cell buffer ---

pub const Cell = struct {
    codepoint: u32 = ' ',
    fg: ansi.Color = .{},
    bg: ansi.Color = .{},
    bold: bool = false,
    italic: bool = false,
    underline: bool = false,
    faint: bool = false,
    inverse: bool = false,
    strikethrough: bool = false,
    wide: bool = false,
    wide_spacer: bool = false,
};

pub const Compositor = struct {
    width: usize,
    height: usize,
    front: []Cell,
    back: []Cell,
    allocator: std.mem.Allocator,
    stats: CompositorStats = .{},
    log_level: LogLevel = .info,
    cursor_x: i32 = 0,
    cursor_y: i32 = 0,
    cursor_style: i32 = 0,
    cursor_visible: bool = false,
    // Last cursor state actually emitted by flush, so idle ticks with an
    // unchanged cursor write nothing at all (previously the unconditional
    // cursor block made every flush a non-empty write — with synchronized
    // output bracketing, that forced the host terminal to commit a frame
    // per tick and caused visible flashing while typing).
    emitted_cursor_x: i32 = -1,
    emitted_cursor_y: i32 = -1,
    emitted_cursor_style: i32 = -1,
    emitted_cursor_visible: bool = false,
    emitted_cursor_valid: bool = false,
    // Self-heal cadence: every heal_interval flushes the front buffer is
    // poisoned, forcing a complete rewrite of the physical screen. The
    // terminal is write-only — we can never VERIFY the glass matches the
    // front buffer — so any silent divergence (emulator bugs, lost bytes)
    // would otherwise persist until a manual full repaint. With flush
    // frames bracketed in DEC 2026 the rewrite is visually atomic, making
    // this invisible insurance. ~600 flushes ≈ 10s at the 60Hz tick.
    flushes_since_heal: u32 = 0,
    // Classic mode: disable outbound 2026 bracketing and periodic
    // self-heal (set from Go via compositor_set_classic when
    // GROVE_COMPOSITOR_CLASSIC=1) — escape hatch to yesterday's
    // rendering behavior.
    classic: bool = false,
};

const heal_interval: u32 = 600;

// --- Lifecycle exports ---

export fn compositor_new(width: c_int, height: c_int, log_level: c_int) ?*Compositor {
    const alloc = std.heap.c_allocator;
    const c = alloc.create(Compositor) catch return null;

    const w: usize = @intCast(width);
    const h: usize = @intCast(height);
    const size = w * h;

    const front = alloc.alloc(Cell, size) catch {
        alloc.destroy(c);
        return null;
    };
    const back = alloc.alloc(Cell, size) catch {
        alloc.free(front);
        alloc.destroy(c);
        return null;
    };

    const lvl: LogLevel = if (log_level >= 0 and log_level <= 4)
        @enumFromInt(log_level)
    else
        .info;

    c.* = .{
        .width = w,
        .height = h,
        .front = front,
        .back = back,
        .allocator = alloc,
        .log_level = lvl,
    };

    @memset(c.front, Cell{ .codepoint = 0xFFFFFFFF });
    @memset(c.back, Cell{});

    compositorLog(lvl, .info, "compositor_new: width={d} height={d} log_level={d}", .{ w, h, log_level });

    return c;
}

export fn compositor_free(c: *Compositor) void {
    compositorLog(c.log_level, .info, "compositor_free: frames={d} cells_flushed={d} bytes={d}", .{
        c.stats.frames_rendered, c.stats.dirty_cells_flushed, c.stats.bytes_written,
    });

    c.allocator.free(c.front);
    c.allocator.free(c.back);
    c.allocator.destroy(c);
}

export fn compositor_resize(c: *Compositor, width: c_int, height: c_int) void {
    const w: usize = @intCast(width);
    const h: usize = @intCast(height);
    if (w == c.width and h == c.height) {
        // Force a full redraw when receiving a resize event with identical dimensions.
        // Crucial when recovering from external editors/ephemeral panes.
        @memset(c.front, Cell{ .codepoint = 0xFFFFFFFF });
        return;
    }

    compositorLog(c.log_level, .info, "compositor_resize: {d}x{d} -> {d}x{d}", .{ c.width, c.height, w, h });

    c.allocator.free(c.front);
    c.allocator.free(c.back);
    c.width = w;
    c.height = h;
    const size = w * h;
    c.front = c.allocator.alloc(Cell, size) catch @panic("OOM");
    c.back = c.allocator.alloc(Cell, size) catch @panic("OOM");
    // Force full redraw on resize by making front != back.
    @memset(c.front, Cell{ .codepoint = 0xFFFFFFFF });
    @memset(c.back, Cell{});
}

// --- Cursor ---

export fn compositor_set_classic(c: *Compositor, classic: bool) void {
    c.classic = classic;
}

export fn compositor_set_cursor(c: *Compositor, x: c_int, y: c_int, style: c_int, visible: bool) void {
    c.cursor_x = @intCast(x);
    c.cursor_y = @intCast(y);
    c.cursor_style = @intCast(style);
    c.cursor_visible = visible;
}

// --- Flush with front/back diff ---

/// Emit the appropriate SGR foreground sequence for the given color.
fn emitFgColor(writer: anytype, color: ansi.Color, sgr_needed: bool) bool {
    switch (color.color_type) {
        .default => {
            writer.writeAll(if (sgr_needed) ";39" else "\x1b[39") catch @panic("OOM");
        },
        .standard => {
            const code: u16 = if (color.r < 8) @as(u16, color.r) + 30 else @as(u16, color.r) - 8 + 90;
            if (sgr_needed) {
                writer.print(";{d}", .{code}) catch @panic("OOM");
            } else {
                writer.print("\x1b[{d}", .{code}) catch @panic("OOM");
            }
        },
        .palette256 => {
            if (sgr_needed) {
                writer.print(";38;5;{d}", .{color.r}) catch @panic("OOM");
            } else {
                writer.print("\x1b[38;5;{d}", .{color.r}) catch @panic("OOM");
            }
        },
        .rgb => {
            if (sgr_needed) {
                writer.print(";38;2;{d};{d};{d}", .{ color.r, color.g, color.b }) catch @panic("OOM");
            } else {
                writer.print("\x1b[38;2;{d};{d};{d}", .{ color.r, color.g, color.b }) catch @panic("OOM");
            }
        },
    }
    return true;
}

/// Emit the appropriate SGR background sequence for the given color.
fn emitBgColor(writer: anytype, color: ansi.Color, sgr_needed: bool) bool {
    switch (color.color_type) {
        .default => {
            writer.writeAll(if (sgr_needed) ";49" else "\x1b[49") catch @panic("OOM");
        },
        .standard => {
            const code: u16 = if (color.r < 8) @as(u16, color.r) + 40 else @as(u16, color.r) - 8 + 100;
            if (sgr_needed) {
                writer.print(";{d}", .{code}) catch @panic("OOM");
            } else {
                writer.print("\x1b[{d}", .{code}) catch @panic("OOM");
            }
        },
        .palette256 => {
            if (sgr_needed) {
                writer.print(";48;5;{d}", .{color.r}) catch @panic("OOM");
            } else {
                writer.print("\x1b[48;5;{d}", .{color.r}) catch @panic("OOM");
            }
        },
        .rgb => {
            if (sgr_needed) {
                writer.print(";48;2;{d};{d};{d}", .{ color.r, color.g, color.b }) catch @panic("OOM");
            } else {
                writer.print("\x1b[48;2;{d};{d};{d}", .{ color.r, color.g, color.b }) catch @panic("OOM");
            }
        },
    }
    return true;
}

export fn compositor_flush(c: *Compositor, fd: c_int) void {
    const start_ns = std.time.nanoTimestamp();

    // Periodic self-heal: poison the front buffer so this flush rewrites
    // the entire screen (see flushes_since_heal).
    c.flushes_since_heal += 1;
    if (!c.classic and c.flushes_since_heal >= heal_interval) {
        c.flushes_since_heal = 0;
        @memset(c.front, Cell{ .codepoint = 0xFFFFFFFF });
        c.emitted_cursor_valid = false;
    }

    const alloc = c.allocator;
    var out: std.ArrayList(u8) = .{};
    defer out.deinit(alloc);

    var dirty_count: u64 = 0;

    // Active SGR state tracking — start at terminal defaults.
    var active = Cell{};
    // Cursor position tracking to skip redundant CUP sequences.
    var current_y: usize = 9999;
    var current_x: usize = 9999;
    // Emit a single SGR reset at the start of flush (not per-cell).
    var sgr_emitted = false;

    for (0..c.height) |y| {
        for (0..c.width) |x| {
            const idx = y * c.width + x;
            const back = c.back[idx];

            // Skip wide spacer cells — the right half of a wide character
            // was already rendered by the terminal when we wrote the left half.
            if (back.wide_spacer) {
                c.front[idx] = back;
                continue;
            }

            // Skip unchanged cells.
            if (cellEqual(c.front[idx], back)) continue;
            dirty_count += 1;

            const writer = out.writer(alloc);

            // Emit SGR reset once at the start of flush.
            if (!sgr_emitted) {
                writer.writeAll("\x1b[0m") catch @panic("OOM");
                active = Cell{};
                sgr_emitted = true;
            }

            // Only emit CUP if the cursor isn't already at the target position.
            if (y != current_y or x != current_x) {
                writer.print("\x1b[{d};{d}H", .{ y + 1, x + 1 }) catch @panic("OOM");
            }

            // Determine if any attribute needs to be turned OFF.
            const needs_reset = (active.bold and !back.bold) or
                (active.faint and !back.faint) or
                (active.italic and !back.italic) or
                (active.underline and !back.underline) or
                (active.inverse and !back.inverse) or
                (active.strikethrough and !back.strikethrough);

            if (needs_reset) {
                writer.writeAll("\x1b[0m") catch @panic("OOM");
                active = Cell{};
            }

            // Build SGR delta sequence.
            var sgr_needed = false;

            if (back.bold and !active.bold) {
                writer.writeAll(if (sgr_needed) ";1" else "\x1b[1") catch @panic("OOM");
                sgr_needed = true;
            }
            if (back.faint and !active.faint) {
                writer.writeAll(if (sgr_needed) ";2" else "\x1b[2") catch @panic("OOM");
                sgr_needed = true;
            }
            if (back.italic and !active.italic) {
                writer.writeAll(if (sgr_needed) ";3" else "\x1b[3") catch @panic("OOM");
                sgr_needed = true;
            }
            if (back.underline and !active.underline) {
                writer.writeAll(if (sgr_needed) ";4" else "\x1b[4") catch @panic("OOM");
                sgr_needed = true;
            }
            if (back.inverse and !active.inverse) {
                writer.writeAll(if (sgr_needed) ";7" else "\x1b[7") catch @panic("OOM");
                sgr_needed = true;
            }
            if (back.strikethrough and !active.strikethrough) {
                writer.writeAll(if (sgr_needed) ";9" else "\x1b[9") catch @panic("OOM");
                sgr_needed = true;
            }

            // Foreground color delta.
            if (!back.fg.eql(active.fg)) {
                sgr_needed = emitFgColor(writer, back.fg, sgr_needed);
            }

            // Background color delta.
            if (!back.bg.eql(active.bg)) {
                sgr_needed = emitBgColor(writer, back.bg, sgr_needed);
            }

            // Close the SGR sequence if we emitted anything.
            if (sgr_needed) {
                writer.writeByte('m') catch @panic("OOM");
            }

            // Update active state to match what we just emitted.
            active = back;

            // UTF-8 encode the codepoint.
            if (back.codepoint == 0 or back.codepoint == ' ') {
                writer.writeByte(' ') catch @panic("OOM");
            } else {
                var buf: [4]u8 = undefined;
                const len = std.unicode.utf8Encode(@intCast(back.codepoint), &buf) catch 1;
                if (len == 1 and buf[0] == 0) buf[0] = ' ';
                writer.writeAll(buf[0..len]) catch @panic("OOM");
            }

            // Update cursor position tracker.
            //
            // Only trust implicit cursor advancement for plain ASCII. For any
            // non-ASCII glyph the REAL terminal may advance by a different
            // width than we computed (emoji, ambiguous-width, ZWJ sequences,
            // spinner/todo symbols in agent output) — if our assumption is
            // wrong, every subsequent skipped-CUP cell in the run lands
            // shifted on the physical screen while the front buffer believes
            // it's correct: persistent drift/duplication until a full
            // repaint. Poisoning the tracker forces an explicit CUP for the
            // next cell, which costs a few bytes but is always correct.
            current_y = y;
            if (back.codepoint < 0x80) {
                current_x = x + 1;
            } else {
                current_x = 9999; // unknown — force CUP on next cell
            }

            // Update front buffer.
            c.front[idx] = back;
        }
    }

    // Append cursor sequences only when cells were repainted (the repaint
    // may have moved the physical cursor / painted over it) or the cursor
    // state changed since the last emit. Idle ticks write nothing.
    const cursor_changed = !c.emitted_cursor_valid or
        c.emitted_cursor_x != c.cursor_x or
        c.emitted_cursor_y != c.cursor_y or
        c.emitted_cursor_style != c.cursor_style or
        c.emitted_cursor_visible != c.cursor_visible;
    if (dirty_count > 0 or cursor_changed) {
        const writer = out.writer(alloc);
        if (!c.cursor_visible) {
            writer.writeAll("\x1b[?25l") catch @panic("OOM");
        } else {
            // CUP: position cursor (1-based).
            writer.print("\x1b[{d};{d}H", .{ c.cursor_y, c.cursor_x }) catch @panic("OOM");
            // DECSCUSR: set cursor shape.
            writer.print("\x1b[{d} q", .{c.cursor_style}) catch @panic("OOM");
            // DECTCEM: show cursor.
            writer.writeAll("\x1b[?25h") catch @panic("OOM");
        }
        c.emitted_cursor_x = c.cursor_x;
        c.emitted_cursor_y = c.cursor_y;
        c.emitted_cursor_style = c.cursor_style;
        c.emitted_cursor_visible = c.cursor_visible;
        c.emitted_cursor_valid = true;
    }

    const bytes_out: u64 = out.items.len;
    if (bytes_out > 0) {
        const file: std.posix.fd_t = @intCast(fd);
        if (c.classic) {
            _ = std.posix.write(file, out.items) catch {};
        } else {
            // Bracket the frame in DEC 2026 synchronized output so the
            // host terminal applies the whole diff atomically.
            _ = std.posix.write(file, "\x1b[?2026h") catch {};
            _ = std.posix.write(file, out.items) catch {};
            _ = std.posix.write(file, "\x1b[?2026l") catch {};
        }
    }

    // Update stats.
    c.stats.frames_rendered += 1;
    c.stats.dirty_cells_flushed += dirty_count;
    c.stats.bytes_written += bytes_out;
    const elapsed_ns = std.time.nanoTimestamp() - start_ns;
    if (elapsed_ns > 0) {
        c.stats.flush_time_us += @intCast(@divFloor(elapsed_ns, 1000));
    }

    compositorLog(c.log_level, .debug, "flush: dirty={d} bytes={d}", .{ dirty_count, bytes_out });
}

// --- ANSI blit ---

export fn compositor_blit_ansi(
    c: *Compositor,
    pane_x: c_int,
    pane_y: c_int,
    pane_w: c_int,
    pane_h: c_int,
    str_ptr: [*c]const u8,
    len: usize,
) void {
    const start_ns = std.time.nanoTimestamp();
    defer {
        const elapsed_ns = std.time.nanoTimestamp() - start_ns;
        if (elapsed_ns > 0) {
            c.stats.blit_ansi_time_us += @intCast(@divFloor(elapsed_ns, 1000));
        }
        compositorLog(c.log_level, .debug, "blit_ansi: input_len={d} pane=({d},{d} {d}x{d})", .{ len, pane_x, pane_y, pane_w, pane_h });
    }

    const pw: usize = @intCast(pane_w);
    const ph: usize = @intCast(pane_h);
    const ox: usize = @intCast(pane_x);
    const oy: usize = @intCast(pane_y);

    // Clear the target bounding box before parsing — ensures old content
    // from the previous frame is erased (fixes leftover line artifacts).
    for (0..ph) |row| {
        const abs_y = oy + row;
        if (abs_y >= c.height) break;
        for (0..pw) |col| {
            const abs_x = ox + col;
            if (abs_x >= c.width) break;
            c.back[abs_y * c.width + abs_x] = Cell{};
        }
    }

    var parser = ansi.AnsiParser{};
    const data = str_ptr[0..len];

    for (data) |byte| {
        if (parser.feed(byte)) |cell| {
            // x was already advanced by feed(): -2 for wide chars, -1 for normal.
            const cx = if (cell.wide) parser.x -| 2 else parser.x -| 1;
            const cy = parser.y;

            if (cx >= pw or cy >= ph) continue;

            const abs_x = ox + cx;
            const abs_y = oy + cy;

            if (abs_x < c.width and abs_y < c.height) {
                const buf_idx = abs_y * c.width + abs_x;
                c.back[buf_idx] = .{
                    .codepoint = cell.codepoint,
                    .fg = cell.fg,
                    .bg = cell.bg,
                    .bold = cell.bold,
                    .italic = cell.italic,
                    .underline = cell.underline,
                    .faint = cell.faint,
                    .inverse = cell.inverse,
                    .strikethrough = cell.strikethrough,
                    .wide = cell.wide,
                };

                // Write a spacer cell at cx+1 for wide characters.
                if (cell.wide) {
                    const spacer_x = abs_x + 1;
                    if (spacer_x < c.width) {
                        c.back[abs_y * c.width + spacer_x] = .{
                            .codepoint = ' ',
                            .fg = cell.fg,
                            .bg = cell.bg,
                            .wide_spacer = true,
                        };
                    }
                }
            }
        }
    }
}

// --- Stats export ---

export fn compositor_get_stats(c: *Compositor) *const CompositorStats {
    return &c.stats;
}

// --- Pointer export for extensions ---

/// Returns a raw pointer to the Compositor struct, allowing terminal-specific
/// extensions (ghostty blit, input routing, diff protocol) to operate on the
/// cell buffer directly via their own CGo bindings.
export fn compositor_pointer(c: *Compositor) *anyopaque {
    return @ptrCast(c);
}

// Inline cell comparison (all fields).
pub inline fn cellEqual(a: Cell, b: Cell) bool {
    return a.codepoint == b.codepoint and
        a.fg.eql(b.fg) and a.bg.eql(b.bg) and
        a.bold == b.bold and a.italic == b.italic and a.underline == b.underline and
        a.faint == b.faint and a.inverse == b.inverse and a.strikethrough == b.strikethrough and
        a.wide == b.wide and a.wide_spacer == b.wide_spacer;
}
