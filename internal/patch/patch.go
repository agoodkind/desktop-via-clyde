// Package patch implements the patch, unpatch, and keychain-migrate workflows.
package patch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/clock"
	shimembed "goodkind.io/desktop-via-clyde/internal/embed"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/signing"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/gklog"
)

// ErrMissingStateEntry reports that the requested target has no persisted patch state.
var ErrMissingStateEntry = errors.New("missing target state entry")

const (
	// AppPatchCapability is the operation capability for app patching.
	AppPatchCapability = "app.patch"
	// AppUnpatchCapability is the operation capability for app unpatching.
	AppUnpatchCapability = "app.unpatch"
	// AppKeychainMigrateCapability is the operation capability for keychain migration.
	AppKeychainMigrateCapability = "app.keychain-migrate"
)

// RegisterOperations links patch-owned operation capabilities.
func RegisterOperations() error {
	if !catalog.HasOperationCapability(AppPatchCapability) {
		if err := catalog.RegisterOperationCapability(AppPatchCapability); err != nil {
			return logPatchRegistrationError("register patch capability", err)
		}
	}
	if err := operations.Register(AppPatchCapability, Operation); err != nil {
		return logPatchRegistrationError("register patch operation", err)
	}
	if !catalog.HasOperationCapability(AppUnpatchCapability) {
		if err := catalog.RegisterOperationCapability(AppUnpatchCapability); err != nil {
			return logPatchRegistrationError("register unpatch capability", err)
		}
	}
	if err := operations.Register(AppUnpatchCapability, UnpatchOperation); err != nil {
		return logPatchRegistrationError("register unpatch operation", err)
	}
	if !catalog.HasOperationCapability(AppKeychainMigrateCapability) {
		if err := catalog.RegisterOperationCapability(AppKeychainMigrateCapability); err != nil {
			return logPatchRegistrationError("register keychain migrate capability", err)
		}
	}
	if err := operations.Register(AppKeychainMigrateCapability, KeychainMigrateOperation); err != nil {
		return logPatchRegistrationError("register keychain migrate operation", err)
	}
	return nil
}

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
	Trace             *Trace
}

// Operation runs the app patch operation for one configured target.
func Operation(ctx context.Context, req operations.Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	if err := Patch(ctx, *req.App, Options{
		DryRun:            req.Flags.Bool("dry-run"),
		NoMigrateKeychain: req.Flags.Bool("no-migrate-keychain"),
		Out:               req.Out,
		Trace:             nil,
	}); err != nil {
		patchLog.ErrorContext(ctx, "patch.operation_failed", "err", err)
		return fmt.Errorf("patch operation: %w",
			operations.Error(ctx, "operations.patch_failed", "patch app", err))
	}
	return nil
}

// UnpatchOperation runs the app unpatch operation for one configured target.
func UnpatchOperation(ctx context.Context, req operations.Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	if err := Unpatch(ctx, *req.App, Options{
		DryRun:            req.Flags.Bool("dry-run"),
		NoMigrateKeychain: false,
		Out:               req.Out,
		Trace:             nil,
	}); err != nil {
		patchLog.ErrorContext(ctx, "patch.unpatch_operation_failed", "err", err)
		return fmt.Errorf("unpatch operation: %w",
			operations.Error(ctx, "operations.unpatch_failed", "restore app bundle", err))
	}
	return nil
}

// KeychainMigrateOperation runs the keychain repair operation for one target.
func KeychainMigrateOperation(ctx context.Context, req operations.Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	if err := KeychainMigrate(ctx, *req.App, Options{
		DryRun:            req.Flags.Bool("dry-run"),
		NoMigrateKeychain: false,
		Out:               req.Out,
		Trace:             nil,
	}); err != nil {
		patchLog.ErrorContext(ctx, "patch.keychain_migrate_operation_failed", "err", err)
		return fmt.Errorf("keychain migrate operation: %w",
			operations.Error(ctx, "operations.keychain_migrate_failed", "restore keychain access", err))
	}
	return nil
}

func logPatchRegistrationError(message string, err error) error {
	patchLog.Error("patch.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}

// Patch performs the full patch flow for one target.
func Patch(ctx context.Context, t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	log := gklog.LoggerFromContext(ctx).With("subcomponent", "patch", "target", t.ID)
	r := NewRunner(ctx, opts.DryRun, opts.Out)
	r.Trace = opts.Trace
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
	notef(r, fmt.Sprintf("target=%s read Info.plist version=%s id=%s exec=%s",
		t.ID, info.CFBundleVersion, info.CFBundleIdentifier, info.CFBundleExecutable))

	if err := stepBackup(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.backup_failed", fmt.Errorf("backup: %w", err))
	}

	var captured []KeychainItem
	switch {
	case opts.NoMigrateKeychain:
		notef(r, fmt.Sprintf("target=%s skipped keychain access repair (--no-migrate-keychain)", t.ID))
	case opts.DryRun:
		notef(r, fmt.Sprintf("target=%s would find keychain items for services=%v", t.ID, t.KeychainServices))
	default:
		captured, err = CaptureItems(ctx, t)
		if err != nil {
			return logPatchError(ctx, "patch.keychain_capture_failed", fmt.Errorf("keychain capture: %w", err))
		}
		notef(r, fmt.Sprintf("target=%s found %d keychain items", t.ID, len(captured)))
	}

	if err := patchBundleSteps(ctx, r, &t, opts); err != nil {
		return err
	}

	if err := runPostBundleHooks(ctx, r, t, opts); err != nil {
		return logPatchError(ctx, "patch.post_bundle_hook_failed", fmt.Errorf("run post-bundle hooks: %w", err))
	}

	switch {
	case opts.NoMigrateKeychain:
		notef(r, fmt.Sprintf("target=%s skipped keychain access restore (--no-migrate-keychain)", t.ID))
	case opts.DryRun:
		notef(r, fmt.Sprintf("target=%s would restore keychain access for captured items", t.ID))
	case len(captured) > 0:
		if err := RegrantItems(ctx, t, captured); err != nil {
			notef(r, fmt.Sprintf("target=%s keychain access restore returned errors (continuing): %v", t.ID, err))
		} else {
			notef(r, fmt.Sprintf("target=%s restored keychain access for %d items", t.ID, len(captured)))
		}
	default:
		notef(r, fmt.Sprintf("target=%s no keychain items needed access restore", t.ID))
	}

	if err := stepWriteState(ctx, r, t, info.CFBundleVersion); err != nil {
		return logPatchError(ctx, "patch.write_state_failed", fmt.Errorf("write state: %w", err))
	}

	if err := stepVerify(ctx, r, t); err != nil {
		return logPatchError(ctx, "patch.verify_failed", fmt.Errorf("verify: %w", err))
	}

	for _, capability := range t.PostPatchHookCapabilities() {
		if err := runPostPatchHook(ctx, r, t, opts, capability); err != nil {
			return logPatchError(ctx, "patch.post_patch_hook_failed", fmt.Errorf("run post-patch hook %q: %w", capability, err))
		}
	}

	notef(r, fmt.Sprintf("target=%s patch complete", t.ID))
	return nil
}

// patchBundleSteps runs the bundle-mutation steps (2 through 7) on
// the bundle at t.AppPath using the supplied Runner.
func patchBundleSteps(ctx context.Context, r *Runner, t *targets.Target, opts Options) error {
	if t.Entitlements == nil {
		return logPatchError(ctx, "patch.entitlement_policy_missing", fmt.Errorf("target %s has no entitlement policy", t.ID))
	}
	entFile, err := stepExtractEntitlements(ctx, r, *t)
	if err != nil {
		return logPatchError(ctx, "patch.extract_entitlements_failed", fmt.Errorf("extract entitlements: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s augment entitlements (strip=%v required=%v)",
		t.ID, t.Entitlements.Strip, t.Entitlements.RequiredBooleanEntitlements))
	if err := stepMoveToReal(ctx, r, *t); err != nil {
		return logPatchError(ctx, "patch.move_binary_failed", fmt.Errorf("move binary to .real: %w", err))
	}
	if err := stepPreLaunchPolicy(ctx, r, t, opts); err != nil {
		return logPatchError(ctx, "patch.pre_launch_policy_hook_failed", fmt.Errorf("run pre-launch-policy hooks: %w", err))
	}
	if err := stepInstallShim(ctx, r, *t); err != nil {
		return logPatchError(ctx, "patch.install_shim_failed", fmt.Errorf("install shim: %w", err))
	}
	if err := runPreResignHooks(ctx, r, *t, Options{
		DryRun:            r.DryRun,
		NoMigrateKeychain: opts.NoMigrateKeychain,
		Out:               r.Out,
		Trace:             r.Trace,
	}); err != nil {
		return logPatchError(ctx, "patch.pre_resign_hook_failed", fmt.Errorf("run pre-resign hooks: %w", err))
	}
	if err := stepRestorePreservedNestedCode(ctx, r, *t); err != nil {
		return logPatchError(ctx, "patch.restore_preserved_nested_code_failed", fmt.Errorf("restore preserved nested code: %w", err))
	}
	if err := stepResign(ctx, r, *t, entFile); err != nil {
		return logPatchError(ctx, "patch.resign_failed", fmt.Errorf("re-sign: %w", err))
	}
	stepStripQuarantine(ctx, r, *t)
	return nil
}

// KeychainMigrate restores keychain access for an app that is already patched.
func KeychainMigrate(ctx context.Context, t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	log := gklog.LoggerFromContext(ctx).With("subcomponent", "keychain-migrate", "target", t.ID)
	r := NewRunner(ctx, opts.DryRun, opts.Out)
	r.Trace = opts.Trace
	notef(r, fmt.Sprintf("target=%s keychain access repair", t.ID))
	log.InfoContext(ctx, "keychain_migrate.start", "app_path", t.AppPath, "dry_run", opts.DryRun)

	if opts.DryRun {
		notef(r, fmt.Sprintf("target=%s would restore keychain access for services=%v", t.ID, t.KeychainServices))
		return nil
	}
	items, err := CaptureItems(ctx, t)
	if err != nil {
		return logPatchError(ctx, "keychain_migrate.capture_failed", fmt.Errorf("keychain capture: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s found %d keychain items", t.ID, len(items)))
	if len(items) == 0 {
		return nil
	}
	if err := RegrantItems(ctx, t, items); err != nil {
		return logPatchError(ctx, "keychain_migrate.regrant_failed", fmt.Errorf("restore keychain access: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s restored keychain access for %d items", t.ID, len(items)))
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
			notef(r, fmt.Sprintf("target=%s backup exists at %s, skipping", t.ID, bundle))
			return nil
		}
		if err := os.MkdirAll(paths.BackupDir(t), 0o755); err != nil {
			return logPatchError(ctx, "patch.create_backup_dir_failed", fmt.Errorf("create backup dir %s: %w", paths.BackupDir(t), err))
		}
	}
	notef(r, fmt.Sprintf("target=%s backup app bundle %s -> %s", t.ID, t.AppPath, bundle))
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
	return writeAugmentedEntitlementsFile(ctx, r, "target="+t.ID+" entitlements", source, *t.Entitlements)
}

func stepMoveToReal(ctx context.Context, r *Runner, t targets.Target) error {
	patchLog.DebugContext(ctx, "patch.move_to_real.boundary", "target", t.ID)
	main := paths.MainBinaryPath(t)
	realPath := paths.RealBinaryPath(t)
	notef(r, fmt.Sprintf("target=%s move original executable %s -> %s", t.ID, main, realPath))
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

func stepPreLaunchPolicy(ctx context.Context, r *Runner, t *targets.Target, opts Options) error {
	for _, capability := range t.PreLaunchPolicyHookCapabilities() {
		if err := runPreLaunchPolicyHook(ctx, r, t, opts, capability); err != nil {
			patchLog.ErrorContext(ctx, "patch.pre_launch_policy_hook_failed", "capability", capability, "err", err)
			return fmt.Errorf("run pre-launch-policy hook %q: %w", capability, err)
		}
	}
	return nil
}

func stepInstallShim(ctx context.Context, r *Runner, t targets.Target) error {
	patchLog.DebugContext(ctx, "patch.install_shim.boundary", "target", t.ID)
	main := paths.MainBinaryPath(t)
	policyPath := paths.LaunchPolicyPath(t)
	notef(r, fmt.Sprintf("target=%s install shim (%d bytes) -> %s", t.ID, len(shimembed.ShimBinary), main))
	notef(r, fmt.Sprintf("target=%s install launch policy -> %s", t.ID, policyPath))
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
	policyBytes, err := json.MarshalIndent(t.LaunchPolicy, "", "  ")
	if err != nil {
		return logPatchError(ctx, "patch.launch_policy_encode_failed", fmt.Errorf("encode launch policy for %s: %w", t.ID, err))
	}
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o755); err != nil {
		return logPatchError(ctx, "patch.launch_policy_mkdir_failed", fmt.Errorf("create launch policy dir for %s: %w", t.ID, err))
	}
	if err := os.WriteFile(policyPath, policyBytes, 0o600); err != nil {
		return logPatchError(ctx, "patch.launch_policy_write_failed", fmt.Errorf("write launch policy %s: %w", policyPath, err))
	}
	return nil
}

func stepRestorePreservedNestedCode(ctx context.Context, r *Runner, t targets.Target) error {
	patchLog.DebugContext(ctx, "patch.restore_preserved_nested_code.boundary", "target", t.ID)
	for _, relPath := range t.PreservedNestedCodePaths {
		source := filepath.Join(paths.BackupBundle(t), filepath.FromSlash(relPath))
		destination := filepath.Join(t.AppPath, filepath.FromSlash(relPath))
		traceAction(r, actionRestorePreservedNestedCode, t.ID, destination)
		notef(r, fmt.Sprintf("target=%s restore preserved nested code %s -> %s", t.ID, source, destination))
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
	traceAction(r, actionSignBundle, t.ID, t.AppPath)
	notef(r, fmt.Sprintf("target=%s re-sign with %q (sha1=%s)", t.ID, paths.SignIdentity(), id))
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
					notef(r, fmt.Sprintf("target=%s nested code object missing, skipping %s", t.ID, codePath))
					continue
				}
				return logPatchError(ctx, "patch.nested_code_stat_failed", fmt.Errorf("stat nested code object %s: %w", codePath, err))
			}
		}
		traceAction(r, actionSignNestedCode, t.ID, codePath)
		notef(r, fmt.Sprintf("target=%s re-sign nested code object %s", t.ID, codePath))
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
	notef(r, fmt.Sprintf("target=%s remove quarantine attribute (best effort)", t.ID))
	if r.DryRun {
		return
	}
	_ = ctx
	_ = unix.Removexattr(t.AppPath, "com.apple.quarantine")
}

func stepWriteState(ctx context.Context, r *Runner, t targets.Target, version string) error {
	notef(r, fmt.Sprintf("target=%s write patch state version=%s -> %s", t.ID, version, paths.StateFile()))
	if r.DryRun {
		return nil
	}
	capturedOriginalDR := false
	var captureOriginalDRErr error
	updateErr := state.Update(paths.StateFile(), func(ms state.MultiState) (state.MultiState, error) {
		originalDR := ms.Targets[t.ID].OriginalDesignatedRequirement
		if originalDR == "" {
			captured, captureErr := captureOriginalDR(ctx, t)
			if captureErr != nil {
				captureOriginalDRErr = captureErr
				return ms, captureErr
			}
			originalDR = captured
			capturedOriginalDR = true
		}
		ms.Targets[t.ID] = state.TargetState{
			PatchedVersion:                version,
			PatchedAt:                     clock.Now().UTC(),
			SignIdentity:                  paths.SignIdentity(),
			OriginalDesignatedRequirement: originalDR,
		}
		return ms, nil
	})
	if updateErr != nil {
		if captureOriginalDRErr != nil {
			return logPatchError(ctx, "patch.capture_original_dr_failed", fmt.Errorf("capture original DR from backup: %w", captureOriginalDRErr))
		}
		return logPatchError(ctx, "patch.save_state_failed", fmt.Errorf("save state file: %w", updateErr))
	}
	if capturedOriginalDR {
		notef(r, fmt.Sprintf("target=%s captured original DR during state update", t.ID))
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
	updateErr := state.Update(paths.StateFile(), func(current state.MultiState) (state.MultiState, error) {
		currentEntry, ok := current.Targets[t.ID]
		if !ok {
			return current, MissingStateEntryError{TargetID: t.ID}
		}
		if currentEntry.OriginalDesignatedRequirement == "" {
			currentEntry.OriginalDesignatedRequirement = captured
			current.Targets[t.ID] = currentEntry
		}
		return current, nil
	})
	if updateErr != nil {
		return "", logPatchError(ctx, "patch.original_dr_save_repair_failed", fmt.Errorf("save repaired state.json: %w", updateErr))
	}
	return captured, nil
}

// captureOriginalDR reads the DesignatedRequirement string from the
// upstream-signed binary inside the backup bundle and returns it as
// a single requirement-language line. The function reads from the
// backup bundle because the live /Applications copy has already been
// re-signed with Goodkind by the time stepWriteState runs; the
// backup created before mutation is the only on-disk copy of the bundle
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
	notef(r, fmt.Sprintf("target=%s verify bundle signature and shim dry-run", t.ID))
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
			notef(r, fmt.Sprintf("target=%s shim dry-run was killed; continuing after signature and entitlement verification", t.ID))
			return nil
		}
		return logPatchError(ctx, "patch.shim_dry_run_failed", fmt.Errorf("shim dry-run: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s shim dry-run output:\n%s", t.ID, string(out)))
	return nil
}

func ignoreShimDryRunError(t targets.Target, err error) bool {
	signalName := t.LaunchPolicy.IgnoreDryRunSignal
	if signalName == "" {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return false
	}
	expectedSignal, ok := signalByName(signalName)
	return ok && status.Signal() == expectedSignal
}

func signalByName(name spec.DryRunSignal) (syscall.Signal, bool) {
	switch name {
	case spec.DryRunSignalSIGKILL:
		return syscall.SIGKILL, true
	case spec.DryRunSignalSIGTERM:
		return syscall.SIGTERM, true
	case spec.DryRunSignalSIGINT:
		return syscall.SIGINT, true
	default:
		return 0, false
	}
}
