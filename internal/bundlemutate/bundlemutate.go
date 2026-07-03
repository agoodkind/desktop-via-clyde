// Package bundlemutate owns the invariant "it is safe to mutate this bundle
// now" and the primitives for staging and atomically committing a bundle
// replacement. It wraps internal/appguard rather than duplicating process
// checks.
package bundlemutate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"goodkind.io/desktop-via-clyde/internal/appguard"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// Policy controls how Mutate reacts to a live target app process.
type Policy int

const (
	// PolicyDefer returns appguard.ErrAppRunning if target processes are live
	// (background daemon context).
	PolicyDefer Policy = iota
	// PolicyClose closes the app first via appguard.EnsureClosed (foreground
	// CLI context).
	PolicyClose
)

// Options controls bundlemutate progress reporting and dry-run behavior.
type Options struct {
	DryRun  bool
	Out     io.Writer
	Timeout time.Duration
}

// SwapOptions controls staged bundle adoption and post-swap verification.
type SwapOptions struct {
	AdoptPath string
	PreCommit func(context.Context) error
	Verify    func(context.Context) error
	Options
}

const (
	stagingDirPrefix = ".dvc-staging-"
	unknownTargetID  = "target"
)

// oldSuffix marks the temporary copy of the live bundle kept during a commit
// so a failed verify can restore it.
const oldSuffix = ".dvc-old"

var (
	bundlemutateLog = slog.With("component", "desktop-via-clyde", "subcomponent", "bundlemutate")
	running         = appguard.Running
	ensureClosed    = appguard.EnsureClosed
	sameDevice      = defaultSameDevice
	copyBundle      = defaultCopyBundle
	lstatPath       = os.Lstat
	renamePath      = os.Rename
	removeAllPath   = os.RemoveAll
)

// Mutate applies the policy against the target's live install path
// immediately before invoking fn. Dry runs skip the policy check and still
// invoke fn (fn is expected to be dry-run aware).
func Mutate(ctx context.Context, t targets.Target, p Policy, opts Options, fn func(context.Context) error) error {
	bundlemutateLog.DebugContext(ctx, "bundlemutate.mutate.boundary", "target", t.ID, "policy", p, "dry_run", opts.DryRun)
	switch p {
	case PolicyDefer, PolicyClose:
	default:
		return logBundlemutateError(ctx, "bundlemutate.mutate.unknown_policy",
			fmt.Errorf("bundlemutate: unknown mutation policy %d for %s", p, t.ID))
	}
	if !opts.DryRun {
		switch p {
		case PolicyDefer:
			if running(ctx, t) {
				wrappedErr := fmt.Errorf("bundlemutate: target %s: %w", t.ID, appguard.ErrAppRunning)
				bundlemutateLog.InfoContext(ctx, "bundlemutate.mutate.app_running", "target", t.ID)
				return wrappedErr
			}
		case PolicyClose:
			if err := ensureClosed(ctx, t, appguard.Options{DryRun: opts.DryRun, Out: opts.Out, Timeout: opts.Timeout}); err != nil {
				return logBundlemutateError(ctx, "bundlemutate.mutate.close_failed",
					fmt.Errorf("bundlemutate: close %s before mutation: %w", t.ID, err))
			}
		}
	}
	return fn(ctx)
}

// StagingRoot returns a directory suitable for staging a bundle destined for
// t.AppPath such that [os.Rename] into place cannot fail with EXDEV: it returns
// a subdirectory of [paths.StateRoot] when that root is on the same device as
// [filepath.Dir](t.AppPath) (compare syscall.Stat_t.Dev), otherwise a hidden
// staging dir inside [filepath.Dir](t.AppPath) (for example
// ".dvc-staging-<suffix>").
func StagingRoot(ctx context.Context, t targets.Target, suffix string) (string, error) {
	appParent := filepath.Dir(t.AppPath)
	stateRoot := paths.StateRoot()

	onSameDevice, err := sameDevice(stateRoot, appParent)
	if err != nil {
		return "", logBundlemutateError(ctx, "bundlemutate.staging_root.device_compare_failed",
			fmt.Errorf("bundlemutate: compare device for %s: %w", t.ID, err))
	}
	if onSameDevice {
		return filepath.Join(stateRoot, stagingDirPrefix+suffix), nil
	}
	return filepath.Join(appParent, stagingDirPrefix+suffix), nil
}

// StagedSwap copies AdoptPath into a same-volume staging root, lets mutate edit
// the staged bundle in place, optionally runs a caller-supplied PreCommit hook
// immediately before the rename, then atomically commits it over the live
// bundle and optionally runs Verify. The staging root is removed on return.
func StagedSwap(
	ctx context.Context,
	t targets.Target,
	opts SwapOptions,
	mutate func(stagedAppPath string) error,
) error {
	bundlemutateLog.DebugContext(ctx, "bundlemutate.staged_swap.boundary", "target", t.ID, "adopt_path", opts.AdoptPath, "dry_run", opts.DryRun)
	adoptPath := strings.TrimSpace(opts.AdoptPath)
	if adoptPath == "" {
		return logBundlemutateError(ctx, "bundlemutate.staged_swap.adopt_path_missing",
			fmt.Errorf("bundlemutate: target %s: adopt path is required", t.ID))
	}
	suffix := fmt.Sprintf("%s-%d", sanitizePathComponent(t.ID), clock.Now().UnixNano())
	stagingRoot, err := StagingRoot(ctx, t, suffix)
	if err != nil {
		return err
	}
	defer cleanupStagingRoot(stagingRoot)
	stagedAppPath := filepath.Join(stagingRoot, filepath.Base(t.AppPath))
	if err := stageBundle(ctx, adoptPath, stagedAppPath, opts.Options); err != nil {
		return err
	}
	if err := mutate(stagedAppPath); err != nil {
		return err
	}
	if opts.PreCommit != nil {
		if err := opts.PreCommit(ctx); err != nil {
			return err
		}
	}
	return CommitSwap(ctx, t, stagedAppPath, opts.Verify, opts.Options)
}

// CommitSwap atomically replaces the live bundle with stagedApp:
//  1. [os.Rename](t.AppPath, t.AppPath+".dvc-old"), skipped when the live
//     bundle does not exist.
//  2. [os.Rename](stagedApp, t.AppPath)
//  3. verify(ctx) when verify != nil
//  4. on verify success: [os.RemoveAll] of the .dvc-old copy
//     on verify failure: roll back (rename live back to staged location,
//     rename .dvc-old back to t.AppPath) and return the verify error.
//
// Dry runs only note the intended operations and return nil.
func CommitSwap(ctx context.Context, t targets.Target, stagedApp string, verify func(context.Context) error, opts Options) error {
	bundlemutateLog.DebugContext(ctx, "bundlemutate.commit_swap.boundary", "target", t.ID, "staged", stagedApp, "dry_run", opts.DryRun)
	oldPath := t.AppPath + oldSuffix

	if opts.DryRun {
		note(opts, fmt.Sprintf("target=%s commit staged bundle %s over %s", t.ID, stagedApp, t.AppPath))
		return nil
	}

	if err := prepareOldPath(ctx, t, oldPath); err != nil {
		return err
	}

	hadLiveBundle, err := renameIfExists(t.AppPath, oldPath)
	if err != nil {
		return logBundlemutateError(ctx, "bundlemutate.commit_swap.save_old_failed",
			fmt.Errorf("bundlemutate: save existing bundle for %s: %w", t.ID, err))
	}

	if err := renamePath(stagedApp, t.AppPath); err != nil {
		return restoreAfterInstallFailure(ctx, t, oldPath, hadLiveBundle, err)
	}

	if verify == nil {
		return finishCommit(ctx, hadLiveBundle, oldPath)
	}

	if err := verify(ctx); err != nil {
		return rollbackCommit(ctx, t, stagedApp, oldPath, hadLiveBundle, err)
	}

	return finishCommit(ctx, hadLiveBundle, oldPath)
}

func finishCommit(ctx context.Context, hadLiveBundle bool, oldPath string) error {
	bundlemutateLog.DebugContext(ctx, "bundlemutate.finish_commit.boundary", "had_live_bundle", hadLiveBundle, "old_path", oldPath)
	if !hadLiveBundle {
		return nil
	}
	if err := removeAllPath(oldPath); err != nil {
		return logBundlemutateError(ctx, "bundlemutate.commit_swap.cleanup_old_failed",
			fmt.Errorf("bundlemutate: remove old bundle copy %s: %w", oldPath, err))
	}
	return nil
}

func rollbackCommit(ctx context.Context, t targets.Target, stagedApp string, oldPath string, hadLiveBundle bool, verifyErr error) error {
	bundlemutateLog.ErrorContext(ctx, "bundlemutate.commit_swap.verify_failed", "target", t.ID, "err", verifyErr)
	rollbackErrors := []error{verifyErr}
	if err := os.MkdirAll(filepath.Dir(stagedApp), 0o755); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("bundlemutate: recreate staged bundle parent for %s: %w", t.ID, err))
	}
	if err := renamePath(t.AppPath, stagedApp); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("bundlemutate: roll back staged bundle for %s: %w", t.ID, err))
	}
	if hadLiveBundle {
		rollbackErrors = append(rollbackErrors, restoreLiveBundleAfterRollback(t, oldPath)...)
	}
	if len(rollbackErrors) > 1 {
		return errors.Join(rollbackErrors...)
	}
	return verifyErr
}

func prepareOldPath(ctx context.Context, t targets.Target, oldPath string) error {
	if _, err := lstatPath(oldPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return logBundlemutateError(ctx, "bundlemutate.commit_swap.stale_old_stat_failed",
			fmt.Errorf("bundlemutate: stat stale backup %s for %s: %w", oldPath, t.ID, err))
	}

	if _, err := lstatPath(t.AppPath); err != nil {
		if os.IsNotExist(err) {
			return logBundlemutateError(ctx, "bundlemutate.commit_swap.stale_old_without_live",
				fmt.Errorf("bundlemutate: stale backup exists at %s for %s while live bundle is missing", oldPath, t.ID))
		}
		return logBundlemutateError(ctx, "bundlemutate.commit_swap.live_stat_failed",
			fmt.Errorf("bundlemutate: stat live bundle %s for %s: %w", t.AppPath, t.ID, err))
	}

	if err := removeAllPath(oldPath); err != nil {
		return logBundlemutateError(ctx, "bundlemutate.commit_swap.remove_stale_old_failed",
			fmt.Errorf("bundlemutate: remove stale backup %s for %s: %w", oldPath, t.ID, err))
	}
	return nil
}

// renameIfExists renames src to dst, reporting false without error when src
// does not exist.
func renameIfExists(src string, dst string) (bool, error) {
	bundlemutateLog.Debug("bundlemutate.rename_if_exists.boundary", "src", src, "dst", dst)
	if _, err := lstatPath(src); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		wrappedErr := fmt.Errorf("lstat %s: %w", src, err)
		bundlemutateLog.Warn("bundlemutate.rename_if_exists.lstat_failed", "src", src, "err", wrappedErr)
		return false, wrappedErr
	}
	if err := renamePath(src, dst); err != nil {
		wrappedErr := fmt.Errorf("rename %s -> %s: %w", src, dst, err)
		bundlemutateLog.Warn("bundlemutate.rename_if_exists.rename_failed", "src", src, "dst", dst, "err", wrappedErr)
		return false, wrappedErr
	}
	return true, nil
}

func stageBundle(ctx context.Context, adoptPath string, stagedAppPath string, opts Options) error {
	bundlemutateLog.DebugContext(ctx, "bundlemutate.stage_bundle.boundary", "adopt_path", adoptPath, "staged_app_path", stagedAppPath, "dry_run", opts.DryRun)
	note(opts, fmt.Sprintf("stage bundle %s -> %s", adoptPath, stagedAppPath))
	if opts.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(stagedAppPath), 0o755); err != nil {
		return logBundlemutateError(ctx, "bundlemutate.stage_bundle.mkdir_failed",
			fmt.Errorf("bundlemutate: create staging parent for %s: %w", stagedAppPath, err))
	}
	if err := copyBundle(ctx, adoptPath, stagedAppPath); err != nil {
		return logBundlemutateError(ctx, "bundlemutate.stage_bundle.copy_failed", err)
	}
	return nil
}

func cleanupStagingRoot(stagingRoot string) {
	if err := removeAllPath(stagingRoot); err != nil {
		if os.IsNotExist(err) {
			return
		}
		bundlemutateLog.Warn("bundlemutate.cleanup_staging_root_failed", "path", stagingRoot, "err", err)
	}
}

func defaultCopyBundle(ctx context.Context, src string, dst string) error {
	bundlemutateLog.DebugContext(ctx, "bundlemutate.copy_bundle.boundary", "src", src, "dst", dst)
	cmd := exec.CommandContext(ctx, "/usr/bin/ditto", src, dst)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return logBundlemutateError(ctx, "bundlemutate.copy_bundle.failed",
			fmt.Errorf("bundlemutate: copy staged bundle %s -> %s: %w (output: %s)", src, dst, err, strings.TrimSpace(string(output))))
	}
	return nil
}

func defaultSameDevice(a string, b string) (bool, error) {
	aDev, err := deviceOf(a)
	if err != nil {
		return false, err
	}
	bDev, err := deviceOf(b)
	if err != nil {
		return false, err
	}
	return aDev == bDev, nil
}

func restoreAfterInstallFailure(
	ctx context.Context,
	t targets.Target,
	oldPath string,
	hadLiveBundle bool,
	installErr error,
) error {
	if !hadLiveBundle {
		return logBundlemutateError(ctx, "bundlemutate.commit_swap.rename_staged_failed",
			fmt.Errorf("bundlemutate: install staged bundle for %s: %w", t.ID, installErr))
	}
	if restoreErr := renamePath(oldPath, t.AppPath); restoreErr != nil {
		return logBundlemutateError(ctx, "bundlemutate.commit_swap.restore_after_install_failed",
			errors.Join(
				fmt.Errorf("bundlemutate: install staged bundle for %s: %w", t.ID, installErr),
				fmt.Errorf("bundlemutate: restore live bundle for %s after install failure: %w", t.ID, restoreErr),
			),
		)
	}
	return logBundlemutateError(ctx, "bundlemutate.commit_swap.rename_staged_failed",
		fmt.Errorf("bundlemutate: install staged bundle for %s: %w", t.ID, installErr))
}

func restoreLiveBundleAfterRollback(t targets.Target, oldPath string) []error {
	rollbackErrors := []error{}
	if _, err := lstatPath(t.AppPath); err == nil {
		if err := removeAllPath(t.AppPath); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("bundlemutate: remove failed live bundle for %s: %w", t.ID, err))
		}
	} else if !os.IsNotExist(err) {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("bundlemutate: stat failed live bundle for %s during rollback: %w", t.ID, err))
	}
	if err := renamePath(oldPath, t.AppPath); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("bundlemutate: restore live bundle for %s: %w", t.ID, err))
	}
	return rollbackErrors
}

// deviceOf resolves the device ID of the nearest existing ancestor of path,
// since staging roots and app parent directories may not exist yet.
func deviceOf(path string) (uint64, error) {
	bundlemutateLog.Debug("bundlemutate.device_of.boundary", "path", path)
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				wrappedErr := fmt.Errorf("bundlemutate: unsupported stat type for %s", current)
				bundlemutateLog.Warn("bundlemutate.device_of.unsupported_stat_type", "path", current, "err", wrappedErr)
				return 0, wrappedErr
			}
			deviceID := int64(stat.Dev)
			if deviceID < 0 {
				wrappedErr := fmt.Errorf("bundlemutate: negative device id for %s", current)
				bundlemutateLog.Warn("bundlemutate.device_of.negative_device_id", "path", current, "err", wrappedErr)
				return 0, wrappedErr
			}
			return uint64(deviceID), nil
		}
		if !os.IsNotExist(err) {
			wrappedErr := fmt.Errorf("stat %s: %w", current, err)
			bundlemutateLog.Warn("bundlemutate.device_of.stat_failed", "path", current, "err", wrappedErr)
			return 0, wrappedErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			wrappedErr := fmt.Errorf("bundlemutate: no existing ancestor found for %s", path)
			bundlemutateLog.Warn("bundlemutate.device_of.no_existing_ancestor", "path", path, "err", wrappedErr)
			return 0, wrappedErr
		}
		current = parent
	}
}

func logBundlemutateError(ctx context.Context, event string, err error) error {
	bundlemutateLog.ErrorContext(ctx, event, "err", err)
	return err
}

func note(opts Options, message string) {
	if opts.Out == nil {
		return
	}
	prefix := "[run]"
	if opts.DryRun {
		prefix = "[dry-run]"
	}
	fmt.Fprintf(opts.Out, "%s %s\n", prefix, message)
}

func sanitizePathComponent(value string) string {
	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(value))
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		return unknownTargetID
	}
	return sanitized
}
