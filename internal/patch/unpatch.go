package patch

import (
	"context"
	"fmt"
	"os"

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

	for _, capability := range t.PreUnpatchHookCapabilities() {
		if err := runPreUnpatchHook(ctx, r, t, opts, capability); err != nil {
			return logPatchError(ctx, "unpatch.pre_unpatch_hook_failed", fmt.Errorf("run pre-unpatch hook %q: %w", capability, err))
		}
	}

	backup := paths.BackupBundle(t)
	if !opts.DryRun {
		if _, err := os.Stat(backup); err != nil {
			return logPatchError(ctx, "unpatch.backup_stat_failed", fmt.Errorf("backup not found at %s: %w", backup, err))
		}
	}
	notef(r, fmt.Sprintf("target=%s restore app bundle %s -> %s", t.ID, backup, t.AppPath))
	if err := r.Run(ctx, "/usr/bin/rsync", "-a", "--delete", backup+"/", t.AppPath+"/"); err != nil {
		return logPatchError(ctx, "unpatch.restore_bundle_failed", fmt.Errorf("restore bundle: %w", err))
	}

	notef(r, fmt.Sprintf("target=%s remove patch state entry", t.ID))
	if !opts.DryRun {
		if err := removeTargetState(ctx, t.ID); err != nil {
			return err
		}
	}

	if !opts.DryRun {
		notef(r, fmt.Sprintf("target=%s verify restored signature", t.ID))
		if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--verbose=2", t.AppPath); err != nil {
			return logPatchError(ctx, "unpatch.verify_failed", fmt.Errorf("verify after unpatch: %w", err))
		}
	}

	notef(r, fmt.Sprintf("target=%s unpatch complete", t.ID))
	return nil
}

func removeTargetState(ctx context.Context, targetID string) error {
	if err := state.Update(paths.StateFile(), func(ms state.MultiState) (state.MultiState, error) {
		delete(ms.Targets, targetID)
		return ms, nil
	}); err != nil {
		return logPatchError(ctx, "unpatch.save_state_failed", fmt.Errorf("update state: %w", err))
	}
	return nil
}
