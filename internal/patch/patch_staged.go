package patch

import (
	"context"
	"fmt"
	"os"

	"goodkind.io/desktop-via-clyde/internal/devpreflight"
	"goodkind.io/desktop-via-clyde/internal/devsign"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// PreparedBundle carries the state that must survive the staged swap boundary.
type PreparedBundle struct {
	WorkTarget  targets.Target
	Captured    []KeychainItem
	Version     string
	OriginalDR  string
	signingPlan *bundleSigningPlan
}

// PrepareForSwap performs the in-bundle mutations that are safe to run against a
// staged copy of the app bundle before the live-path rename.
func PrepareForSwap(ctx context.Context, t targets.Target, opts Options) (PreparedBundle, error) {
	runner := newPatchRunner(ctx, opts)
	return preparePreparedBundle(ctx, runner, t, opts)
}

// FinalizeAfterSwap performs the live-path work that must run only after the
// staged bundle has been renamed into the final install location.
func FinalizeAfterSwap(ctx context.Context, prepared PreparedBundle, t targets.Target, opts Options) error {
	runner := newPatchRunner(ctx, opts)
	return finalizePreparedBundle(ctx, runner, prepared, finalTargetForOptions(t, opts), opts)
}

func newPatchRunner(ctx context.Context, opts Options) *Runner {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	runner := NewRunner(ctx, opts.DryRun, opts.Out)
	if opts.LogOut != nil {
		runner.RawOut = opts.LogOut
	}
	runner.Progress = opts.Progress
	runner.Trace = opts.Trace
	return runner
}

func preparePreparedBundle(ctx context.Context, r *Runner, t targets.Target, opts Options) (PreparedBundle, error) {
	workTarget := workTargetForOptions(t, opts)

	devpreflight.Warn(ctx, r.Progress, opts.DryRun, t)

	if !opts.DryRun {
		if _, err := os.Stat(workTarget.AppPath); err != nil {
			return PreparedBundle{}, logPatchError(ctx, "patch.bundle_stat_failed", fmt.Errorf("bundle not found at %s: %w", workTarget.AppPath, err))
		}
		if err := devsign.EnsureTrustedMITMCA(ctx, t); err != nil {
			return PreparedBundle{}, logPatchError(ctx, "patch.mitm_trust_required", fmt.Errorf("verify MITM CA trust before patch: %w", err))
		}
	}

	info, err := loadInfoPlistOrPlaceholder(workTarget, opts.DryRun)
	if err != nil {
		return PreparedBundle{}, err
	}
	notef(r, fmt.Sprintf("target=%s read Info.plist version=%s id=%s exec=%s",
		workTarget.ID, info.CFBundleVersion, info.CFBundleIdentifier, info.CFBundleExecutable))

	originalDR, err := resolveOriginalDRForPatch(ctx, r, workTarget)
	if err != nil {
		return PreparedBundle{}, err
	}

	var captured []KeychainItem
	switch {
	case !opts.MigrateKeychain:
		notef(r, fmt.Sprintf("target=%s skipped keychain access repair (pass --migrate-keychain to run)", workTarget.ID))
	case opts.DryRun:
		notef(r, fmt.Sprintf("target=%s would find keychain items for services=%v", workTarget.ID, workTarget.KeychainServices))
	default:
		captured, err = CaptureItems(ctx, t)
		if err != nil {
			return PreparedBundle{}, logPatchError(ctx, "patch.keychain_capture_failed", fmt.Errorf("keychain capture: %w", err))
		}
		notef(r, fmt.Sprintf("target=%s found %d keychain items", workTarget.ID, len(captured)))
	}

	signingPlan, err := patchBundleSteps(ctx, r, &workTarget, opts)
	if err != nil {
		return PreparedBundle{}, err
	}
	return PreparedBundle{
		WorkTarget:  workTarget,
		Captured:    captured,
		Version:     info.CFBundleVersion,
		OriginalDR:  originalDR,
		signingPlan: signingPlan,
	}, nil
}

func finalizePreparedBundle(ctx context.Context, r *Runner, prepared PreparedBundle, t targets.Target, opts Options) error {
	signingPlan := prepared.signingPlan
	if signingPlan == nil {
		signingPlan = defaultStandardSigningPlan()
	}

	if err := runPostBundleHooks(ctx, r, t, opts); err != nil {
		return logPatchError(ctx, "patch.post_bundle_hook_failed", fmt.Errorf("run post-bundle hooks: %w", err))
	}

	for _, capability := range t.PostPatchHookCapabilities() {
		if err := runPostPatchHook(ctx, r, t, opts, capability); err != nil {
			return logPatchError(ctx, "patch.post_patch_hook_failed", fmt.Errorf("run post-patch hook %q: %w", capability, err))
		}
	}

	if signingPlan.sealPhase == bundleSealAfterFinalize {
		if err := sealBundleSigningPlan(ctx, r, t, signingPlan); err != nil {
			return err
		}
	}
	if err := verifyBundleSigningPlan(ctx, r, t, signingPlan); err != nil {
		return logPatchError(ctx, "patch.verify_failed", fmt.Errorf("verify: %w", err))
	}

	if err := stepWriteState(ctx, r, t, prepared.Version, prepared.OriginalDR); err != nil {
		return logPatchError(ctx, "patch.write_state_failed", fmt.Errorf("write state: %w", err))
	}

	restoreKeychainAccess(ctx, r, t, opts, prepared.Captured)

	notef(r, fmt.Sprintf("target=%s patch complete", t.ID))
	return nil
}

func workTargetForOptions(t targets.Target, opts Options) targets.Target {
	workTarget := t
	if opts.AppPathOverride != "" {
		workTarget.AppPath = opts.AppPathOverride
	}
	return workTarget
}

func finalTargetForOptions(t targets.Target, opts Options) targets.Target {
	finalTarget := t
	switch {
	case opts.FinalAppPath != "":
		finalTarget.AppPath = opts.FinalAppPath
	case opts.AppPathOverride != "":
		finalTarget.AppPath = opts.AppPathOverride
	}
	return finalTarget
}
