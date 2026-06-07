package daemon

import (
	"context"
	"maps"
	"slices"

	"google.golang.org/grpc"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
)

// SubscribeActive streams the in-flight run's events, or completes immediately
// when the daemon is idle. It is the explicit attach for a bare status view.
func (s *server) SubscribeActive(_ *desktopviaclydev1.SubscribeActiveRequest, stream grpc.ServerStreamingServer[desktopviaclydev1.ProgressEvent]) error {
	run := s.exec.active()
	if run == nil {
		return nil
	}
	return run.broadcaster.stream(stream.Context(), stream.Send)
}

// GetUpdaterStatus reports the tick loop's timing and the last per-target check,
// plus the currently running operation if any.
func (s *server) GetUpdaterStatus(_ context.Context, _ *desktopviaclydev1.GetUpdaterStatusRequest) (*desktopviaclydev1.GetUpdaterStatusResponse, error) {
	snap := s.state.snapshot()
	resp := &desktopviaclydev1.GetUpdaterStatusResponse{
		LastTickUnix:    snap.lastTickUnix,
		NextTickUnix:    snap.nextTickUnix,
		IntervalSeconds: snap.intervalSec,
	}
	if run := s.exec.active(); run != nil {
		resp.Running = true
		resp.ActiveOperation = run.operation
		resp.ActiveTarget = run.target
	}
	for _, id := range slices.Sorted(maps.Keys(snap.checks)) {
		check := snap.checks[id]
		resp.Targets = append(resp.Targets, &desktopviaclydev1.UpdaterTargetStatus{
			Target:           id,
			CurrentVersion:   check.currentVersion,
			AvailableVersion: check.availableVersion,
			UpdateAvailable:  check.updateAvailable,
			AppRunning:       check.appRunning,
			LastOutcome:      check.outcome,
			LastCheckedUnix:  check.checkedAtUnix,
		})
	}
	return resp, nil
}
