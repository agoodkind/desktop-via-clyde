package patch_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

	trace := &patch.Trace{}
	if err := patch.Patch(context.Background(), tg, patch.Options{
		DryRun:            true,
		NoMigrateKeychain: true,
		Out:               io.Discard,
		Trace:             trace,
	}); err != nil {
		t.Fatalf("Patch dry-run: %v", err)
	}
	bundledHelperPath := filepath.Join(tg.AppPath, filepath.FromSlash(tg.Extensions.ComputerUse.BundledAppPath))
	senderPath := filepath.Join(bundledHelperPath, "Contents/MacOS/SkyComputerUseService")
	requirementPath := filepath.Join(bundledHelperPath, "Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement")

	requireTraceAction(t, trace, computeruseext.ActionRepairBundledComputerUse, bundledHelperPath)
	requireTraceAction(t, trace, computeruseext.ActionRepairComputerUseTrustedTeam, senderPath)
	requireTraceAction(t, trace, computeruseext.ActionRepairComputerUseRequirement, requirementPath)
	requireTraceAction(t, trace, computeruseext.ActionSignComputerUseHelper, bundledHelperPath)

	helperRepairIdx := traceActionIndex(trace, computeruseext.ActionRepairBundledComputerUse, bundledHelperPath)
	resignIdx := traceActionIndex(trace, patch.ActionSignBundle, tg.AppPath)
	if helperRepairIdx < 0 || resignIdx < 0 {
		t.Fatalf("expected helper repair and bundle signing in trace: %#v", trace.Events)
	}
	if helperRepairIdx > resignIdx {
		t.Fatalf("helper repair ran after bundle signing: %#v", trace.Events)
	}
}

func TestCodexNestedSignPathsIncludeTCCActiveResourceExecutables(t *testing.T) {
	tg, err := lookupTarget(t, "codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}

	required := []string{
		"Contents/Resources/codex",
		"Contents/Resources/codex_chronicle",
		"Contents/Resources/node",
		"Contents/Resources/node_repl",
		"Contents/Resources/native/bare-modifier-monitor",
	}
	for _, want := range required {
		if !containsString(tg.NestedSignPaths, want) {
			t.Fatalf("codex NestedSignPaths missing %q", want)
		}
	}
}

func TestPatchDryRunScansComputerUseCacheHelpers(t *testing.T) {
	tg, err := lookupTarget(t, "codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}

	trace := &patch.Trace{}
	if err := patch.Patch(context.Background(), tg, patch.Options{
		DryRun:            true,
		NoMigrateKeychain: true,
		Out:               io.Discard,
		Trace:             trace,
	}); err != nil {
		t.Fatalf("Patch dry-run: %v", err)
	}

	pattern := filepath.Join(paths.Home(), filepath.FromSlash(tg.Extensions.ComputerUse.CacheAppGlobsFromHome[0]))
	requireTraceAction(t, trace, computeruseext.ActionScanComputerUseCache, pattern)
}

func TestPatchDryRunRepairsComputerUseAuthPlugin(t *testing.T) {
	tg, err := lookupTarget(t, "codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}

	trace := &patch.Trace{}
	if err := patch.Patch(context.Background(), tg, patch.Options{
		DryRun:            true,
		NoMigrateKeychain: true,
		Out:               io.Discard,
		Trace:             trace,
	}); err != nil {
		t.Fatalf("Patch dry-run: %v", err)
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

	trace := &patch.Trace{}
	if err := patch.Patch(context.Background(), tg, patch.Options{
		DryRun:            true,
		NoMigrateKeychain: true,
		Out:               io.Discard,
		Trace:             trace,
	}); err != nil {
		t.Fatalf("Patch dry-run: %v", err)
	}

	restorePath := filepath.Join(tg.AppPath, "Contents", "Frameworks", "Squirrel.framework")
	requireTraceAction(t, trace, patch.ActionRestorePreservedNestedCode, restorePath)
	if traceActionIndex(trace, patch.ActionSignNestedCode, restorePath) >= 0 {
		t.Fatalf("Patch dry-run trace re-signs preserved nested code: %#v", trace.Events)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
