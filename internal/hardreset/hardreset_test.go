package hardreset

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestBuildPlanSeparatesFullIdentitySetFromTCCUtilBundleIDs(t *testing.T) {
	appPath := writeTestCodexBundle(t)

	target := targets.Target{
		ID:              "codex",
		AppPath:         appPath,
		BundleID:        "com.openai.codex.beta",
		BundleIDAliases: []string{"com.openai.codex"},
		HelperBundleIDs: []string{"com.openai.sky.CUAService", "com.openai.codex.helper"},
	}

	plan, err := BuildPlan(context.Background(), target)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, want := range []string{
		"com.openai.codex",
		"com.openai.codex.beta",
		"com.openai.codex.helper",
		"com.openai.sky.CUAService",
	} {
		if !containsString(plan.BundleIDs, want) {
			t.Fatalf("bundle IDs missing %q: %v", want, plan.BundleIDs)
		}
	}
	for _, want := range []string{
		"com.openai.codex",
		"com.openai.codex.beta",
		"com.openai.codex.helper",
		"com.openai.sky.CUAService",
	} {
		if !containsString(plan.TCCUtilBundleIDs, want) {
			t.Fatalf("tccutil bundle IDs missing %q: %v", want, plan.TCCUtilBundleIDs)
		}
	}
	for _, forbidden := range []string{
		"com.openai.codex.framework",
		"com.openai.sky.CUAService.AuthorizationPlugin",
	} {
		if containsString(plan.TCCUtilBundleIDs, forbidden) {
			t.Fatalf("tccutil bundle IDs includes non-app bundle %q: %v", forbidden, plan.TCCUtilBundleIDs)
		}
	}
}

func TestRunDryRunPrintsTCCResetCommandsAndReportOnlyAftercare(t *testing.T) {
	appPath := writeTestCodexBundle(t)
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		BundleID: "com.openai.codex.beta",
	}

	var out bytes.Buffer
	if err := Run(context.Background(), target, Options{DryRun: true, Out: &out}); err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"target=codex hard-reset",
		"dry-run: /usr/bin/tccutil reset All com.openai.codex.beta",
		"dry-run: sqlite delete user_tcc_rows",
		"dry-run: sqlite count tcc_rows db=user",
		"dry-run: sqlite count tcc_rows db=system",
		"dry-run: /usr/bin/killall tccd reason=after_tcc_db_cleanup",
		"dry-run: remove patch_state target=codex",
		"dry-run: artifact action=remove kind=app_bundle",
		"aftercare=report-only",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dry-run output missing %q\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"System Settings", "open ", "launchctl"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("dry-run output contains forbidden aftercare %q\n%s", forbidden, text)
		}
	}
}

func TestBuildPlanIncludesTargetDerivedArtifacts(t *testing.T) {
	home := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)
	appPath := writeTestCodexBundle(t)
	appSupportDir := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code")
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		BundleID: "com.openai.codex.beta",
		Extensions: extensions.Target{
			ComputerUse: &targets.ComputerUsePolicy{
				AppPathFromHome:       ".codex/computer-use/Codex Computer Use.app",
				CacheAppGlobsFromHome: []string{".codex/plugins/cache/openai-bundled/computer-use/*/Codex Computer Use.app"},
				AuthPluginPath:        "/Library/Security/SecurityAgentPlugins/CodexComputerUseAuthorizationPlugin.bundle",
			},
			BundledCLITee: &extensions.BundledCLITeeSpec{
				AppSupportDir: appSupportDir,
				BundledCLIRel: "claude.app/Contents/MacOS/claude",
			},
		},
	}

	plan, err := BuildPlan(context.Background(), target)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	wantArtifacts := []Artifact{
		{Kind: "app_bundle", Path: appPath, Action: artifactActionRemove, Glob: false},
		{
			Kind:   "upgrade_staging",
			Path:   filepath.Join(stateHome, "clyde", "upgrade-staging", "codex-*"),
			Action: artifactActionRemove,
			Glob:   true,
		},
		{
			Kind:   "computer_use_helper",
			Path:   filepath.Join(home, ".codex", "computer-use", "Codex Computer Use.app"),
			Action: artifactActionRemove,
			Glob:   false,
		},
		{
			Kind:   "computer_use_cache_helper",
			Path:   filepath.Join(home, ".codex", "plugins", "cache", "openai-bundled", "computer-use", "*", "Codex Computer Use.app"),
			Action: artifactActionRemove,
			Glob:   true,
		},
		{
			Kind:   "computer_use_auth_plugin",
			Path:   "/Library/Security/SecurityAgentPlugins/CodexComputerUseAuthorizationPlugin.bundle",
			Action: artifactActionRemoveSudo,
			Glob:   false,
		},
		{
			Kind:   "bundled_cli_tee",
			Path:   filepath.Join(appSupportDir, "*", "claude.app", "Contents", "MacOS", "claude"),
			Action: artifactActionRestoreReal,
			Glob:   true,
		},
	}
	for _, want := range wantArtifacts {
		if !containsArtifact(plan.Artifacts, want) {
			t.Fatalf("artifact missing %#v from %#v", want, plan.Artifacts)
		}
	}
}

func TestRemovePatchStateAndResetArtifacts(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	helperPath := filepath.Join(t.TempDir(), "Codex Computer Use.app")
	stagingPath := filepath.Join(stateHome, "clyde", "upgrade-staging", "codex-1")
	teePath := filepath.Join(t.TempDir(), "claude")
	for _, path := range []string{appPath, helperPath, stagingPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(teePath), 0o755); err != nil {
		t.Fatalf("MkdirAll tee parent: %v", err)
	}
	if err := os.WriteFile(teePath, []byte("shim"), 0o755); err != nil {
		t.Fatalf("WriteFile tee shim: %v", err)
	}
	if err := os.WriteFile(teePath+".real", []byte("real"), 0o755); err != nil {
		t.Fatalf("WriteFile tee real: %v", err)
	}
	if err := state.Update(paths.StateFile(), func(ms state.MultiState) (state.MultiState, error) {
		ms.Targets["codex"] = state.TargetState{PatchedVersion: "1", PatchedAt: time.Unix(0, 0).UTC()}
		ms.Targets["claude"] = state.TargetState{PatchedVersion: "2", PatchedAt: time.Unix(0, 0).UTC()}
		return ms, nil
	}); err != nil {
		t.Fatalf("state.Update: %v", err)
	}

	var out bytes.Buffer
	opts := Options{Out: &out}
	if err := removePatchState(context.Background(), opts, "codex"); err != nil {
		t.Fatalf("removePatchState: %v", err)
	}
	if err := resetArtifacts(context.Background(), opts, []Artifact{
		{Kind: "app_bundle", Path: appPath, Action: artifactActionRemove},
		{Kind: "computer_use_helper", Path: helperPath, Action: artifactActionRemove},
		{Kind: "upgrade_staging", Path: filepath.Join(stateHome, "clyde", "upgrade-staging", "codex-*"), Action: artifactActionRemove, Glob: true},
		{Kind: "bundled_cli_tee", Path: teePath, Action: artifactActionRestoreReal},
	}); err != nil {
		t.Fatalf("resetArtifacts: %v", err)
	}
	loaded, err := state.Load(paths.StateFile())
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if _, ok := loaded.Targets["codex"]; ok {
		t.Fatalf("codex state still present: %#v", loaded.Targets)
	}
	if _, ok := loaded.Targets["claude"]; !ok {
		t.Fatalf("claude state missing after codex reset: %#v", loaded.Targets)
	}
	for _, path := range []string{appPath, helperPath, stagingPath, teePath + ".real"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("path %s stat err = %v, want not exists", path, err)
		}
	}
	data, err := os.ReadFile(teePath)
	if err != nil {
		t.Fatalf("ReadFile restored tee: %v", err)
	}
	if string(data) != "real" {
		t.Fatalf("restored tee = %q, want real", string(data))
	}
	text := out.String()
	for _, want := range []string{"patch_state_removed target=codex", "kind=app_bundle", "kind=bundled_cli_tee"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q\n%s", want, text)
		}
	}
}

func writeTestCodexBundle(t *testing.T) string {
	t.Helper()
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	writeTestBundle(t, appPath, "com.openai.codex.beta", "APPL", "Codex", "Contents/MacOS/Codex")
	writeTestBundle(
		t,
		filepath.Join(appPath, "Contents/Frameworks/Codex Framework.framework"),
		"com.openai.codex.framework",
		"FMWK",
		"Codex Framework",
		"Versions/Current/Codex Framework",
	)
	writeTestBundle(
		t,
		filepath.Join(appPath, "Contents/Frameworks/Codex Framework.framework/Helpers/Codex Helper.app"),
		"com.openai.codex.helper",
		"APPL",
		"Codex Helper",
		"Contents/MacOS/Codex Helper",
	)
	writeTestBundle(
		t,
		filepath.Join(appPath, "Contents/Resources/CodexComputerUseAuthorizationPlugin.bundle"),
		"com.openai.sky.CUAService.AuthorizationPlugin",
		"BNDL",
		"CodexComputerUseAuthorizationPlugin",
		"Contents/MacOS/CodexComputerUseAuthorizationPlugin",
	)
	writeTestBundle(
		t,
		filepath.Join(appPath, "Contents/Resources/Codex Computer Use.app"),
		"com.openai.sky.CUAService",
		"APPL",
		"SkyComputerUseService",
		"Contents/MacOS/SkyComputerUseService",
	)
	return appPath
}

func writeTestBundle(t *testing.T, root string, bundleID string, packageType string, executable string, executableRelPath string) {
	t.Helper()
	infoPath := filepath.Join(root, "Contents", "Info.plist")
	if packageType == "FMWK" {
		infoPath = filepath.Join(root, "Resources", "Info.plist")
	}
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatalf("mkdir plist parent: %v", err)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
<key>CFBundleIdentifier</key>
<string>` + bundleID + `</string>
<key>CFBundlePackageType</key>
<string>` + packageType + `</string>
<key>CFBundleExecutable</key>
<string>` + executable + `</string>
</dict>
</plist>
`
	if err := os.WriteFile(infoPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	executablePath := filepath.Join(root, filepath.FromSlash(executableRelPath))
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatalf("mkdir executable parent: %v", err)
	}
	if err := os.WriteFile(executablePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
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

func containsArtifact(values []Artifact, want Artifact) bool {
	for _, value := range values {
		if value.Kind == want.Kind && value.Path == want.Path && value.Action == want.Action && value.Glob == want.Glob {
			return true
		}
	}
	return false
}
