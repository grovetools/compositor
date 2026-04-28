const std = @import("std");

// Maximum number of intercept sequences the compositor can track.
pub const max_intercepts = 128;
// Maximum byte length of a single intercept or leader sequence.
pub const max_seq_len = 16;

pub const InputState = struct {
    mutex: std.Thread.Mutex = .{},
    active_pty_fd: std.posix.fd_t = -1,
    passthrough: bool = false,
    running: bool = false,

    // Leader chord sequence (e.g. ESC O S for F4, or raw bytes for ctrl+g).
    leader_seq: [max_seq_len]u8 = undefined,
    leader_len: usize = 0,

    // Intercept sequences for global hotkeys (alt+l, ctrl+f, f9, etc.).
    intercept_seqs: [max_intercepts][max_seq_len]u8 = undefined,
    intercept_lens: [max_intercepts]usize = [_]usize{0} ** max_intercepts,
    intercept_count: usize = 0,

    // Callback for forwarding bytes to Go (bubbletea pipe).
    input_cb: ?InputCb = null,

    // Input thread handle for join on shutdown.
    thread: ?std.Thread = null,
};

pub const InputCb = *const fn ([*]const u8, usize) callconv(.c) void;

/// The input thread's main loop. Reads from STDIN_FILENO and routes bytes
/// to either the active PTY fd (zero-copy hot path) or the Go callback
/// (intercept path).
pub fn inputLoop(state: *InputState) void {
    while (true) {
        var buf: [4096]u8 = undefined;
        const n = std.posix.read(std.posix.STDIN_FILENO, &buf) catch break;
        if (n == 0) break;

        const data = buf[0..n];

        // Snapshot state under lock.
        state.mutex.lock();
        const running = state.running;
        const fd = state.active_pty_fd;
        const pt = state.passthrough;
        const cb = state.input_cb;
        const leader_len = state.leader_len;
        var leader_buf: [max_seq_len]u8 = undefined;
        if (leader_len > 0) {
            @memcpy(leader_buf[0..leader_len], state.leader_seq[0..leader_len]);
        }
        const ic = state.intercept_count;
        var intercept_lens: [max_intercepts]usize = undefined;
        var intercept_bufs: [max_intercepts][max_seq_len]u8 = undefined;
        for (0..ic) |i| {
            intercept_lens[i] = state.intercept_lens[i];
            @memcpy(intercept_bufs[i][0..intercept_lens[i]], state.intercept_seqs[i][0..intercept_lens[i]]);
        }
        state.mutex.unlock();

        if (!running) break;

        var send_to_go = pt or fd < 0;

        // Check leader sequence — if input starts with leader, enter passthrough.
        if (!send_to_go and leader_len > 0 and n >= leader_len) {
            if (std.mem.eql(u8, data[0..leader_len], leader_buf[0..leader_len])) {
                send_to_go = true;
                state.mutex.lock();
                state.passthrough = true;
                state.mutex.unlock();
            }
        }

        // Check intercept sequences (global hotkeys).
        if (!send_to_go) {
            for (0..ic) |i| {
                const slen = intercept_lens[i];
                if (slen > 0 and n >= slen) {
                    if (std.mem.eql(u8, data[0..slen], intercept_bufs[i][0..slen])) {
                        send_to_go = true;
                        break;
                    }
                }
            }
        }

        if (send_to_go) {
            if (cb) |callback| {
                callback(data.ptr, n);
            }
        } else {
            _ = std.posix.write(@intCast(fd), data) catch {};
        }
    }
}
