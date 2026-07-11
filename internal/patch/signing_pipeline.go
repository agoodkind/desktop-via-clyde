package patch

import (
	"context"
	"fmt"

	"goodkind.io/desktop-via-clyde/internal/devsign"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

type bundleSigningStrategyKind string

const (
	bundleSigningStrategyStandard    bundleSigningStrategyKind = "standard"
	bundleSigningStrategyDevelopment bundleSigningStrategyKind = "development"
)

type bundleSealPhase string

const (
	bundleSealBeforeFinalize bundleSealPhase = "before_finalize"
	bundleSealAfterFinalize  bundleSealPhase = "after_finalize"
)

type bundleSigningPlan struct {
	kind                 bundleSigningStrategyKind
	sealPhase            bundleSealPhase
	developmentPlan      *devsign.Plan
	standardEntitlements string
	prepare              bundleSigningPrepareFunc
	seal                 bundleSigningSealFunc
	verify               bundleSigningVerifyFunc
}

type (
	bundleSigningPrepareFunc func(context.Context, *Runner, *targets.Target, Options, *bundleSigningPlan) error
	bundleSigningSealFunc    func(context.Context, *Runner, targets.Target, *bundleSigningPlan) error
	bundleSigningVerifyFunc  func(context.Context, *Runner, targets.Target) error
)

var selectBundleSigningPlanForPatch = selectBundleSigningPlan

func selectBundleSigningPlan(target targets.Target) bundleSigningPlan {
	policy := target.DevelopmentSigning
	if policy != nil && policy.Enabled && len(devsign.MissingAssets(*policy)) == 0 {
		return bundleSigningPlan{
			kind:                 bundleSigningStrategyDevelopment,
			sealPhase:            bundleSealAfterFinalize,
			developmentPlan:      nil,
			standardEntitlements: "",
			prepare:              prepareDevelopmentBundleSigning,
			seal:                 sealDevelopmentBundle,
			verify:               stepVerifyDevelopmentSigned,
		}
	}
	return bundleSigningPlan{
		kind:                 bundleSigningStrategyStandard,
		sealPhase:            bundleSealBeforeFinalize,
		developmentPlan:      nil,
		standardEntitlements: "",
		prepare:              prepareStandardBundleSigning,
		seal:                 sealStandardBundle,
		verify:               stepVerify,
	}
}

func prepareBundleSigning(
	ctx context.Context,
	runner *Runner,
	target *targets.Target,
	options Options,
	plan *bundleSigningPlan,
) error {
	if plan == nil || plan.prepare == nil {
		return fmt.Errorf("bundle signing plan for target %s has no preparation function", target.ID)
	}
	traceAction(runner, actionPrepareSigningStrategy, target.ID, string(plan.kind))
	return plan.prepare(ctx, runner, target, options, plan)
}

func prepareStandardBundleSigning(
	ctx context.Context,
	runner *Runner,
	target *targets.Target,
	options Options,
	plan *bundleSigningPlan,
) error {
	noteDevelopmentSigningFallback(runner, *target)
	preservedRoot, cleanupPreserved, err := stagePreservedNestedCode(ctx, runner, *target)
	if err != nil {
		return logPatchError(ctx, "patch.stage_preserved_nested_code_failed", fmt.Errorf("stage preserved nested code: %w", err))
	}
	defer cleanupPreserved()

	entitlementsFile, err := stepExtractEntitlements(ctx, runner, *target)
	if err != nil {
		return logPatchError(ctx, "patch.extract_entitlements_failed", fmt.Errorf("extract entitlements: %w", err))
	}
	plan.standardEntitlements = entitlementsFile
	notef(runner, fmt.Sprintf("target=%s augment entitlements (strip=%v required=%v)",
		target.ID, target.Entitlements.Strip, target.Entitlements.RequiredBooleanEntitlements))
	if err := stepMoveToReal(ctx, runner, *target); err != nil {
		return logPatchError(ctx, "patch.move_binary_failed", fmt.Errorf("move binary to .real: %w", err))
	}
	if err := stepPreLaunchPolicy(ctx, runner, target, options); err != nil {
		return logPatchError(ctx, "patch.pre_launch_policy_hook_failed", fmt.Errorf("run pre-launch-policy hooks: %w", err))
	}
	if err := stepInstallShim(ctx, runner, *target); err != nil {
		return logPatchError(ctx, "patch.install_shim_failed", fmt.Errorf("install shim: %w", err))
	}
	if err := maybeApplyStandardProxyInjection(ctx, runner, *target); err != nil {
		return err
	}
	if err := stepRestorePreservedNestedCode(ctx, runner, *target, preservedRoot); err != nil {
		return logPatchError(ctx, "patch.restore_preserved_nested_code_failed", fmt.Errorf("restore preserved nested code: %w", err))
	}
	if err := stepEmbedProvisioningProfile(ctx, runner, *target); err != nil {
		return logPatchError(ctx, "patch.embed_provisioning_profile_failed", fmt.Errorf("embed provisioning profile: %w", err))
	}
	return nil
}

func prepareDevelopmentBundleSigning(
	ctx context.Context,
	runner *Runner,
	target *targets.Target,
	_ Options,
	plan *bundleSigningPlan,
) error {
	notef(runner, fmt.Sprintf("target=%s development signing enabled; applying enrollment-fix overlay (skipping shim, move-to-real, and Developer ID re-sign)", target.ID))
	developmentPlan, err := devsign.ApplyNestedMutations(ctx, runner, devsign.Options{
		DryRun:   runner.DryRun,
		Out:      runner.Out,
		Progress: runner.Progress,
	}, *target)
	if err != nil {
		return logPatchError(ctx, "patch.development_signing_failed", fmt.Errorf("apply development signing: %w", err))
	}
	plan.developmentPlan = developmentPlan
	return nil
}

func noteDevelopmentSigningFallback(runner *Runner, target targets.Target) {
	policy := target.DevelopmentSigning
	if policy == nil || !policy.Enabled {
		return
	}
	for _, asset := range devsign.MissingAssets(*policy) {
		notef(runner, fmt.Sprintf("target=%s development signing requested but %s is missing at %s; provide it to enable the enrollment fix, continuing with the shim + Developer ID path",
			target.ID, asset.Label, asset.Path))
	}
}

func sealBundleSigningPlan(
	ctx context.Context,
	runner *Runner,
	target targets.Target,
	plan *bundleSigningPlan,
) error {
	if plan == nil || plan.seal == nil {
		return fmt.Errorf("bundle signing plan for target %s has no seal function", target.ID)
	}
	traceAction(runner, actionSealSigningStrategy, target.ID, string(plan.kind))
	return plan.seal(ctx, runner, target, plan)
}

func verifyBundleSigningPlan(
	ctx context.Context,
	runner *Runner,
	target targets.Target,
	plan *bundleSigningPlan,
) error {
	if plan == nil || plan.verify == nil {
		return fmt.Errorf("bundle signing plan for target %s has no verification function", target.ID)
	}
	traceAction(runner, actionVerifySigningStrategy, target.ID, string(plan.kind))
	return plan.verify(ctx, runner, target)
}

func sealStandardBundle(ctx context.Context, runner *Runner, target targets.Target, plan *bundleSigningPlan) error {
	if err := stepResign(ctx, runner, target, plan.standardEntitlements); err != nil {
		return logPatchError(ctx, "patch.resign_failed", fmt.Errorf("re-sign: %w", err))
	}
	stepStripQuarantine(ctx, runner, target)
	return nil
}

func sealDevelopmentBundle(ctx context.Context, runner *Runner, target targets.Target, plan *bundleSigningPlan) error {
	if err := devsign.Reseal(ctx, runner, devsign.Options{
		DryRun:   runner.DryRun,
		Out:      runner.Out,
		Progress: runner.Progress,
	}, target, plan.developmentPlan); err != nil {
		return logPatchError(ctx, "patch.development_signing_reseal_failed", fmt.Errorf("reseal development-signed bundle: %w", err))
	}
	return nil
}

func defaultStandardSigningPlan() *bundleSigningPlan {
	plan := bundleSigningPlan{
		kind:                 bundleSigningStrategyStandard,
		sealPhase:            bundleSealBeforeFinalize,
		developmentPlan:      nil,
		standardEntitlements: "",
		prepare:              prepareStandardBundleSigning,
		seal:                 sealStandardBundle,
		verify:               stepVerify,
	}
	return &plan
}
