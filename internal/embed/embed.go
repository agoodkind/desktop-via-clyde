// Package shimembed exposes the universal Mach-O binaries built by the
// Makefile shim targets and embedded into the desktop-via-clyde binary.
//
// ShimBinary is the MITM-routing shim that replaces an Electron app's main
// executable so launches route through the local Clyde MITM proxy. It is
// built by shim/build.sh from the Swift sources under shim/.
//
// StdioTeeShim is the stdio-tee shim that replaces a configured child
// process, runs the original as a child, and tees the stdin and stdout
// streams to log files. It is built by `make stdio-tee-shim` from the Go
// sources under cmd/dvc-stdio-tee-shim/.
package shimembed

import (
	_ "embed"
)

// ShimBinary is the embedded MITM-routing shim payload.
//
//go:embed shim
var ShimBinary []byte

// StdioTeeShim is the embedded stdio tee shim payload.
//
//go:embed dvc-stdio-tee-shim
var StdioTeeShim []byte
