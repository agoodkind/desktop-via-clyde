package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"goodkind.io/gklog/correlation"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/hardreset"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

const daemonProbeTimeout = 2 * time.Second

// RunOperation is the CLI's OperationRunner. For the daemon-owned app operations
// it streams the run from the daemon and drives req.Progress so the existing
// session renders it exactly like a local run, attaching to the in-flight run
// when the daemon is already busy. For every other capability, or when the
// daemon is not running, it executes the operation in-process so the CLI keeps
// working whether or not the daemon is installed.
func RunOperation(ctx context.Context, req operations.Request) error {
	if !isDaemonStreamingCapability(req.Capability) || req.App == nil || req.Progress == nil {
		return runLocal(ctx, req)
	}
	if !daemonReachable(ctx) {
		daemonLog.DebugContext(ctx, "daemon.client.unreachable_fallback_local", "capability", req.Capability)
		return runLocal(ctx, req)
	}
	return streamDaemonOperation(ctx, req)
}

func runLocal(ctx context.Context, req operations.Request) error {
	if err := operations.Run(ctx, req); err != nil {
		daemonLog.WarnContext(ctx, "daemon.client.local_operation_failed", "err", err, "capability", req.Capability)
		return fmt.Errorf("run operation %s: %w", req.Capability, err)
	}
	return nil
}

// isDaemonStreamingCapability reports whether a capability is one of the
// app-targeted progress operations the daemon serves as a stream.
func isDaemonStreamingCapability(capability string) bool {
	switch capability {
	case upgrade.AppUpgradeCapability,
		patch.AppPatchCapability,
		hardreset.AppHardResetCapability,
		patch.AppKeychainMigrateCapability:
		return true
	default:
		return false
	}
}

// daemonReachable probes the daemon with a short-timeout GetStatus. Any reply
// other than Unavailable means the daemon is up.
func daemonReachable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(correlation.NewOutgoingContext(ctx), daemonProbeTimeout)
	defer cancel()
	conn, client, err := dial()
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	_, err = client.GetStatus(probeCtx, &desktopviaclydev1.GetStatusRequest{})
	return status.Code(err) != codes.Unavailable
}

func dial() (*grpc.ClientConn, desktopviaclydev1.DesktopServiceClient, error) {
	socketPath := paths.DaemonSocketPath()
	conn, err := grpc.NewClient("unix://"+socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		daemonLog.Error("daemon.client.dial_failed", "err", err, "socket", socketPath)
		return nil, nil, fmt.Errorf("connect daemon at %s: %w", socketPath, err)
	}
	return conn, desktopviaclydev1.NewDesktopServiceClient(conn), nil
}

func streamDaemonOperation(ctx context.Context, req operations.Request) error {
	conn, client, err := dial()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	// Attach the CLI command's correlation to the outgoing call so the daemon
	// runs the operation under the same trace and span IDs shown in the header.
	stream, err := openOperationStream(correlation.NewOutgoingContext(ctx), client, req)
	if err != nil {
		return err
	}
	return renderOperationStream(stream, req.Progress)
}

// openOperationStream calls the per-capability streaming RPC for one app target.
func openOperationStream(
	ctx context.Context,
	client desktopviaclydev1.DesktopServiceClient,
	req operations.Request,
) (grpc.ServerStreamingClient[desktopviaclydev1.ProgressEvent], error) {
	target := req.App.ID
	format := string(req.Format)
	flags := flagsToProto(req.Flags)

	var stream grpc.ServerStreamingClient[desktopviaclydev1.ProgressEvent]
	var err error
	switch req.Capability {
	case upgrade.AppUpgradeCapability:
		stream, err = client.RunUpgrade(ctx, &desktopviaclydev1.RunUpgradeRequest{Target: target, Format: format, Flags: flags})
	case patch.AppPatchCapability:
		stream, err = client.RunPatch(ctx, &desktopviaclydev1.RunPatchRequest{Target: target, Format: format, Flags: flags})
	case hardreset.AppHardResetCapability:
		stream, err = client.RunHardReset(ctx, &desktopviaclydev1.RunHardResetRequest{Target: target, Format: format, Flags: flags})
	case patch.AppKeychainMigrateCapability:
		stream, err = client.RunKeychainMigrate(ctx, &desktopviaclydev1.RunKeychainMigrateRequest{Target: target, Format: format, Flags: flags})
	default:
		return nil, fmt.Errorf("capability %q has no daemon streaming operation", req.Capability)
	}
	if err != nil {
		if streamStatus, ok := status.FromError(err); ok && streamStatus.Code() == codes.FailedPrecondition {
			return nil, errors.New(streamStatus.Message())
		}
		daemonLog.ErrorContext(ctx, "daemon.client.open_stream_failed", "err", err, "capability", req.Capability)
		return nil, fmt.Errorf("open %s stream: %w", req.Capability, err)
	}
	return stream, nil
}

// renderOperationStream replays the daemon's run onto progress so the session
// renders it. Lifecycle events are owned by the client session, so only the
// step-level events and the terminal outcome are mapped here. A failed terminal
// event is returned as an error so the session marks the target failed exactly
// like a local run.
func renderOperationStream(
	stream grpc.ServerStreamingClient[desktopviaclydev1.ProgressEvent],
	progress clioutput.Progress,
) error {
	failureDetail := ""
	failed := false
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			daemonLog.Error("daemon.client.stream_recv_failed", "err", err)
			return fmt.Errorf("receive progress event: %w", err)
		}
		if detail, isFailure := applyProgressEvent(event, progress); isFailure {
			failed = true
			failureDetail = detail
		}
	}
	if failed {
		return errors.New(failureDetail)
	}
	return nil
}

// applyProgressEvent maps one streamed event onto the progress sink, returning
// the failure detail when the event is a failed terminal event.
func applyProgressEvent(event *desktopviaclydev1.ProgressEvent, progress clioutput.Progress) (string, bool) {
	switch clioutput.EventType(event.GetType()) {
	case clioutput.EventStepDone:
		progress.Step(event.GetDetail())
	case clioutput.EventStepSkipped:
		progress.Skip(event.GetDetail())
	case clioutput.EventStepFailed:
		progress.Fail(event.GetDetail())
	case clioutput.EventTargetDone:
		if event.GetStatus() == string(clioutput.OutcomeFailed) {
			return event.GetDetail(), true
		}
		progress.SetOutcome(clioutput.Outcome(event.GetStatus()), event.GetDetail())
	case clioutput.EventRunStarted,
		clioutput.EventTargetQueued,
		clioutput.EventTargetStarted,
		clioutput.EventStepStarted,
		clioutput.EventRunDone:
		// Lifecycle events are owned by the client session and are regenerated
		// from the progress calls above, so they are ignored here.
	}
	return "", false
}

// flagsToProto serializes parsed flag values onto the wire.
func flagsToProto(flags operations.FlagValues) *desktopviaclydev1.OperationFlags {
	return &desktopviaclydev1.OperationFlags{
		Strings: flags.StringValues(),
		Bools:   flags.BoolValues(),
	}
}
