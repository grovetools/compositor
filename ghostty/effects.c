#include <ghostty/vt.h>
#include <stdbool.h>
#include <string.h>

// Defined in Go via //export
extern void goWritePTYCallback(void* userdata, const unsigned char* data, unsigned long len);

static void c_write_pty(GhosttyTerminal terminal, void* userdata, const uint8_t* data, size_t len) {
    (void)terminal;
    goWritePTYCallback(userdata, data, len);
}

// Device attributes: report as a VT220-class terminal.
// This responds to DA1/DA2/DA3 queries that programs like fish, vim,
// tmux send on startup to determine terminal capabilities.
static bool c_device_attributes(GhosttyTerminal terminal, void* userdata,
                                GhosttyDeviceAttributes* out_attrs) {
    (void)terminal;
    (void)userdata;

    // DA1: VT220 with common features
    out_attrs->primary.conformance_level = GHOSTTY_DA_CONFORMANCE_VT220;
    out_attrs->primary.features[0] = GHOSTTY_DA_FEATURE_COLUMNS_132;
    out_attrs->primary.features[1] = GHOSTTY_DA_FEATURE_SELECTIVE_ERASE;
    out_attrs->primary.features[2] = GHOSTTY_DA_FEATURE_ANSI_COLOR;
    out_attrs->primary.num_features = 3;

    // DA2: VT220 type, version 1
    out_attrs->secondary.device_type = GHOSTTY_DA_DEVICE_TYPE_VT220;
    out_attrs->secondary.firmware_version = 1;
    out_attrs->secondary.rom_cartridge = 0;

    // DA3: unit id 0
    out_attrs->tertiary.unit_id = 0;

    return true;
}

// XTVERSION: report our application name.
static GhosttyString c_xtversion(GhosttyTerminal terminal, void* userdata) {
    (void)terminal;
    (void)userdata;
    return (GhosttyString){ .ptr = (const uint8_t*)"grove-terminal", .len = 14 };
}

void ghostty_setup_effects(GhosttyTerminal term, void* userdata) {
    ghostty_terminal_set(term, GHOSTTY_TERMINAL_OPT_USERDATA, userdata);
    ghostty_terminal_set(term, GHOSTTY_TERMINAL_OPT_WRITE_PTY, (const void*)c_write_pty);
    ghostty_terminal_set(term, GHOSTTY_TERMINAL_OPT_DEVICE_ATTRIBUTES, (const void*)c_device_attributes);
    ghostty_terminal_set(term, GHOSTTY_TERMINAL_OPT_XTVERSION, (const void*)c_xtversion);
}
