// Package patch implements the patch, unpatch, and keychain-migrate workflows.
package patch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"goodkind.io/desktop-via-clyde/internal/claudetee"
	"goodkind.io/desktop-via-clyde/internal/clock"
	shimembed "goodkind.io/desktop-via-clyde/internal/embed"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/signing"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/gklog"
)

// ErrMissingStateEntry reports that the requested target has no persisted patch state.
var ErrMissingStateEntry = errors.New("missing target state entry")

// MissingStateEntryError names the missing target when state is absent.
type MissingStateEntryError struct {
	TargetID string
}

// Error reports the missing target state entry with a repair hint.
func (e MissingStateEntryError) Error() string {
	return fmt.Sprintf("no state entry for target %s; run `desktop-via-clyde %s patch` first", e.TargetID, e.TargetID)
}

// Is lets callers match MissingStateEntryError against ErrMissingStateEntry.
func (e MissingStateEntryError) Is(target error) bool {
	return target == ErrMissingStateEntry
}

// Options controls a patch invocation.
type Options struct {
	DryRun            bool
	NoMigrateKeychain bool
	Out               io.Writer
}

// Patch performs the full patch flow for one target. Steps are numbered to
// match the plan: 1, 1b, 2..7, 7a, 8..9.
func Patch(ctx context.Context, t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	log := gklog.LoggerFromContext(ctx).With("subcomponent", "patch", "target", t.ID)
	r := NewRunner(ctx, opts.DryRun, opts.Out)
	log.InfoContext(ctx, "patch.start", "app_path", t.AppPath, "dry_run", opts.DryRun, "no_migrate_keychain", opts.NoMigrateKeychain)

	if !opts.DryRun {
		if _, err := os.Stat(t.AppPath); err != nil {
			return logPatchError(ctx, "patch.bundle_stat_failed", fmt.Errorf("bundle not found at %s: %w", t.AppPath, err))
		}
	}

	info, err := loadInfoPlistOrPlaceholder(t, opts.DryRun)
	if err != nil {
		return err
	}
	notef(r, fmt.Sprintf("target=%s step 0: read Info.plist version=%s id=%s exec=%s",
		t.ID, info.CFBundleVersion, info.CFBundleIdentifier, info.CFBundleExecutable))

	// Step 1: backup.
	if err := stepBackup(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.backup_failed", fmt.Errorf("backup: %w", err))
	}

	// Step 1b: keychain capture (in-memory).
	var captured []KeychainItem
	switch {
	case opts.NoMigrateKeychain:
		notef(r, fmt.Sprintf("target=%s step 1b: skipped (--no-migrate-keychain)", t.ID))
	case opts.DryRun:
		notef(r, fmt.Sprintf("target=%s step 1b: would capture keychain items for services=%v", t.ID, t.KeychainServices))
	default:
		captured, err = CaptureItems(ctx, t)
		if err != nil {
			return logPatchError(ctx, "patch.keychain_capture_failed", fmt.Errorf("keychain capture: %w", err))
		}
		notef(r, fmt.Sprintf("target=%s step 1b: captured %d keychain items", t.ID, len(captured)))
	}

	// Steps 2-7: bundle mutation (entitlements, exec rename, shim
	// install, re-sign, strip quarantine).
	if err := patchBundleSteps(ctx, r, t); err != nil {
		return err
	}

	if err := stepPatchComputerUse(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.computer_use_failed", fmt.Errorf("patch computer use helper: %w", err))
	}

	// Step 7a: keychain re-grant.
	switch {
	case opts.NoMigrateKeychain:
		notef(r, fmt.Sprintf("target=%s step 7a: skipped (--no-migrate-keychain)", t.ID))
	case opts.DryRun:
		notef(r, fmt.Sprintf("target=%s step 7a: would re-grant ACLs on captured keychain items", t.ID))
	case len(captured) > 0:
		if err := RegrantItems(ctx, t, captured); err != nil {
			notef(r, fmt.Sprintf("target=%s step 7a: re-grant returned errors (continuing): %v", t.ID, err))
		} else {
			notef(r, fmt.Sprintf("target=%s step 7a: re-granted ACLs on %d keychain items", t.ID, len(captured)))
		}
	default:
		notef(r, fmt.Sprintf("target=%s step 7a: no captured items, nothing to re-grant", t.ID))
	}

	// Step 8: update state.json.
	if err := stepWriteState(ctx, r, t, info.CFBundleVersion); err != nil {
		return logPatchError(ctx, "patch.write_state_failed", fmt.Errorf("write state: %w", err))
	}

	// Step 9: verify.
	if err := stepVerify(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.verify_failed", fmt.Errorf("verify: %w", err))
	}

	// Step 10: bundled-CLI stdio tee (claude only). Claude Desktop spawns a
	// separate claude binary under Application Support over stdio for tasks
	// such as the /context slash command; that traffic does not cross the
	// MITM HTTPS proxy. Wrapping the bundled CLI with the stdio tee makes
	// the SDK control protocol bytes visible alongside the Electron HTTPS
	// captures, so a "patched" claude reflects the full canonical state.
	if t.ID == "claude" {
		if err := stepInstallBundledCLITee(ctx, r, opts); err != nil {
			return logPatchError(ctx, "patch.install_bundled_cli_tee_failed", fmt.Errorf("install bundled cli tee: %w", err))
		}
	}

	notef(r, fmt.Sprintf("target=%s patch complete", t.ID))
	return nil
}

// stepInstallBundledCLITee wraps Claude Desktop's bundled claude CLI with the
// stdio tee shim. Idempotent: if the bundled CLI is already wrapped, the
// step logs and returns without acting. A missing Application Support tree
// (Claude Desktop not yet launched) is a no-op rather than a hard failure;
// the patch flow's invariant is the Electron main, and the tee is a layered
// diagnostic surface.
func stepInstallBundledCLITee(ctx context.Context, r *Runner, opts Options) error {
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
		notef(r, fmt.Sprintf("target=claude step 10: bundled CLI not present, skipping tee install (%v)", resolveErr))
		return nil
	}
	if _, statErr := os.Stat(bundled); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return logPatchError(ctx, "patch.bundled_cli_stat_failed", fmt.Errorf("stat bundled cli %s: %w", bundled, statErr))
		}
		notef(r, fmt.Sprintf("target=claude step 10: bundled CLI missing at %s, skipping tee install", bundled))
		return nil
	}
	if _, realErr := os.Stat(bundled + ".real"); realErr == nil {
		notef(r, "target=claude step 10: bundled CLI already wrapped (.real present), skipping")
		return nil
	}
	notef(r, "target=claude step 10: install bundled-CLI stdio tee at "+bundled)
	if opts.DryRun {
		return nil
	}
	if err := claudetee.Install(ctx, teeOpts); err != nil {
		return logPatchError(ctx, "patch.bundled_cli_tee_install_failed", fmt.Errorf("install bundled cli tee: %w", err))
	}
	return nil
}

// patchBundleSteps runs the bundle-mutation steps (2 through 7) on
// the bundle at t.AppPath using the supplied Runner.
func patchBundleSteps(ctx context.Context, r *Runner, t targets.Target) error {
	if t.Entitlements == nil {
		return logPatchError(ctx, "patch.entitlement_policy_missing", fmt.Errorf("target %s has no entitlement policy", t.ID))
	}
	entFile, err := stepExtractEntitlements(ctx, r, t)
	if err != nil {
		return logPatchError(ctx, "patch.extract_entitlements_failed", fmt.Errorf("extract entitlements: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s step 3: augment entitlements (strip=%v required=%v)",
		t.ID, t.Entitlements.Strip, t.Entitlements.RequiredBooleanEntitlements))
	if err := stepMoveToReal(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.move_binary_failed", fmt.Errorf("move binary to .real: %w", err))
	}
	if err := stepInstallShim(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.install_shim_failed", fmt.Errorf("install shim: %w", err))
	}
	if err := stepPatchBundledComputerUse(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.bundled_computer_use_failed", fmt.Errorf("patch bundled computer use helper: %w", err))
	}
	if err := stepRestorePreservedNestedCode(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.restore_preserved_nested_code_failed", fmt.Errorf("restore preserved nested code: %w", err))
	}
	if err := stepResign(ctx, r, t, entFile); err != nil {
		return logPatchError(ctx, "patch.resign_failed", fmt.Errorf("re-sign: %w", err))
	}
	stepStripQuarantine(ctx, r, t)
	return nil
}

// KeychainMigrate runs only steps 1b + 7a against an already-patched app.
// Useful for retroactive ACL cleanup.
func KeychainMigrate(ctx context.Context, t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	log := gklog.LoggerFromContext(ctx).With("subcomponent", "keychain-migrate", "target", t.ID)
	r := NewRunner(ctx, opts.DryRun, opts.Out)
	notef(r, fmt.Sprintf("target=%s keychain-migrate: step 1b + 7a only", t.ID))
	log.InfoContext(ctx, "keychain_migrate.start", "app_path", t.AppPath, "dry_run", opts.DryRun)

	if opts.DryRun {
		notef(r, fmt.Sprintf("target=%s would capture and re-grant services=%v", t.ID, t.KeychainServices))
		return nil
	}
	items, err := CaptureItems(ctx, t)
	if err != nil {
		return logPatchError(ctx, "keychain_migrate.capture_failed", fmt.Errorf("keychain capture: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s captured %d items", t.ID, len(items)))
	if len(items) == 0 {
		return nil
	}
	if err := RegrantItems(ctx, t, items); err != nil {
		return logPatchError(ctx, "keychain_migrate.regrant_failed", fmt.Errorf("keychain re-grant: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s re-granted %d items", t.ID, len(items)))
	return nil
}

func loadInfoPlistOrPlaceholder(t targets.Target, dryRun bool) (InfoPlist, error) {
	p := paths.InfoPlistPath(t)
	if _, err := os.Stat(p); err != nil {
		if dryRun {
			return InfoPlist{
				CFBundleExecutable: t.ExecName,
				CFBundleVersion:    "0.0.0-dry-run",
				CFBundleIdentifier: t.BundleID,
				SUPublicEDKey:      "",
			}, nil
		}
		return InfoPlist{}, logPatchErrorNoContext("patch.info_plist_stat_failed", fmt.Errorf("info plist not found at %s: %w", p, err))
	}
	return ReadInfoPlist(p)
}

func stepBackup(ctx context.Context, r *Runner, t targets.Target) error {
	bundle := paths.BackupBundle(t)
	if !r.DryRun {
		if _, err := os.Stat(bundle); err == nil {
			notef(r, fmt.Sprintf("target=%s step 1: backup exists at %s, skipping", t.ID, bundle))
			return nil
		}
		if err := os.MkdirAll(paths.BackupDir(t), 0o755); err != nil {
			return logPatchError(ctx, "patch.create_backup_dir_failed", fmt.Errorf("create backup dir %s: %w", paths.BackupDir(t), err))
		}
	}
	notef(r, fmt.Sprintf("target=%s step 1: backup %s -> %s", t.ID, t.AppPath, bundle))
	return r.Run(ctx, "/usr/bin/rsync", "-a", t.AppPath+"/", bundle+"/")
}

func stepExtractEntitlements(ctx context.Context, r *Runner, t targets.Target) (string, error) {
	// Prefer .real if it exists (idempotent re-patch path); else read from the
	// main binary slot, which on a fresh patch is the vendor binary.
	source := paths.MainBinaryPath(t)
	if _, err := os.Stat(paths.RealBinaryPath(t)); err == nil {
		source = paths.RealBinaryPath(t)
	}
	if t.Entitlements == nil {
		return "", logPatchError(ctx, "patch.entitlement_policy_missing", fmt.Errorf("target %s has no entitlement policy", t.ID))
	}
	return writeAugmentedEntitlementsFile(ctx, r, "target="+t.ID+" step 2", source, *t.Entitlements)
}

func stepMoveToReal(ctx context.Context, r *Runner, t targets.Target) error {
	patchLog.DebugContext(ctx, "patch.move_to_real.boundary", "target", t.ID)
	main := paths.MainBinaryPath(t)
	realPath := paths.RealBinaryPath(t)
	notef(r, fmt.Sprintf("target=%s step 4: mv %s -> %s", t.ID, main, realPath))
	if r.DryRun {
		return nil
	}
	if _, err := os.Stat(realPath); err == nil {
		notef(r, fmt.Sprintf("target=%s %s.real already exists, skipping move", t.ID, t.ExecName))
		return nil
	}
	if err := os.Rename(main, realPath); err != nil {
		return logPatchError(ctx, "patch.rename_real_failed", fmt.Errorf("rename %s -> %s: %w", main, realPath, err))
	}
	return nil
}

func stepInstallShim(ctx context.Context, r *Runner, t targets.Target) error {
	patchLog.DebugContext(ctx, "patch.install_shim.boundary", "target", t.ID)
	main := paths.MainBinaryPath(t)
	notef(r, fmt.Sprintf("target=%s step 5: install shim (%d bytes) -> %s", t.ID, len(shimembed.ShimBinary), main))
	if r.DryRun {
		return nil
	}
	if len(shimembed.ShimBinary) == 0 {
		return errors.New("embedded shim is empty; run `make shim` before building")
	}
	if err := os.WriteFile(main, shimembed.ShimBinary, 0o600); err != nil {
		return logPatchError(ctx, "patch.write_shim_failed", fmt.Errorf("write shim %s: %w", main, err))
	}
	if err := os.Chmod(main, 0o755); err != nil {
		return logPatchError(ctx, "patch.chmod_shim_failed", fmt.Errorf("chmod shim %s: %w", main, err))
	}
	return nil
}

func stepRestorePreservedNestedCode(ctx context.Context, r *Runner, t targets.Target) error {
	patchLog.DebugContext(ctx, "patch.restore_preserved_nested_code.boundary", "target", t.ID)
	for _, relPath := range t.PreservedNestedCodePaths {
		source := filepath.Join(paths.BackupBundle(t), filepath.FromSlash(relPath))
		destination := filepath.Join(t.AppPath, filepath.FromSlash(relPath))
		notef(r, fmt.Sprintf("target=%s step 5c: restore preserved nested code %s -> %s", t.ID, source, destination))
		if r.DryRun {
			continue
		}
		info, err := os.Stat(source)
		if err != nil {
			return logPatchError(ctx, "patch.preserved_nested_code_source_stat_failed", fmt.Errorf("stat preserved nested code source %s: %w", source, err))
		}
		if info.IsDir() {
			if err := os.RemoveAll(destination); err != nil {
				return logPatchError(ctx, "patch.preserved_nested_code_remove_failed", fmt.Errorf("remove preserved nested code destination %s: %w", destination, err))
			}
			if err := r.Run(ctx, "/usr/bin/rsync", "-a", source+"/", destination+"/"); err != nil {
				return logPatchError(ctx, "patch.preserved_nested_code_directory_restore_failed", fmt.Errorf("restore preserved nested code directory %s: %w", relPath, err))
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return logPatchError(ctx, "patch.preserved_nested_code_parent_failed", fmt.Errorf("create preserved nested code parent %s: %w", filepath.Dir(destination), err))
		}
		if err := r.Run(ctx, "/bin/cp", "-p", source, destination); err != nil {
			return logPatchError(ctx, "patch.preserved_nested_code_file_restore_failed", fmt.Errorf("restore preserved nested code file %s: %w", relPath, err))
		}
	}
	return nil
}

func stepResign(ctx context.Context, r *Runner, t targets.Target, entFile string) error {
	id, err := resolveSignIdentity(ctx, r.DryRun)
	if err != nil {
		return err
	}
	notef(r, fmt.Sprintf("target=%s step 6: re-sign with %q (sha1=%s)", t.ID, paths.SignIdentity(), id))
	if err := stepResignNestedCode(ctx, r, t, id); err != nil {
		return err
	}
	if err := r.Run(ctx, "/usr/bin/codesign", codesignRuntimeEntitlementsArgs(id, entFile, paths.RealBinaryPath(t))...); err != nil {
		return logPatchError(ctx, "patch.sign_real_failed", fmt.Errorf("sign %s.real: %w", t.ExecName, err))
	}
	if err := r.Run(ctx, "/usr/bin/codesign", codesignRuntimeEntitlementsArgs(id, entFile, paths.MainBinaryPath(t))...); err != nil {
		return logPatchError(ctx, "patch.sign_shim_failed", fmt.Errorf("sign %s shim: %w", t.ExecName, err))
	}
	if err := r.Run(ctx, "/usr/bin/codesign", codesignRuntimeEntitlementsArgs(id, entFile, t.AppPath)...); err != nil {
		return logPatchError(ctx, "patch.seal_bundle_failed", fmt.Errorf("seal outer bundle: %w", err))
	}
	return nil
}

func codesignRuntimeEntitlementsArgs(id string, entFile string, codePath string) []string {
	return signing.RuntimeEntitlementsArgs(id, entFile, codePath)
}

func codesignRuntimeArgs(id string, codePath string) []string {
	return signing.RuntimeArgs(id, codePath)
}

func stepResignNestedCode(ctx context.Context, r *Runner, t targets.Target, id string) error {
	for _, relPath := range t.NestedSignPaths {
		codePath := filepath.Join(t.AppPath, filepath.FromSlash(relPath))
		if !r.DryRun {
			if _, err := os.Stat(codePath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					notef(r, fmt.Sprintf("target=%s step 6: nested code object missing, skipping %s", t.ID, codePath))
					continue
				}
				return logPatchError(ctx, "patch.nested_code_stat_failed", fmt.Errorf("stat nested code object %s: %w", codePath, err))
			}
		}
		notef(r, fmt.Sprintf("target=%s step 6: re-sign nested code object %s", t.ID, codePath))
		if err := r.Run(ctx,
			"/usr/bin/codesign",
			"--force",
			"--sign",
			id,
			"--options",
			"runtime",
			"--preserve-metadata=entitlements",
			codePath,
		); err != nil {
			return logPatchError(ctx, "patch.sign_nested_code_failed", fmt.Errorf("sign nested code object %s: %w", codePath, err))
		}
	}
	return nil
}

func resolveSignIdentity(ctx context.Context, dryRun bool) (string, error) {
	identity, err := signing.ResolveIdentity(ctx, dryRun)
	if err != nil {
		return "", logPatchError(ctx, "patch.resolve_signing_identity_failed", fmt.Errorf("resolve signing identity: %w", err))
	}
	return identity, nil
}

func stepStripQuarantine(ctx context.Context, r *Runner, t targets.Target) {
	notef(r, fmt.Sprintf("target=%s step 7: strip com.apple.quarantine (best effort)", t.ID))
	if r.DryRun {
		return
	}
	_ = ctx
	_ = unix.Removexattr(t.AppPath, "com.apple.quarantine")
}

func stepWriteState(ctx context.Context, r *Runner, t targets.Target, version string) error {
	notef(r, fmt.Sprintf("target=%s step 8: write state.json version=%s -> %s", t.ID, version, paths.StateFile()))
	if r.DryRun {
		return nil
	}
	ms, err := state.Load(paths.StateFile())
	if err != nil {
		return logPatchError(ctx, "patch.load_state_failed", fmt.Errorf("load state file: %w", err))
	}
	originalDR := ms.Targets[t.ID].OriginalDesignatedRequirement
	if originalDR == "" {
		captured, captureErr := captureOriginalDR(ctx, t)
		if captureErr != nil {
			return logPatchError(ctx, "patch.capture_original_dr_failed", fmt.Errorf("capture original DR from backup: %w", captureErr))
		}
		originalDR = captured
		notef(r, fmt.Sprintf("target=%s captured original DR (%d bytes)", t.ID, len(originalDR)))
	}
	ms.Targets[t.ID] = state.TargetState{
		PatchedVersion:                version,
		PatchedAt:                     clock.Now().UTC(),
		SignIdentity:                  paths.SignIdentity(),
		OriginalDesignatedRequirement: originalDR,
	}
	if err := state.Save(paths.StateFile(), ms); err != nil {
		return logPatchError(ctx, "patch.save_state_failed", fmt.Errorf("save state file: %w", err))
	}
	return nil
}

// OriginalDesignatedRequirement returns the recorded upstream
// DesignatedRequirement for t. If repair is true and a state entry exists but
// predates the field, it writes the captured DR back to state.json. If repair
// is false, it captures from backup when possible but leaves state.json alone.
func OriginalDesignatedRequirement(ctx context.Context, t targets.Target, repair bool) (string, error) {
	ms, err := state.Load(paths.StateFile())
	if err != nil {
		return "", logPatchError(ctx, "patch.original_dr_load_state_failed", fmt.Errorf("load state.json: %w", err))
	}
	entry, ok := ms.Targets[t.ID]
	if !ok {
		return "", MissingStateEntryError{TargetID: t.ID}
	}
	if entry.OriginalDesignatedRequirement != "" {
		return entry.OriginalDesignatedRequirement, nil
	}
	captured, err := captureOriginalDR(ctx, t)
	if err != nil {
		return "", logPatchError(ctx, "patch.original_dr_capture_failed", fmt.Errorf("state entry for target %s has no recorded OriginalDesignatedRequirement and repair from backup failed: %w", t.ID, err))
	}
	if !repair {
		return captured, nil
	}
	entry.OriginalDesignatedRequirement = captured
	ms.Targets[t.ID] = entry
	if err := state.Save(paths.StateFile(), ms); err != nil {
		return "", logPatchError(ctx, "patch.original_dr_save_repair_failed", fmt.Errorf("save repaired state.json: %w", err))
	}
	return captured, nil
}

// captureOriginalDR reads the DesignatedRequirement string from the
// upstream-signed binary inside the backup bundle and returns it as
// a single requirement-language line. The function reads from the
// backup bundle because the live /Applications copy has already been
// re-signed with Goodkind by the time stepWriteState runs; the
// backup created in step 1 is the only on-disk copy of the bundle
// that still carries the vendor signature, so the DR captured from
// there reflects what a freshly downloaded update payload from
// upstream must satisfy. Falls back to the live bundle for cases
// where the backup is missing, with a warning logged at the call
// site.
func captureOriginalDR(ctx context.Context, t targets.Target) (string, error) {
	source := filepath.Join(paths.BackupBundle(t), "Contents", "MacOS", t.ExecName)
	if _, err := os.Stat(source); err != nil {
		realInBackup := filepath.Join(paths.BackupBundle(t), "Contents", "MacOS", t.ExecName+".real")
		if _, err := os.Stat(realInBackup); err == nil {
			source = realInBackup
		} else {
			return "", logPatchError(ctx, "patch.original_dr_backup_missing", fmt.Errorf("no upstream-signed binary in backup at %s: %w", paths.BackupBundle(t), err))
		}
	}
	return DesignatedRequirement(ctx, source)
}

// DesignatedRequirement returns the designated requirement string for one code object.
func DesignatedRequirement(ctx context.Context, codePath string) (string, error) {
	patchLog.DebugContext(ctx, "patch.designated_requirement.boundary", "code_path", codePath)
	cmd := exec.CommandContext(ctx, "/usr/bin/codesign", "--display", "--requirements", "-", codePath)
	out, err := cmd.Output()
	if err != nil {
		return "", logPatchError(ctx, "patch.codesign_requirements_failed", fmt.Errorf("codesign --display --requirements -: %w", err))
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("codesign produced empty DR blob for %s", codePath)
	}
	const designatedPrefix = "designated => "
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		trimmedLine, ok := strings.CutPrefix(line, designatedPrefix)
		if ok {
			return strings.TrimSpace(trimmedLine), nil
		}
	}
	return "", fmt.Errorf("no 'designated =>' line in codesign output for %s", codePath)
}

func stepVerify(ctx context.Context, r *Runner, t targets.Target) error {
	notef(r, fmt.Sprintf("target=%s step 9: verify bundle signature and shim dry-run", t.ID))
	if r.DryRun {
		return nil
	}
	for _, relPath := range t.NestedSignPaths {
		codePath := filepath.Join(t.AppPath, filepath.FromSlash(relPath))
		if _, err := os.Stat(codePath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return logPatchError(ctx, "patch.verify_nested_code_stat_failed", fmt.Errorf("stat nested code object %s: %w", codePath, err))
		}
		if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--verbose=2", codePath); err != nil {
			return logPatchError(ctx, "patch.verify_nested_code_failed", fmt.Errorf("verify nested code object %s: %w", codePath, err))
		}
	}
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--verbose=2", t.AppPath); err != nil {
		return logPatchError(ctx, "patch.verify_app_bundle_failed", fmt.Errorf("verify app bundle %s: %w", t.AppPath, err))
	}
	if err := verifyRequiredEntitlements(ctx, r, t); err != nil {
		return err
	}
	out, err := r.RunCaptureStdout(ctx, paths.MainBinaryPath(t), "--clyde-dry-run")
	if err != nil {
		if ignoreShimDryRunError(t, err) {
			notef(r, fmt.Sprintf("target=%s step 9: shim dry-run was killed; continuing after signature and entitlement verification", t.ID))
			return nil
		}
		return logPatchError(ctx, "patch.shim_dry_run_failed", fmt.Errorf("shim dry-run: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s shim dry-run output:\n%s", t.ID, string(out)))
	return nil
}

func ignoreShimDryRunError(t targets.Target, err error) bool {
	if t.ID != "claude" {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}
