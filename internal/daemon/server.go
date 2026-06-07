package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/statusreport"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

var daemonLog = slog.With("component", "desktop-via-clyde", "subcomponent", "daemon")

// server implements desktopviaclydev1.DesktopServiceServer. It embeds the
// generated Unimplemented base so RPCs that are not wired yet return a clean
// Unimplemented status rather than panicking, and each method this daemon owns
// overrides that default.
type server struct {
	desktopviaclydev1.UnimplementedDesktopServiceServer
	exec *executor
}

func newServer() *server {
	return &server{
		UnimplementedDesktopServiceServer: desktopviaclydev1.UnimplementedDesktopServiceServer{},
		exec:                              newExecutor(),
	}
}

// GetStatus builds the per-target patch-state report and returns it as JSON so
// the client renders text or passes the JSON through unchanged. An empty target
// reports every configured app; a named target reports just that one.
func (s *server) GetStatus(ctx context.Context, req *desktopviaclydev1.GetStatusRequest) (*desktopviaclydev1.GetStatusResponse, error) {
	target := strings.TrimSpace(req.GetTarget())
	daemonLog.DebugContext(ctx, "daemon.get_status", "target", target)

	report, err := s.buildReport(ctx, target)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(report)
	if err != nil {
		daemonLog.ErrorContext(ctx, "daemon.get_status.marshal_failed", "err", err)
		return nil, status.Errorf(codes.Internal, "marshal status report: %v", err)
	}
	return &desktopviaclydev1.GetStatusResponse{ReportJson: string(body)}, nil
}

func (s *server) buildReport(ctx context.Context, target string) (statusreport.Report, error) {
	if target == "" {
		report, err := statusreport.BuildAll(ctx)
		if err != nil {
			daemonLog.ErrorContext(ctx, "daemon.get_status.build_all_failed", "err", err)
			return statusreport.Report{}, status.Errorf(codes.Internal, "build status report: %v", err)
		}
		return report, nil
	}
	appTarget := lookupAppTarget(target)
	if appTarget == nil {
		return statusreport.Report{}, status.Errorf(codes.NotFound, "unknown target %q", target)
	}
	report, err := statusreport.BuildTarget(ctx, *appTarget)
	if err != nil {
		daemonLog.ErrorContext(ctx, "daemon.get_status.build_target_failed", "err", err, "target", target)
		return statusreport.Report{}, status.Errorf(codes.Internal, "build status report for %s: %v", target, err)
	}
	return report, nil
}

// lookupAppTarget resolves one configured app target by id, returning nil when
// no target matches.
func lookupAppTarget(id string) *targets.Target {
	for _, target := range targets.All() {
		if target.ID == id {
			match := target
			return &match
		}
	}
	return nil
}
