// Package shimembed exposes the Swift launch shim payload.
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
