#ifndef INPUT_BRIDGE_H
#define INPUT_BRIDGE_H

#include "compositor_ext.h"

// Returns the Go-exported compositorOnInput function as a CompositorInputCb.
// Defined in _input_bridge.c which includes the cgo-generated export header.
CompositorInputCb input_bridge_get_cb(void);

#endif
