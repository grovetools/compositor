#include "_cgo_export.h"
#include "input_bridge.h"

CompositorInputCb input_bridge_get_cb(void) {
    return (CompositorInputCb)compositorOnInput;
}
