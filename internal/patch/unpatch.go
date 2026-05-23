package patch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// Unpatch restores t.AppPath from the per-target backup, removes the target's
// entry from state.json, and unloads the watcher LaunchAgent only if no other
// targets remain patched.
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

	// Remove this target from state.json.
	r.Note("target=%s step 2: remove state.json entry", t.ID)
	remaining := 0
	if !opts.DryRun {
		ms, err := state.Load(paths.StateFile())
		if err != nil {
			return fmt.Errorf("load state: %w", err)
		}
		delete(ms.Targets, t.ID)
		remaining = len(ms.Targets)
		if remaining == 0 {
			if err := state.Remove(paths.StateFile()); err != nil {
				return fmt.Errorf("remove state file: %w", err)
			}
		} else {
			if err := state.Save(paths.StateFile(), ms); err != nil {
				return fmt.Errorf("save state: %w", err)
			}
		}
	}

	// Unload watcher only if no other targets remain patched.
	if remaining == 0 {
		r.Note("step 3: no targets remain patched; unloading LaunchAgent")
		uid := strconv.Itoa(os.Getuid())
		if opts.DryRun {
			r.Note("would: launchctl bootout gui/%s %s", uid, paths.LaunchAgentPlist())
			r.Note("would: rm %s", paths.LaunchAgentPlist())
		} else {
			cmd := exec.Command("/bin/launchctl", "bootout", "gui/"+uid, paths.LaunchAgentPlist())
			cmd.Stdout = opts.Out
			cmd.Stderr = opts.Out
			if err := cmd.Run(); err != nil {
				_, _ = io.WriteString(opts.Out, fmt.Sprintf("launchctl bootout reported: %v (ignored)\n", err))
			}
			if err := os.Remove(paths.LaunchAgentPlist()); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove plist: %w", err)
			}
		}
	} else {
		r.Note("step 3: %d targets still patched; leaving LaunchAgent active", remaining)
	}

	if !opts.DryRun {
		r.Note("target=%s step 4: verify restored signature", t.ID)
		if err := r.Run("/usr/bin/codesign", "--verify", "--verbose=2", t.AppPath); err != nil {
			return fmt.Errorf("verify after unpatch: %w", err)
		}
	}

	r.Note("target=%s unpatch complete", t.ID)
	return nil
}
