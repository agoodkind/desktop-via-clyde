// Package patch implements the patch and keychain-migrate workflows.
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
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
	"goodkind.io/desktop-via-clyde/internal/appguard"
	"goodkind.io/desktop-via-clyde/internal/bundleidentity"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/devpreflight"
	"goodkind.io/desktop-via-clyde/internal/devsign"
	shimembed "goodkind.io/desktop-via-clyde/internal/embed"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/signing"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/gklog"
)

const (
	// AppPatchCapability is the operation capability for app patching.
	AppPatchCapability = "app.patch"
	// AppKeychainMigrateCapability is the operation capability for keychain migration.
	AppKeychainMigrateCapability = "app.keychain-migrate"
)

type machOMagic string

const (
	machOMagic32BE    machOMagic = "\xfe\xed\xfa\xce"
	machOMagic32LE    machOMagic = "\xce\xfa\xed\xfe"
	machOMagic64BE    machOMagic = "\xfe\xed\xfa\xcf"
	machOMagic64LE    machOMagic = "\xcf\xfa\xed\xfe"
	machOMagicFat32BE machOMagic = "\xca\xfe\xba\xbe"
	machOMagicFat32LE machOMagic = "\xbe\xba\xfe\xca"
	machOMagicFat64BE machOMagic = "\xca\xfe\xba\xbf"
	machOMagicFat64LE machOMagic = "\xbf\xba\xfe\xca"
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

// Options controls a patch invocation.
type Options struct {
	DryRun            bool
	MigrateKeychain   bool
	Out               io.Writer
	LogOut            io.Writer
	Progress          clioutput.Progress
	Trace             *Trace
	CloseBeforeMutate bool
}

// Operation runs the app patch operation for one configured target.
func Operation(ctx context.Context, req operations.Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	if shouldUpgradeMissingBundle(*req.App, req.Flags.Bool("dry-run")) {
		if err := runUpgradeForMissingBundle(ctx, req); err != nil {
			patchLog.ErrorContext(ctx, "patch.missing_bundle_upgrade_failed", "err", err)
			return fmt.Errorf("patch operation: %w",
				operations.Error(ctx, "operations.patch_missing_bundle_upgrade_failed", "install missing app before patch", err))
		}
		return nil
	}
	if err := Patch(ctx, *req.App, Options{
		DryRun:            req.Flags.Bool("dry-run"),
		MigrateKeychain:   req.Flags.Bool("migrate-keychain"),
		Out:               req.Out,
		LogOut:            req.LogOut,
		Progress:          req.Progress,
		Trace:             nil,
		CloseBeforeMutate: !req.Flags.Bool("background"),
	}); err != nil {
		patchLog.ErrorContext(ctx, "patch.operation_failed", "err", err)
		return fmt.Errorf("patch operation: %w",
			operations.Error(ctx, "operations.patch_failed", "patch app", err))
	}
	return nil
}

func shouldUpgradeMissingBundle(target targets.Target, dryRun bool) bool {
	if dryRun {
		return false
	}
	if _, err := os.Stat(target.AppPath); err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return false
}

func runUpgradeForMissingBundle(ctx context.Context, req operations.Request) error {
	handler, ok := operations.Lookup("app.upgrade")
	if !ok {
		return fmt.Errorf("app.upgrade operation is not registered")
	}
	flags := operations.NewFlagValues()
	flags.SetBool("dry-run", req.Flags.Bool("dry-run"))
	flags.SetBool("migrate-keychain", req.Flags.Bool("migrate-keychain"))
	flags.SetBool("background", req.Flags.Bool("background"))
	flags.SetString("channel", "")
	notice := fmt.Sprintf("target=%s app missing at %s; running upgrade before patch", req.App.ID, req.App.AppPath)
	switch {
	case req.Progress != nil:
		req.Progress.Step(notice)
	case req.Out != nil:
		if _, err := fmt.Fprintf(req.Out, "[run] %s\n", notice); err != nil {
			patchLog.ErrorContext(ctx, "patch.write_missing_bundle_upgrade_notice_failed", "target", req.App.ID, "app_path", req.App.AppPath, "err", err)
			return fmt.Errorf("write missing bundle upgrade notice: %w", err)
		}
	}
	return handler(ctx, operations.Request{
		Out:        req.Out,
		LogOut:     req.LogOut,
		Progress:   req.Progress,
		App:        req.App,
		CLI:        nil,
		Capability: "app.upgrade",
		Flags:      flags,
		Format:     req.Format,
	})
}

// KeychainMigrateOperation runs the keychain repair operation for one target.
func KeychainMigrateOperation(ctx context.Context, req operations.Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	if err := KeychainMigrate(ctx, *req.App, Options{
		DryRun:            req.Flags.Bool("dry-run"),
		MigrateKeychain:   true,
		Out:               req.Out,
		LogOut:            req.LogOut,
		Progress:          req.Progress,
		Trace:             nil,
		CloseBeforeMutate: false,
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
	if opts.LogOut != nil {
		r.RawOut = opts.LogOut
	}
	r.Progress = opts.Progress
	r.Trace = opts.Trace
	log.InfoContext(ctx, "patch.start", "app_path", t.AppPath, "dry_run", opts.DryRun, "migrate_keychain", opts.MigrateKeychain)

	// Shared, non-blocking development-signing credential preflight. Both the
	// direct patch path and the upgrade path (which re-patches through Patch after
	// the bundle swap) reach this point, so wiring the check here covers both
	// without a second call site. It never returns an error: a target without
	// credentials continues on the standard shim plus Developer ID path.
	devpreflight.Warn(ctx, r.Progress, opts.DryRun, t)

	if !opts.DryRun {
		if _, err := os.Stat(t.AppPath); err != nil {
			return logPatchError(ctx, "patch.bundle_stat_failed", fmt.Errorf("bundle not found at %s: %w", t.AppPath, err))
		}
		if err := devsign.EnsureTrustedMITMCA(ctx, t); err != nil {
			return logPatchError(ctx, "patch.mitm_trust_required", fmt.Errorf("verify MITM CA trust before patch: %w", err))
		}
	}
	if opts.CloseBeforeMutate {
		if err := appguard.EnsureClosed(ctx, t, appguard.Options{
			DryRun:  opts.DryRun,
			Out:     opts.Out,
			Timeout: 0,
		}); err != nil {
			return logPatchError(ctx, "patch.close_app_failed", fmt.Errorf("close app before mutation: %w", err))
		}
	}

	info, err := loadInfoPlistOrPlaceholder(t, opts.DryRun)
	if err != nil {
		return err
	}
	notef(r, fmt.Sprintf("target=%s read Info.plist version=%s id=%s exec=%s",
		t.ID, info.CFBundleVersion, info.CFBundleIdentifier, info.CFBundleExecutable))

	originalDR, err := resolveOriginalDRForPatch(ctx, r, t)
	if err != nil {
		return err
	}

	var captured []KeychainItem
	switch {
	case !opts.MigrateKeychain:
		notef(r, fmt.Sprintf("target=%s skipped keychain access repair (pass --migrate-keychain to run)", t.ID))
	case opts.DryRun:
		notef(r, fmt.Sprintf("target=%s would find keychain items for services=%v", t.ID, t.KeychainServices))
	default:
		captured, err = CaptureItems(ctx, t)
		if err != nil {
			return logPatchError(ctx, "patch.keychain_capture_failed", fmt.Errorf("keychain capture: %w", err))
		}
		notef(r, fmt.Sprintf("target=%s found %d keychain items", t.ID, len(captured)))
	}

	devPlan, err := patchBundleSteps(ctx, r, &t, opts)
	if err != nil {
		return err
	}
	devSigned := devPlan != nil

	if err := runPostBundleHooks(ctx, r, t, opts); err != nil {
		return logPatchError(ctx, "patch.post_bundle_hook_failed", fmt.Errorf("run post-bundle hooks: %w", err))
	}

	restoreKeychainAccess(ctx, r, t, opts, captured)

	if err := stepWriteState(ctx, r, t, info.CFBundleVersion, originalDR); err != nil {
		return logPatchError(ctx, "patch.write_state_failed", fmt.Errorf("write state: %w", err))
	}

	// The standard shim path verifies before post-patch hooks. The
	// development-signing path instead defers its top-bundle reseal until after
	// every nested re-signing step (post-bundle hooks, post-patch hooks), so its
	// verify must wait for that reseal below.
	if !devSigned {
		if err := stepVerify(ctx, r, t); err != nil {
			return logPatchError(ctx, "patch.verify_failed", fmt.Errorf("verify: %w", err))
		}
	}

	for _, capability := range t.PostPatchHookCapabilities() {
		if err := runPostPatchHook(ctx, r, t, opts, capability); err != nil {
			return logPatchError(ctx, "patch.post_patch_hook_failed", fmt.Errorf("run post-patch hook %q: %w", capability, err))
		}
	}

	if devSigned {
		// The --shallow top-bundle reseal records the cdhashes of every nested
		// object, so it must be the last signing action: any earlier placement
		// lets a later nested re-sign (post-bundle or post-patch hooks) invalidate
		// the seal.
		if err := devsign.Reseal(ctx, r, devsign.Options{DryRun: r.DryRun, Out: r.Out, Progress: r.Progress}, t, devPlan); err != nil {
			return logPatchError(ctx, "patch.development_signing_reseal_failed", fmt.Errorf("reseal development-signed bundle: %w", err))
		}
		if err := stepVerifyDevelopmentSigned(ctx, r, t); err != nil {
			return logPatchError(ctx, "patch.verify_failed", fmt.Errorf("verify: %w", err))
		}
	}

	notef(r, fmt.Sprintf("target=%s patch complete", t.ID))
	return nil
}

// patchBundleSteps runs the bundle-mutation steps (2 through 7) on the bundle at
// t.AppPath using the supplied Runner. A non-nil returned plan signals the
// development-signing path: its nested mutations have run, but the final
// top-bundle reseal is deferred to Patch so it lands after all nested re-signing.
func patchBundleSteps(ctx context.Context, r *Runner, t *targets.Target, opts Options) (*devsign.Plan, error) {
	if t.Entitlements == nil {
		return nil, logPatchError(ctx, "patch.entitlement_policy_missing", fmt.Errorf("target %s has no entitlement policy", t.ID))
	}
	plan, err := maybeApplyDevelopmentSigning(ctx, r, t)
	if err != nil {
		return nil, err
	}
	if plan != nil {
		return plan, nil
	}
	preservedRoot, cleanupPreserved, err := stagePreservedNestedCode(ctx, r, *t)
	if err != nil {
		return nil, logPatchError(ctx, "patch.stage_preserved_nested_code_failed", fmt.Errorf("stage preserved nested code: %w", err))
	}
	defer cleanupPreserved()
	entFile, err := stepExtractEntitlements(ctx, r, *t)
	if err != nil {
		return nil, logPatchError(ctx, "patch.extract_entitlements_failed", fmt.Errorf("extract entitlements: %w", err))
	}
	notef(r, fmt.Sprintf("target=%s augment entitlements (strip=%v required=%v)",
		t.ID, t.Entitlements.Strip, t.Entitlements.RequiredBooleanEntitlements))
	if err := stepMoveToReal(ctx, r, *t); err != nil {
		return nil, logPatchError(ctx, "patch.move_binary_failed", fmt.Errorf("move binary to .real: %w", err))
	}
	if err := stepPreLaunchPolicy(ctx, r, t, opts); err != nil {
		return nil, logPatchError(ctx, "patch.pre_launch_policy_hook_failed", fmt.Errorf("run pre-launch-policy hooks: %w", err))
	}
	if err := stepInstallShim(ctx, r, *t); err != nil {
		return nil, logPatchError(ctx, "patch.install_shim_failed", fmt.Errorf("install shim: %w", err))
	}
	if err := runPreResignHooks(ctx, r, *t, Options{
		DryRun:            r.DryRun,
		MigrateKeychain:   opts.MigrateKeychain,
		Out:               r.Out,
		LogOut:            r.RawOut,
		Progress:          r.Progress,
		Trace:             r.Trace,
		CloseBeforeMutate: false,
	}); err != nil {
		return nil, logPatchError(ctx, "patch.pre_resign_hook_failed", fmt.Errorf("run pre-resign hooks: %w", err))
	}
	if err := stepRestorePreservedNestedCode(ctx, r, *t, preservedRoot); err != nil {
		return nil, logPatchError(ctx, "patch.restore_preserved_nested_code_failed", fmt.Errorf("restore preserved nested code: %w", err))
	}
	if err := stepEmbedProvisioningProfile(ctx, r, *t); err != nil {
		return nil, logPatchError(ctx, "patch.embed_provisioning_profile_failed", fmt.Errorf("embed provisioning profile: %w", err))
	}
	if err := stepResign(ctx, r, *t, entFile); err != nil {
		return nil, logPatchError(ctx, "patch.resign_failed", fmt.Errorf("re-sign: %w", err))
	}
	stepStripQuarantine(ctx, r, *t)
	return nil, nil
}

// maybeApplyDevelopmentSigning runs the opt-in development-profile overlay in
// place of the shim plus Developer ID path when the target enables it and every
// required asset is present. On success it returns a non-nil plan whose final
// top-bundle reseal the caller must run last; a nil plan means the target did not
// take this path. A missing asset is non-blocking (criterion #8): it emits a
// warning naming the exact file to provide and returns a nil plan so the caller
// continues with the standard shim path.
func maybeApplyDevelopmentSigning(ctx context.Context, r *Runner, t *targets.Target) (*devsign.Plan, error) {
	if t.DevelopmentSigning == nil || !t.DevelopmentSigning.Enabled {
		return nil, nil
	}
	missing := devsign.MissingAssets(*t.DevelopmentSigning)
	if len(missing) > 0 {
		for _, asset := range missing {
			notef(r, fmt.Sprintf("target=%s development signing requested but %s is missing at %s; provide it to enable the enrollment fix, continuing with the shim + Developer ID path",
				t.ID, asset.Label, asset.Path))
		}
		return nil, nil
	}
	notef(r, fmt.Sprintf("target=%s development signing enabled; applying enrollment-fix overlay (skipping shim, move-to-real, and Developer ID re-sign)", t.ID))
	plan, err := devsign.ApplyNestedMutations(ctx, r, devsign.Options{DryRun: r.DryRun, Out: r.Out, Progress: r.Progress}, *t)
	if err != nil {
		return nil, logPatchError(ctx, "patch.development_signing_failed", fmt.Errorf("apply development signing: %w", err))
	}
	return plan, nil
}

// KeychainMigrate restores keychain access for an app that is already patched.
func KeychainMigrate(ctx context.Context, t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	log := gklog.LoggerFromContext(ctx).With("subcomponent", "keychain-migrate", "target", t.ID)
	r := NewRunner(ctx, opts.DryRun, opts.Out)
	r.Progress = opts.Progress
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

// stepEmbedProvisioningProfile replaces Contents/embedded.provisionprofile with
// the target's configured profile before re-signing. The original app ships an
// upstream-team profile that does not match the local signing team, so a locally
// generated profile must take its place for team-scoped entitlements to validate.
func stepEmbedProvisioningProfile(ctx context.Context, r *Runner, t targets.Target) error {
	patchLog.DebugContext(ctx, "patch.embed_provisioning_profile.boundary", "target", t.ID)
	destination := filepath.Join(t.AppPath, "Contents", "embedded.provisionprofile")
	if t.ProvisioningProfile == "" {
		// No local profile is configured. Remove any stale upstream-team profile so
		// the Developer ID signature self-asserts explicit team-prefixed entitlements.
		notef(r, fmt.Sprintf("target=%s remove stale provisioning profile %s", t.ID, destination))
		if err := r.Run(ctx, "/bin/rm", "-f", destination); err != nil {
			return logPatchError(ctx, "patch.remove_provisioning_profile_failed", fmt.Errorf("remove stale provisioning profile %s: %w", destination, err))
		}
		return nil
	}
	source := filepath.Clean(t.ProvisioningProfile)
	notef(r, fmt.Sprintf("target=%s embed provisioning profile %s -> %s", t.ID, source, destination))
	if err := r.Run(ctx, "/bin/cp", "-f", source, destination); err != nil {
		return logPatchError(ctx, "patch.embed_provisioning_profile_copy_failed", fmt.Errorf("copy provisioning profile %s to %s: %w", source, destination, err))
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

func stepRestorePreservedNestedCode(ctx context.Context, r *Runner, t targets.Target, preservedRoot string) error {
	patchLog.DebugContext(ctx, "patch.restore_preserved_nested_code.boundary", "target", t.ID)
	for _, relPath := range t.PreservedNestedCodePaths {
		source := filepath.Join(preservedRoot, filepath.FromSlash(relPath))
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
	codePaths, err := nestedCodeSignPaths(ctx, r, t)
	if err != nil {
		return err
	}
	for _, codePath := range codePaths {
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

func nestedCodeSignPaths(ctx context.Context, r *Runner, t targets.Target) ([]string, error) {
	items := make([]nestedCodeSignPath, 0, len(t.NestedSignPaths))
	for _, relPath := range t.NestedSignPaths {
		items = append(items, nestedCodeSignPath{
			Path: filepath.Join(t.AppPath, filepath.FromSlash(relPath)),
		})
	}

	entries, err := bundleidentity.Scan(ctx, t.AppPath, bundleidentity.ScanOptions{
		IncludeSignatures: false,
		SignatureReader:   nil,
	})
	if err != nil {
		return nil, logPatchError(ctx, "patch.runtime_identity_scan_failed", fmt.Errorf("scan runtime bundle identities: %w", err))
	}
	for _, entry := range bundleidentity.RuntimeNestedEntries(entries, t.AppPath, t.PreservedNestedCodePaths) {
		items = append(items, nestedCodeSignPath{Path: entry.RootPath})
	}
	machOPaths, err := discoverMachOCodeSignPaths(ctx, t)
	if err != nil {
		return nil, logPatchError(ctx, "patch.macho_code_discovery_failed", err)
	}
	for _, codePath := range machOPaths {
		items = append(items, nestedCodeSignPath{Path: codePath})
	}

	results := dedupeNestedCodeSignPaths(items)
	if len(results) > len(t.NestedSignPaths) {
		notef(r, fmt.Sprintf("target=%s discovered %d additional nested code objects for signing", t.ID, len(results)-len(t.NestedSignPaths)))
	}
	return results, nil
}

type nestedCodeSignPath struct {
	Path string
}

func dedupeNestedCodeSignPaths(items []nestedCodeSignPath) []string {
	seen := map[string]bool{}
	results := make([]string, 0, len(items))
	for _, item := range items {
		cleanPath := filepath.Clean(item.Path)
		key := cleanPath
		if resolved, err := filepath.EvalSymlinks(cleanPath); err == nil {
			key = filepath.Clean(resolved)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, cleanPath)
	}
	sort.Slice(results, func(i int, j int) bool {
		leftDepth := strings.Count(filepath.ToSlash(results[i]), "/")
		rightDepth := strings.Count(filepath.ToSlash(results[j]), "/")
		if leftDepth == rightDepth {
			return results[i] < results[j]
		}
		return leftDepth > rightDepth
	})
	return results
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

func stepWriteState(ctx context.Context, r *Runner, t targets.Target, version string, originalDR string) error {
	notef(r, fmt.Sprintf("target=%s write patch state version=%s -> %s", t.ID, version, paths.StateFile()))
	if r.DryRun {
		return nil
	}
	updateErr := state.Update(paths.StateFile(), func(ms state.MultiState) (state.MultiState, error) {
		ms.Targets[t.ID] = state.TargetState{
			PatchedVersion:                version,
			PatchedAt:                     clock.Now().UTC(),
			SignIdentity:                  paths.SignIdentity(),
			OriginalDesignatedRequirement: originalDR,
		}
		return ms, nil
	})
	if updateErr != nil {
		return logPatchError(ctx, "patch.save_state_failed", fmt.Errorf("save state file: %w", updateErr))
	}
	return nil
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

var readDesignatedRequirement = DesignatedRequirement

// resolveOriginalDRForPatch captures the upstream DesignatedRequirement for
// display only; nothing gates on it. It prefers the recorded value, then reads
// from the original executable, which lives at <exec>.real once the bundle is
// patched and at the main binary slot on a clean bundle. Capture is best effort:
// a missing or local-team DR records empty and the patch continues. Only a
// genuine filesystem stat error (not NotExist) is propagated.
func resolveOriginalDRForPatch(ctx context.Context, r *Runner, t targets.Target) (string, error) {
	ms, err := state.Load(paths.StateFile())
	if err != nil {
		return "", logPatchError(ctx, "patch.original_dr_load_state_failed", fmt.Errorf("load state.json: %w", err))
	}
	if entry, ok := ms.Targets[t.ID]; ok {
		recorded := strings.TrimSpace(entry.OriginalDesignatedRequirement)
		if recorded != "" && !designatedRequirementIdentifiesLocalTeam(recorded) {
			return recorded, nil
		}
	}

	source := paths.MainBinaryPath(t)
	realExists, err := realBinaryExists(ctx, t)
	if err != nil {
		return "", err
	}
	if realExists {
		source = paths.RealBinaryPath(t)
	}
	notef(r, fmt.Sprintf("target=%s capture upstream DR for display from %s", t.ID, source))
	if r.DryRun {
		if _, err := os.Stat(source); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", nil
			}
			return "", logPatchError(ctx, "patch.original_dr_source_stat_failed", fmt.Errorf("stat upstream executable %s: %w", source, err))
		}
	}
	dr, err := readDesignatedRequirement(ctx, source)
	if err != nil {
		notef(r, fmt.Sprintf("target=%s could not capture upstream DR from %s (continuing without it): %v", t.ID, source, err))
		return "", nil
	}
	if designatedRequirementIdentifiesLocalTeam(dr) {
		notef(r, fmt.Sprintf("target=%s captured DR identifies local signing team; recording no upstream DR", t.ID))
		return "", nil
	}
	return dr, nil
}

func stagePreservedNestedCode(ctx context.Context, r *Runner, t targets.Target) (string, func(), error) {
	patchLog.DebugContext(ctx, "patch.stage_preserved_nested_code.boundary", "target", t.ID, "count", len(t.PreservedNestedCodePaths), "dry_run", r.DryRun)
	if len(t.PreservedNestedCodePaths) == 0 {
		return "", func() {}, nil
	}
	stageRoot := filepath.Join(os.TempDir(), "desktop-via-clyde-preserved-code", t.ID)
	cleanup := func() {}
	if !r.DryRun {
		tempRoot, err := os.MkdirTemp("", "desktop-via-clyde-preserved-code-*")
		if err != nil {
			return "", nil, logPatchError(ctx, "patch.preserved_nested_code_temp_dir_failed", fmt.Errorf("create preserved nested code temp dir: %w", err))
		}
		stageRoot = tempRoot
		cleanup = func() {
			_ = os.RemoveAll(tempRoot)
		}
	}
	for _, relPath := range t.PreservedNestedCodePaths {
		if err := stagePreservedNestedCodePath(ctx, r, t, stageRoot, relPath); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return stageRoot, cleanup, nil
}

func stagePreservedNestedCodePath(ctx context.Context, r *Runner, t targets.Target, stageRoot string, relPath string) error {
	source := filepath.Join(t.AppPath, filepath.FromSlash(relPath))
	destination := filepath.Join(stageRoot, filepath.FromSlash(relPath))
	if r.DryRun {
		if err := r.Run(ctx, "/bin/cp", "-p", source, destination); err != nil {
			return logPatchError(ctx, "patch.preserved_nested_code_stage_file_failed", fmt.Errorf("stage preserved nested code file %s: %w", relPath, err))
		}
		return nil
	}
	info, err := os.Stat(source)
	if err != nil {
		return logPatchError(ctx, "patch.preserved_nested_code_source_stat_failed", fmt.Errorf("stat preserved nested code source %s: %w", source, err))
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return logPatchError(ctx, "patch.preserved_nested_code_stage_parent_failed", fmt.Errorf("create preserved nested code staging parent %s: %w", filepath.Dir(destination), err))
	}
	if info.IsDir() {
		if err := r.Run(ctx, "/usr/bin/rsync", "-a", source+"/", destination+"/"); err != nil {
			return logPatchError(ctx, "patch.preserved_nested_code_stage_directory_failed", fmt.Errorf("stage preserved nested code directory %s: %w", relPath, err))
		}
		return nil
	}
	if err := r.Run(ctx, "/bin/cp", "-p", source, destination); err != nil {
		return logPatchError(ctx, "patch.preserved_nested_code_stage_file_failed", fmt.Errorf("stage preserved nested code file %s: %w", relPath, err))
	}
	return nil
}

func designatedRequirementIdentifiesLocalTeam(dr string) bool {
	localTeamID := strings.TrimSpace(paths.SignTeamID())
	if localTeamID == "" {
		return false
	}
	return strings.Contains(dr, localTeamID)
}

func realBinaryExists(ctx context.Context, t targets.Target) (bool, error) {
	_, err := os.Stat(paths.RealBinaryPath(t))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, logPatchError(ctx, "patch.real_binary_stat_failed", fmt.Errorf("stat real binary %s: %w", paths.RealBinaryPath(t), err))
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
	if err := verifyRuntimeBundleTeams(ctx, r, t); err != nil {
		return err
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

// stepVerifyDevelopmentSigned verifies a bundle that took the development-signing
// overlay. That path intentionally leaves nested code on its vendor/Developer ID
// signatures and installs no shim, so the runtime-team check and the shim
// `--clyde-dry-run` (which would launch the real Electron binary) do not apply.
// Verification is the deep codesign check the spike used (verify_rc=0); amfi still
// accepts the launch through the embedded development profile for the registered
// device even though spctl rejects the unnotarized signature.
func stepVerifyDevelopmentSigned(ctx context.Context, r *Runner, t targets.Target) error {
	notef(r, fmt.Sprintf("target=%s verify development-signed bundle signature", t.ID))
	if r.DryRun {
		return nil
	}
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", t.AppPath); err != nil {
		return logPatchError(ctx, "patch.verify_development_signed_failed", fmt.Errorf("verify development-signed bundle %s: %w", t.AppPath, err))
	}
	if t.DevelopmentSigning != nil && t.DevelopmentSigning.ProxyInjection {
		if err := verifyDevelopmentSignedInjector(ctx, r, t); err != nil {
			return err
		}
	}
	return nil
}

func verifyRuntimeBundleTeams(ctx context.Context, r *Runner, t targets.Target) error {
	entries, err := bundleidentity.Scan(ctx, t.AppPath, bundleidentity.ScanOptions{
		IncludeSignatures: true,
		SignatureReader:   nil,
	})
	if err != nil {
		return logPatchError(ctx, "patch.verify_runtime_identity_scan_failed", fmt.Errorf("scan runtime bundle identities: %w", err))
	}
	localTeamID := strings.TrimSpace(paths.SignTeamID())
	for _, entry := range entries {
		if !entry.RuntimeCode {
			continue
		}
		if bundleidentity.IsPreserved(entry.RelativePath, t.PreservedNestedCodePaths) {
			continue
		}
		if entry.SignatureError != "" {
			return logPatchError(ctx, "patch.verify_runtime_identity_signature_failed",
				fmt.Errorf("runtime bundle %s at %s signature read failed: %s", entry.BundleID, entry.RelativePath, entry.SignatureError))
		}
		if entry.TeamID != localTeamID {
			return logPatchError(ctx, "patch.verify_runtime_identity_team_failed",
				fmt.Errorf("runtime bundle %s at %s signed by team %s, want %s", entry.BundleID, entry.RelativePath, entry.TeamID, localTeamID))
		}
	}
	notef(r, fmt.Sprintf("target=%s runtime bundle identities signed by team %s", t.ID, localTeamID))
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
