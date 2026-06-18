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
	"goodkind.io/desktop-via-clyde/internal/devsign"
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

func TestCodexDevelopmentSigningResealIsLastSigningAction(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tg, err := lookupTarget(t, "codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	tg.AppPath = appPath
	tg.Extensions.ComputerUse.HostAppPath = appPath

	assetDir := t.TempDir()
	tg.DevelopmentSigning = &targets.DevelopmentSigningPolicy{
		Enabled:         true,
		ProfilePath:     writeDevSigningAsset(t, assetDir, "dev.provisionprofile"),
		P12Path:         writeDevSigningAsset(t, assetDir, "dev.p12"),
		P12PasswordFile: writeDevSigningAsset(t, assetDir, "p12-password"),
		ProxyInjection:  true,
	}

	trace := &patch.Trace{}
	if err := patch.Patch(context.Background(), tg, patch.Options{
		DryRun:          true,
		MigrateKeychain: false,
		Out:             io.Discard,
		Trace:           trace,
	}); err != nil {
		t.Fatalf("Patch dry-run (codex development signing): %v", err)
	}

	resealIndex := -1
	lastSigningIndex := -1
	for i, event := range trace.Events {
		if !isSigningCommand(event) {
			continue
		}
		lastSigningIndex = i
		if isDevResealCommand(event, appPath) {
			resealIndex = i
		}
	}
	if resealIndex < 0 {
		t.Fatalf("trace has no development-signing --shallow reseal command: %#v", trace.Events)
	}
	if resealIndex != lastSigningIndex {
		t.Fatalf("development-signing reseal at index %d is not the last signing command (last signing index=%d): %#v",
			resealIndex, lastSigningIndex, trace.Events)
	}
	if !hasNestedCodesignBefore(trace, resealIndex) {
		t.Fatalf("no nested codesign signing command precedes the development-signing reseal; the computer-use hooks must run before the seal: %#v", trace.Events)
	}
	requireExternalInjectorDryRun(t, trace, tg)
}

func requireExternalInjectorDryRun(t *testing.T, trace *patch.Trace, tg targets.Target) {
	t.Helper()
	appLocalInjector := devsign.AppLocalInjectorPath(tg)
	externalInjector := devsign.InjectorPath(tg)
	policyPath := devsign.InjectorPolicyPath(tg)
	for _, event := range trace.Events {
		if event.Action != "run_command" {
			continue
		}
		if filepath.Base(event.Command) == "codesign" && containsString(event.Args, appLocalInjector) {
			t.Fatalf("dry-run signs app-local injector %s: %#v", appLocalInjector, event)
		}
		if filepath.Base(event.Command) == "rcodesign" && containsString(event.Args, appLocalInjector) {
			t.Fatalf("dry-run rcodesign signs app-local injector %s: %#v", appLocalInjector, event)
		}
		if filepath.Base(event.Command) == "cp" && containsString(event.Args, appLocalInjector) {
			t.Fatalf("dry-run copies injector into app bundle %s: %#v", appLocalInjector, event)
		}
	}
	requireTraceCommand(t, trace, "/usr/libexec/PlistBuddy", []string{"-c", "Add :LSEnvironment:" + devsign.DyldInsertLibrariesKey + " string " + externalInjector, paths.InfoPlistPath(tg)})
	requireTraceCommand(t, trace, "/usr/libexec/PlistBuddy", []string{"-c", "Add :LSEnvironment:" + devsign.InjectorPolicyEnvKey + " string " + policyPath, paths.InfoPlistPath(tg)})
}

func writeDevSigningAsset(t *testing.T, dir string, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("test-asset"), 0o600); err != nil {
		t.Fatalf("write development signing asset %s: %v", path, err)
	}
	return path
}

func isSigningCommand(event patch.TraceEvent) bool {
	if event.Action != "run_command" {
		return false
	}
	base := filepath.Base(event.Command)
	if base != "codesign" && base != "rcodesign" {
		return false
	}
	for _, arg := range event.Args {
		if arg == "--verify" || arg == "--display" {
			return false
		}
	}
	return true
}

func isDevResealCommand(event patch.TraceEvent, appPath string) bool {
	if !isSigningCommand(event) || filepath.Base(event.Command) != "rcodesign" {
		return false
	}
	hasShallow := false
	sealsApp := false
	for _, arg := range event.Args {
		if arg == "--shallow" {
			hasShallow = true
		}
		if arg == appPath {
			sealsApp = true
		}
	}
	return hasShallow && sealsApp
}

func hasNestedCodesignBefore(trace *patch.Trace, resealIndex int) bool {
	for i, event := range trace.Events {
		if i >= resealIndex {
			return false
		}
		if isSigningCommand(event) && filepath.Base(event.Command) == "codesign" {
			return true
		}
	}
	return false
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
