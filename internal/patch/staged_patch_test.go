package patch

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/appguard"
	"goodkind.io/desktop-via-clyde/internal/bundlemutate"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestPrepareForSwapDryRunSkipsPostBundleHooks(t *testing.T) {
	capability := "test-staged-prelaunch-" + t.Name()
	postBundleHook := "test-staged-post-bundle-skip-" + t.Name()
	if err := catalog.RegisterPreLaunchPolicyHookCapability(capability); err != nil {
		t.Fatalf("RegisterPreLaunchPolicyHookCapability: %v", err)
	}
	if err := RegisterPreLaunchPolicyHook(capability, func(_ context.Context, _ *Runner, _ *targets.Target, _ Options) error {
		return nil
	}); err != nil {
		t.Fatalf("RegisterPreLaunchPolicyHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(preLaunchPolicyHooks, capability)
		hooksMu.Unlock()
	})
	if err := RegisterPostBundleHook(postBundleHook, func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
		RecordTrace(runner, Action("post_bundle_finalize"), target.ID, target.AppPath)
		return nil
	}); err != nil {
		t.Fatalf("RegisterPostBundleHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(postBundleHooks, postBundleHook)
		hooksMu.Unlock()
	})

	trace := &Trace{}
	_, err := PrepareForSwap(context.Background(), stagedPatchTarget(filepath.Join(t.TempDir(), "staged", "Codex.app"), capability), Options{
		DryRun: true,
		Out:    io.Discard,
		Trace:  trace,
	})
	if err != nil {
		t.Fatalf("PrepareForSwap: %v", err)
	}
	requireNoTraceAction(t, trace, Action("post_bundle_finalize"))
}

func TestFinalizeAfterSwapDryRunRunsPostBundleHooks(t *testing.T) {
	hookName := "test-staged-post-bundle-" + t.Name()
	if err := RegisterPostBundleHook(hookName, func(_ context.Context, runner *Runner, target targets.Target, _ Options) error {
		RecordTrace(runner, Action("post_bundle_finalize"), target.ID, target.AppPath)
		return nil
	}); err != nil {
		t.Fatalf("RegisterPostBundleHook: %v", err)
	}
	t.Cleanup(func() {
		hooksMu.Lock()
		delete(postBundleHooks, hookName)
		hooksMu.Unlock()
	})

	trace := &Trace{}
	err := FinalizeAfterSwap(context.Background(), PreparedBundle{
		Version:    "1.0.0",
		OriginalDR: "identifier \"com.openai.codex.beta\"",
	}, stagedPatchTarget(filepath.Join(t.TempDir(), "live", "Codex.app"), ""), Options{
		DryRun: true,
		Out:    io.Discard,
		Trace:  trace,
	})
	if err != nil {
		t.Fatalf("FinalizeAfterSwap: %v", err)
	}
	requireTraceAction(t, trace, Action("post_bundle_finalize"))
}

func TestPatchBackgroundDefersWhenMutationGateReturnsAppRunning(t *testing.T) {
	originalMutateTargetBundle := mutateTargetBundle
	mutateTargetBundle = func(
		context.Context,
		targets.Target,
		bundlemutate.Policy,
		bundlemutate.Options,
		func(context.Context) error,
	) error {
		return appguard.ErrAppRunning
	}
	t.Cleanup(func() {
		mutateTargetBundle = originalMutateTargetBundle
	})

	progress := &recordingProgress{}
	err := Patch(context.Background(), stagedPatchTarget(filepath.Join(t.TempDir(), "Codex.app"), ""), Options{
		DryRun:            false,
		Out:               io.Discard,
		Progress:          progress,
		CloseBeforeMutate: false,
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if progress.outcome != clioutput.OutcomeSkipped {
		t.Fatalf("outcome = %q, want skipped", progress.outcome)
	}
	if !strings.HasPrefix(progress.detail, "deferred:") {
		t.Fatalf("detail = %q, want deferred prefix", progress.detail)
	}
}

func stagedPatchTarget(appPath string, capability string) targets.Target {
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		ExecName: "Codex",
		Entitlements: &targets.EntitlementsPolicy{
			Strip:                       nil,
			RequiredBooleanEntitlements: nil,
		},
		LaunchPolicy: spec.LaunchPolicySpec{
			ProxyHost:              "::1",
			ProxyPort:              48723,
			CACertificate:          "/tmp/clyde-mitm-ca.crt",
			NoProxy:                "localhost,127.0.0.1,::1,[::1]",
			LaunchWorkingDirectory: "/tmp",
		},
	}
	if capability != "" {
		target.Extensions.CodexCLIShim = &extensions.CodexCLIShimSpec{
			Capability:     capability,
			ChatGPTBaseURL: "http://localhost:48730/backend-api",
		}
	}
	return target
}

func requireTraceAction(t *testing.T, trace *Trace, action Action) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Action == action {
			return
		}
	}
	t.Fatalf("trace missing action=%s events=%#v", action, trace.Events)
}

func requireNoTraceAction(t *testing.T, trace *Trace, action Action) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Action == action {
			t.Fatalf("trace unexpectedly contains action=%s events=%#v", action, trace.Events)
		}
	}
}

type recordingProgress struct {
	outcome clioutput.Outcome
	detail  string
}

func (p *recordingProgress) Step(string) {}
func (p *recordingProgress) Skip(string) {}
func (p *recordingProgress) Fail(string) {}
func (p *recordingProgress) SetOutcome(outcome clioutput.Outcome, detail string) {
	p.outcome = outcome
	p.detail = detail
}
