package bundledclitee

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/claudetee"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestPostPatchHookDeferredInstallMarksSkippedOutcome(t *testing.T) {
	bundledPath := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bundledPath, []byte("cli"), 0o755); err != nil {
		t.Fatalf("WriteFile bundled cli: %v", err)
	}

	originalInstallBundledCLI := installBundledCLI
	installBundledCLI = func(context.Context, claudetee.Options) error {
		return claudetee.ErrProcessesRunning
	}
	t.Cleanup(func() {
		installBundledCLI = originalInstallBundledCLI
	})

	progress := &recordingProgress{}
	runner := patch.NewRunner(context.Background(), false, io.Discard)
	runner.Progress = progress
	target := targets.Target{
		ID:      "claude",
		AppPath: filepath.Join(t.TempDir(), "Claude.app"),
		Extensions: extensions.Target{
			BundledCLITee: &extensions.BundledCLITeeSpec{
				Capability:     HookCapability,
				BundledCLIPath: bundledPath,
			},
		},
		LaunchPolicy: spec.LaunchPolicySpec{},
	}

	err := PostPatchHook(context.Background(), runner, target, patch.Options{Out: io.Discard})
	if err != nil {
		t.Fatalf("PostPatchHook: %v", err)
	}
	if progress.outcome != clioutput.OutcomeSkipped {
		t.Fatalf("outcome = %q, want skipped", progress.outcome)
	}
}

type recordingProgress struct {
	outcome clioutput.Outcome
}

func (p *recordingProgress) Step(string) {}
func (p *recordingProgress) Skip(string) {}
func (p *recordingProgress) Fail(string) {}
func (p *recordingProgress) SetOutcome(outcome clioutput.Outcome, _ string) {
	p.outcome = outcome
}
