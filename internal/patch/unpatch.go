package patch

import (
	"fmt"
	"os"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// Unpatch restores t.AppPath from the per-target backup and removes the
// target's entry from state.json.
func Unpatch(t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	r := NewRunner(opts.DryRun, opts.Out)

	backup := paths.BackupBundle(t)
	if !opts.DryRun {
		if _, err := os.Stat(backup); err != nil {
			return fmt.Errorf("backup not found at %s: %w", backup, err)
		}
	}
	r.Note("target=%s step 1: restore %s -> %s", t.ID, backup, t.AppPath)
	if err := r.Run("/usr/bin/rsync", "-a", "--delete", backup+"/", t.AppPath+"/"); err != nil {
		return fmt.Errorf("restore bundle: %w", err)
	}

	r.Note("target=%s step 2: remove state.json entry", t.ID)
	if !opts.DryRun {
		ms, err := state.Load(paths.StateFile())
		if err != nil {
			return fmt.Errorf("load state: %w", err)
		}
		delete(ms.Targets, t.ID)
		if len(ms.Targets) == 0 {
			if err := state.Remove(paths.StateFile()); err != nil {
				return fmt.Errorf("remove state file: %w", err)
			}
		} else {
			if err := state.Save(paths.StateFile(), ms); err != nil {
				return fmt.Errorf("save state: %w", err)
			}
		}
	}

	if !opts.DryRun {
		r.Note("target=%s step 3: verify restored signature", t.ID)
		if err := r.Run("/usr/bin/codesign", "--verify", "--verbose=2", t.AppPath); err != nil {
			return fmt.Errorf("verify after unpatch: %w", err)
		}
	}

	r.Note("target=%s unpatch complete", t.ID)
	return nil
}
