package patch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/bundlemutate"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestSelectBundleSigningPlanUsesStandardStrategyByDefault(t *testing.T) {
	plan := selectBundleSigningPlan(targets.Target{ID: "standard"})

	if plan.kind != bundleSigningStrategyStandard {
		t.Fatalf("strategy = %q, want %q", plan.kind, bundleSigningStrategyStandard)
	}
	if plan.sealPhase != bundleSealBeforeFinalize {
		t.Fatalf("seal phase = %q, want %q", plan.sealPhase, bundleSealBeforeFinalize)
	}
}

func TestSelectBundleSigningPlanUsesDevelopmentStrategyWhenAssetsExist(t *testing.T) {
	policy := developmentSigningPolicyWithAssets(t)
	plan := selectBundleSigningPlan(targets.Target{ID: "development", DevelopmentSigning: policy})

	if plan.kind != bundleSigningStrategyDevelopment {
		t.Fatalf("strategy = %q, want %q", plan.kind, bundleSigningStrategyDevelopment)
	}
	if plan.sealPhase != bundleSealAfterFinalize {
		t.Fatalf("seal phase = %q, want %q", plan.sealPhase, bundleSealAfterFinalize)
	}
}

func TestSelectBundleSigningPlanFallsBackToStandardWhenAssetsAreMissing(t *testing.T) {
	plan := selectBundleSigningPlan(targets.Target{
		ID: "missing-assets",
		DevelopmentSigning: &targets.DevelopmentSigningPolicy{
			Enabled:         true,
			ProfilePath:     filepath.Join(t.TempDir(), "missing.provisionprofile"),
			P12Path:         filepath.Join(t.TempDir(), "missing.p12"),
			P12PasswordFile: filepath.Join(t.TempDir(), "missing-password"),
		},
	})

	if plan.kind != bundleSigningStrategyStandard {
		t.Fatalf("strategy = %q, want %q", plan.kind, bundleSigningStrategyStandard)
	}
	if plan.sealPhase != bundleSealBeforeFinalize {
		t.Fatalf("seal phase = %q, want %q", plan.sealPhase, bundleSealBeforeFinalize)
	}
}

func TestSelectBundleSigningPlanProvidesStrategyFunctions(t *testing.T) {
	standard := selectBundleSigningPlan(targets.Target{ID: "standard"})
	if standard.prepare == nil {
		t.Fatal("standard strategy has no preparation function")
	}
	if standard.seal == nil {
		t.Fatal("standard strategy has no seal function")
	}
	if standard.verify == nil {
		t.Fatal("standard strategy has no verification function")
	}

	development := selectBundleSigningPlan(targets.Target{
		ID:                 "development",
		DevelopmentSigning: developmentSigningPolicyWithAssets(t),
	})
	if development.prepare == nil {
		t.Fatal("development strategy has no preparation function")
	}
	if development.seal == nil {
		t.Fatal("development strategy has no seal function")
	}
	if development.verify == nil {
		t.Fatal("development strategy has no verification function")
	}
}

func TestPatchBundleStepsPreparationFailureStopsPipeline(t *testing.T) {
	preparationErr := errors.New("preparation failed")
	registerSigningPipelineHook(t, Action("bundle_mutation"))
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}
	runner := NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace
	plan := bundleSigningPlan{
		kind:      bundleSigningStrategyStandard,
		sealPhase: bundleSealBeforeFinalize,
		prepare: func(context.Context, *Runner, *targets.Target, Options, *bundleSigningPlan) error {
			return preparationErr
		},
		seal: func(_ context.Context, runner *Runner, target targets.Target, _ *bundleSigningPlan) error {
			RecordTrace(runner, Action("test_seal"), target.ID, target.AppPath)
			return nil
		},
		verify: stepVerify,
	}

	_, err := patchBundleStepsWithPlan(context.Background(), runner, &target, Options{DryRun: true}, plan)
	if !errors.Is(err, preparationErr) {
		t.Fatalf("patchBundleStepsWithPlan error = %v, want %v", err, preparationErr)
	}
	if traceActionIndexForPipeline(trace, Action("bundle_mutation")) >= 0 {
		t.Fatalf("bundle mutation ran after preparation failure: %#v", trace.Events)
	}
	if traceActionIndexForPipeline(trace, Action("test_seal")) >= 0 {
		t.Fatalf("seal ran after preparation failure: %#v", trace.Events)
	}
}

func TestPatchPreparationFailureStopsPipelineAndStateWrite(t *testing.T) {
	installFixture(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	preparationErr := errors.New("preparation failed")
	registerSigningPipelineHook(t, Action("bundle_mutation"))
	previousSelector := selectBundleSigningPlanForPatch
	selectBundleSigningPlanForPatch = func(targets.Target) bundleSigningPlan {
		return bundleSigningPlan{
			kind:      bundleSigningStrategyStandard,
			sealPhase: bundleSealBeforeFinalize,
			prepare: func(context.Context, *Runner, *targets.Target, Options, *bundleSigningPlan) error {
				return preparationErr
			},
			seal: func(_ context.Context, runner *Runner, target targets.Target, _ *bundleSigningPlan) error {
				RecordTrace(runner, Action("test_seal"), target.ID, target.AppPath)
				return nil
			},
			verify: func(_ context.Context, runner *Runner, target targets.Target) error {
				RecordTrace(runner, Action("test_verify"), target.ID, target.AppPath)
				return nil
			},
		}
	}
	t.Cleanup(func() {
		selectBundleSigningPlanForPatch = previousSelector
	})
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}

	err := Patch(context.Background(), target, Options{DryRun: true, Out: io.Discard, Trace: trace})
	if !errors.Is(err, preparationErr) {
		t.Fatalf("Patch error = %v, want %v", err, preparationErr)
	}
	requirePipelineActionCount(t, trace, actionPrepareSigningStrategy, 1)
	for _, action := range []Action{
		Action("bundle_mutation"),
		Action("test_seal"),
		Action("test_verify"),
		actionWritePatchState,
	} {
		requirePipelineActionCount(t, trace, action, 0)
	}
	requireNoPatchStateFile(t)
}

func TestPatchPreResignMutationFailureStopsSealVerificationAndStateWrite(t *testing.T) {
	installFixture(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	mutationErr := errors.New("bundle mutation failed")
	registerFailingSigningPipelineHook(t, mutationErr)
	setSigningPlanSelectorForTest(t, func(targets.Target) bundleSigningPlan {
		return testSigningPlan(bundleSigningStrategyStandard, bundleSealBeforeFinalize, nil)
	})
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}

	err := Patch(context.Background(), target, Options{DryRun: true, Out: io.Discard, Trace: trace})
	if !errors.Is(err, mutationErr) {
		t.Fatalf("Patch error = %v, want %v", err, mutationErr)
	}
	requirePipelineSubsequence(t, trace, actionPrepareSigningStrategy, Action("test_prepare"), Action("failing_bundle_mutation"))
	for _, action := range []Action{actionPrepareSigningStrategy, Action("test_prepare"), Action("failing_bundle_mutation")} {
		requirePipelineActionCount(t, trace, action, 1)
	}
	for _, action := range []Action{actionSealSigningStrategy, Action("test_seal"), actionVerifySigningStrategy, Action("test_verify"), actionWritePatchState} {
		requirePipelineActionCount(t, trace, action, 0)
	}
	requireNoPatchStateFile(t)
}

func TestPatchStandardSealFailureStopsVerificationAndStateWrite(t *testing.T) {
	installFixture(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	sealErr := errors.New("standard seal failed")
	registerSigningPipelineHook(t, Action("bundle_mutation"))
	setSigningPlanSelectorForTest(t, func(targets.Target) bundleSigningPlan {
		return testSigningPlan(bundleSigningStrategyStandard, bundleSealBeforeFinalize, sealErr)
	})
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}

	err := Patch(context.Background(), target, Options{DryRun: true, Out: io.Discard, Trace: trace})
	if !errors.Is(err, sealErr) {
		t.Fatalf("Patch error = %v, want %v", err, sealErr)
	}
	want := []Action{actionPrepareSigningStrategy, Action("test_prepare"), Action("bundle_mutation"), actionSealSigningStrategy, Action("test_seal")}
	requirePipelineSubsequence(t, trace, want...)
	for _, action := range want {
		requirePipelineActionCount(t, trace, action, 1)
	}
	for _, action := range []Action{actionVerifySigningStrategy, Action("test_verify"), actionWritePatchState} {
		requirePipelineActionCount(t, trace, action, 0)
	}
	requireNoPatchStateFile(t)
}

func TestPatchBundleStepsSealFailureStopsAfterMutation(t *testing.T) {
	sealErr := errors.New("seal failed")
	registerSigningPipelineHook(t, Action("bundle_mutation"))
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}
	runner := NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace
	plan := bundleSigningPlan{
		kind:      bundleSigningStrategyStandard,
		sealPhase: bundleSealBeforeFinalize,
		prepare: func(context.Context, *Runner, *targets.Target, Options, *bundleSigningPlan) error {
			return nil
		},
		seal: func(_ context.Context, runner *Runner, target targets.Target, _ *bundleSigningPlan) error {
			RecordTrace(runner, Action("test_seal"), target.ID, target.AppPath)
			return sealErr
		},
		verify: stepVerify,
	}

	_, err := patchBundleStepsWithPlan(context.Background(), runner, &target, Options{DryRun: true}, plan)
	if !errors.Is(err, sealErr) {
		t.Fatalf("patchBundleStepsWithPlan error = %v, want %v", err, sealErr)
	}
	requirePipelineOrder(
		t, trace,
		actionPrepareSigningStrategy,
		Action("bundle_mutation"),
		actionSealSigningStrategy,
		Action("test_seal"),
	)
	if traceActionIndexForPipeline(trace, actionWritePatchState) >= 0 {
		t.Fatalf("state write ran after seal failure: %#v", trace.Events)
	}
}

func TestFinalizePreparedBundleVerificationFailureSuppressesStateWrite(t *testing.T) {
	verificationErr := errors.New("verification failed")
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}
	runner := NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace
	plan := &bundleSigningPlan{
		kind:      bundleSigningStrategyStandard,
		sealPhase: bundleSealBeforeFinalize,
		verify: func(_ context.Context, runner *Runner, target targets.Target) error {
			RecordTrace(runner, Action("test_verify"), target.ID, target.AppPath)
			return verificationErr
		},
	}

	err := finalizePreparedBundle(context.Background(), runner, PreparedBundle{
		Version:     "1.0.0",
		OriginalDR:  "identifier test",
		signingPlan: plan,
	}, target, Options{DryRun: true})
	if !errors.Is(err, verificationErr) {
		t.Fatalf("finalizePreparedBundle error = %v, want %v", err, verificationErr)
	}
	requirePipelineOrder(t, trace, actionVerifySigningStrategy, Action("test_verify"))
	if traceActionIndexForPipeline(trace, actionWritePatchState) >= 0 {
		t.Fatalf("state write ran after verification failure: %#v", trace.Events)
	}
}

func TestFinalizePreparedBundleDevelopmentSealFailureSuppressesVerificationAndState(t *testing.T) {
	sealErr := errors.New("development seal failed")
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}
	runner := NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace
	plan := &bundleSigningPlan{
		kind:      bundleSigningStrategyDevelopment,
		sealPhase: bundleSealAfterFinalize,
		seal: func(_ context.Context, runner *Runner, target targets.Target, _ *bundleSigningPlan) error {
			RecordTrace(runner, Action("test_seal"), target.ID, target.AppPath)
			return sealErr
		},
		verify: func(_ context.Context, runner *Runner, target targets.Target) error {
			RecordTrace(runner, Action("test_verify"), target.ID, target.AppPath)
			return nil
		},
	}

	err := finalizePreparedBundle(context.Background(), runner, PreparedBundle{
		Version:     "1.0.0",
		OriginalDR:  "identifier test",
		signingPlan: plan,
	}, target, Options{DryRun: true})
	if !errors.Is(err, sealErr) {
		t.Fatalf("finalizePreparedBundle error = %v, want %v", err, sealErr)
	}
	requirePipelineOrder(t, trace, actionSealSigningStrategy, Action("test_seal"))
	if traceActionIndexForPipeline(trace, Action("test_verify")) >= 0 {
		t.Fatalf("verification ran after development seal failure: %#v", trace.Events)
	}
	if traceActionIndexForPipeline(trace, actionWritePatchState) >= 0 {
		t.Fatalf("state write ran after development seal failure: %#v", trace.Events)
	}
}

func TestStandardSigningSealsBeforeFinalization(t *testing.T) {
	installFixture(t)
	registerSigningPipelineHook(t, Action("bundle_mutation"))
	registerSigningPipelinePostBundleHook(t, Action("post_bundle"))
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}
	if err := Patch(context.Background(), target, Options{DryRun: true, Out: io.Discard, Trace: trace}); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	requirePipelineOrder(
		t, trace,
		actionPrepareSigningStrategy,
		Action("bundle_mutation"),
		actionSealSigningStrategy,
		Action("post_bundle"),
		actionVerifySigningStrategy,
		actionWritePatchState,
	)
}

func TestMissingDevelopmentAssetsRunStandardSigningPipeline(t *testing.T) {
	installFixture(t)
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	missingDirectory := t.TempDir()
	target.DevelopmentSigning = &targets.DevelopmentSigningPolicy{
		Enabled:         true,
		ProfilePath:     filepath.Join(missingDirectory, "missing.provisionprofile"),
		P12Path:         filepath.Join(missingDirectory, "missing.p12"),
		P12PasswordFile: filepath.Join(missingDirectory, "missing-password"),
	}
	trace := &Trace{}
	var output bytes.Buffer
	if err := Patch(context.Background(), target, Options{DryRun: true, Out: &output, Trace: trace}); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	prepareIndex := traceActionIndexForPipeline(trace, actionPrepareSigningStrategy)
	if prepareIndex < 0 || trace.Events[prepareIndex].Path != string(bundleSigningStrategyStandard) {
		t.Fatalf("missing assets did not select standard strategy: %#v", trace.Events)
	}
	if traceActionIndexForPipeline(trace, actionSignBundle) < 0 {
		t.Fatalf("missing assets did not run Developer ID bundle signing: %#v", trace.Events)
	}
	if traceDevelopmentSealIndex(trace, target.AppPath) >= 0 {
		t.Fatalf("missing assets ran development reseal: %#v", trace.Events)
	}
	for _, missingPath := range []string{
		target.DevelopmentSigning.ProfilePath,
		target.DevelopmentSigning.P12Path,
		target.DevelopmentSigning.P12PasswordFile,
	} {
		if !strings.Contains(output.String(), missingPath) {
			t.Fatalf("fallback output does not name missing asset %q:\n%s", missingPath, output.String())
		}
	}
}

func TestStagedDevelopmentSigningSealsAfterFinalization(t *testing.T) {
	installFixture(t)
	registerSigningPipelineHook(t, Action("bundle_mutation"))
	registerSigningPipelinePostBundleHook(t, Action("post_bundle"))
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "live", "Codex.app"), "")
	target.BundleID = "com.openai.codex.beta"
	target.DevelopmentSigning = developmentSigningPolicyWithAssets(t)
	trace := &Trace{}
	options := Options{
		DryRun:          true,
		Out:             io.Discard,
		Trace:           trace,
		AppPathOverride: filepath.Join(t.TempDir(), "staged", "Codex.app"),
		FinalAppPath:    target.AppPath,
	}
	prepared, err := PrepareForSwap(context.Background(), target, options)
	if err != nil {
		t.Fatalf("PrepareForSwap: %v", err)
	}
	if traceActionIndexForPipeline(trace, actionSealSigningStrategy) >= 0 {
		t.Fatalf("development signing sealed before staged finalization: %#v", trace.Events)
	}
	if err := FinalizeAfterSwap(context.Background(), prepared, target, options); err != nil {
		t.Fatalf("FinalizeAfterSwap: %v", err)
	}

	requirePipelineOrder(
		t, trace,
		actionPrepareSigningStrategy,
		Action("bundle_mutation"),
		Action("post_bundle"),
		actionSealSigningStrategy,
		actionVerifySigningStrategy,
		actionWritePatchState,
	)
	sealIndex := traceDevelopmentSealIndex(trace, target.AppPath)
	if sealIndex < 0 {
		t.Fatalf("staged development signing did not reseal final app path %s: %#v", target.AppPath, trace.Events)
	}
}

func TestDevelopmentSigningRunsPreResignMutationBeforeFinalSeal(t *testing.T) {
	installFixture(t)
	registerSigningPipelineHook(t, Action("bundle_mutation"))
	registerSigningPipelinePostBundleHook(t, Action("post_bundle"))

	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	target.BundleID = "com.openai.codex.beta"
	target.DevelopmentSigning = developmentSigningPolicyWithAssets(t)
	trace := &Trace{}
	if err := Patch(context.Background(), target, Options{DryRun: true, Out: io.Discard, Trace: trace}); err != nil {
		t.Fatalf("Patch: %v", err)
	}

	mutationIndex := traceActionIndexForPipeline(trace, Action("bundle_mutation"))
	sealIndex := traceDevelopmentSealIndex(trace, target.AppPath)
	if mutationIndex < 0 {
		t.Fatalf("development strategy skipped the pre-resign mutation: %#v", trace.Events)
	}
	if sealIndex < 0 {
		t.Fatalf("development strategy did not seal the bundle: %#v", trace.Events)
	}
	if mutationIndex >= sealIndex {
		t.Fatalf("pre-resign mutation index %d must precede development seal index %d: %#v", mutationIndex, sealIndex, trace.Events)
	}
	requirePipelineOrder(
		t, trace,
		actionPrepareSigningStrategy,
		Action("bundle_mutation"),
		Action("post_bundle"),
		actionSealSigningStrategy,
		actionVerifySigningStrategy,
		actionWritePatchState,
	)
}

func TestStagedSigningCommitBoundaryOrdersBothStrategies(t *testing.T) {
	for _, test := range []struct {
		name  string
		kind  bundleSigningStrategyKind
		phase bundleSealPhase
		want  []Action
	}{
		{
			name:  "standard",
			kind:  bundleSigningStrategyStandard,
			phase: bundleSealBeforeFinalize,
			want: []Action{
				actionPrepareSigningStrategy,
				Action("test_prepare"),
				Action("bundle_mutation"),
				actionSealSigningStrategy,
				Action("test_seal"),
				Action("staged_commit"),
				Action("post_bundle"),
				Action("post_patch"),
				actionVerifySigningStrategy,
				Action("test_verify"),
				actionWritePatchState,
			},
		},
		{
			name:  "development",
			kind:  bundleSigningStrategyDevelopment,
			phase: bundleSealAfterFinalize,
			want: []Action{
				actionPrepareSigningStrategy,
				Action("test_prepare"),
				Action("bundle_mutation"),
				Action("staged_commit"),
				Action("post_bundle"),
				Action("post_patch"),
				actionSealSigningStrategy,
				Action("test_seal"),
				actionVerifySigningStrategy,
				Action("test_verify"),
				actionWritePatchState,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			root := t.TempDir()
			liveApp := writePipelineBundle(t, filepath.Join(root, "Applications", "Codex.app"), "old")
			adoptApp := writePipelineBundle(t, filepath.Join(root, "Downloads", "Codex.app"), "new")
			target := stagedPatchTarget(liveApp, "")
			trace := &Trace{}
			registerSigningPipelineHook(t, Action("bundle_mutation"))
			registerSigningPipelinePostBundleHookWithAssertion(t, Action("post_bundle"), func() {
				requirePipelineBundleContents(t, liveApp, "new")
			})
			capability := registerSigningPipelinePostPatchHook(t, Action("post_patch"), func() {
				requirePipelineBundleContents(t, liveApp, "new")
			})
			target.Extensions = extensions.Target{
				BundledCLITee: &extensions.BundledCLITeeSpec{Capability: capability},
			}

			var prepared PreparedBundle
			err := bundlemutate.StagedSwap(context.Background(), target, bundlemutate.SwapOptions{
				AdoptPath: adoptApp,
				Verify: func(ctx context.Context) error {
					runner := NewRunner(ctx, true, io.Discard)
					runner.Trace = trace
					RecordTrace(runner, Action("staged_commit"), target.ID, target.AppPath)
					requirePipelineBundleContents(t, liveApp, "new")
					return FinalizeAfterSwap(ctx, prepared, target, Options{DryRun: true, Out: io.Discard, Trace: trace})
				},
			}, func(stagedAppPath string) error {
				runner := NewRunner(context.Background(), true, io.Discard)
				runner.Trace = trace
				workTarget := target
				workTarget.AppPath = stagedAppPath
				plan := testSigningPlan(test.kind, test.phase, nil)
				plan.seal = func(_ context.Context, runner *Runner, sealTarget targets.Target, _ *bundleSigningPlan) error {
					RecordTrace(runner, Action("test_seal"), sealTarget.ID, sealTarget.AppPath)
					if test.kind == bundleSigningStrategyStandard {
						requirePipelineBundleContents(t, liveApp, "old")
						requirePipelineBundleContents(t, stagedAppPath, "new")
						if sealTarget.AppPath != stagedAppPath {
							t.Fatalf("standard seal path = %q, want staged path %q", sealTarget.AppPath, stagedAppPath)
						}
						return nil
					}
					if sealTarget.AppPath != liveApp {
						t.Fatalf("development seal path = %q, want live path %q", sealTarget.AppPath, liveApp)
					}
					requirePipelineBundleContents(t, liveApp, "new")
					return nil
				}
				selectedPlan, prepareErr := patchBundleStepsWithPlan(context.Background(), runner, &workTarget, Options{DryRun: true, FinalAppPath: liveApp}, plan)
				if prepareErr != nil {
					return prepareErr
				}
				prepared = PreparedBundle{Version: "1", OriginalDR: "identifier test", signingPlan: selectedPlan}
				return nil
			})
			if err != nil {
				t.Fatalf("StagedSwap: %v", err)
			}

			requirePipelineSubsequence(t, trace, test.want...)
			for _, action := range test.want {
				requirePipelineActionCount(t, trace, action, 1)
			}
		})
	}
}

func TestStagedFinalizeFailureRestoresPreviousBundleAndSuppressesStateWrite(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	liveApp := writePipelineBundle(t, filepath.Join(root, "Applications", "Codex.app"), "old")
	adoptApp := writePipelineBundle(t, filepath.Join(root, "Downloads", "Codex.app"), "new")
	target := stagedPatchTarget(liveApp, "")
	finalizeErr := errors.New("post-bundle finalization failed")
	registerFailingPostBundleHook(t, finalizeErr)
	trace := &Trace{}
	var prepared PreparedBundle

	err := bundlemutate.StagedSwap(context.Background(), target, bundlemutate.SwapOptions{
		AdoptPath: adoptApp,
		Verify: func(ctx context.Context) error {
			return FinalizeAfterSwap(ctx, prepared, target, Options{Out: io.Discard, Trace: trace})
		},
	}, func(stagedAppPath string) error {
		runner := NewRunner(context.Background(), true, io.Discard)
		runner.Trace = trace
		workTarget := target
		workTarget.AppPath = stagedAppPath
		selectedPlan, prepareErr := patchBundleStepsWithPlan(
			context.Background(), runner, &workTarget, Options{DryRun: true, FinalAppPath: liveApp},
			testSigningPlan(bundleSigningStrategyStandard, bundleSealBeforeFinalize, nil),
		)
		if prepareErr != nil {
			return prepareErr
		}
		prepared = PreparedBundle{Version: "1", OriginalDR: "identifier test", signingPlan: selectedPlan}
		return nil
	})
	if !errors.Is(err, finalizeErr) {
		t.Fatalf("StagedSwap error = %v, want %v", err, finalizeErr)
	}
	requirePipelineBundleContents(t, liveApp, "old")
	requirePipelineActionCount(t, trace, actionWritePatchState, 0)
	requireNoPatchStateFile(t)
}

func registerSigningPipelineHook(t *testing.T, action Action) string {
	t.Helper()
	hookName := "test-signing-pipeline-" + t.Name()
	if err := RegisterPreResignHook(hookName, func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
		RecordTrace(runner, action, target.ID, target.AppPath)
		return nil
	}); err != nil {
		t.Fatalf("RegisterPreResignHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(preResignHooks, hookName)
		hooksMu.Unlock()
	})
	return hookName
}

func registerFailingSigningPipelineHook(t *testing.T, hookErr error) {
	t.Helper()
	hookName := "000-test-signing-pipeline-failure-" + t.Name()
	if err := RegisterPreResignHook(hookName, func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
		RecordTrace(runner, Action("failing_bundle_mutation"), target.ID, target.AppPath)
		return hookErr
	}); err != nil {
		t.Fatalf("RegisterPreResignHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(preResignHooks, hookName)
		hooksMu.Unlock()
	})
}

func setSigningPlanSelectorForTest(t *testing.T, selector func(targets.Target) bundleSigningPlan) {
	t.Helper()
	previousSelector := selectBundleSigningPlanForPatch
	selectBundleSigningPlanForPatch = selector
	t.Cleanup(func() {
		selectBundleSigningPlanForPatch = previousSelector
	})
}

func testSigningPlan(kind bundleSigningStrategyKind, phase bundleSealPhase, sealErr error) bundleSigningPlan {
	return bundleSigningPlan{
		kind:      kind,
		sealPhase: phase,
		prepare: func(_ context.Context, runner *Runner, target *targets.Target, _ Options, _ *bundleSigningPlan) error {
			RecordTrace(runner, Action("test_prepare"), target.ID, target.AppPath)
			return nil
		},
		seal: func(_ context.Context, runner *Runner, target targets.Target, _ *bundleSigningPlan) error {
			RecordTrace(runner, Action("test_seal"), target.ID, target.AppPath)
			return sealErr
		},
		verify: func(_ context.Context, runner *Runner, target targets.Target) error {
			RecordTrace(runner, Action("test_verify"), target.ID, target.AppPath)
			return nil
		},
	}
}

func registerSigningPipelinePostBundleHook(t *testing.T, action Action) string {
	t.Helper()
	hookName := "test-signing-pipeline-post-bundle-" + t.Name()
	if err := RegisterPostBundleHook(hookName, func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
		RecordTrace(runner, action, target.ID, target.AppPath)
		return nil
	}); err != nil {
		t.Fatalf("RegisterPostBundleHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(postBundleHooks, hookName)
		hooksMu.Unlock()
	})
	return hookName
}

func registerSigningPipelinePostBundleHookWithAssertion(t *testing.T, action Action, assertion func()) {
	t.Helper()
	hookName := "test-signing-pipeline-post-bundle-assert-" + t.Name()
	if err := RegisterPostBundleHook(hookName, func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
		assertion()
		RecordTrace(runner, action, target.ID, target.AppPath)
		return nil
	}); err != nil {
		t.Fatalf("RegisterPostBundleHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(postBundleHooks, hookName)
		hooksMu.Unlock()
	})
}

func registerFailingPostBundleHook(t *testing.T, hookErr error) {
	t.Helper()
	hookName := "000-test-signing-pipeline-post-bundle-failure-" + t.Name()
	if err := RegisterPostBundleHook(hookName, func(context.Context, *Runner, targets.Target, Options) error {
		return hookErr
	}); err != nil {
		t.Fatalf("RegisterPostBundleHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(postBundleHooks, hookName)
		hooksMu.Unlock()
	})
}

func registerSigningPipelinePostPatchHook(t *testing.T, action Action, assertion func()) string {
	t.Helper()
	capability := "test-signing-pipeline-post-patch-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := catalog.RegisterPatchHookCapability(capability); err != nil {
		t.Fatalf("RegisterPatchHookCapability: %v", err)
	}
	if err := RegisterPostPatchHook(capability, func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
		assertion()
		RecordTrace(runner, action, target.ID, target.AppPath)
		return nil
	}); err != nil {
		t.Fatalf("RegisterPostPatchHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(postPatchHooks, capability)
		hooksMu.Unlock()
	})
	return capability
}

func writePipelineBundle(t *testing.T, appPath string, executableContents string) string {
	t.Helper()
	executablePath := filepath.Join(appPath, "Contents", "MacOS", "Codex")
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatalf("MkdirAll executable parent: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(appPath, "Contents", "Resources"), 0o755); err != nil {
		t.Fatalf("MkdirAll resources: %v", err)
	}
	if err := os.WriteFile(executablePath, []byte(executableContents), 0o755); err != nil {
		t.Fatalf("WriteFile executable: %v", err)
	}
	return appPath
}

func requirePipelineBundleContents(t *testing.T, appPath string, want string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(appPath, "Contents", "MacOS", "Codex"))
	if err != nil {
		t.Fatalf("ReadFile bundle executable: %v", err)
	}
	if string(contents) != want {
		t.Fatalf("bundle executable contents = %q, want %q", contents, want)
	}
}

func developmentSigningPolicyWithAssets(t *testing.T) *targets.DevelopmentSigningPolicy {
	t.Helper()
	directory := t.TempDir()
	writeAsset := func(name string) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("write asset %s: %v", path, err)
		}
		return path
	}
	return &targets.DevelopmentSigningPolicy{
		Enabled:         true,
		ProfilePath:     writeAsset("development.provisionprofile"),
		P12Path:         writeAsset("development.p12"),
		P12PasswordFile: writeAsset("development-password"),
	}
}

func traceActionIndexForPipeline(trace *Trace, action Action) int {
	for index, event := range trace.Events {
		if event.Action == action {
			return index
		}
	}
	return -1
}

func requirePipelineActionCount(t *testing.T, trace *Trace, action Action, want int) {
	t.Helper()
	count := 0
	for _, event := range trace.Events {
		if event.Action == action {
			count++
		}
	}
	if count != want {
		t.Fatalf("action %q count = %d, want %d: %#v", action, count, want, trace.Events)
	}
}

func requirePipelineSubsequence(t *testing.T, trace *Trace, actions ...Action) {
	t.Helper()
	nextEvent := 0
	for _, action := range actions {
		found := false
		for nextEvent < len(trace.Events) {
			event := trace.Events[nextEvent]
			nextEvent++
			if event.Action == action {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("trace is missing ordered action %q after index %d: %#v", action, nextEvent-1, trace.Events)
		}
	}
}

func requireNoPatchStateFile(t *testing.T) {
	t.Helper()
	statePath := filepath.Join(os.Getenv("XDG_STATE_HOME"), "clyde", "state.json")
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("patch state file stat error = %v, want not-exist", err)
	}
}

func requirePipelineOrder(t *testing.T, trace *Trace, actions ...Action) {
	t.Helper()
	requirePipelineSubsequence(t, trace, actions...)
	for _, action := range actions {
		requirePipelineActionCount(t, trace, action, 1)
	}
}

func traceDevelopmentSealIndex(trace *Trace, appPath string) int {
	for index, event := range trace.Events {
		if event.Action != actionRunCommand || filepath.Base(event.Command) != "rcodesign" {
			continue
		}
		if containsPipelineArgument(event.Args, "--shallow") && containsPipelineArgument(event.Args, appPath) {
			return index
		}
	}
	return -1
}

func containsPipelineArgument(arguments []string, wanted string) bool {
	for _, argument := range arguments {
		if argument == wanted {
			return true
		}
	}
	return false
}
