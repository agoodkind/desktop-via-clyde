package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/statusreport"
)

func setupConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	config.SetCurrent(&spec.Config{
		Signing: spec.SigningSpec{Identity: "Test Identity", TeamID: "TEST123456"},
		Apps: map[string]spec.AppSpec{
			"demo": {
				ID:       "demo",
				AppPath:  filepath.Join(t.TempDir(), "Demo.app"),
				BundleID: "example.demo",
				ExecName: "Demo",
				Command:  spec.CommandSpec{Use: "demo"},
				Operations: map[string]spec.OperationSpec{
					"status": {ID: "status", Use: "status", Capability: "app.status"},
				},
			},
		},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })
}

func decodeReport(t *testing.T, body string) statusreport.Report {
	t.Helper()
	var report statusreport.Report
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	return report
}

func TestServerGetStatusAllTargets(t *testing.T) {
	setupConfig(t)
	resp, err := newServer().GetStatus(context.Background(), &desktopviaclydev1.GetStatusRequest{})
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	report := decodeReport(t, resp.GetReportJson())
	if len(report.Targets) != 1 || report.Targets[0].ID != "demo" {
		t.Fatalf("report targets = %+v, want one demo target", report.Targets)
	}
}

func TestServerGetStatusNamedTarget(t *testing.T) {
	setupConfig(t)
	resp, err := newServer().GetStatus(context.Background(), &desktopviaclydev1.GetStatusRequest{Target: "demo"})
	if err != nil {
		t.Fatalf("GetStatus(demo): %v", err)
	}
	report := decodeReport(t, resp.GetReportJson())
	if len(report.Targets) != 1 || report.Targets[0].ID != "demo" {
		t.Fatalf("named report targets = %+v, want one demo target", report.Targets)
	}
}

func TestServerGetStatusUnknownTarget(t *testing.T) {
	setupConfig(t)
	_, err := newServer().GetStatus(context.Background(), &desktopviaclydev1.GetStatusRequest{Target: "nope"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("GetStatus(nope) code = %v, want NotFound", status.Code(err))
	}
}
