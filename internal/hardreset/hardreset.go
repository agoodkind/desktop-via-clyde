// Package hardreset resets per-bundle macOS privacy grants for one target.
package hardreset

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/appguard"
	"goodkind.io/desktop-via-clyde/internal/bundleidentity"
	"goodkind.io/desktop-via-clyde/internal/bundlemutate"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

var hardResetLog = slog.With("component", "desktop-via-clyde", "subcomponent", "hardreset")

const (
	// AppHardResetCapability is the operation capability for privacy hard resets.
	AppHardResetCapability = "app.hard-reset"
)

type artifactAction string

const (
	artifactActionRemove      artifactAction = "remove"
	artifactActionRemoveSudo  artifactAction = "remove_sudo"
	artifactActionRestoreReal artifactAction = "restore_real"
)

// systemTCCDatabasePath points at the machine-wide privacy database. It is a
// package var so unit tests can redirect it at a temp file and never touch the
// real, SIP-protected database.
var systemTCCDatabasePath = "/Library/Application Support/com.apple.TCC/TCC.db"

// commandRunner executes an external command and returns its combined output.
// It is a package-level seam so tests can assert and stub privileged calls
// (sqlite3, sudo, tccutil, killall) without executing them against the host.
type commandRunner func(ctx context.Context, name string, args []string, stdin string) ([]byte, error)

type mutateBundleFunc func(ctx context.Context, target targets.Target, opts Options, fn func(context.Context) error) error

var (
	runCommand      commandRunner    = execCommand
	mutateBundle    mutateBundleFunc = defaultMutateBundle
	runBundleMutate                  = bundlemutate.Mutate
)

func execCommand(ctx context.Context, name string, args []string, stdin string) ([]byte, error) {
	hardResetLog.DebugContext(ctx, "hardreset.run_command.boundary", "name", name, "args", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		hardResetLog.WarnContext(ctx, "hardreset.run_command_failed", "name", name, "err", err, "output", strings.TrimSpace(string(output)))
		return output, fmt.Errorf("run command %s: %w", name, err)
	}
	return output, nil
}

// RegisterOperations links hard-reset operation capabilities.
func RegisterOperations() error {
	if !catalog.HasOperationCapability(AppHardResetCapability) {
		if err := catalog.RegisterOperationCapability(AppHardResetCapability); err != nil {
			return logHardResetRegistrationError("register hard-reset capability", err)
		}
	}
	if err := operations.Register(AppHardResetCapability, Operation); err != nil {
		return logHardResetRegistrationError("register hard-reset operation", err)
	}
	return nil
}

// Operation runs the hard-reset operation for one configured target.
func Operation(ctx context.Context, req operations.Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	out := req.Out
	if out == nil {
		out = os.Stdout
	}
	if err := Run(ctx, *req.App, Options{
		DryRun:            req.Flags.Bool("dry-run"),
		Out:               out,
		LogOut:            req.LogOut,
		CloseBeforeMutate: !req.Flags.Bool("background"),
	}); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.operation_failed", "err", err)
		return fmt.Errorf("hard-reset operation: %w",
			operations.Error(ctx, "operations.hard_reset_failed", "hard-reset app", err))
	}
	return nil
}

// Options controls one hard-reset invocation.
type Options struct {
	DryRun            bool
	Out               io.Writer
	LogOut            io.Writer
	CloseBeforeMutate bool
}

// Plan records the TCC reset commands for one target.
type Plan struct {
	TargetID         string
	AppPath          string
	BundleIDs        []string
	TCCUtilBundleIDs []string
	Artifacts        []Artifact
}

// Artifact records one filesystem side effect created by patch or upgrade.
type Artifact struct {
	Kind   string
	Path   string
	Action artifactAction
	Glob   bool
}

// BuildPlan builds the hard-reset plan without mutating system state.
func BuildPlan(ctx context.Context, target targets.Target) (Plan, error) {
	bundleIDs, tccUtilBundleIDs, err := identitySets(ctx, target)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		TargetID:         target.ID,
		AppPath:          target.AppPath,
		BundleIDs:        bundleIDs,
		TCCUtilBundleIDs: tccUtilBundleIDs,
		Artifacts:        buildArtifacts(target),
	}, nil
}

// Run executes or prints the hard-reset plan.
func Run(ctx context.Context, target targets.Target, opts Options) error {
	hardResetLog.InfoContext(ctx, "hardreset.run.boundary", "target", target.ID, "app_path", target.AppPath, "dry_run", opts.DryRun)
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	opts.Out = out
	plan, err := BuildPlan(ctx, target)
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.build_plan_failed", "target", target.ID, "err", err)
		return err
	}
	if len(plan.BundleIDs) == 0 {
		return fmt.Errorf("target %s has no bundle IDs to reset", target.ID)
	}
	if len(plan.TCCUtilBundleIDs) == 0 {
		return fmt.Errorf("target %s has no tccutil bundle IDs to reset", target.ID)
	}
	if _, err := fmt.Fprintf(out, "target=%s hard-reset app=%s\n", plan.TargetID, plan.AppPath); err != nil {
		return fmt.Errorf("write hard-reset header: %w", err)
	}
	if _, err := fmt.Fprintf(out, "bundle_ids=%s\n", strings.Join(plan.BundleIDs, ",")); err != nil {
		return fmt.Errorf("write hard-reset bundle IDs: %w", err)
	}
	if _, err := fmt.Fprintf(out, "tccutil_bundle_ids=%s\n", strings.Join(plan.TCCUtilBundleIDs, ",")); err != nil {
		return fmt.Errorf("write hard-reset tccutil bundle IDs: %w", err)
	}
	for _, artifact := range plan.Artifacts {
		if _, err := fmt.Fprintf(
			out,
			"artifact action=%s kind=%s path=%s glob=%t\n",
			artifact.Action,
			artifact.Kind,
			artifact.Path,
			artifact.Glob,
		); err != nil {
			return fmt.Errorf("write hard-reset artifact: %w", err)
		}
	}
	return mutateBundle(ctx, target, opts, func(mutateCtx context.Context) error {
		return executePlan(mutateCtx, target, opts, plan, out)
	})
}

func executePlan(ctx context.Context, target targets.Target, opts Options, plan Plan, out io.Writer) error {
	if err := resetTCCUtilAll(ctx, opts, plan.TCCUtilBundleIDs); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.tccutil_reset_all_failed", "target", target.ID, "err", err)
		return err
	}
	if err := restartTCCD(ctx, opts, "before_tcc_db_cleanup"); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.restart_tccd_before_cleanup_failed", "target", target.ID, "err", err)
		return err
	}
	if err := deleteUserTCCRows(ctx, opts, plan.BundleIDs); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.delete_user_tcc_rows_failed", "target", target.ID, "err", err)
		return err
	}
	if err := deleteSystemTCCRows(ctx, opts, plan.BundleIDs); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.delete_system_tcc_rows_failed", "target", target.ID, "err", err)
		return err
	}
	if err := restartTCCD(ctx, opts, "after_tcc_db_cleanup"); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.restart_tccd_after_cleanup_failed", "target", target.ID, "err", err)
		return err
	}
	if err := verifyNoTCCRows(ctx, opts, plan.BundleIDs); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.verify_tcc_rows_failed", "target", target.ID, "err", err)
		return err
	}
	if err := resetArtifacts(ctx, opts, plan.Artifacts); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.reset_artifacts_failed", "target", target.ID, "err", err)
		return err
	}
	if err := removePatchState(ctx, opts, plan.TargetID); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.remove_patch_state_failed", "target", target.ID, "err", err)
		return err
	}
	_, err := fmt.Fprintln(out, "aftercare=report-only")
	if err != nil {
		return fmt.Errorf("write hard-reset aftercare: %w", err)
	}
	return nil
}

func defaultMutateBundle(
	ctx context.Context,
	target targets.Target,
	opts Options,
	fn func(context.Context) error,
) error {
	policy := bundlemutate.PolicyDefer
	if opts.CloseBeforeMutate {
		policy = bundlemutate.PolicyClose
	}
	err := runBundleMutate(ctx, target, policy, bundlemutate.Options{
		DryRun:  opts.DryRun,
		Out:     opts.Out,
		Timeout: 0,
	}, fn)
	if errors.Is(err, appguard.ErrAppRunning) {
		if opts.CloseBeforeMutate {
			return err
		}
		if opts.Out != nil {
			_, _ = fmt.Fprintf(opts.Out, "deferred: target=%s app running at mutation time; retry when closed\n", target.ID)
		}
		return nil
	}
	return err
}

func identitySets(ctx context.Context, target targets.Target) ([]string, []string, error) {
	fullValues := make([]string, 0, 1+len(target.BundleIDAliases)+len(target.HelperBundleIDs))
	tccUtilValues := make([]string, 0, 1+len(target.BundleIDAliases))
	fullValues = append(fullValues, target.BundleID)
	fullValues = append(fullValues, target.BundleIDAliases...)
	fullValues = append(fullValues, target.HelperBundleIDs...)
	tccUtilValues = append(tccUtilValues, target.BundleID)
	tccUtilValues = append(tccUtilValues, target.BundleIDAliases...)

	entries, err := bundleidentity.Scan(ctx, target.AppPath, bundleidentity.ScanOptions{
		IncludeSignatures: false,
		SignatureReader:   nil,
	})
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.identity_set_scan_failed", "target", target.ID, "app_path", target.AppPath, "err", err)
		return nil, nil, fmt.Errorf("scan runtime bundle identities: %w", err)
	}
	for _, entry := range entries {
		if entry.RuntimeCode && entry.BundleID != "" {
			fullValues = append(fullValues, entry.BundleID)
		}
		if entry.RuntimeCode && entry.BundleID != "" && entry.PackageType == "APPL" {
			tccUtilValues = append(tccUtilValues, entry.BundleID)
		}
	}
	return sortedUnique(fullValues), sortedUnique(tccUtilValues), nil
}

func buildArtifacts(target targets.Target) []Artifact {
	artifacts := []Artifact{
		{
			Kind:   "upgrade_staging",
			Path:   filepath.Join(paths.StateRoot(), "upgrade-staging", target.ID+"-*"),
			Action: artifactActionRemove,
			Glob:   true,
		},
	}
	if target.Extensions.ComputerUse != nil {
		policy := target.Extensions.ComputerUse
		if policy.AuthPluginPath != "" {
			artifacts = append(artifacts, Artifact{
				Kind:   "computer_use_auth_plugin",
				Path:   policy.AuthPluginPath,
				Action: artifactActionRemoveSudo,
				Glob:   false,
			})
		}
		if policy.AppPathFromHome != "" {
			artifacts = append(artifacts, Artifact{
				Kind:   "computer_use_helper",
				Path:   filepath.Join(paths.Home(), filepath.FromSlash(policy.AppPathFromHome)),
				Action: artifactActionRemove,
				Glob:   false,
			})
		}
		for _, cacheGlob := range policy.CacheAppGlobsFromHome {
			artifacts = append(artifacts, Artifact{
				Kind:   "computer_use_cache_helper",
				Path:   filepath.Join(paths.Home(), filepath.FromSlash(cacheGlob)),
				Action: artifactActionRemove,
				Glob:   true,
			})
		}
	}
	if target.Extensions.BundledCLITee != nil {
		for _, teePath := range bundledCLITeePaths(target) {
			artifacts = append(artifacts, Artifact{
				Kind:   "bundled_cli_tee",
				Path:   teePath,
				Action: artifactActionRestoreReal,
				Glob:   strings.Contains(teePath, "*"),
			})
		}
	}
	artifacts = append(artifacts, Artifact{
		Kind:   "app_bundle",
		Path:   target.AppPath,
		Action: artifactActionRemove,
		Glob:   false,
	})
	return artifacts
}

func bundledCLITeePaths(target targets.Target) []string {
	tee := target.Extensions.BundledCLITee
	if tee.BundledCLIPath != "" {
		return []string{tee.BundledCLIPath}
	}
	if tee.AppSupportDir == "" || tee.BundledCLIRel == "" {
		return nil
	}
	if tee.VersionDir != "" {
		return []string{filepath.Join(tee.AppSupportDir, tee.VersionDir, filepath.FromSlash(tee.BundledCLIRel))}
	}
	return []string{filepath.Join(tee.AppSupportDir, "*", filepath.FromSlash(tee.BundledCLIRel))}
}

func removePatchState(ctx context.Context, opts Options, targetID string) error {
	stateFile := paths.StateFile()
	hardResetLog.InfoContext(ctx, "hardreset.remove_patch_state.boundary", "target", targetID, "state_file", stateFile, "dry_run", opts.DryRun)
	if opts.DryRun {
		_, err := fmt.Fprintf(opts.Out, "dry-run: remove patch_state target=%s state_file=%s\n", targetID, stateFile)
		if err != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_dry_run_state_remove_failed", "err", err)
			return fmt.Errorf("write dry-run patch state removal: %w", err)
		}
		return nil
	}
	if err := state.Update(stateFile, func(ms state.MultiState) (state.MultiState, error) {
		delete(ms.Targets, targetID)
		return ms, nil
	}); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.patch_state_remove_failed", "state_file", stateFile, "target", targetID, "err", err)
		return fmt.Errorf("remove patch state for %s: %w", targetID, err)
	}
	_, err := fmt.Fprintf(opts.Out, "patch_state_removed target=%s state_file=%s\n", targetID, stateFile)
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.write_state_removed_failed", "err", err)
		return fmt.Errorf("write patch state removal: %w", err)
	}
	return nil
}

func resetArtifacts(ctx context.Context, opts Options, artifacts []Artifact) error {
	for _, artifact := range artifacts {
		if opts.DryRun {
			if err := writeDryRunArtifact(opts, artifact); err != nil {
				return err
			}
			continue
		}
		candidates, err := artifactCandidates(artifact)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			if _, writeErr := fmt.Fprintf(opts.Out, "artifact action=%s kind=%s path=%s status=missing\n", artifact.Action, artifact.Kind, artifact.Path); writeErr != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_missing_artifact_status_failed", "kind", artifact.Kind, "path", artifact.Path, "err", writeErr)
				return fmt.Errorf("write missing artifact status: %w", writeErr)
			}
			continue
		}
		for _, candidate := range candidates {
			if err := resetArtifact(ctx, opts, artifact, candidate); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeDryRunArtifact(opts Options, artifact Artifact) error {
	_, err := fmt.Fprintf(
		opts.Out,
		"dry-run: artifact action=%s kind=%s path=%s glob=%t\n",
		artifact.Action,
		artifact.Kind,
		artifact.Path,
		artifact.Glob,
	)
	if err != nil {
		hardResetLog.Error("hardreset.write_dry_run_artifact_failed", "err", err)
		return fmt.Errorf("write dry-run artifact: %w", err)
	}
	return nil
}

func artifactCandidates(artifact Artifact) ([]string, error) {
	if !artifact.Glob {
		return []string{artifact.Path}, nil
	}
	candidates := map[string]bool{}
	matches, err := filepath.Glob(artifact.Path)
	if err != nil {
		hardResetLog.Error("hardreset.artifact_glob_failed", "path", artifact.Path, "err", err)
		return nil, fmt.Errorf("glob artifact %s: %w", artifact.Path, err)
	}
	for _, match := range matches {
		candidates[match] = true
	}
	if artifact.Action == artifactActionRestoreReal {
		realMatches, err := filepath.Glob(artifact.Path + ".real")
		if err != nil {
			hardResetLog.Error("hardreset.artifact_real_glob_failed", "path", artifact.Path+".real", "err", err)
			return nil, fmt.Errorf("glob artifact %s.real: %w", artifact.Path, err)
		}
		for _, match := range realMatches {
			candidates[strings.TrimSuffix(match, ".real")] = true
		}
	}
	results := make([]string, 0, len(candidates))
	for candidate := range candidates {
		results = append(results, candidate)
	}
	sort.Strings(results)
	return results, nil
}

func resetArtifact(ctx context.Context, opts Options, artifact Artifact, path string) error {
	switch artifact.Action {
	case artifactActionRemove:
		return removeArtifact(ctx, opts, artifact, path)
	case artifactActionRemoveSudo:
		return removeArtifactWithSudo(ctx, opts, artifact, path)
	case artifactActionRestoreReal:
		return restoreRealArtifact(ctx, opts, artifact, path)
	default:
		return fmt.Errorf("unknown artifact action %q for %s", artifact.Action, path)
	}
}

func removeArtifact(ctx context.Context, opts Options, artifact Artifact, path string) error {
	hardResetLog.InfoContext(ctx, "hardreset.remove_artifact.boundary", "kind", artifact.Kind, "path", path)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			_, writeErr := fmt.Fprintf(opts.Out, "artifact action=%s kind=%s path=%s status=missing\n", artifact.Action, artifact.Kind, path)
			if writeErr != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_missing_artifact_status_failed", "kind", artifact.Kind, "path", path, "err", writeErr)
				return fmt.Errorf("write missing artifact status: %w", writeErr)
			}
			return nil
		}
		hardResetLog.ErrorContext(ctx, "hardreset.artifact_stat_failed", "kind", artifact.Kind, "path", path, "err", err)
		return fmt.Errorf("stat artifact %s: %w", path, err)
	}
	if err := os.RemoveAll(path); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.artifact_remove_failed", "kind", artifact.Kind, "path", path, "err", err)
		return fmt.Errorf("remove artifact %s: %w", path, err)
	}
	_, err := fmt.Fprintf(opts.Out, "artifact action=%s kind=%s path=%s status=removed\n", artifact.Action, artifact.Kind, path)
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.write_artifact_removed_failed", "kind", artifact.Kind, "path", path, "err", err)
		return fmt.Errorf("write artifact removal status: %w", err)
	}
	return nil
}

func removeArtifactWithSudo(ctx context.Context, opts Options, artifact Artifact, path string) error {
	hardResetLog.InfoContext(ctx, "hardreset.remove_sudo_artifact.boundary", "kind", artifact.Kind, "path", path)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			_, writeErr := fmt.Fprintf(opts.Out, "artifact action=%s kind=%s path=%s status=missing\n", artifact.Action, artifact.Kind, path)
			if writeErr != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_missing_sudo_artifact_status_failed", "kind", artifact.Kind, "path", path, "err", writeErr)
				return fmt.Errorf("write missing sudo artifact status: %w", writeErr)
			}
			return nil
		}
		hardResetLog.ErrorContext(ctx, "hardreset.sudo_artifact_stat_failed", "kind", artifact.Kind, "path", path, "err", err)
		return fmt.Errorf("stat sudo artifact %s: %w", path, err)
	}
	output, err := runCommand(ctx, "/usr/bin/sudo", []string{"/bin/rm", "-rf", path}, "")
	writeCommandLog(opts, "$ /usr/bin/sudo /bin/rm -rf "+path)
	writeCommandLog(opts, strings.TrimSpace(string(output)))
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.remove_sudo_artifact_failed", "kind", artifact.Kind, "path", path, "err", err, "output", strings.TrimSpace(string(output)))
		return fmt.Errorf("remove sudo artifact %s: %w", path, err)
	}
	_, writeErr := fmt.Fprintf(opts.Out, "artifact action=%s kind=%s path=%s status=removed\n", artifact.Action, artifact.Kind, path)
	if writeErr != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.write_sudo_artifact_removed_failed", "kind", artifact.Kind, "path", path, "err", writeErr)
		return fmt.Errorf("write sudo artifact removal status: %w", writeErr)
	}
	return nil
}

func restoreRealArtifact(ctx context.Context, opts Options, artifact Artifact, path string) error {
	hardResetLog.InfoContext(ctx, "hardreset.restore_real_artifact.boundary", "kind", artifact.Kind, "path", path)
	realPath := path + ".real"
	if _, err := os.Stat(realPath); err != nil {
		if os.IsNotExist(err) {
			_, writeErr := fmt.Fprintf(opts.Out, "artifact action=%s kind=%s path=%s status=missing\n", artifact.Action, artifact.Kind, path)
			if writeErr != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_missing_restore_artifact_status_failed", "kind", artifact.Kind, "path", path, "err", writeErr)
				return fmt.Errorf("write missing restore artifact status: %w", writeErr)
			}
			return nil
		}
		hardResetLog.ErrorContext(ctx, "hardreset.restore_artifact_stat_failed", "kind", artifact.Kind, "path", realPath, "err", err)
		return fmt.Errorf("stat restore artifact %s: %w", realPath, err)
	}
	if err := os.RemoveAll(path); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.restore_artifact_remove_shim_failed", "kind", artifact.Kind, "path", path, "err", err)
		return fmt.Errorf("remove shim artifact %s: %w", path, err)
	}
	if err := os.Rename(realPath, path); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.restore_artifact_rename_failed", "kind", artifact.Kind, "from", realPath, "to", path, "err", err)
		return fmt.Errorf("restore real artifact %s -> %s: %w", realPath, path, err)
	}
	_, err := fmt.Fprintf(opts.Out, "artifact action=%s kind=%s path=%s status=restored\n", artifact.Action, artifact.Kind, path)
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.write_restore_artifact_status_failed", "kind", artifact.Kind, "path", path, "err", err)
		return fmt.Errorf("write restore artifact status: %w", err)
	}
	return nil
}

func resetTCCUtilAll(ctx context.Context, opts Options, bundleIDs []string) error {
	hardResetLog.InfoContext(ctx, "hardreset.tccutil_reset_all.boundary", "bundle_ids", strings.Join(bundleIDs, ","), "dry_run", opts.DryRun)
	if opts.DryRun {
		for _, bundleID := range bundleIDs {
			args := []string{"reset", "All", bundleID}
			_, err := fmt.Fprintf(opts.Out, "dry-run: /usr/bin/tccutil %s\n", strings.Join(args, " "))
			if err != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_dry_run_tccutil_failed", "err", err)
				return fmt.Errorf("write dry-run tccutil command: %w", err)
			}
		}
		return nil
	}

	resetCount := 0
	unregistered := make([]string, 0)
	for _, bundleID := range bundleIDs {
		args := []string{"reset", "All", bundleID}
		output, err := runCommand(ctx, "/usr/bin/tccutil", args, "")
		writeCommandLog(opts, "$ /usr/bin/tccutil "+strings.Join(args, " "))
		writeCommandLog(opts, strings.TrimSpace(string(output)))
		if err != nil {
			outputText := string(output)
			if isNoSuchBundleIdentifier(outputText) {
				hardResetLog.WarnContext(ctx, "hardreset.tccutil_bundle_unregistered", "bundle_id", bundleID, "output", strings.TrimSpace(outputText))
				unregistered = append(unregistered, bundleID)
				continue
			}
			hardResetLog.ErrorContext(ctx, "hardreset.tccutil_failed", "bundle_id", bundleID, "err", err)
			return fmt.Errorf("run tccutil %s: %w", strings.Join(args, " "), err)
		}
		resetCount++
	}
	_, err := fmt.Fprintf(
		opts.Out,
		"tccutil_reset_all attempted=%d reset=%d unregistered=%d unregistered_bundle_ids=%s\n",
		len(bundleIDs),
		resetCount,
		len(unregistered),
		strings.Join(unregistered, ","),
	)
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.write_tccutil_summary_failed", "err", err)
		return fmt.Errorf("write tccutil reset summary: %w", err)
	}
	return nil
}

func restartTCCD(ctx context.Context, opts Options, reason string) error {
	hardResetLog.InfoContext(ctx, "hardreset.restart_tccd.boundary", "reason", reason, "dry_run", opts.DryRun)
	args := []string{"tccd"}
	if opts.DryRun {
		_, err := fmt.Fprintf(opts.Out, "dry-run: /usr/bin/killall %s reason=%s\n", strings.Join(args, " "), reason)
		if err != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_dry_run_killall_failed", "err", err)
			return fmt.Errorf("write dry-run killall command: %w", err)
		}
		return nil
	}
	output, err := runCommand(ctx, "/usr/bin/killall", args, "")
	writeCommandLog(opts, "$ /usr/bin/killall tccd")
	writeCommandLog(opts, strings.TrimSpace(string(output)))
	if err != nil {
		hardResetLog.WarnContext(ctx, "hardreset.restart_tccd_nonfatal", "err", err, "output", strings.TrimSpace(string(output)))
		_, writeErr := fmt.Fprintf(opts.Out, "tccd_restart=not_running_or_unavailable reason=%s\n", reason)
		if writeErr != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_tccd_restart_status_failed", "err", writeErr)
			return fmt.Errorf("write tccd restart status: %w", writeErr)
		}
		return nil
	}
	_, err = fmt.Fprintf(opts.Out, "tccd_restart=requested reason=%s\n", reason)
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.write_tccd_restart_status_failed", "err", err)
		return fmt.Errorf("write tccd restart status: %w", err)
	}
	return nil
}

func deleteUserTCCRows(ctx context.Context, opts Options, bundleIDs []string) error {
	databasePath, err := userTCCDatabasePath()
	if err != nil {
		return err
	}
	hardResetLog.InfoContext(ctx, "hardreset.delete_user_tcc_rows.boundary", "db", databasePath, "bundle_ids", strings.Join(bundleIDs, ","), "dry_run", opts.DryRun)
	if opts.DryRun {
		_, writeErr := fmt.Fprintf(opts.Out, "dry-run: sqlite delete user_tcc_rows db=%s bundle_ids=%s\n", databasePath, strings.Join(bundleIDs, ","))
		if writeErr != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_dry_run_tcc_delete_failed", "err", writeErr)
			return fmt.Errorf("write dry-run TCC delete command: %w", writeErr)
		}
		return nil
	}

	if _, err := os.Stat(databasePath); err != nil {
		if os.IsNotExist(err) {
			_, writeErr := fmt.Fprintf(opts.Out, "user_tcc_rows_deleted db=%s deleted=0 reason=missing\n", databasePath)
			if writeErr != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_user_tcc_missing_failed", "err", writeErr)
				return fmt.Errorf("write missing TCC db status: %w", writeErr)
			}
			return nil
		}
		hardResetLog.WarnContext(ctx, "hardreset.user_tcc_stat_nonfatal", "db", databasePath, "err", err)
		_, writeErr := fmt.Fprintf(opts.Out, "user_tcc_rows_deleted db=%s deleted=unknown reason=stat_error\n", databasePath)
		if writeErr != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_user_tcc_stat_error_failed", "err", writeErr)
			return fmt.Errorf("write user TCC stat error status: %w", writeErr)
		}
		return nil
	}

	sql := "DELETE FROM access WHERE client IN (" + sqlStringList(bundleIDs) + ");\nSELECT changes();\n"
	output, err := runCommand(ctx, "/usr/bin/sqlite3", []string{"-cmd", ".timeout 10000", databasePath}, sql)
	writeCommandLog(opts, "$ /usr/bin/sqlite3 -cmd .timeout 10000 "+databasePath)
	writeCommandLog(opts, strings.TrimSpace(string(output)))
	if err != nil {
		hardResetLog.WarnContext(ctx, "hardreset.user_tcc_delete_nonfatal", "db", databasePath, "err", err, "output", strings.TrimSpace(string(output)))
		_, writeErr := fmt.Fprintf(opts.Out, "user_tcc_rows_deleted db=%s deleted=error\n", databasePath)
		if writeErr != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_user_tcc_delete_error_failed", "err", writeErr)
			return fmt.Errorf("write user TCC delete error status: %w", writeErr)
		}
		return nil
	}
	deleted := strings.TrimSpace(string(output))
	if deleted == "" {
		deleted = "0"
	}
	_, writeErr := fmt.Fprintf(opts.Out, "user_tcc_rows_deleted db=%s deleted=%s\n", databasePath, deleted)
	if writeErr != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.write_user_tcc_delete_summary_failed", "err", writeErr)
		return fmt.Errorf("write user TCC delete summary: %w", writeErr)
	}
	return nil
}

// deleteSystemTCCRows removes the target bundle rows from the machine-wide TCC
// database. The system database is root-owned and SIP-protected, so the delete
// runs through the same sudo-capable runner used for privileged artifact
// removal. It mirrors deleteUserTCCRows but is best-effort: a stat or delete
// failure (for example a host without Full Disk Access) is logged and reported,
// not propagated, so the rest of the reset still runs. Only failures to write
// the status line to opts.Out are returned.
func deleteSystemTCCRows(ctx context.Context, opts Options, bundleIDs []string) error {
	databasePath := systemTCCDatabasePath
	hardResetLog.InfoContext(ctx, "hardreset.delete_system_tcc_rows.boundary", "db", databasePath, "bundle_ids", strings.Join(bundleIDs, ","), "dry_run", opts.DryRun)
	if opts.DryRun {
		_, writeErr := fmt.Fprintf(opts.Out, "dry-run: sudo sqlite delete system_tcc_rows db=%s bundle_ids=%s\n", databasePath, strings.Join(bundleIDs, ","))
		if writeErr != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_dry_run_system_tcc_delete_failed", "err", writeErr)
			return fmt.Errorf("write dry-run system TCC delete command: %w", writeErr)
		}
		return nil
	}

	if _, err := os.Stat(databasePath); err != nil {
		if os.IsNotExist(err) {
			_, writeErr := fmt.Fprintf(opts.Out, "system_tcc_rows_deleted db=%s deleted=0 reason=missing\n", databasePath)
			if writeErr != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_system_tcc_missing_failed", "err", writeErr)
				return fmt.Errorf("write missing system TCC db status: %w", writeErr)
			}
			return nil
		}
		hardResetLog.WarnContext(ctx, "hardreset.system_tcc_stat_nonfatal", "db", databasePath, "err", err)
		_, writeErr := fmt.Fprintf(opts.Out, "system_tcc_rows_deleted db=%s deleted=unknown reason=stat_error\n", databasePath)
		if writeErr != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_system_tcc_stat_error_failed", "err", writeErr)
			return fmt.Errorf("write system TCC stat error status: %w", writeErr)
		}
		return nil
	}

	sql := "DELETE FROM access WHERE client IN (" + sqlStringList(bundleIDs) + ");\nSELECT changes();\n"
	output, err := runCommand(ctx, "/usr/bin/sudo", []string{"/usr/bin/sqlite3", "-cmd", ".timeout 10000", databasePath}, sql)
	writeCommandLog(opts, "$ /usr/bin/sudo /usr/bin/sqlite3 -cmd .timeout 10000 "+databasePath)
	writeCommandLog(opts, strings.TrimSpace(string(output)))
	if err != nil {
		hardResetLog.WarnContext(ctx, "hardreset.system_tcc_delete_nonfatal", "db", databasePath, "err", err, "output", strings.TrimSpace(string(output)))
		_, writeErr := fmt.Fprintf(opts.Out, "system_tcc_rows_deleted db=%s deleted=error\n", databasePath)
		if writeErr != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_system_tcc_delete_error_failed", "err", writeErr)
			return fmt.Errorf("write system TCC delete error status: %w", writeErr)
		}
		return nil
	}
	deleted := strings.TrimSpace(string(output))
	if deleted == "" {
		deleted = "0"
	}
	_, writeErr := fmt.Fprintf(opts.Out, "system_tcc_rows_deleted db=%s deleted=%s\n", databasePath, deleted)
	if writeErr != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.write_system_tcc_delete_summary_failed", "err", writeErr)
		return fmt.Errorf("write system TCC delete summary: %w", writeErr)
	}
	return nil
}

func verifyNoTCCRows(ctx context.Context, opts Options, bundleIDs []string) error {
	userDatabasePath, err := userTCCDatabasePath()
	if err != nil {
		return err
	}
	checks := []struct {
		label string
		path  string
	}{
		{label: "user", path: userDatabasePath},
		{label: "system", path: systemTCCDatabasePath},
	}
	for _, check := range checks {
		if opts.DryRun {
			_, writeErr := fmt.Fprintf(opts.Out, "dry-run: sqlite count tcc_rows db=%s bundle_ids=%s\n", check.label, strings.Join(bundleIDs, ","))
			if writeErr != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_dry_run_tcc_count_failed", "err", writeErr, "db", check.label)
				return fmt.Errorf("write dry-run TCC count command: %w", writeErr)
			}
			continue
		}
		count, err := countTCCRows(ctx, opts, check.path, bundleIDs)
		if err != nil {
			hardResetLog.WarnContext(ctx, "hardreset.tcc_count_nonfatal", "db", check.label, "err", err)
			_, writeErr := fmt.Fprintf(opts.Out, "tcc_rows_remaining db=%s count=unknown reason=count_error\n", check.label)
			if writeErr != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.write_tcc_count_error_failed", "err", writeErr, "db", check.label)
				return fmt.Errorf("write TCC count error status: %w", writeErr)
			}
			continue
		}
		_, writeErr := fmt.Fprintf(opts.Out, "tcc_rows_remaining db=%s count=%d\n", check.label, count)
		if writeErr != nil {
			hardResetLog.ErrorContext(ctx, "hardreset.write_tcc_row_count_failed", "err", writeErr, "db", check.label)
			return fmt.Errorf("write TCC row count: %w", writeErr)
		}
		if count > 0 {
			err := fmt.Errorf("%s TCC still has %d row(s) for reset bundle identities", check.label, count)
			hardResetLog.ErrorContext(ctx, "hardreset.tcc_rows_remain", "db", check.label, "count", count, "err", err)
			return err
		}
	}
	return nil
}

func countTCCRows(ctx context.Context, opts Options, databasePath string, bundleIDs []string) (int, error) {
	if _, err := os.Stat(databasePath); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		hardResetLog.ErrorContext(ctx, "hardreset.tcc_count_stat_failed", "db", databasePath, "err", err)
		return 0, fmt.Errorf("stat TCC db %s: %w", databasePath, err)
	}
	sql := "SELECT count(*) FROM access WHERE client IN (" + sqlStringList(bundleIDs) + ");\n"
	output, err := runCommand(ctx, "/usr/bin/sqlite3", []string{databasePath, sql}, "")
	writeCommandLog(opts, fmt.Sprintf("$ /usr/bin/sqlite3 %s <count query>", databasePath))
	writeCommandLog(opts, strings.TrimSpace(string(output)))
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.tcc_count_failed", "db", databasePath, "err", err, "output", strings.TrimSpace(string(output)))
		return 0, fmt.Errorf("count TCC rows in %s: %w", databasePath, err)
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return 0, nil
	}
	var count int
	if _, scanErr := fmt.Sscanf(text, "%d", &count); scanErr != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.tcc_count_parse_failed", "db", databasePath, "output", text, "err", scanErr)
		return 0, fmt.Errorf("parse TCC row count %q: %w", text, scanErr)
	}
	return count, nil
}

func writeCommandLog(opts Options, text string) {
	if strings.TrimSpace(text) == "" || opts.LogOut == nil {
		return
	}
	if _, err := fmt.Fprintln(opts.LogOut, text); err != nil {
		hardResetLog.Warn("hardreset.write_command_log_failed", "err", err)
	}
}

func userTCCDatabasePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		hardResetLog.Error("hardreset.user_home_failed", "err", err)
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "com.apple.TCC", "TCC.db"), nil
}

func sqlStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, sqlQuote(value))
	}
	return strings.Join(quoted, ",")
}

func sqlQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func isNoSuchBundleIdentifier(output string) bool {
	return strings.Contains(output, "No such bundle identifier")
}

func logHardResetRegistrationError(message string, err error) error {
	hardResetLog.Error("hardreset.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	results := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		results = append(results, trimmed)
	}
	sort.Strings(results)
	return results
}
