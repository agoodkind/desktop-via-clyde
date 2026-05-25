package patch

import (
	"context"
	"errors"
	"fmt"
	"os"

	"goodkind.io/desktop-via-clyde/internal/claudetee"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/gklog"
)

// Unpatch restores t.AppPath from the per-target backup and removes the
// target's entry from state.json.
func Unpatch(ctx context.Context, t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	log := gklog.LoggerFromContext(ctx).With("subcomponent", "unpatch", "target", t.ID)
	r := NewRunner(ctx, opts.DryRun, opts.Out)
	log.InfoContext(ctx, "unpatch.start", "app_path", t.AppPath, "dry_run", opts.DryRun)

	// Step 0 (claude only): undo the bundled-CLI stdio tee wrap before
	// restoring the Electron bundle, so the bundled claude.real binary
	// moves back over the shim while the .real sibling is still present.
	if t.ID == "claude" {
		if err := stepUninstallBundledCLITee(ctx, r, opts); err != nil {
			return logPatchError(ctx, "unpatch.uninstall_bundled_cli_tee_failed", fmt.Errorf("uninstall bundled cli tee: %w", err))
		}
	}

	backup := paths.BackupBundle(t)
	if !opts.DryRun {
		if _, err := os.Stat(backup); err != nil {
			return logPatchError(ctx, "unpatch.backup_stat_failed", fmt.Errorf("backup not found at %s: %w", backup, err))
		}
	}
	notef(r, fmt.Sprintf("target=%s step 1: restore %s -> %s", t.ID, backup, t.AppPath))
	if err := r.Run(ctx, "/usr/bin/rsync", "-a", "--delete", backup+"/", t.AppPath+"/"); err != nil {
		return logPatchError(ctx, "unpatch.restore_bundle_failed", fmt.Errorf("restore bundle: %w", err))
	}

	notef(r, fmt.Sprintf("target=%s step 2: remove state.json entry", t.ID))
	if !opts.DryRun {
		if err := removeTargetState(ctx, t.ID); err != nil {
			return err
		}
	}

	if !opts.DryRun {
		notef(r, fmt.Sprintf("target=%s step 3: verify restored signature", t.ID))
		if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--verbose=2", t.AppPath); err != nil {
			return logPatchError(ctx, "unpatch.verify_failed", fmt.Errorf("verify after unpatch: %w", err))
		}
	}

	notef(r, fmt.Sprintf("target=%s unpatch complete", t.ID))
	return nil
}

// stepUninstallBundledCLITee removes the bundled-CLI stdio tee wrap. If
// there is no .real sibling next to the bundled claude (the tee was never
// installed, or already removed), the step is a no-op. Failures inside
// claudetee.Uninstall surface as errors so the overall unpatch can be
// retried.
func stepUninstallBundledCLITee(ctx context.Context, r *Runner, opts Options) error {
	teeOpts := claudetee.Options{
		DryRun:         opts.DryRun,
		VersionDir:     "",
		BundledCLIPath: "",
		LogDir:         "",
		HomeDir:        "",
		Out:            opts.Out,
	}
	bundled, resolveErr := claudetee.ResolveBundledCLIPath(teeOpts)
	if resolveErr != nil {
		notef(r, fmt.Sprintf("target=claude step 0: bundled CLI not present, skipping tee uninstall (%v)", resolveErr))
		return nil
	}
	if _, statErr := os.Stat(bundled + ".real"); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return logPatchError(ctx, "unpatch.bundled_cli_real_stat_failed", fmt.Errorf("stat bundled cli real sibling %s.real: %w", bundled, statErr))
		}
		notef(r, fmt.Sprintf("target=claude step 0: no .real sibling at %s.real, nothing to uninstall", bundled))
		return nil
	}
	notef(r, "target=claude step 0: uninstall bundled-CLI stdio tee at "+bundled)
	if opts.DryRun {
		return nil
	}
	if err := claudetee.Uninstall(ctx, teeOpts); err != nil {
		return logPatchError(ctx, "unpatch.bundled_cli_tee_uninstall_failed", fmt.Errorf("uninstall bundled cli tee: %w", err))
	}
	return nil
}

func removeTargetState(ctx context.Context, targetID string) error {
	ms, err := state.Load(paths.StateFile())
	if err != nil {
		return logPatchError(ctx, "unpatch.load_state_failed", fmt.Errorf("load state: %w", err))
	}
	delete(ms.Targets, targetID)
	if len(ms.Targets) == 0 {
		if err := state.Remove(paths.StateFile()); err != nil {
			return logPatchError(ctx, "unpatch.remove_state_file_failed", fmt.Errorf("remove state file: %w", err))
		}
		return nil
	}
	if err := state.Save(paths.StateFile(), ms); err != nil {
		return logPatchError(ctx, "unpatch.save_state_failed", fmt.Errorf("save state: %w", err))
	}
	return nil
}
