// Package shimembed exposes embedded helper payloads.
//
// ShimBinary is the MITM-routing shim that replaces an Electron app's main
// executable so launches route through the local Clyde MITM proxy.
package shimembed

import (
	_ "embed"
)

// ShimBinary is the embedded MITM-routing shim payload.
//
//go:embed shim
var ShimBinary []byte

// InjectorDylib is the generic DYLD_INSERT_LIBRARIES helper used by the Codex
// development-signing path.
//
//go:embed clyde-inject.dylib
var InjectorDylib []byte
