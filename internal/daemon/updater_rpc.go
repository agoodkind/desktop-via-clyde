package daemon

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"google.golang.org/grpc"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
)

// SubscribeActive streams active run events, optionally filtered by operation
// and target. When no filter is supplied it multiplexes every active run onto
// one stream without relabeling the events.
func (s *server) SubscribeActive(req *desktopviaclydev1.SubscribeActiveRequest, stream grpc.ServerStreamingServer[desktopviaclydev1.ProgressEvent]) error {
	runs := s.exec.activeMatching(strings.TrimSpace(req.GetOperation()), strings.TrimSpace(req.GetTarget()))
	if len(runs) == 0 {
		return nil
	}
	return streamActiveRuns(stream.Context(), runs, stream.Send)
}

// GetUpdaterStatus reports the tick loop's timing and the last per-target check,
// plus every currently running operation.
func (s *server) GetUpdaterStatus(_ context.Context, _ *desktopviaclydev1.GetUpdaterStatusRequest) (*desktopviaclydev1.GetUpdaterStatusResponse, error) {
	snap := s.state.snapshot()
	resp := &desktopviaclydev1.GetUpdaterStatusResponse{
		LastTickUnix:    snap.lastTickUnix,
		NextTickUnix:    snap.nextTickUnix,
		IntervalSeconds: snap.intervalSec,
	}
	runs := s.exec.activeRuns()
	if len(runs) > 0 {
		resp.Running = true
		resp.ActiveOperation = runs[0].operation
		resp.ActiveTarget = runs[0].target
		for _, run := range runs {
			resp.ActiveRuns = append(resp.ActiveRuns, &desktopviaclydev1.ActiveRun{
				Operation: run.operation,
				Target:    run.target,
			})
		}
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

func streamActiveRuns(
	ctx context.Context,
	runs []*activeRun,
	send func(*desktopviaclydev1.ProgressEvent) error,
) error {
	if len(runs) == 1 {
		return runs[0].broadcaster.stream(ctx, send)
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		sendMu sync.Mutex
		wg     sync.WaitGroup
	)
	errCh := make(chan error, 1)
	for _, run := range runs {
		wg.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					daemonLog.ErrorContext(ctx, "daemon.subscribe_active.stream_panic", "err", fmt.Sprintf("panic: %v", recovered))
					select {
					case errCh <- fmt.Errorf("subscribe active panic: %v", recovered):
					default:
					}
					cancel()
				}
			}()
			err := run.broadcaster.stream(streamCtx, func(event *desktopviaclydev1.ProgressEvent) error {
				sendMu.Lock()
				defer sendMu.Unlock()
				return send(event)
			})
			if err == nil {
				return
			}
			select {
			case errCh <- err:
			default:
			}
			cancel()
		})
	}

	doneCh := make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				daemonLog.ErrorContext(ctx, "daemon.subscribe_active.wait_panic", "err", fmt.Sprintf("panic: %v", recovered))
			}
		}()
		wg.Wait()
		close(doneCh)
	}()

	select {
	case err := <-errCh:
		<-doneCh
		return err
	case <-doneCh:
		return nil
	}
}
