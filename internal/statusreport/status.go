// Package statusreport builds the typed app patch-status report used by the
// root and per-target status commands.
package statusreport

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/bundleidentity"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"howett.net/plist"
)

var statusReportLog = slog.With("component", "desktop-via-clyde", "subcomponent", "statusreport")

var (
	readBundleVersionFn     = readBundleVersion
	runtimeBundleStatusesFn = runtimeBundleStatuses
)

type runtimeBundleState string

const (
	runtimeBundleStatePatched        runtimeBundleState = "patched"
	runtimeBundleStateVendor         runtimeBundleState = "vendor"
	runtimeBundleStateLocal          runtimeBundleState = "local"
	runtimeBundleStatePreserved      runtimeBundleState = "preserved"
	runtimeBundleStateUnknown        runtimeBundleState = "unknown"
	runtimeBundleStateTeamMismatch   runtimeBundleState = "team-mismatch"
	runtimeBundleStateSignatureError runtimeBundleState = "signature-error"
	runtimeBundleStateScanFailed     runtimeBundleState = "scan-failed"
)

// TargetStatus is one rendered target row.
type TargetStatus struct {
	ID              string                `json:"id"`
	State           string                `json:"state"`
	Version         string                `json:"version"`
	Notes           string                `json:"notes"`
	UpstreamSigning string                `json:"upstream_signing"`
	AppPath         string                `json:"app_path"`
	RuntimeBundles  []RuntimeBundleStatus `json:"runtime_bundles,omitempty"`
}

// RuntimeBundleStatus records signing coverage for one executable bundle.
type RuntimeBundleStatus struct {
	BundleID string `json:"bundle_id"`
	Path     string `json:"path"`
	TeamID   string `json:"team_id,omitempty"`
	State    string `json:"state"`
	Notes    string `json:"notes,omitempty"`
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
		notes := target.Notes
		if target.UpstreamSigning != "" {
			notes = notes + "; upstream=" + target.UpstreamSigning
		}
		if _, err := fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", target.ID, target.State, target.Version, notes); err != nil {
			statusReportLog.Warn("statusreport.write_row_failed", "err", err, "target", target.ID)
			return fmt.Errorf("write status row for %s: %w", target.ID, err)
		}
		for _, bundle := range target.RuntimeBundles {
			if _, err := fmt.Fprintf(out, "  runtime  %-9s  %-12s  %-48s  %s\n", bundle.State, bundle.TeamID, bundle.BundleID, bundle.Path); err != nil {
				statusReportLog.Warn("statusreport.write_runtime_row_failed", "err", err, "target", target.ID)
				return fmt.Errorf("write runtime status row for %s: %w", target.ID, err)
			}
		}
	}
	return nil
}

func buildTargetStatus(ctx context.Context, target targets.Target, multiState state.MultiState) (TargetStatus, error) {
	_ = ctx
	result := TargetStatus{
		ID:              target.ID,
		State:           "clean",
		Version:         "-",
		Notes:           "bundle present, no state entry",
		UpstreamSigning: "",
		AppPath:         target.AppPath,
		RuntimeBundles:  nil,
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
		result.RuntimeBundles = runtimeBundleStatusesFn(ctx, target, false)
		return result, nil
	}

	developmentSigned := developmentSigningOverlayActive(target)
	result.State = "patched"
	result.Version = entry.PatchedVersion
	result.Notes = fmt.Sprintf("signed-as=%q", entry.SignIdentity)
	if developmentSigned {
		result.Notes += "; development-signing active"
	}
	result.UpstreamSigning = upstreamSigningLabel(entry.OriginalDesignatedRequirement)
	result.RuntimeBundles = runtimeBundleStatusesFn(ctx, target, !developmentSigned)
	if drift := firstRuntimeBundleDrift(result.RuntimeBundles); drift != "" {
		result.State = "drifted"
		result.Notes += "; " + drift
	}

	currentVersion := readBundleVersionFn(target)
	if !developmentSigned {
		if _, err := os.Stat(paths.RealBinaryPath(target)); err != nil {
			if os.IsNotExist(err) {
				result.State = "drifted"
				result.Notes = result.Notes + "; " + target.ExecName + ".real missing"
				return result, nil
			}
			statusReportLog.ErrorContext(ctx, "statusreport.stat_real_binary_failed", "err", err, "target", target.ID, "path", paths.RealBinaryPath(target))
			return TargetStatus{}, fmt.Errorf("stat restored binary path %s: %w", paths.RealBinaryPath(target), err)
		}
	}
	if currentVersion != "" && currentVersion != entry.PatchedVersion {
		result.State = "drifted"
		result.Notes += fmt.Sprintf("; current version %s != patched %s", currentVersion, entry.PatchedVersion)
	}
	return result, nil
}

func runtimeBundleStatuses(ctx context.Context, target targets.Target, expectLocalTeam bool) []RuntimeBundleStatus {
	entries, err := bundleidentity.Scan(ctx, target.AppPath, bundleidentity.ScanOptions{
		IncludeSignatures: true,
		SignatureReader:   nil,
	})
	if err != nil {
		return []RuntimeBundleStatus{{
			BundleID: target.BundleID,
			Path:     target.AppPath,
			TeamID:   "",
			State:    string(runtimeBundleStateScanFailed),
			Notes:    err.Error(),
		}}
	}
	results := make([]RuntimeBundleStatus, 0, len(entries))
	localTeamID := strings.TrimSpace(paths.SignTeamID())
	for _, entry := range entries {
		if !entry.RuntimeCode {
			continue
		}
		item := RuntimeBundleStatus{
			BundleID: entry.BundleID,
			Path:     entry.RelativePath,
			TeamID:   entry.TeamID,
			State:    string(runtimeBundleStateUnknown),
			Notes:    "",
		}
		switch {
		case bundleidentity.IsPreserved(entry.RelativePath, target.PreservedNestedCodePaths):
			item.State = string(runtimeBundleStatePreserved)
		case entry.SignatureError != "":
			item.State = string(runtimeBundleStateSignatureError)
			item.Notes = entry.SignatureError
		case expectLocalTeam && entry.TeamID == localTeamID:
			item.State = string(runtimeBundleStatePatched)
		case expectLocalTeam:
			item.State = string(runtimeBundleStateTeamMismatch)
			item.Notes = fmt.Sprintf("signed by team %s, want %s", entry.TeamID, localTeamID)
		case entry.TeamID == localTeamID:
			item.State = string(runtimeBundleStateLocal)
		default:
			item.State = string(runtimeBundleStateVendor)
		}
		results = append(results, item)
	}
	return results
}

func developmentSigningOverlayActive(target targets.Target) bool {
	if target.DevelopmentSigning == nil || !target.DevelopmentSigning.Enabled {
		return false
	}
	profilePath := filepath.Join(target.AppPath, "Contents", "embedded.provisionprofile")
	_, err := os.Stat(profilePath)
	return err == nil
}

func firstRuntimeBundleDrift(bundles []RuntimeBundleStatus) string {
	for _, bundle := range bundles {
		switch runtimeBundleState(bundle.State) {
		case runtimeBundleStateTeamMismatch, runtimeBundleStateSignatureError, runtimeBundleStateScanFailed:
			return fmt.Sprintf("runtime identity drift %s at %s: %s", bundle.BundleID, bundle.Path, bundle.Notes)
		case runtimeBundleStatePatched,
			runtimeBundleStateVendor,
			runtimeBundleStateLocal,
			runtimeBundleStatePreserved,
			runtimeBundleStateUnknown:
			continue
		}
	}
	return ""
}

// upstreamSigningLabel renders the recorded upstream signing identity for
// display. It extracts the certificate team OU from the recorded designated
// requirement when present, falls back to the raw requirement string, and
// returns "unknown" when no upstream requirement was captured.
func upstreamSigningLabel(designatedRequirement string) string {
	trimmed := strings.TrimSpace(designatedRequirement)
	if trimmed == "" {
		return "unknown"
	}
	const teamMarker = `subject.OU] = `
	_, afterMarker, found := strings.Cut(trimmed, teamMarker)
	if !found {
		return trimmed
	}
	afterMarker = strings.TrimSpace(afterMarker)
	// codesign emits the team OU either quoted ("TEAMID") or bare (TEAMID
	// terminated by whitespace or a closing token in a compound requirement).
	if quoted, ok := strings.CutPrefix(afterMarker, `"`); ok {
		if team, _, ok := strings.Cut(quoted, `"`); ok {
			if trimmedTeam := strings.TrimSpace(team); trimmedTeam != "" {
				return trimmedTeam
			}
		}
		return trimmed
	}
	bareTeam := afterMarker
	if end := strings.IndexAny(afterMarker, " \t)"); end >= 0 {
		bareTeam = afterMarker[:end]
	}
	if trimmedTeam := strings.TrimSpace(bareTeam); trimmedTeam != "" {
		return trimmedTeam
	}
	return trimmed
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
