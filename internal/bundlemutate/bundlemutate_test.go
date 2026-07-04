package bundlemutate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/appguard"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestMutateDeferReturnsErrAppRunningWithoutCallingMutation(t *testing.T) {
	target := targets.Target{
		ID:      "codex",
		AppPath: "/Applications/Codex.app",
	}
	originalRunning := running
	originalEnsureClosed := ensureClosed
	running = func(context.Context, targets.Target) bool {
		return true
	}
	ensureClosed = func(context.Context, targets.Target, appguard.Options) error {
		t.Fatal("ensureClosed should not run for PolicyDefer")
		return nil
	}
	t.Cleanup(func() {
		running = originalRunning
		ensureClosed = originalEnsureClosed
	})

	called := false
	err := Mutate(context.Background(), target, PolicyDefer, Options{}, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, appguard.ErrAppRunning) {
		t.Fatalf("Mutate error = %v, want ErrAppRunning", err)
	}
	if called {
		t.Fatal("mutation callback ran while app was running")
	}
}

func TestMutateCloseCallsEnsureClosedBeforeMutation(t *testing.T) {
	target := targets.Target{
		ID:      "codex",
		AppPath: "/Applications/Codex.app",
	}
	originalRunning := running
	originalEnsureClosed := ensureClosed
	running = func(context.Context, targets.Target) bool {
		t.Fatal("running should not run for PolicyClose")
		return false
	}
	closed := false
	ensureClosed = func(_ context.Context, got targets.Target, opts appguard.Options) error {
		closed = true
		if got.ID != target.ID {
			t.Fatalf("target ID = %q, want %q", got.ID, target.ID)
		}
		if opts.DryRun {
			t.Fatal("ensureClosed dry-run = true, want false")
		}
		return nil
	}
	t.Cleanup(func() {
		running = originalRunning
		ensureClosed = originalEnsureClosed
	})

	called := false
	err := Mutate(context.Background(), target, PolicyClose, Options{}, func(context.Context) error {
		if !closed {
			t.Fatal("ensureClosed did not run before mutation")
		}
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if !closed {
		t.Fatal("ensureClosed was not called")
	}
	if !called {
		t.Fatal("mutation callback did not run")
	}
}

func TestMutateRejectsUnknownPolicy(t *testing.T) {
	target := targets.Target{
		ID:      "codex",
		AppPath: "/Applications/Codex.app",
	}
	called := false
	err := Mutate(context.Background(), target, Policy(99), Options{}, func(context.Context) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("Mutate succeeded, want unknown policy error")
	}
	if !strings.Contains(err.Error(), "unknown mutation policy") {
		t.Fatalf("Mutate error = %v, want unknown policy", err)
	}
	if called {
		t.Fatal("mutation callback ran for unknown policy")
	}
}

func TestStagedSwapCommitsMutatedBundle(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	liveApp := writeBundle(t, root, "Applications/Codex.app", "old")
	adoptApp := writeBundle(t, root, "Downloads/Codex.app", "new")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}

	err := StagedSwap(context.Background(), target, SwapOptions{
		AdoptPath: adoptApp,
	}, func(stagedAppPath string) error {
		return os.WriteFile(
			filepath.Join(stagedAppPath, "Contents", "Resources", "marker.txt"),
			[]byte("mutated"),
			0o644,
		)
	})
	if err != nil {
		t.Fatalf("StagedSwap: %v", err)
	}
	assertFileContents(t, filepath.Join(liveApp, "Contents", "MacOS", "Codex"), "new")
	assertFileContents(t, filepath.Join(liveApp, "Contents", "Resources", "marker.txt"), "mutated")
	if _, err := os.Stat(liveApp + oldSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old bundle stat err = %v, want not-exist", err)
	}
}

func TestStagedSwapFallsBackToAppVolumeWhenStateRootDiffers(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	liveApp := writeBundle(t, root, "Applications/Codex.app", "old")
	adoptApp := writeBundle(t, root, "Downloads/Codex.app", "new")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}
	originalSameDevice := sameDevice
	sameDevice = func(left string, right string) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() {
		sameDevice = originalSameDevice
	})

	var stagedAppPath string
	err := StagedSwap(context.Background(), target, SwapOptions{
		AdoptPath: adoptApp,
	}, func(path string) error {
		stagedAppPath = path
		return nil
	})
	if err != nil {
		t.Fatalf("StagedSwap: %v", err)
	}
	appParent := filepath.Dir(liveApp)
	if !strings.HasPrefix(filepath.Clean(stagedAppPath), filepath.Clean(appParent)+string(filepath.Separator)) {
		t.Fatalf("staged app path = %q, want under %q", stagedAppPath, appParent)
	}
}

func TestStagedSwapRollsBackWhenVerifyFails(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	liveApp := writeBundle(t, root, "Applications/Codex.app", "old")
	adoptApp := writeBundle(t, root, "Downloads/Codex.app", "new")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}

	verifyErr := errors.New("verify failed")
	err := StagedSwap(context.Background(), target, SwapOptions{
		AdoptPath: adoptApp,
		Verify: func(context.Context) error {
			return verifyErr
		},
	}, func(stagedAppPath string) error {
		return os.WriteFile(
			filepath.Join(stagedAppPath, "Contents", "MacOS", "Codex"),
			[]byte("broken"),
			0o755,
		)
	})
	if !errors.Is(err, verifyErr) {
		t.Fatalf("StagedSwap error = %v, want %v", err, verifyErr)
	}
	assertFileContents(t, filepath.Join(liveApp, "Contents", "MacOS", "Codex"), "old")
	if _, statErr := os.Stat(liveApp + oldSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("old bundle stat err = %v, want not-exist", statErr)
	}
}

func TestStagedSwapStopsBeforeCommitWhenPreCommitReturnsErrAppRunning(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	liveApp := writeBundle(t, root, "Applications/Codex.app", "old")
	adoptApp := writeBundle(t, root, "Downloads/Codex.app", "new")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}

	preCommitCalls := 0
	err := StagedSwap(context.Background(), target, SwapOptions{
		AdoptPath: adoptApp,
		PreCommit: func(context.Context) error {
			preCommitCalls++
			return appguard.ErrAppRunning
		},
	}, func(stagedAppPath string) error {
		return os.WriteFile(
			filepath.Join(stagedAppPath, "Contents", "Resources", "marker.txt"),
			[]byte("mutated"),
			0o644,
		)
	})
	if !errors.Is(err, appguard.ErrAppRunning) {
		t.Fatalf("StagedSwap error = %v, want ErrAppRunning", err)
	}
	if preCommitCalls != 1 {
		t.Fatalf("PreCommit calls = %d, want 1", preCommitCalls)
	}
	assertFileContents(t, filepath.Join(liveApp, "Contents", "MacOS", "Codex"), "old")
	if _, statErr := os.Stat(liveApp + oldSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("old bundle stat err = %v, want not-exist", statErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(paths.StateRoot(), stagingDirPrefix+"*"))
	if globErr != nil {
		t.Fatalf("Glob staging roots: %v", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("staging roots = %v, want none", matches)
	}
}

func TestCommitSwapRestoresLiveBundleWhenInstallRenameFails(t *testing.T) {
	root := t.TempDir()
	liveApp := writeBundle(t, root, "Applications/Codex.app", "old")
	stagedApp := writeBundle(t, root, "Downloads/Codex.app", "new")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}

	originalRenamePath := renamePath
	renamePath = func(src string, dst string) error {
		switch {
		case src == liveApp && dst == liveApp+oldSuffix:
			return os.Rename(src, dst)
		case src == stagedApp && dst == liveApp:
			return errors.New("install failed")
		case src == liveApp+oldSuffix && dst == liveApp:
			return os.Rename(src, dst)
		default:
			return originalRenamePath(src, dst)
		}
	}
	t.Cleanup(func() {
		renamePath = originalRenamePath
	})

	err := CommitSwap(context.Background(), target, stagedApp, nil, Options{})
	if err == nil {
		t.Fatal("CommitSwap succeeded, want install failure")
	}
	if !strings.Contains(err.Error(), "install staged bundle") {
		t.Fatalf("CommitSwap error = %v, want install failure", err)
	}
	assertFileContents(t, filepath.Join(liveApp, "Contents", "MacOS", "Codex"), "old")
	if _, statErr := os.Stat(liveApp + oldSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("old bundle stat err = %v, want not-exist", statErr)
	}
}

func TestCommitSwapRemovesStaleBackupWhenLiveBundleExists(t *testing.T) {
	root := t.TempDir()
	liveApp := writeBundle(t, root, "Applications/Codex.app", "old")
	stagedApp := writeBundle(t, root, "Downloads/Codex.app", "new")
	staleOld := writeBundle(t, root, "Applications/Codex.app.dvc-old", "stale")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}

	err := CommitSwap(context.Background(), target, stagedApp, nil, Options{})
	if err != nil {
		t.Fatalf("CommitSwap: %v", err)
	}
	assertFileContents(t, filepath.Join(liveApp, "Contents", "MacOS", "Codex"), "new")
	if _, statErr := os.Stat(staleOld); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale old stat err = %v, want not-exist", statErr)
	}
}

func TestCommitSwapErrorsWhenOnlyStaleBackupExists(t *testing.T) {
	root := t.TempDir()
	stagedApp := writeBundle(t, root, "Downloads/Codex.app", "new")
	liveApp := filepath.Join(root, "Applications", "Codex.app")
	writeBundle(t, root, "Applications/Codex.app.dvc-old", "stale")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}

	err := CommitSwap(context.Background(), target, stagedApp, nil, Options{})
	if err == nil {
		t.Fatal("CommitSwap succeeded, want stale backup error")
	}
	if !strings.Contains(err.Error(), "stale backup exists") {
		t.Fatalf("CommitSwap error = %v, want stale backup error", err)
	}
	if _, statErr := os.Stat(stagedApp); statErr != nil {
		t.Fatalf("staged bundle stat err = %v, want staged bundle preserved", statErr)
	}
}

func TestCommitSwapRollbackRestoresLiveBundleWhenStagedRollbackMoveFails(t *testing.T) {
	root := t.TempDir()
	liveApp := writeBundle(t, root, "Applications/Codex.app", "new")
	stagedApp := filepath.Join(root, "Downloads", "Codex.app")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}

	verifyErr := errors.New("verify failed")
	originalRenamePath := renamePath
	renamePath = func(src string, dst string) error {
		switch {
		case src == liveApp && dst == stagedApp:
			return errors.New("rollback move failed")
		case src == filepath.Join(root, "Applications", "Codex.app")+oldSuffix && dst == liveApp:
			return os.Rename(src, dst)
		default:
			return originalRenamePath(src, dst)
		}
	}
	t.Cleanup(func() {
		renamePath = originalRenamePath
	})

	oldPath := filepath.Join(root, "Applications", "Codex.app") + oldSuffix
	if err := os.Rename(liveApp, oldPath); err != nil {
		t.Fatalf("Rename live to old: %v", err)
	}
	if err := os.Rename(writeBundle(t, root, "Downloads/Codex.app", "broken"), liveApp); err != nil {
		t.Fatalf("Rename staged to live: %v", err)
	}

	err := rollbackCommit(context.Background(), target, stagedApp, oldPath, true, verifyErr)
	if err == nil {
		t.Fatal("rollbackCommit succeeded, want combined error")
	}
	if !strings.Contains(err.Error(), "rollback move failed") {
		t.Fatalf("rollbackCommit error = %v, want rollback move failure", err)
	}
	assertFileContents(t, filepath.Join(liveApp, "Contents", "MacOS", "Codex"), "new")
}

func TestRollbackCommitRecreatesStagedParentDirectory(t *testing.T) {
	root := t.TempDir()
	liveApp := writeBundle(t, root, "Applications/Codex.app", "new")
	stagedDir := filepath.Join(root, "staging")
	stagedApp := filepath.Join(stagedDir, "Codex.app")
	target := targets.Target{
		ID:      "codex",
		AppPath: liveApp,
	}

	verifyErr := errors.New("verify failed")
	oldPath := filepath.Join(root, "Applications", "Codex.app") + oldSuffix
	if err := os.Rename(liveApp, oldPath); err != nil {
		t.Fatalf("Rename live to old: %v", err)
	}
	if err := os.Rename(writeBundle(t, root, "Downloads/Codex.app", "broken"), liveApp); err != nil {
		t.Fatalf("Rename staged to live: %v", err)
	}
	if err := os.RemoveAll(stagedDir); err != nil {
		t.Fatalf("RemoveAll staged dir: %v", err)
	}

	err := rollbackCommit(context.Background(), target, stagedApp, oldPath, true, verifyErr)
	if err != verifyErr {
		t.Fatalf("rollbackCommit error = %v, want verifyErr", err)
	}
	assertFileContents(t, filepath.Join(liveApp, "Contents", "MacOS", "Codex"), "new")
	if _, statErr := os.Stat(stagedDir); statErr != nil {
		t.Fatalf("staged dir stat err = %v, want recreated dir", statErr)
	}
}

func TestStagedSwapCleansUpStagingRootWhenStageBundleFails(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	target := targets.Target{
		ID:      "codex",
		AppPath: filepath.Join(root, "Applications", "Codex.app"),
	}
	adoptApp := writeBundle(t, root, "Downloads/Codex.app", "new")

	originalCopyBundle := copyBundle
	copyBundle = func(context.Context, string, string) error {
		return errors.New("copy failed")
	}
	t.Cleanup(func() {
		copyBundle = originalCopyBundle
	})

	err := StagedSwap(context.Background(), target, SwapOptions{
		AdoptPath: adoptApp,
	}, func(string) error {
		t.Fatal("mutate callback should not run when staging fails")
		return nil
	})
	if err == nil {
		t.Fatal("StagedSwap succeeded, want stage failure")
	}
	matches, globErr := filepath.Glob(filepath.Join(paths.StateRoot(), stagingDirPrefix+"*"))
	if globErr != nil {
		t.Fatalf("Glob staging roots: %v", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("staging roots = %v, want none", matches)
	}
}

func TestStagedSwapSanitizesTargetIDForStagingPath(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	adoptApp := writeBundle(t, root, "Downloads/Codex.app", "new")
	target := targets.Target{
		ID:      "../bad/id",
		AppPath: filepath.Join(root, "Applications", "Codex.app"),
	}

	var stagedAppPath string
	err := StagedSwap(context.Background(), target, SwapOptions{
		AdoptPath: adoptApp,
		Options: Options{
			DryRun: true,
		},
	}, func(path string) error {
		stagedAppPath = path
		return nil
	})
	if err != nil {
		t.Fatalf("StagedSwap: %v", err)
	}
	relPath, relErr := filepath.Rel(paths.StateRoot(), filepath.Dir(stagedAppPath))
	if relErr != nil {
		t.Fatalf("Rel staging path: %v", relErr)
	}
	if strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
		t.Fatalf("staged app dir = %q escapes state root %q", filepath.Dir(stagedAppPath), paths.StateRoot())
	}
	if strings.Contains(relPath, "..") {
		t.Fatalf("staged app dir = %q still contains parent traversal", filepath.Dir(stagedAppPath))
	}
}

func TestStagedSwapDryRunCleansUpMutationArtifacts(t *testing.T) {
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	target := targets.Target{
		ID:      "codex",
		AppPath: filepath.Join(root, "Applications", "Codex.app"),
	}
	adoptApp := writeBundle(t, root, "Downloads/Codex.app", "new")

	err := StagedSwap(context.Background(), target, SwapOptions{
		AdoptPath: adoptApp,
		Options: Options{
			DryRun: true,
		},
	}, func(stagedAppPath string) error {
		if err := os.MkdirAll(filepath.Dir(stagedAppPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(stagedAppPath, []byte("artifact"), 0o644)
	})
	if err != nil {
		t.Fatalf("StagedSwap: %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(paths.StateRoot(), stagingDirPrefix+"*"))
	if globErr != nil {
		t.Fatalf("Glob staging roots: %v", globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("staging roots = %v, want none", matches)
	}
}

func TestCleanupStagingRootUsesRemoveAllPathSeam(t *testing.T) {
	originalRemoveAllPath := removeAllPath
	calledPath := ""
	removeAllPath = func(path string) error {
		calledPath = path
		return nil
	}
	t.Cleanup(func() {
		removeAllPath = originalRemoveAllPath
	})

	cleanupStagingRoot("/tmp/staging-root")
	if calledPath != "/tmp/staging-root" {
		t.Fatalf("cleanupStagingRoot path = %q, want /tmp/staging-root", calledPath)
	}
}

func writeBundle(t *testing.T, root string, relPath string, executableContents string) string {
	t.Helper()
	appPath := filepath.Join(root, relPath)
	executablePath := filepath.Join(appPath, "Contents", "MacOS", "Codex")
	resourceDir := filepath.Join(appPath, "Contents", "Resources")
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatalf("MkdirAll executable: %v", err)
	}
	if err := os.MkdirAll(resourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll resources: %v", err)
	}
	if err := os.WriteFile(executablePath, []byte(executableContents), 0o755); err != nil {
		t.Fatalf("WriteFile executable: %v", err)
	}
	return appPath
}

func assertFileContents(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
