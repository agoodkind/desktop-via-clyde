package daemon

import (
	"context"

	"goodkind.io/gklog/correlation"
)

// correlatedContext rebuilds the caller's correlation from incoming gRPC
// metadata and attaches it to ctx, so the daemon's logs and the progress events
// it emits carry the same trace, span, and request IDs as the CLI command that
// triggered the RPC. Without it, the daemon would run each operation under a
// fresh, unrelated trace. Handlers call it at their entry rather than through an
// interceptor so the wiring stays plain functions with no context-bearing
// wrapper struct.
func correlatedContext(ctx context.Context) context.Context {
	return correlation.WithContext(ctx, correlation.FromIncomingMetadata(ctx))
}
