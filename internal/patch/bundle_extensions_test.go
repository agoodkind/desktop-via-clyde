package patch

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestBundleExtensionsRunOnceInStableOrderForBothStrategies(t *testing.T) {
	for _, test := range []struct {
		name  string
		kind  bundleSigningStrategyKind
		phase bundleSealPhase
	}{
		{name: "standard", kind: bundleSigningStrategyStandard, phase: bundleSealBeforeFinalize},
		{name: "development", kind: bundleSigningStrategyDevelopment, phase: bundleSealAfterFinalize},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace := &Trace{}
			registerTracingBundleExtension(t, "z-last", Action("mutate_z"), Action("verify_z"))
			registerTracingBundleExtension(t, "a-first", Action("mutate_a"), Action("verify_a"))
			target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
			runner := NewRunner(context.Background(), true, io.Discard)
			runner.Trace = trace

			plan, err := patchBundleStepsWithPlan(
				context.Background(),
				runner,
				&target,
				Options{DryRun: true},
				testSigningPlan(test.kind, test.phase, nil),
			)
			if err != nil {
				t.Fatalf("patchBundleStepsWithPlan: %v", err)
			}
			if err := finalizePreparedBundle(context.Background(), runner, PreparedBundle{
				Version:     "1",
				OriginalDR:  "identifier test",
				signingPlan: plan,
			}, target, Options{DryRun: true}); err != nil {
				t.Fatalf("finalizePreparedBundle: %v", err)
			}

			want := []Action{
				actionPrepareSigningStrategy,
				Action("test_prepare"),
				Action("mutate_a"),
				Action("mutate_z"),
				actionSealSigningStrategy,
				Action("test_seal"),
				Action("verify_a"),
				Action("verify_z"),
				actionVerifySigningStrategy,
				Action("test_verify"),
				actionWritePatchState,
			}
			requirePipelineSubsequence(t, trace, want...)
			for _, action := range want {
				requirePipelineActionCount(t, trace, action, 1)
			}
		})
	}
}

func TestBundleExtensionMutationFailureSuppressesSealsVerificationAndState(t *testing.T) {
	mutationErr := errors.New("extension mutation failed")
	registerBundleExtensionForTest(t, "failing-mutation", BundleExtension{
		MutateBeforeSeal: func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
			RecordTrace(runner, Action("failing_extension_mutation"), target.ID, target.AppPath)
			return mutationErr
		},
		VerifyAfterSeal: func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
			RecordTrace(runner, Action("extension_verification"), target.ID, target.AppPath)
			return nil
		},
	})
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}
	runner := NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace

	_, err := patchBundleStepsWithPlan(
		context.Background(), runner, &target, Options{DryRun: true},
		testSigningPlan(bundleSigningStrategyStandard, bundleSealBeforeFinalize, nil),
	)
	if !errors.Is(err, mutationErr) {
		t.Fatalf("patchBundleStepsWithPlan error = %v, want %v", err, mutationErr)
	}
	requirePipelineActionCount(t, trace, Action("failing_extension_mutation"), 1)
	for _, action := range []Action{
		actionSealSigningStrategy,
		Action("test_seal"),
		Action("extension_verification"),
		actionVerifySigningStrategy,
		Action("test_verify"),
		actionWritePatchState,
	} {
		requirePipelineActionCount(t, trace, action, 0)
	}
}

func TestBundleExtensionVerificationFailureSuppressesStrategyVerificationAndState(t *testing.T) {
	verificationErr := errors.New("extension verification failed")
	registerBundleExtensionForTest(t, "failing-verification", BundleExtension{
		MutateBeforeSeal: func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
			RecordTrace(runner, Action("extension_mutation"), target.ID, target.AppPath)
			return nil
		},
		VerifyAfterSeal: func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
			RecordTrace(runner, Action("failing_extension_verification"), target.ID, target.AppPath)
			return verificationErr
		},
	})
	target := stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), "")
	trace := &Trace{}
	runner := NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace
	plan, err := patchBundleStepsWithPlan(
		context.Background(), runner, &target, Options{DryRun: true},
		testSigningPlan(bundleSigningStrategyStandard, bundleSealBeforeFinalize, nil),
	)
	if err != nil {
		t.Fatalf("patchBundleStepsWithPlan: %v", err)
	}

	err = finalizePreparedBundle(context.Background(), runner, PreparedBundle{
		Version:     "1",
		OriginalDR:  "identifier test",
		signingPlan: plan,
	}, target, Options{DryRun: true})
	if !errors.Is(err, verificationErr) {
		t.Fatalf("finalizePreparedBundle error = %v, want %v", err, verificationErr)
	}
	requirePipelineSubsequence(
		t, trace,
		Action("extension_mutation"),
		actionSealSigningStrategy,
		Action("test_seal"),
		Action("failing_extension_verification"),
	)
	for _, action := range []Action{actionVerifySigningStrategy, Action("test_verify"), actionWritePatchState} {
		requirePipelineActionCount(t, trace, action, 0)
	}
}

func registerTracingBundleExtension(t *testing.T, name string, mutateAction Action, verifyAction Action) {
	t.Helper()
	registerBundleExtensionForTest(t, name, BundleExtension{
		MutateBeforeSeal: func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
			RecordTrace(runner, mutateAction, target.ID, target.AppPath)
			return nil
		},
		VerifyAfterSeal: func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
			RecordTrace(runner, verifyAction, target.ID, target.AppPath)
			return nil
		},
	})
}

func registerBundleExtensionForTest(t *testing.T, name string, extension BundleExtension) {
	t.Helper()
	uniqueName := t.Name() + "/" + name
	if err := RegisterBundleExtension(uniqueName, extension); err != nil {
		t.Fatalf("RegisterBundleExtension: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(bundleExtensions, uniqueName)
		hooksMu.Unlock()
	})
}
