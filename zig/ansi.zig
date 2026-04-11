const std = @import("std");
const runewidth = @import("runewidth.zig");

/// Color type: preserves the original ANSI color format instead of converting to RGB.
pub const ColorType = enum(u8) {
    default = 0,
    standard = 1, // SGR 30-37, 40-47, 90-97, 100-107 (index 0-15)
    palette256 = 2, // SGR 38;5;N / 48;5;N (index 0-255)
    rgb = 3, // SGR 38;2;R;G;B / 48;2;R;G;B
};

/// Color value. For standard/palette256, the index is stored in `r`.
/// For rgb, all three components are used.
pub const Color = struct {
    color_type: ColorType = .default,
    r: u8 = 0,
    g: u8 = 0,
    b: u8 = 0,

    pub fn eql(a: Color, b: Color) bool {
        return a.color_type == b.color_type and a.r == b.r and a.g == b.g and a.b == b.b;
    }

    pub fn initDefault() Color {
        return .{};
    }

    pub fn initStandard(index: u8) Color {
        return .{ .color_type = .standard, .r = index };
    }

    pub fn initPalette256(index: u8) Color {
        return .{ .color_type = .palette256, .r = index };
    }

    pub fn initRgb(r: u8, g: u8, b: u8) Color {
        return .{ .color_type = .rgb, .r = r, .g = g, .b = b };
    }
};

/// Lightweight ANSI escape sequence parser for lipgloss/bubbletea output.
/// Tracks cursor position and SGR attributes, writing decoded cells into
/// the compositor's back buffer.
pub const AnsiParser = struct {
    // Current cursor position relative to the blit region.
    x: usize = 0,
    y: usize = 0,

    // Current SGR attributes.
    fg: Color = .{},
    bg: Color = .{},
    bold: bool = false,
    italic: bool = false,
    underline: bool = false,
    faint: bool = false,
    inverse: bool = false,
    strikethrough: bool = false,

    // CSI parameter accumulation.
    params: [16]u16 = undefined,
    param_count: u8 = 0,
    current_param: u16 = 0,
    has_current: bool = false,

    state: State = .normal,

    // UTF-8 multi-byte accumulation.
    utf8_codepoint: u32 = 0,
    utf8_bytes_left: u8 = 0,

    const State = enum {
        normal,
        esc,
        csi,
        utf8,
    };

    pub fn reset(self: *AnsiParser) void {
        self.* = .{};
    }

    pub fn resetAttributes(self: *AnsiParser) void {
        self.fg = Color.initDefault();
        self.bg = Color.initDefault();
        self.bold = false;
        self.italic = false;
        self.underline = false;
        self.faint = false;
        self.inverse = false;
        self.strikethrough = false;
    }

    /// Feed a single byte into the state machine.
    /// Returns a Cell if a printable character was decoded, null otherwise.
    pub fn feed(self: *AnsiParser, byte: u8) ?Cell {
        switch (self.state) {
            .normal => return self.handleNormal(byte),
            .esc => {
                self.handleEsc(byte);
                return null;
            },
            .csi => {
                self.handleCsi(byte);
                return null;
            },
            .utf8 => return self.handleUtf8(byte),
        }
    }

    fn handleNormal(self: *AnsiParser, byte: u8) ?Cell {
        switch (byte) {
            0x1b => {
                self.state = .esc;
                return null;
            },
            '\n' => {
                self.y += 1;
                self.x = 0;
                return null;
            },
            '\r' => {
                self.x = 0;
                return null;
            },
            '\t' => {
                // Advance to next tab stop (every 8 columns).
                const next = (self.x + 8) & ~@as(usize, 7);
                // Emit spaces up to the tab stop.
                const cell = self.makeCell(' ');
                self.x = next;
                return cell;
            },
            else => {
                // Skip other control characters.
                if (byte < 0x20) return null;

                // UTF-8 multi-byte sequence detection.
                if (byte >= 0x80) {
                    if ((byte & 0xE0) == 0xC0) {
                        // 2-byte sequence (110xxxxx).
                        self.utf8_codepoint = @as(u32, byte & 0x1F);
                        self.utf8_bytes_left = 1;
                        self.state = .utf8;
                        return null;
                    } else if ((byte & 0xF0) == 0xE0) {
                        // 3-byte sequence (1110xxxx).
                        self.utf8_codepoint = @as(u32, byte & 0x0F);
                        self.utf8_bytes_left = 2;
                        self.state = .utf8;
                        return null;
                    } else if ((byte & 0xF8) == 0xF0) {
                        // 4-byte sequence (11110xxx).
                        self.utf8_codepoint = @as(u32, byte & 0x07);
                        self.utf8_bytes_left = 3;
                        self.state = .utf8;
                        return null;
                    }
                    // Unexpected continuation byte in normal state — drop it.
                    return null;
                }

                const cell = self.makeCell(@as(u32, byte));
                self.x += 1;
                return cell;
            },
        }
    }

    fn handleUtf8(self: *AnsiParser, byte: u8) ?Cell {
        // Validate continuation byte (10xxxxxx).
        if ((byte & 0xC0) != 0x80) {
            // Malformed sequence — drop it and return to normal.
            self.state = .normal;
            return null;
        }

        self.utf8_codepoint = (self.utf8_codepoint << 6) | @as(u32, byte & 0x3F);
        self.utf8_bytes_left -= 1;

        if (self.utf8_bytes_left == 0) {
            self.state = .normal;
            const w = runewidth.charWidth(self.utf8_codepoint);
            if (w == 0) {
                // Zero-width combining/nonprint character — do not advance cursor.
                return null;
            }
            var cell = self.makeCell(self.utf8_codepoint);
            if (w == 2) {
                cell.wide = true;
                self.x += 2;
            } else {
                self.x += 1;
            }
            return cell;
        }
        return null;
    }

    fn handleEsc(self: *AnsiParser, byte: u8) void {
        switch (byte) {
            '[' => {
                self.state = .csi;
                self.param_count = 0;
                self.current_param = 0;
                self.has_current = false;
            },
            else => {
                // Unknown escape — return to normal.
                self.state = .normal;
            },
        }
    }

    fn handleCsi(self: *AnsiParser, byte: u8) void {
        switch (byte) {
            '0'...'9' => {
                self.current_param = self.current_param *% 10 +% (@as(u16, byte) - '0');
                self.has_current = true;
            },
            ';' => {
                self.pushParam();
            },
            'm' => {
                // SGR — Select Graphic Rendition.
                self.pushParam();
                self.applySgr();
                self.state = .normal;
            },
            'H', 'f' => {
                // CUP — Cursor Position (1-based).
                self.pushParam();
                const row = if (self.param_count > 0 and self.params[0] > 0) self.params[0] - 1 else 0;
                const col = if (self.param_count > 1 and self.params[1] > 0) self.params[1] - 1 else 0;
                self.y = @intCast(row);
                self.x = @intCast(col);
                self.state = .normal;
            },
            'A' => {
                // CUU — Cursor Up.
                self.pushParam();
                const n = if (self.param_count > 0 and self.params[0] > 0) self.params[0] else 1;
                self.y -|= @intCast(n);
                self.state = .normal;
            },
            'B' => {
                // CUD — Cursor Down.
                self.pushParam();
                const n = if (self.param_count > 0 and self.params[0] > 0) self.params[0] else 1;
                self.y += @intCast(n);
                self.state = .normal;
            },
            'C' => {
                // CUF — Cursor Forward.
                self.pushParam();
                const n = if (self.param_count > 0 and self.params[0] > 0) self.params[0] else 1;
                self.x += @intCast(n);
                self.state = .normal;
            },
            'D' => {
                // CUB — Cursor Back.
                self.pushParam();
                const n = if (self.param_count > 0 and self.params[0] > 0) self.params[0] else 1;
                self.x -|= @intCast(n);
                self.state = .normal;
            },
            'J', 'K' => {
                // ED/EL — Erase in Display/Line. Ignore for blit purposes.
                self.state = .normal;
            },
            else => {
                // Unknown CSI final byte — ignore the sequence.
                if (byte >= 0x40 and byte <= 0x7e) {
                    self.state = .normal;
                }
                // Otherwise keep accumulating (intermediate bytes 0x20-0x3f).
            },
        }
    }

    fn pushParam(self: *AnsiParser) void {
        if (self.param_count < self.params.len) {
            self.params[self.param_count] = if (self.has_current) self.current_param else 0;
            self.param_count += 1;
        }
        self.current_param = 0;
        self.has_current = false;
    }

    fn applySgr(self: *AnsiParser) void {
        // ESC[m with no params is equivalent to ESC[0m.
        if (self.param_count == 0) {
            self.resetAttributes();
            return;
        }

        var i: u8 = 0;
        while (i < self.param_count) : (i += 1) {
            const p = self.params[i];
            switch (p) {
                0 => self.resetAttributes(),
                1 => self.bold = true,
                2 => self.faint = true,
                3 => self.italic = true,
                4 => self.underline = true,
                7 => self.inverse = true,
                9 => self.strikethrough = true,
                22 => {
                    self.bold = false;
                    self.faint = false;
                },
                23 => self.italic = false,
                24 => self.underline = false,
                27 => self.inverse = false,
                29 => self.strikethrough = false,
                // Standard foreground colors (30-37).
                30...37 => {
                    self.fg = Color.initStandard(@truncate(p - 30));
                },
                // Default foreground.
                39 => {
                    self.fg = Color.initDefault();
                },
                // Standard background colors (40-47).
                40...47 => {
                    self.bg = Color.initStandard(@truncate(p - 40));
                },
                // Default background.
                49 => {
                    self.bg = Color.initDefault();
                },
                // Bright foreground colors (90-97).
                90...97 => {
                    self.fg = Color.initStandard(@truncate(p - 90 + 8));
                },
                // Bright background colors (100-107).
                100...107 => {
                    self.bg = Color.initStandard(@truncate(p - 100 + 8));
                },
                // Extended color: 38;5;N (fg) or 38;2;R;G;B (fg).
                38 => {
                    if (i + 1 < self.param_count) {
                        if (self.params[i + 1] == 5 and i + 2 < self.param_count) {
                            // 256-color — preserve as palette index.
                            self.fg = Color.initPalette256(@truncate(self.params[i + 2]));
                            i += 2;
                        } else if (self.params[i + 1] == 2 and i + 4 < self.param_count) {
                            // Truecolor.
                            self.fg = Color.initRgb(
                                @truncate(self.params[i + 2]),
                                @truncate(self.params[i + 3]),
                                @truncate(self.params[i + 4]),
                            );
                            i += 4;
                        }
                    }
                },
                // Extended color: 48;5;N (bg) or 48;2;R;G;B (bg).
                48 => {
                    if (i + 1 < self.param_count) {
                        if (self.params[i + 1] == 5 and i + 2 < self.param_count) {
                            self.bg = Color.initPalette256(@truncate(self.params[i + 2]));
                            i += 2;
                        } else if (self.params[i + 1] == 2 and i + 4 < self.param_count) {
                            self.bg = Color.initRgb(
                                @truncate(self.params[i + 2]),
                                @truncate(self.params[i + 3]),
                                @truncate(self.params[i + 4]),
                            );
                            i += 4;
                        }
                    }
                },
                else => {},
            }
        }
    }

    fn makeCell(self: *const AnsiParser, codepoint: u32) Cell {
        return .{
            .codepoint = codepoint,
            .fg = self.fg,
            .bg = self.bg,
            .bold = self.bold,
            .italic = self.italic,
            .underline = self.underline,
            .faint = self.faint,
            .inverse = self.inverse,
            .strikethrough = self.strikethrough,
        };
    }
};

/// Cell mirrors the Cell struct in compositor.zig.
pub const Cell = struct {
    codepoint: u32 = ' ',
    fg: Color = .{},
    bg: Color = .{},
    bold: bool = false,
    italic: bool = false,
    underline: bool = false,
    faint: bool = false,
    inverse: bool = false,
    strikethrough: bool = false,
    wide: bool = false,
};

/// Re-export charWidth for use by compositor.zig.
pub const charWidth = runewidth.charWidth;

// --- Tests ---

test "basic SGR parsing" {
    var parser = AnsiParser{};

    // Feed ESC[1;31m (bold red)
    _ = parser.feed(0x1b);
    _ = parser.feed('[');
    _ = parser.feed('1');
    _ = parser.feed(';');
    _ = parser.feed('3');
    _ = parser.feed('1');
    _ = parser.feed('m');

    try std.testing.expect(parser.bold);
    // Standard color 1 (red) — stored as index, not RGB.
    try std.testing.expectEqual(ColorType.standard, parser.fg.color_type);
    try std.testing.expectEqual(@as(u8, 1), parser.fg.r);

    // Feed 'A' — should produce a cell.
    const cell = parser.feed('A');
    try std.testing.expect(cell != null);
    try std.testing.expectEqual(@as(u32, 'A'), cell.?.codepoint);
    try std.testing.expect(cell.?.bold);
    try std.testing.expectEqual(ColorType.standard, cell.?.fg.color_type);
    try std.testing.expectEqual(@as(u8, 1), cell.?.fg.r);
}

test "truecolor SGR" {
    var parser = AnsiParser{};

    // ESC[38;2;100;150;200m (truecolor fg)
    const seq = "\x1b[38;2;100;150;200m";
    for (seq) |b| _ = parser.feed(b);

    try std.testing.expectEqual(ColorType.rgb, parser.fg.color_type);
    try std.testing.expectEqual(@as(u8, 100), parser.fg.r);
    try std.testing.expectEqual(@as(u8, 150), parser.fg.g);
    try std.testing.expectEqual(@as(u8, 200), parser.fg.b);
}

test "256-color SGR" {
    var parser = AnsiParser{};

    // ESC[48;5;196m (256-color bg — palette index 196)
    const seq = "\x1b[48;5;196m";
    for (seq) |b| _ = parser.feed(b);

    // Preserved as palette index, not converted to RGB.
    try std.testing.expectEqual(ColorType.palette256, parser.bg.color_type);
    try std.testing.expectEqual(@as(u8, 196), parser.bg.r);
}

test "standard color passthrough" {
    var parser = AnsiParser{};

    // ESC[36m (cyan fg — standard color 6)
    for ("\x1b[36m") |b| _ = parser.feed(b);
    try std.testing.expectEqual(ColorType.standard, parser.fg.color_type);
    try std.testing.expectEqual(@as(u8, 6), parser.fg.r);

    // ESC[96m (bright cyan fg — standard color 14)
    for ("\x1b[96m") |b| _ = parser.feed(b);
    try std.testing.expectEqual(ColorType.standard, parser.fg.color_type);
    try std.testing.expectEqual(@as(u8, 14), parser.fg.r);

    // ESC[42m (green bg — standard color 2)
    for ("\x1b[42m") |b| _ = parser.feed(b);
    try std.testing.expectEqual(ColorType.standard, parser.bg.color_type);
    try std.testing.expectEqual(@as(u8, 2), parser.bg.r);

    // ESC[39m (default fg)
    for ("\x1b[39m") |b| _ = parser.feed(b);
    try std.testing.expectEqual(ColorType.default, parser.fg.color_type);
}

test "newline and cursor position" {
    var parser = AnsiParser{};

    _ = parser.feed('A');
    try std.testing.expectEqual(@as(usize, 1), parser.x);

    _ = parser.feed('\n');
    try std.testing.expectEqual(@as(usize, 1), parser.y);
    try std.testing.expectEqual(@as(usize, 0), parser.x);

    // CUP: ESC[3;5H → row=2, col=4 (0-based)
    const seq = "\x1b[3;5H";
    for (seq) |b| _ = parser.feed(b);
    try std.testing.expectEqual(@as(usize, 2), parser.y);
    try std.testing.expectEqual(@as(usize, 4), parser.x);
}

test "reset SGR" {
    var parser = AnsiParser{};

    // Set bold + italic.
    for ("\x1b[1;3m") |b| _ = parser.feed(b);
    try std.testing.expect(parser.bold);
    try std.testing.expect(parser.italic);

    // Reset.
    for ("\x1b[0m") |b| _ = parser.feed(b);
    try std.testing.expect(!parser.bold);
    try std.testing.expect(!parser.italic);
}

test "utf8 3-byte box-drawing character" {
    var parser = AnsiParser{};

    // ─ (U+2500) = 0xE2, 0x94, 0x80
    const r1 = parser.feed(0xE2);
    try std.testing.expect(r1 == null);
    const r2 = parser.feed(0x94);
    try std.testing.expect(r2 == null);
    const r3 = parser.feed(0x80);
    try std.testing.expect(r3 != null);
    try std.testing.expectEqual(@as(u32, 0x2500), r3.?.codepoint);
    try std.testing.expect(!r3.?.wide);
    try std.testing.expectEqual(@as(usize, 1), parser.x);
}

test "utf8 4-byte nerd font icon (single-width per go-runewidth)" {
    var parser = AnsiParser{};

    // 󰊢 (U+F0122) = 0xF3, 0xB0, 0x84, 0xA2
    // go-runewidth does NOT include PUA in doublewidth — treated as width 1.
    const r1 = parser.feed(0xF3);
    try std.testing.expect(r1 == null);
    const r2 = parser.feed(0xB0);
    try std.testing.expect(r2 == null);
    const r3 = parser.feed(0x84);
    try std.testing.expect(r3 == null);
    const r4 = parser.feed(0xA2);
    try std.testing.expect(r4 != null);
    try std.testing.expectEqual(@as(u32, 0xF0122), r4.?.codepoint);
    try std.testing.expect(!r4.?.wide);
    try std.testing.expectEqual(@as(usize, 1), parser.x);
}

test "utf8 2-byte latin character" {
    var parser = AnsiParser{};

    // é (U+00E9) = 0xC3, 0xA9
    const r1 = parser.feed(0xC3);
    try std.testing.expect(r1 == null);
    const r2 = parser.feed(0xA9);
    try std.testing.expect(r2 != null);
    try std.testing.expectEqual(@as(u32, 0xE9), r2.?.codepoint);
    try std.testing.expect(!r2.?.wide);
    try std.testing.expectEqual(@as(usize, 1), parser.x);
}

test "utf8 malformed sequence recovery" {
    var parser = AnsiParser{};

    // Start a 3-byte sequence but send a non-continuation byte.
    _ = parser.feed(0xE2);
    const r = parser.feed('A'); // not a continuation byte
    try std.testing.expect(r == null); // malformed — dropped
    try std.testing.expectEqual(AnsiParser.State.normal, parser.state);

    // Parser should recover and handle normal bytes.
    const cell = parser.feed('B');
    try std.testing.expect(cell != null);
    try std.testing.expectEqual(@as(u32, 'B'), cell.?.codepoint);
}
