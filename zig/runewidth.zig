const std = @import("std");

/// Character width detection using the exact same range tables as go-runewidth
/// (github.com/mattn/go-runewidth), which is what lipgloss uses internally.
/// EastAsianWidth=false (default), so ambiguous chars are single-width.

const Range = [2]u32;

/// Double-width codepoint ranges from go-runewidth's doublewidth table.
const doublewidth_ranges = [_]Range{
    .{ 0x1100, 0x115F },   .{ 0x231A, 0x231B },   .{ 0x2329, 0x232A },
    .{ 0x23E9, 0x23EC },   .{ 0x23F0, 0x23F0 },   .{ 0x23F3, 0x23F3 },
    .{ 0x25FD, 0x25FE },   .{ 0x2614, 0x2615 },   .{ 0x2630, 0x2637 },
    .{ 0x2648, 0x2653 },   .{ 0x267F, 0x267F },   .{ 0x268A, 0x268F },
    .{ 0x2693, 0x2693 },   .{ 0x26A1, 0x26A1 },   .{ 0x26AA, 0x26AB },
    .{ 0x26BD, 0x26BE },   .{ 0x26C4, 0x26C5 },   .{ 0x26CE, 0x26CE },
    .{ 0x26D4, 0x26D4 },   .{ 0x26EA, 0x26EA },   .{ 0x26F2, 0x26F3 },
    .{ 0x26F5, 0x26F5 },   .{ 0x26FA, 0x26FA },   .{ 0x26FD, 0x26FD },
    .{ 0x2705, 0x2705 },   .{ 0x270A, 0x270B },   .{ 0x2728, 0x2728 },
    .{ 0x274C, 0x274C },   .{ 0x274E, 0x274E },   .{ 0x2753, 0x2755 },
    .{ 0x2757, 0x2757 },   .{ 0x2795, 0x2797 },   .{ 0x27B0, 0x27B0 },
    .{ 0x27BF, 0x27BF },   .{ 0x2B1B, 0x2B1C },   .{ 0x2B50, 0x2B50 },
    .{ 0x2B55, 0x2B55 },   .{ 0x2E80, 0x2E99 },   .{ 0x2E9B, 0x2EF3 },
    .{ 0x2F00, 0x2FD5 },   .{ 0x2FF0, 0x303E },   .{ 0x3041, 0x3096 },
    .{ 0x3099, 0x30FF },   .{ 0x3105, 0x312F },   .{ 0x3131, 0x318E },
    .{ 0x3190, 0x31E5 },   .{ 0x31EF, 0x321E },   .{ 0x3220, 0x3247 },
    .{ 0x3250, 0xA48C },   .{ 0xA490, 0xA4C6 },   .{ 0xA960, 0xA97C },
    .{ 0xAC00, 0xD7A3 },   .{ 0xF900, 0xFAFF },   .{ 0xFE10, 0xFE19 },
    .{ 0xFE30, 0xFE52 },   .{ 0xFE54, 0xFE66 },   .{ 0xFE68, 0xFE6B },
    .{ 0xFF01, 0xFF60 },   .{ 0xFFE0, 0xFFE6 },   .{ 0x16FE0, 0x16FE4 },
    .{ 0x16FF0, 0x16FF6 }, .{ 0x17000, 0x18CD5 }, .{ 0x18CFF, 0x18D1E },
    .{ 0x18D80, 0x18DF2 }, .{ 0x1AFF0, 0x1AFF3 }, .{ 0x1AFF5, 0x1AFFB },
    .{ 0x1AFFD, 0x1AFFE }, .{ 0x1B000, 0x1B122 }, .{ 0x1B132, 0x1B132 },
    .{ 0x1B150, 0x1B152 }, .{ 0x1B155, 0x1B155 }, .{ 0x1B164, 0x1B167 },
    .{ 0x1B170, 0x1B2FB }, .{ 0x1D300, 0x1D356 }, .{ 0x1D360, 0x1D376 },
    .{ 0x1F004, 0x1F004 }, .{ 0x1F0CF, 0x1F0CF }, .{ 0x1F18E, 0x1F18E },
    .{ 0x1F191, 0x1F19A }, .{ 0x1F200, 0x1F202 }, .{ 0x1F210, 0x1F23B },
    .{ 0x1F240, 0x1F248 }, .{ 0x1F250, 0x1F251 }, .{ 0x1F260, 0x1F265 },
    .{ 0x1F300, 0x1F320 }, .{ 0x1F32D, 0x1F335 }, .{ 0x1F337, 0x1F37C },
    .{ 0x1F37E, 0x1F393 }, .{ 0x1F3A0, 0x1F3CA }, .{ 0x1F3CF, 0x1F3D3 },
    .{ 0x1F3E0, 0x1F3F0 }, .{ 0x1F3F4, 0x1F3F4 }, .{ 0x1F3F8, 0x1F43E },
    .{ 0x1F440, 0x1F440 }, .{ 0x1F442, 0x1F4FC }, .{ 0x1F4FF, 0x1F53D },
    .{ 0x1F54B, 0x1F54E }, .{ 0x1F550, 0x1F567 }, .{ 0x1F57A, 0x1F57A },
    .{ 0x1F595, 0x1F596 }, .{ 0x1F5A4, 0x1F5A4 }, .{ 0x1F5FB, 0x1F64F },
    .{ 0x1F680, 0x1F6C5 }, .{ 0x1F6CC, 0x1F6CC }, .{ 0x1F6D0, 0x1F6D2 },
    .{ 0x1F6D5, 0x1F6D8 }, .{ 0x1F6DC, 0x1F6DF }, .{ 0x1F6EB, 0x1F6EC },
    .{ 0x1F6F4, 0x1F6FC }, .{ 0x1F7E0, 0x1F7EB }, .{ 0x1F7F0, 0x1F7F0 },
    .{ 0x1F90C, 0x1F93A }, .{ 0x1F93C, 0x1F945 }, .{ 0x1F947, 0x1F9FF },
    .{ 0x1FA70, 0x1FA7C }, .{ 0x1FA80, 0x1FA8A }, .{ 0x1FA8E, 0x1FAC6 },
    .{ 0x1FAC8, 0x1FAC8 }, .{ 0x1FACD, 0x1FADC }, .{ 0x1FADF, 0x1FAEA },
    .{ 0x1FAEF, 0x1FAF8 }, .{ 0x20000, 0x2FFFD }, .{ 0x30000, 0x3FFFD },
};

/// Zero-width codepoint ranges: combining + nonprint from go-runewidth, sorted.
/// Where ranges overlap (e.g. 0x180B-0x180E from nonprint covers 0x180B-0x180D
/// from combining), the superset is kept and duplicates removed.
const zerowidth_ranges = [_]Range{
    .{ 0x0000, 0x001F },   // nonprint
    .{ 0x007F, 0x009F },   // nonprint
    .{ 0x00AD, 0x00AD },   // nonprint
    .{ 0x0300, 0x036F },   // combining
    .{ 0x0483, 0x0489 },   // combining
    .{ 0x070F, 0x070F },   // nonprint
    .{ 0x07EB, 0x07F3 },   // combining
    .{ 0x0C00, 0x0C00 },   // combining
    .{ 0x0C04, 0x0C04 },   // combining
    .{ 0x0CF3, 0x0CF3 },   // combining
    .{ 0x0D00, 0x0D01 },   // combining
    .{ 0x135D, 0x135F },   // combining
    .{ 0x180B, 0x180F },   // nonprint 0x180B-0x180E + combining 0x180F
    .{ 0x1A7F, 0x1A7F },   // combining
    .{ 0x1AB0, 0x1ADD },   // combining
    .{ 0x1AE0, 0x1AEB },   // combining
    .{ 0x1B6B, 0x1B73 },   // combining
    .{ 0x1DC0, 0x1DFF },   // combining
    .{ 0x200B, 0x200F },   // nonprint
    .{ 0x2028, 0x202E },   // nonprint
    .{ 0x206A, 0x206F },   // nonprint
    .{ 0x20D0, 0x20F0 },   // combining
    .{ 0x2CEF, 0x2CF1 },   // combining
    .{ 0x2DE0, 0x2DFF },   // combining
    .{ 0x3099, 0x309A },   // combining
    .{ 0xA66F, 0xA672 },   // combining
    .{ 0xA674, 0xA67D },   // combining
    .{ 0xA69E, 0xA69F },   // combining
    .{ 0xA6F0, 0xA6F1 },   // combining
    .{ 0xA8E0, 0xA8F1 },   // combining
    .{ 0xD800, 0xDFFF },   // nonprint (surrogates)
    .{ 0xFE00, 0xFE0F },   // combining (variation selectors)
    .{ 0xFE20, 0xFE2F },   // combining
    .{ 0xFEFF, 0xFEFF },   // nonprint (BOM)
    .{ 0xFFF9, 0xFFFB },   // nonprint
    .{ 0xFFFE, 0xFFFF },   // nonprint
    .{ 0x101FD, 0x101FD }, // combining
    .{ 0x10376, 0x1037A }, // combining
    .{ 0x10EAB, 0x10EAC }, // combining
    .{ 0x10F46, 0x10F50 }, // combining
    .{ 0x10F82, 0x10F85 }, // combining
    .{ 0x11300, 0x11301 }, // combining
    .{ 0x1133B, 0x1133C }, // combining
    .{ 0x11366, 0x1136C }, // combining
    .{ 0x11370, 0x11374 }, // combining
    .{ 0x16AF0, 0x16AF4 }, // combining
    .{ 0x1CF00, 0x1CF2D }, // combining
    .{ 0x1CF30, 0x1CF46 }, // combining
    .{ 0x1D165, 0x1D169 }, // combining
    .{ 0x1D16D, 0x1D172 }, // combining
    .{ 0x1D17B, 0x1D182 }, // combining
    .{ 0x1D185, 0x1D18B }, // combining
    .{ 0x1D1AA, 0x1D1AD }, // combining
    .{ 0x1D242, 0x1D244 }, // combining
    .{ 0x1E000, 0x1E006 }, // combining
    .{ 0x1E008, 0x1E018 }, // combining
    .{ 0x1E01B, 0x1E021 }, // combining
    .{ 0x1E023, 0x1E024 }, // combining
    .{ 0x1E026, 0x1E02A }, // combining
    .{ 0x1E08F, 0x1E08F }, // combining
    .{ 0x1E8D0, 0x1E8D6 }, // combining
    .{ 0xE0100, 0xE01EF }, // combining (variation selectors supplement)
};

/// Returns the display width of a codepoint:
///   0 = zero-width (combining, nonprint)
///   2 = double-width (CJK, emoji, fullwidth forms)
///   1 = everything else
pub fn charWidth(cp: u32) u2 {
    if (inRanges(cp, &zerowidth_ranges)) return 0;
    if (inRanges(cp, &doublewidth_ranges)) return 2;
    return 1;
}

/// Binary search on sorted range pairs.
fn inRanges(cp: u32, ranges: []const Range) bool {
    var lo: usize = 0;
    var hi: usize = ranges.len;
    while (lo < hi) {
        const mid = lo + (hi - lo) / 2;
        if (cp > ranges[mid][1]) {
            lo = mid + 1;
        } else if (cp < ranges[mid][0]) {
            hi = mid;
        } else {
            return true;
        }
    }
    return false;
}

// --- Tests ---

test "ASCII is width 1" {
    try std.testing.expectEqual(@as(u2, 1), charWidth('A'));
    try std.testing.expectEqual(@as(u2, 1), charWidth('z'));
    try std.testing.expectEqual(@as(u2, 1), charWidth(' '));
}

test "CJK ideograph is width 2" {
    try std.testing.expectEqual(@as(u2, 2), charWidth(0x4E2D));
}

test "Hangul syllable is width 2" {
    try std.testing.expectEqual(@as(u2, 2), charWidth(0xAC00));
}

test "combining character is width 0" {
    try std.testing.expectEqual(@as(u2, 0), charWidth(0x0300));
}

test "nonprint character is width 0" {
    try std.testing.expectEqual(@as(u2, 0), charWidth(0x200B));
}

test "nerd font icon (PUA) is width 1" {
    try std.testing.expectEqual(@as(u2, 1), charWidth(0xF0122));
    try std.testing.expectEqual(@as(u2, 1), charWidth(0xF0000));
    try std.testing.expectEqual(@as(u2, 1), charWidth(0x100000));
}

test "fullwidth forms are width 2" {
    try std.testing.expectEqual(@as(u2, 2), charWidth(0xFF01));
}

test "emoji are width 2" {
    try std.testing.expectEqual(@as(u2, 2), charWidth(0x1F600));
    try std.testing.expectEqual(@as(u2, 2), charWidth(0x1F4A9));
}

test "box drawing is width 1" {
    try std.testing.expectEqual(@as(u2, 1), charWidth(0x2500));
}

test "variation selectors are width 0" {
    try std.testing.expectEqual(@as(u2, 0), charWidth(0xFE0F));
}
