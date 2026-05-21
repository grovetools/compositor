// compositor.zig — Terminal-specific extensions to the base compositor.
//
// Contains: Ghostty blit, cursor reading, delta protocol, input thread mgmt.
// The base cell buffer, ANSI parser, flush, and cursor set are in the
// external compositor library (github.com/grovetools/compositor).

const std = @import("std");
const base = @import("compositor_base");
const input = @import("ext_input.zig");
const vt = @cImport({
    @cInclude("ghostty/vt.h");
});

const Cell = base.Cell;
const ansi = base.ansi;
const runewidth = base.runewidth;
const compositorLog = base.compositorLog;
const LogLevel = base.LogLevel;
const Compositor = base.Compositor;

// --- Extension Stats ---

const ExtStats = extern struct {
    blit_ghostty_time_us: u64 = 0,
};

// --- Selection state per terminal ---

const SelectionState = struct {
    active: bool = false,
    start_col: i32 = 0,
    start_row: i32 = 0,
    end_col: i32 = 0,
    end_row: i32 = 0,
};

// --- Search highlight range (passed from Go as a C struct array) ---

const CompositorHighlight = extern struct {
    start_x: c_int,
    start_y: c_int,
    end_x: c_int,
    end_y: c_int,
};

// Per-terminal search highlights state.
const SearchHighlightsState = struct {
    ranges: []CompositorHighlight = &.{},

    fn deinit(self: *SearchHighlightsState, alloc: std.mem.Allocator) void {
        if (self.ranges.len > 0) {
            alloc.free(self.ranges);
            self.ranges = &.{};
        }
    }
};

// --- Extension State (file-level globals for singleton compositor) ---

var g_render_states: std.AutoHashMap(?*anyopaque, RenderCtx) = undefined;
var g_selections: std.AutoHashMap(?*anyopaque, SelectionState) = undefined;
var g_search_highlights: std.AutoHashMap(?*anyopaque, SearchHighlightsState) = undefined;
var g_broadcast_front: []Cell = &.{};
var g_diff_buf: std.ArrayListUnmanaged(u8) = .{};
var g_input_state: input.InputState = .{};
var g_ext_stats: ExtStats = .{};
var g_alloc: std.mem.Allocator = std.heap.c_allocator;
var g_initialized: bool = false;
var g_last_w: usize = 0;
var g_last_h: usize = 0;

// Cached render state objects per terminal, avoiding per-frame allocation.
const RenderCtx = struct {
    state: vt.GhosttyRenderState,
    row_iter: vt.GhosttyRenderStateRowIterator,
    cells: vt.GhosttyRenderStateRowCells,
};

// --- Zig helper replacing C's GHOSTTY_INIT_SIZED macro ---
fn initSized(comptime T: type) T {
    var val: T = std.mem.zeroes(T);
    val.size = @sizeOf(T);
    return val;
}

// --- Back buffer clear ---

export fn ext_clear(c_ptr: *anyopaque) void {
    const c: *Compositor = @ptrCast(@alignCast(c_ptr));
    const default_cell = Cell{};
    for (c.back) |*cell| {
        cell.* = default_cell;
    }
}

// --- Extension lifecycle ---

export fn ext_init() void {
    if (g_initialized) return;
    g_render_states = std.AutoHashMap(?*anyopaque, RenderCtx).init(g_alloc);
    g_selections = std.AutoHashMap(?*anyopaque, SelectionState).init(g_alloc);
    g_search_highlights = std.AutoHashMap(?*anyopaque, SearchHighlightsState).init(g_alloc);
    g_initialized = true;
}

export fn ext_free() void {
    if (!g_initialized) return;

    // Stop input thread.
    ext_stop_input_thread();

    // Free render states.
    var it = g_render_states.iterator();
    while (it.next()) |entry| {
        vt.ghostty_render_state_row_cells_free(entry.value_ptr.cells);
        vt.ghostty_render_state_row_iterator_free(entry.value_ptr.row_iter);
        vt.ghostty_render_state_free(entry.value_ptr.state);
    }
    g_render_states.deinit();
    g_selections.deinit();

    // Free search highlights.
    var sh_it = g_search_highlights.iterator();
    while (sh_it.next()) |entry| {
        entry.value_ptr.deinit(g_alloc);
    }
    g_search_highlights.deinit();

    // Free broadcast front.
    if (g_broadcast_front.len > 0) {
        g_alloc.free(g_broadcast_front);
        g_broadcast_front = &.{};
    }
    g_diff_buf.deinit(g_alloc);

    g_initialized = false;
}

// --- Selection API ---

export fn ext_set_selection(term_ptr: ?*anyopaque, active: bool, sc: c_int, sr: c_int, ec: c_int, er: c_int) void {
    if (active) {
        g_selections.put(term_ptr, .{
            .active = true,
            .start_col = sc,
            .start_row = sr,
            .end_col = ec,
            .end_row = er,
        }) catch return;
    } else {
        _ = g_selections.remove(term_ptr);
    }
}

// --- Search Highlights API ---

export fn ext_set_search_highlights(term_ptr: ?*anyopaque, matches: [*]const CompositorHighlight, count: usize) void {
    const entry = g_search_highlights.getOrPut(term_ptr) catch return;
    if (entry.found_existing) {
        entry.value_ptr.deinit(g_alloc);
    }
    if (count == 0) {
        entry.value_ptr.* = .{};
        return;
    }
    const buf = g_alloc.alloc(CompositorHighlight, count) catch return;
    @memcpy(buf, matches[0..count]);
    entry.value_ptr.* = .{ .ranges = buf };
}

/// Check if cell (col, row) falls within any search highlight range.
fn isCellSearchHighlighted(highlights: SearchHighlightsState, col: usize, row: usize) bool {
    for (highlights.ranges) |h| {
        const r: i32 = @intCast(row);
        const c_i: i32 = @intCast(col);
        const r1 = h.start_y;
        const c1 = h.start_x;
        const r2 = h.end_y;
        const c2 = h.end_x;

        if (r < r1 or r > r2) continue;
        if (r == r1 and r == r2) {
            if (c_i >= c1 and c_i <= c2) return true;
            continue;
        }
        if (r == r1) {
            if (c_i >= c1) return true;
            continue;
        }
        if (r == r2) {
            if (c_i <= c2) return true;
            continue;
        }
        return true; // middle row
    }
    return false;
}

/// Check if cell (col, row) falls within the selection range.
/// Assumes start <= end (topologically sorted by caller).
fn isCellSelected(sel: SelectionState, col: usize, row: usize) bool {
    const r: i32 = @intCast(row);
    const c_i: i32 = @intCast(col);

    const r1 = sel.start_row;
    const c1 = sel.start_col;
    const r2 = sel.end_row;
    const c2 = sel.end_col;

    if (r < r1 or r > r2) return false;
    if (r == r1 and r == r2) return c_i >= c1 and c_i <= c2;
    if (r == r1) return c_i >= c1;
    if (r == r2) return c_i <= c2;
    return true; // middle row, fully selected
}

// --- Ghostty blit ---

export fn ext_blit_ghostty(
    c_ptr: *anyopaque,
    term_ptr: ?*anyopaque,
    pane_x: c_int,
    pane_y: c_int,
    pane_w: c_int,
    pane_h: c_int,
) void {
    const c: *Compositor = @ptrCast(@alignCast(c_ptr));
    const start_ns = std.time.nanoTimestamp();
    defer {
        const elapsed_ns = std.time.nanoTimestamp() - start_ns;
        if (elapsed_ns > 0) {
            g_ext_stats.blit_ghostty_time_us += @intCast(@divFloor(elapsed_ns, 1000));
        }
    }

    const term: vt.GhosttyTerminal = @ptrCast(term_ptr);

    // 1. Get or create cached RenderState for this terminal.
    const ctx_entry = g_render_states.getOrPut(term_ptr) catch return;
    if (!ctx_entry.found_existing) {
        var state: vt.GhosttyRenderState = null;
        var row_iter: vt.GhosttyRenderStateRowIterator = null;
        var cells: vt.GhosttyRenderStateRowCells = null;

        _ = vt.ghostty_render_state_new(null, &state);
        _ = vt.ghostty_render_state_row_iterator_new(null, &row_iter);
        _ = vt.ghostty_render_state_row_cells_new(null, &cells);

        ctx_entry.value_ptr.* = .{ .state = state, .row_iter = row_iter, .cells = cells };
    }
    const ctx = ctx_entry.value_ptr;

    // Look up selection and search highlight state for this terminal.
    const sel = g_selections.get(term_ptr);
    const search_hl = g_search_highlights.get(term_ptr);

    // 2. Update render state from terminal.
    _ = vt.ghostty_render_state_update(ctx.state, term);

    // 3. Get default colors.
    var colors = initSized(vt.GhosttyRenderStateColors);
    _ = vt.ghostty_render_state_colors_get(ctx.state, &colors);

    // 4. Populate the row iterator.
    _ = vt.ghostty_render_state_get(ctx.state, vt.GHOSTTY_RENDER_STATE_DATA_ROW_ITERATOR, @as(?*anyopaque, @ptrCast(&ctx.row_iter)));

    // 5. Iterate rows and cells, writing to the back buffer.
    var row_idx: usize = 0;
    while (vt.ghostty_render_state_row_iterator_next(ctx.row_iter)) {
        if (row_idx >= @as(usize, @intCast(pane_h))) break;

        _ = vt.ghostty_render_state_row_get(ctx.row_iter, vt.GHOSTTY_RENDER_STATE_ROW_DATA_CELLS, @as(?*anyopaque, @ptrCast(&ctx.cells)));

        var col_idx: usize = 0;
        while (vt.ghostty_render_state_row_cells_next(ctx.cells)) {
            if (col_idx >= @as(usize, @intCast(pane_w))) break;

            var style = initSized(vt.GhosttyStyle);
            _ = vt.ghostty_render_state_row_cells_get(ctx.cells, vt.GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_STYLE, &style);

            var fg_color = ansi.Color.initDefault();
            var fg: vt.GhosttyColorRgb = colors.foreground;
            if (vt.ghostty_render_state_row_cells_get(ctx.cells, vt.GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_FG_COLOR, &fg) == vt.GHOSTTY_SUCCESS) {
                fg_color = ansi.Color.initRgb(fg.r, fg.g, fg.b);
            }

            var bg_color = ansi.Color.initDefault();
            var bg: vt.GhosttyColorRgb = colors.background;
            if (vt.ghostty_render_state_row_cells_get(ctx.cells, vt.GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_BG_COLOR, &bg) == vt.GHOSTTY_SUCCESS) {
                bg_color = ansi.Color.initRgb(bg.r, bg.g, bg.b);
            }

            var grapheme_len: u32 = 0;
            _ = vt.ghostty_render_state_row_cells_get(ctx.cells, vt.GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_LEN, &grapheme_len);

            var codepoint: u32 = ' ';
            if (grapheme_len > 0) {
                var codepoints: [16]u32 = undefined;
                _ = vt.ghostty_render_state_row_cells_get(ctx.cells, vt.GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_BUF, &codepoints);
                codepoint = codepoints[0];
            }

            const abs_x = @as(usize, @intCast(pane_x)) + col_idx;
            const abs_y = @as(usize, @intCast(pane_y)) + row_idx;

            if (abs_x < c.width and abs_y < c.height) {
                const w = runewidth.charWidth(codepoint);
                const buf_idx = abs_y * c.width + abs_x;

                // If this cell is within the visual selection or a search
                // highlight, flip the inverse attribute for reverse-video.
                var cell_inverse = style.inverse;
                if (sel) |s| {
                    if (s.active and isCellSelected(s, col_idx, row_idx)) {
                        cell_inverse = !cell_inverse;
                    }
                }
                if (search_hl) |sh| {
                    if (isCellSearchHighlighted(sh, col_idx, row_idx)) {
                        cell_inverse = !cell_inverse;
                    }
                }

                c.back[buf_idx] = .{
                    .codepoint = codepoint,
                    .fg = fg_color,
                    .bg = bg_color,
                    .bold = style.bold,
                    .italic = style.italic,
                    .underline = style.underline != 0,
                    .faint = style.faint,
                    .inverse = cell_inverse,
                    .strikethrough = style.strikethrough,
                    .wide = (w == 2),
                };

                if (w == 2) {
                    const spacer_x = abs_x + 1;
                    if (spacer_x < c.width) {
                        c.back[abs_y * c.width + spacer_x] = .{
                            .codepoint = ' ',
                            .fg = fg_color,
                            .bg = bg_color,
                            .wide_spacer = true,
                        };
                    }
                    // Skip the ghostty spacer cell that follows a wide character
                    col_idx += 1;
                    _ = vt.ghostty_render_state_row_cells_next(ctx.cells);
                }
            }
            col_idx += 1;
        }

        var clean: bool = false;
        _ = vt.ghostty_render_state_row_set(ctx.row_iter, vt.GHOSTTY_RENDER_STATE_ROW_OPTION_DIRTY, &clean);
        row_idx += 1;
    }

    var clean_state: c_int = vt.GHOSTTY_RENDER_STATE_DIRTY_FALSE;
    _ = vt.ghostty_render_state_set(ctx.state, vt.GHOSTTY_RENDER_STATE_OPTION_DIRTY, &clean_state);

    compositorLog(c.log_level, .debug, "blit_ghostty: rows={d} pane=({d},{d} {d}x{d})", .{ row_idx, pane_x, pane_y, pane_w, pane_h });
}

export fn ext_unregister_terminal(term: ?*anyopaque) void {
    if (g_render_states.fetchRemove(term)) |kv| {
        vt.ghostty_render_state_row_cells_free(kv.value.cells);
        vt.ghostty_render_state_row_iterator_free(kv.value.row_iter);
        vt.ghostty_render_state_free(kv.value.state);
    }
    _ = g_selections.remove(term);
    if (g_search_highlights.fetchRemove(term)) |kv| {
        var state = kv.value;
        state.deinit(g_alloc);
    }
}

// --- Cursor ---

const CompositorCursor = extern struct {
    x: u16 = 0,
    y: u16 = 0,
    style: i32 = 0,
    visible: bool = false,
};

export fn ext_get_cursor(term_ptr: ?*anyopaque, out: *CompositorCursor) void {
    const ctx = g_render_states.getPtr(term_ptr) orelse {
        out.* = .{};
        return;
    };

    var has_value: bool = false;
    _ = vt.ghostty_render_state_get(ctx.state, vt.GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_HAS_VALUE, @as(?*anyopaque, @ptrCast(&has_value)));
    if (!has_value) {
        out.* = .{};
        return;
    }

    var cursor_visible: bool = true;
    _ = vt.ghostty_render_state_get(ctx.state, vt.GHOSTTY_RENDER_STATE_DATA_CURSOR_VISIBLE, @as(?*anyopaque, @ptrCast(&cursor_visible)));

    var vx: u16 = 0;
    var vy: u16 = 0;
    _ = vt.ghostty_render_state_get(ctx.state, vt.GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_X, @as(?*anyopaque, @ptrCast(&vx)));
    _ = vt.ghostty_render_state_get(ctx.state, vt.GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_Y, @as(?*anyopaque, @ptrCast(&vy)));

    var visual_style: c_int = 0;
    _ = vt.ghostty_render_state_get(ctx.state, vt.GHOSTTY_RENDER_STATE_DATA_CURSOR_VISUAL_STYLE, @as(?*anyopaque, @ptrCast(&visual_style)));

    const decscusr: i32 = switch (visual_style) {
        0 => 6,
        1 => 2,
        2 => 4,
        else => 0,
    };

    out.* = .{
        .x = vx,
        .y = vy,
        .style = decscusr,
        .visible = cursor_visible,
    };
}

// --- Delta protocol ---

const DIFF_HEADER_SIZE = 12;
const DIFF_CELL_SIZE = 16;

fn packFlags(cell: Cell) u8 {
    var f: u8 = 0;
    if (cell.bold) f |= 1;
    if (cell.faint) f |= 2;
    if (cell.italic) f |= 4;
    if (cell.underline) f |= 8;
    if (cell.inverse) f |= 16;
    if (cell.strikethrough) f |= 32;
    if (cell.wide) f |= 64;
    if (cell.wide_spacer) f |= 128;
    return f;
}

fn unpackFlags(flags: u8) struct { bold: bool, faint: bool, italic: bool, underline: bool, inverse: bool, strikethrough: bool, wide: bool, wide_spacer: bool } {
    return .{
        .bold = (flags & 1) != 0,
        .faint = (flags & 2) != 0,
        .italic = (flags & 4) != 0,
        .underline = (flags & 8) != 0,
        .inverse = (flags & 16) != 0,
        .strikethrough = (flags & 32) != 0,
        .wide = (flags & 64) != 0,
        .wide_spacer = (flags & 128) != 0,
    };
}

fn packColorFlags(cell: Cell) u8 {
    return @as(u8, @intFromEnum(cell.fg.color_type)) |
        (@as(u8, @intFromEnum(cell.bg.color_type)) << 2);
}

fn unpackColorFlags(flags: u8) struct { fg_type: ansi.ColorType, bg_type: ansi.ColorType } {
    return .{
        .fg_type = @enumFromInt(flags & 0x3),
        .bg_type = @enumFromInt((flags >> 2) & 0x3),
    };
}

/// Ensure broadcast_front matches compositor dimensions.
fn ensureBroadcastFront(c: *Compositor) void {
    const size = c.width * c.height;
    if (g_broadcast_front.len != size) {
        if (g_broadcast_front.len > 0) {
            g_alloc.free(g_broadcast_front);
        }
        g_broadcast_front = g_alloc.alloc(Cell, size) catch @panic("OOM");
        @memset(g_broadcast_front, Cell{});
    }
}

export fn ext_get_dirty_payload(c_ptr: *anyopaque, out_ptr: *[*]const u8, out_len: *usize) void {
    const c: *Compositor = @ptrCast(@alignCast(c_ptr));
    ensureBroadcastFront(c);

    g_diff_buf.clearRetainingCapacity();
    g_diff_buf.appendNTimes(g_alloc, 0, DIFF_HEADER_SIZE) catch @panic("OOM");

    var count: u32 = 0;
    for (0..c.height) |y| {
        for (0..c.width) |x| {
            const idx = y * c.width + x;
            const back_cell = c.back[idx];
            if (base.cellEqual(g_broadcast_front[idx], back_cell)) continue;

            const x16: u16 = @intCast(x);
            const y16: u16 = @intCast(y);
            const record = [DIFF_CELL_SIZE]u8{
                @truncate(x16), @truncate(x16 >> 8),
                @truncate(y16), @truncate(y16 >> 8),
                @truncate(back_cell.codepoint),
                @truncate(back_cell.codepoint >> 8),
                @truncate(back_cell.codepoint >> 16),
                @truncate(back_cell.codepoint >> 24),
                back_cell.fg.r, back_cell.fg.g, back_cell.fg.b,
                back_cell.bg.r, back_cell.bg.g, back_cell.bg.b,
                packFlags(back_cell),
                packColorFlags(back_cell),
            };
            g_diff_buf.appendSlice(g_alloc, &record) catch @panic("OOM");
            g_broadcast_front[idx] = back_cell;
            count += 1;
        }
    }

    if (count == 0) {
        g_diff_buf.clearRetainingCapacity();
        out_ptr.* = undefined;
        out_len.* = 0;
        return;
    }

    const buf = g_diff_buf.items;
    const magic = [4]u8{ 'G', 'R', 'V', '1' };
    @memcpy(buf[0..4], &magic);
    const w16: u16 = @intCast(c.width);
    const h16: u16 = @intCast(c.height);
    buf[4] = @truncate(w16);
    buf[5] = @truncate(w16 >> 8);
    buf[6] = @truncate(h16);
    buf[7] = @truncate(h16 >> 8);
    buf[8] = @truncate(count);
    buf[9] = @truncate(count >> 8);
    buf[10] = @truncate(count >> 16);
    buf[11] = @truncate(count >> 24);

    out_ptr.* = buf.ptr;
    out_len.* = buf.len;
}

export fn ext_apply_diff(c_ptr: *anyopaque, payload: [*]const u8, len: usize) void {
    const c: *Compositor = @ptrCast(@alignCast(c_ptr));
    if (len < DIFF_HEADER_SIZE) return;
    if (payload[0] != 'G' or payload[1] != 'R' or payload[2] != 'V' or payload[3] != '1') return;

    const count: u32 = @as(u32, payload[8]) |
        (@as(u32, payload[9]) << 8) |
        (@as(u32, payload[10]) << 16) |
        (@as(u32, payload[11]) << 24);

    const expected = DIFF_HEADER_SIZE + @as(usize, count) * DIFF_CELL_SIZE;
    if (len < expected) return;

    var offset: usize = DIFF_HEADER_SIZE;
    for (0..count) |_| {
        const rec = payload[offset .. offset + DIFF_CELL_SIZE];
        const cx: usize = @as(usize, rec[0]) | (@as(usize, rec[1]) << 8);
        const cy: usize = @as(usize, rec[2]) | (@as(usize, rec[3]) << 8);
        const codepoint: u32 = @as(u32, rec[4]) |
            (@as(u32, rec[5]) << 8) |
            (@as(u32, rec[6]) << 16) |
            (@as(u32, rec[7]) << 24);

        if (cx < c.width and cy < c.height) {
            const buf_idx = cy * c.width + cx;
            const flags = unpackFlags(rec[14]);
            const cflags = unpackColorFlags(rec[15]);
            c.back[buf_idx] = .{
                .codepoint = codepoint,
                .fg = .{ .color_type = cflags.fg_type, .r = rec[8], .g = rec[9], .b = rec[10] },
                .bg = .{ .color_type = cflags.bg_type, .r = rec[11], .g = rec[12], .b = rec[13] },
                .bold = flags.bold,
                .faint = flags.faint,
                .italic = flags.italic,
                .underline = flags.underline,
                .inverse = flags.inverse,
                .strikethrough = flags.strikethrough,
                .wide = flags.wide,
                .wide_spacer = flags.wide_spacer,
            };
        }
        offset += DIFF_CELL_SIZE;
    }
}

export fn ext_request_full_sync(c_ptr: *anyopaque) void {
    const c: *Compositor = @ptrCast(@alignCast(c_ptr));
    ensureBroadcastFront(c);
    const sentinel = Cell{ .codepoint = 0xFFFFFFFF };
    @memset(g_broadcast_front, sentinel);
}

// --- Extension stats ---

export fn ext_get_stats() *const ExtStats {
    return &g_ext_stats;
}

// --- Input routing ---

export fn ext_start_input_thread(cb: input.InputCb) void {
    g_input_state.mutex.lock();
    if (g_input_state.running) {
        g_input_state.mutex.unlock();
        return;
    }
    g_input_state.running = true;
    g_input_state.input_cb = cb;
    g_input_state.mutex.unlock();

    g_input_state.thread = std.Thread.spawn(.{}, input.inputLoop, .{&g_input_state}) catch null;
}

export fn ext_stop_input_thread() void {
    g_input_state.mutex.lock();
    g_input_state.running = false;
    g_input_state.mutex.unlock();
    if (g_input_state.thread) |t| {
        t.join();
        g_input_state.thread = null;
    }
}

export fn ext_set_active_pty(fd: c_int) void {
    g_input_state.mutex.lock();
    defer g_input_state.mutex.unlock();
    g_input_state.active_pty_fd = @intCast(fd);
}

export fn ext_set_passthrough(passthrough: bool) void {
    g_input_state.mutex.lock();
    defer g_input_state.mutex.unlock();
    g_input_state.passthrough = passthrough;
}

export fn ext_set_leader_sequence(bytes: [*]const u8, len: usize) void {
    g_input_state.mutex.lock();
    defer g_input_state.mutex.unlock();
    const n = @min(len, input.max_seq_len);
    @memcpy(g_input_state.leader_seq[0..n], bytes[0..n]);
    g_input_state.leader_len = n;
}

export fn ext_add_intercept_sequence(bytes: [*]const u8, len: usize) void {
    g_input_state.mutex.lock();
    defer g_input_state.mutex.unlock();
    if (g_input_state.intercept_count >= input.max_intercepts) return;
    const n = @min(len, input.max_seq_len);
    const idx = g_input_state.intercept_count;
    @memcpy(g_input_state.intercept_seqs[idx][0..n], bytes[0..n]);
    g_input_state.intercept_lens[idx] = n;
    g_input_state.intercept_count += 1;
}
