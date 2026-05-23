// Package shimembed exposes the universal Mach-O shim binary built by
// shim/build.sh and embedded into the desktop-via-clyde binary.
package shimembed

import (
	_ "embed"
)

//go:embed shim
var ShimBinary []byte
