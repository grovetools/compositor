const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});

    // Base compositor module.
    const mod = b.addModule("compositor", .{
        .root_source_file = b.path("compositor.zig"),
        .target = target,
        .optimize = optimize,
        .link_libc = true,
    });

    const lib = b.addLibrary(.{
        .linkage = .static,
        .name = "compositor",
        .root_module = mod,
    });

    b.installArtifact(lib);

    // Extension module — terminal-specific code (ghostty blit, diff, input).
    const ext_mod = b.addModule("compositor_ext", .{
        .root_source_file = b.path("ext.zig"),
        .target = target,
        .optimize = optimize,
        .link_libc = true,
    });
    ext_mod.addImport("compositor_base", mod);
    ext_mod.addIncludePath(b.path("../lib/ghostty/include"));

    const ext_lib = b.addLibrary(.{
        .linkage = .static,
        .name = "grove-compositor-ext",
        .root_module = ext_mod,
    });

    b.installArtifact(ext_lib);

    // Zig unit tests.
    const ansi_test_mod = b.addModule("ansi_test", .{
        .root_source_file = b.path("ansi.zig"),
        .target = target,
        .optimize = optimize,
    });
    const ansi_tests = b.addTest(.{ .root_module = ansi_test_mod });

    const rw_test_mod = b.addModule("runewidth_test", .{
        .root_source_file = b.path("runewidth.zig"),
        .target = target,
        .optimize = optimize,
    });
    const runewidth_tests = b.addTest(.{ .root_module = rw_test_mod });

    const test_step = b.step("test", "Run unit tests");
    test_step.dependOn(&b.addRunArtifact(ansi_tests).step);
    test_step.dependOn(&b.addRunArtifact(runewidth_tests).step);
}
