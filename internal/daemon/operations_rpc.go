package daemon

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/hardreset"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

// RunUpgrade streams the upgrade operation for one target, or attaches to the
// in-flight operation when the daemon is already busy.
func (s *server) RunUpgrade(req *desktopviaclydev1.RunUpgradeRequest, stream grpc.ServerStreamingServer[desktopviaclydev1.ProgressEvent]) error {
	return s.streamOperation(stream, upgrade.AppUpgradeCapability, "upgrade", req.GetTarget(), req.GetFormat(), req.GetFlags())
}

// RunPatch streams the patch operation for one target.
func (s *server) RunPatch(req *desktopviaclydev1.RunPatchRequest, stream grpc.ServerStreamingServer[desktopviaclydev1.ProgressEvent]) error {
	return s.streamOperation(stream, patch.AppPatchCapability, "patch", req.GetTarget(), req.GetFormat(), req.GetFlags())
}

// RunHardReset streams the hard-reset operation for one target.
func (s *server) RunHardReset(req *desktopviaclydev1.RunHardResetRequest, stream grpc.ServerStreamingServer[desktopviaclydev1.ProgressEvent]) error {
	return s.streamOperation(stream, hardreset.AppHardResetCapability, "hard-reset", req.GetTarget(), req.GetFormat(), req.GetFlags())
}

// RunKeychainMigrate streams the keychain-migrate operation for one target.
func (s *server) RunKeychainMigrate(req *desktopviaclydev1.RunKeychainMigrateRequest, stream grpc.ServerStreamingServer[desktopviaclydev1.ProgressEvent]) error {
	return s.streamOperation(stream, patch.AppKeychainMigrateCapability, "keychain-migrate", req.GetTarget(), req.GetFormat(), req.GetFlags())
}

// streamOperation validates the target, starts or attaches to the run, and
// streams its events to the client. A request for an operation while another is
// in flight attaches to that run, so the daemon's single-operation serialization
// holds and a hand-run command renders the same events as the daemon's own tick.
func (s *server) streamOperation(
	stream grpc.ServerStreamingServer[desktopviaclydev1.ProgressEvent],
	capability string,
	operation string,
	target string,
	format string,
	flags *desktopviaclydev1.OperationFlags,
) error {
	ctx := stream.Context()
	if lookupAppTarget(target) == nil {
		return status.Errorf(codes.NotFound, "unknown target %q", target)
	}
	job := newOperationJob(capability, operation, target, format, flags)
	run := s.exec.startOrAttach(operation, target, job)
	return run.broadcaster.stream(ctx, stream.Send)
}
