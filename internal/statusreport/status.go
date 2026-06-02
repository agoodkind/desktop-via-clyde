// Package statusreport builds the typed app patch-status report used by the
// root and per-target status commands.
package statusreport

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"howett.net/plist"
)

var statusReportLog = slog.With("component", "desktop-via-clyde", "subcomponent", "statusreport")

// TargetStatus is one rendered target row.
type TargetStatus struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Version string `json:"version"`
	Notes   string `json:"notes"`
	AppPath string `json:"app_path"`
}

// Report is the typed status payload for one or more targets.
type Report struct {
	StateFile string         `json:"state_file"`
	Targets   []TargetStatus `json:"targets"`
}

// BuildAll loads and builds one report for all configured app targets.
func BuildAll(ctx context.Context) (Report, error) {
	multiState, err := state.Load(paths.StateFile())
	if err != nil {
		statusReportLog.ErrorContext(ctx, "statusreport.load_state_failed", "err", err)
		return Report{}, fmt.Errorf("load state file %s: %w", paths.StateFile(), err)
	}
	report := Report{
		StateFile: paths.StateFile(),
		Targets:   make([]TargetStatus, 0, len(targets.All())),
	}
	for _, target := range targets.All() {
		item, err := buildTargetStatus(ctx, target, multiState)
		if err != nil {
			return Report{}, err
		}
		report.Targets = append(report.Targets, item)
	}
	return report, nil
}

// BuildTarget loads and builds one report for a single app target.
func BuildTarget(ctx context.Context, target targets.Target) (Report, error) {
	multiState, err := state.Load(paths.StateFile())
	if err != nil {
		statusReportLog.ErrorContext(ctx, "statusreport.load_target_state_failed", "err", err, "target", target.ID)
		return Report{}, fmt.Errorf("load state file %s: %w", paths.StateFile(), err)
	}
	item, err := buildTargetStatus(ctx, target, multiState)
	if err != nil {
		return Report{}, err
	}
	return Report{
		StateFile: paths.StateFile(),
		Targets:   []TargetStatus{item},
	}, nil
}

// WriteText renders one human-readable report.
func WriteText(out io.Writer, report Report) error {
	if _, err := fmt.Fprintf(out, "state file: %s\n", report.StateFile); err != nil {
		statusReportLog.Warn("statusreport.write_state_file_failed", "err", err)
		return fmt.Errorf("write state file line: %w", err)
	}
	if _, err := fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", "TARGET", "STATE", "VERSION", "NOTES"); err != nil {
		statusReportLog.Warn("statusreport.write_header_failed", "err", err)
		return fmt.Errorf("write status header: %w", err)
	}
	for _, target := range report.Targets {
		if _, err := fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", target.ID, target.State, target.Version, target.Notes); err != nil {
			statusReportLog.Warn("statusreport.write_row_failed", "err", err, "target", target.ID)
			return fmt.Errorf("write status row for %s: %w", target.ID, err)
		}
	}
	return nil
}

func buildTargetStatus(ctx context.Context, target targets.Target, multiState state.MultiState) (TargetStatus, error) {
	_ = ctx
	result := TargetStatus{
		ID:      target.ID,
		State:   "clean",
		Version: "-",
		Notes:   "bundle present, no state entry",
		AppPath: target.AppPath,
	}

	if _, err := os.Stat(target.AppPath); err != nil {
		if os.IsNotExist(err) {
			result.State = "absent"
			result.Notes = "bundle missing at " + target.AppPath
			return result, nil
		}
		statusReportLog.ErrorContext(ctx, "statusreport.stat_app_failed", "err", err, "target", target.ID, "path", target.AppPath)
		return TargetStatus{}, fmt.Errorf("stat app bundle %s: %w", target.AppPath, err)
	}

	entry, patched := multiState.Targets[target.ID]
	if !patched {
		return result, nil
	}

	result.State = "patched"
	result.Version = entry.PatchedVersion
	result.Notes = fmt.Sprintf("signed-as=%q", entry.SignIdentity)

	currentVersion := readBundleVersion(target)
	if _, err := os.Stat(paths.RealBinaryPath(target)); err != nil {
		if os.IsNotExist(err) {
			result.State = "drifted"
			result.Notes = result.Notes + "; " + target.ExecName + ".real missing"
			return result, nil
		}
		statusReportLog.ErrorContext(ctx, "statusreport.stat_real_binary_failed", "err", err, "target", target.ID, "path", paths.RealBinaryPath(target))
		return TargetStatus{}, fmt.Errorf("stat restored binary path %s: %w", paths.RealBinaryPath(target), err)
	}
	if currentVersion != "" && currentVersion != entry.PatchedVersion {
		result.State = "drifted"
		result.Notes += fmt.Sprintf("; current version %s != patched %s", currentVersion, entry.PatchedVersion)
	}
	return result, nil
}

func readBundleVersion(target targets.Target) string {
	var info struct {
		CFBundleVersion string `plist:"CFBundleVersion"`
	}
	data, err := os.ReadFile(paths.InfoPlistPath(target))
	if err != nil {
		return ""
	}
	if _, err := plist.Unmarshal(data, &info); err != nil {
		return ""
	}
	return info.CFBundleVersion
}
