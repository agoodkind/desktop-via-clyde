// Package daemon runs the desktop-via-clyde updater daemon and the thin client
// that drives it. The daemon owns every operation behind a unix-socket gRPC
// control plane, so the CLI is a client that invokes an RPC and renders the
// streamed progress through the same live model used for in-process runs.
package daemon
