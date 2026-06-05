package patch_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/bundledclitee"
	"goodkind.io/desktop-via-clyde/internal/codexclishim"
	"goodkind.io/desktop-via-clyde/internal/computeruseext"
	"goodkind.io/desktop-via-clyde/internal/config"
	patch "goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

var (
	bundleStepsRegisterOnce sync.Once
	bundleStepsRegisterErr  error
)

func TestPatchDryRunRepairsBundledComputerUseBeforeResign(t *testing.T) {
	tg, err := lookupTarget(t, "codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	tg.AppPath = filepath.Join(t.TempDir(), "Codex.app")
	tg.Extensions.ComputerUse.HostAppPath = tg.AppPath

	trace := &patch.Trace{}
	runner := patch.NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace
	if err := computeruseext.BundledLifecycleHook(context.Background(), runner, tg, patch.Options{
		DryRun:          true,
		MigrateKeychain: false,
		Out:             io.Discard,
		Trace:           trace,
	}); err != nil {
		t.Fatalf("BundledLifecycleHook dry-run: %v", err)
	}
	bundledHelperPath := filepath.Join(tg.AppPath, filepath.FromSlash(tg.Extensions.ComputerUse.BundledAppPath))
	senderPath := filepath.Join(bundledHelperPath, "Contents/MacOS/SkyComputerUseService")
	requirementPath := filepath.Join(bundledHelperPath, "Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement")

	requireTraceAction(t, trace, computeruseext.ActionRepairBundledComputerUse, bundledHelperPath)
	requireTraceAction(t, trace, computeruseext.ActionRepairComputerUseTrustedTeam, senderPath)
	requireTraceAction(t, trace, computeruseext.ActionRepairComputerUseRequirement, requirementPath)
	requireTraceAction(t, trace, computeruseext.ActionSignComputerUseHelper, bundledHelperPath)
}

func TestPatchDryRunScansComputerUseCacheHelpers(t *testing.T) {
	tg, err := lookupTarget(t, "codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	tg.AppPath = filepath.Join(t.TempDir(), "Codex.app")
	tg.Extensions.ComputerUse.HostAppPath = tg.AppPath

	trace := &patch.Trace{}
	runner := patch.NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace
	if err := computeruseext.LifecycleHook(context.Background(), runner, tg, patch.Options{
		DryRun:          true,
		MigrateKeychain: false,
		Out:             io.Discard,
		Trace:           trace,
	}); err != nil {
		t.Fatalf("LifecycleHook dry-run: %v", err)
	}

	pattern := filepath.Join(paths.Home(), filepath.FromSlash(tg.Extensions.ComputerUse.CacheAppGlobsFromHome[0]))
	requireTraceAction(t, trace, computeruseext.ActionScanComputerUseCache, pattern)
}

func TestPatchDryRunRepairsComputerUseAuthPlugin(t *testing.T) {
	tg, err := lookupTarget(t, "codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	tg.AppPath = filepath.Join(t.TempDir(), "Codex.app")
	tg.Extensions.ComputerUse.HostAppPath = tg.AppPath

	trace := &patch.Trace{}
	runner := patch.NewRunner(context.Background(), true, io.Discard)
	runner.Trace = trace
	if err := computeruseext.LifecycleHook(context.Background(), runner, tg, patch.Options{
		DryRun:          true,
		MigrateKeychain: false,
		Out:             io.Discard,
		Trace:           trace,
	}); err != nil {
		t.Fatalf("LifecycleHook dry-run: %v", err)
	}

	pluginPath := tg.Extensions.ComputerUse.AuthPluginPath
	stagingPath := filepath.Join(os.TempDir(), "desktop-via-clyde-auth-plugin", filepath.Base(pluginPath))
	executablePath := filepath.Join(pluginPath, filepath.FromSlash(tg.Extensions.ComputerUse.AuthPluginExecutable))
	requireTraceAction(t, trace, computeruseext.ActionRepairComputerUseAuthPlugin, pluginPath)
	requireTraceAction(t, trace, computeruseext.ActionRepairComputerUseTrustedTeam, executablePath)
	requireTraceCommand(t, trace, "/usr/bin/sudo", []string{"/usr/bin/rsync", "-rltp", "--delete", stagingPath + "/", pluginPath + "/"})
}

func TestClaudePatchRestoresSquirrelInsteadOfResigningIt(t *testing.T) {
	tg, err := lookupTarget(t, "claude")
	if err != nil {
		t.Fatalf("Lookup(claude): %v", err)
	}
	tg.AppPath = filepath.Join(t.TempDir(), "Claude.app")

	trace := &patch.Trace{}
	if err := patch.Patch(context.Background(), tg, patch.Options{
		DryRun:          true,
		MigrateKeychain: false,
		Out:             io.Discard,
		Trace:           trace,
	}); err != nil {
		t.Fatalf("Patch dry-run: %v", err)
	}

	restorePath := filepath.Join(tg.AppPath, "Contents", "Frameworks", "Squirrel.framework")
	requireTraceAction(t, trace, patch.ActionRestorePreservedNestedCode, restorePath)
	if traceActionIndex(trace, patch.ActionSignNestedCode, restorePath) >= 0 {
		t.Fatalf("Patch dry-run trace re-signs preserved nested code: %#v", trace.Events)
	}
}

func TestPatchDryRunSkipsKeychainPreviewUnlessMigrateRequested(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tg, err := lookupTarget(t, "claude")
	if err != nil {
		t.Fatalf("Lookup(claude): %v", err)
	}
	tg.AppPath = filepath.Join(t.TempDir(), "Claude.app")

	var skippedOutput bytes.Buffer
	if err := patch.Patch(context.Background(), tg, patch.Options{
		DryRun:          true,
		MigrateKeychain: false,
		Out:             &skippedOutput,
	}); err != nil {
		t.Fatalf("Patch dry-run without migrate-keychain: %v", err)
	}
	skippedText := skippedOutput.String()
	if !strings.Contains(skippedText, "skipped keychain access repair") {
		t.Fatalf("dry-run output missing repair skip\noutput:\n%s", skippedText)
	}
	if !strings.Contains(skippedText, "skipped keychain access restore") {
		t.Fatalf("dry-run output missing restore skip\noutput:\n%s", skippedText)
	}
	if strings.Contains(skippedText, "would find keychain items") {
		t.Fatalf("dry-run output unexpectedly previews keychain capture\noutput:\n%s", skippedText)
	}
	if strings.Contains(skippedText, "would restore keychain access") {
		t.Fatalf("dry-run output unexpectedly previews keychain restore\noutput:\n%s", skippedText)
	}

	var migrateOutput bytes.Buffer
	if err := patch.Patch(context.Background(), tg, patch.Options{
		DryRun:          true,
		MigrateKeychain: true,
		Out:             &migrateOutput,
	}); err != nil {
		t.Fatalf("Patch dry-run with migrate-keychain: %v", err)
	}
	migrateText := migrateOutput.String()
	if !strings.Contains(migrateText, "would find keychain items") {
		t.Fatalf("dry-run output missing keychain capture preview\noutput:\n%s", migrateText)
	}
	if !strings.Contains(migrateText, "would restore keychain access") {
		t.Fatalf("dry-run output missing keychain restore preview\noutput:\n%s", migrateText)
	}
}

func requireTraceAction(t *testing.T, trace *patch.Trace, action patch.Action, path string) {
	t.Helper()
	if traceActionIndex(trace, action, path) < 0 {
		t.Fatalf("trace missing action=%s path=%s events=%#v", action, path, trace.Events)
	}
}

func traceActionIndex(trace *patch.Trace, action patch.Action, path string) int {
	for i, event := range trace.Events {
		if event.Action == action && event.Path == path {
			return i
		}
	}
	return -1
}

func requireTraceCommand(t *testing.T, trace *patch.Trace, command string, args []string) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Action != "run_command" || event.Command != command {
			continue
		}
		if equalStrings(event.Args, args) {
			return
		}
	}
	t.Fatalf("trace missing command=%s args=%v events=%#v", command, args, trace.Events)
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func lookupTarget(t *testing.T, id string) (targets.Target, error) {
	t.Helper()
	installFixture(t)
	for _, target := range targets.All() {
		if target.ID == id {
			return target, nil
		}
	}
	return targets.Target{}, fmt.Errorf("unknown target %q", id)
}

func installFixture(t *testing.T) {
	t.Helper()
	bundleStepsRegisterOnce.Do(func() {
		if err := registerFixtureCapabilities(); err != nil {
			bundleStepsRegisterErr = err
			return
		}
		if err := computeruseext.RegisterLifecycleHooks(); err != nil {
			bundleStepsRegisterErr = err
			return
		}
		if err := codexclishim.RegisterPreLaunchPolicyHooks(); err != nil {
			bundleStepsRegisterErr = err
			return
		}
		bundleStepsRegisterErr = bundledclitee.RegisterPatchHooks()
	})
	if bundleStepsRegisterErr != nil {
		t.Fatalf("register fixture hooks: %v", bundleStepsRegisterErr)
	}
	cfg, err := config.LoadPath(filepath.Join("..", "testconfig", "testdata", "current-config.toml"))
	if err != nil {
		t.Fatalf("LoadPath(current-config.toml): %v", err)
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() {
		config.SetCurrent(nil)
	})
}
