package targets

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/spec"
)

func TestAllHasThreeTargets(t *testing.T) {
	installFixture(t)
	if len(All()) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(All()))
	}
}

func TestLookupKnown(t *testing.T) {
	installFixture(t)
	for _, id := range []string{"cursor", "codex", "claude"} {
		tg, err := lookupTarget(id)
		if err != nil {
			t.Errorf("Lookup(%q) returned error: %v", id, err)
			continue
		}
		if tg.ID != id {
			t.Errorf("Lookup(%q).ID = %q", id, tg.ID)
		}
		if tg.AppPath == "" || tg.ExecName == "" || tg.BundleID == "" {
			t.Errorf("Lookup(%q) returned incomplete target: %+v", id, tg)
		}
	}
}

func TestLookupUnknown(t *testing.T) {
	installFixture(t)
	if _, err := lookupTarget("nope"); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

func TestEntitlementsPolicyPerTarget(t *testing.T) {
	installFixture(t)
	wantStrip := map[string][]string{
		"cursor": {},
		"codex": {
			"com.apple.application-identifier",
			"com.apple.developer.team-identifier",
			"keychain-access-groups",
		},
		"claude": {
			"com.apple.application-identifier",
			"com.apple.developer.team-identifier",
			"keychain-access-groups",
		},
	}
	wantRequired := map[string][]string{
		"cursor": {
			"com.apple.security.automation.apple-events",
			"com.apple.security.cs.disable-library-validation",
		},
		"codex": {
			"com.apple.security.automation.apple-events",
			"com.apple.security.cs.disable-library-validation",
		},
		"claude": {
			"com.apple.security.cs.disable-library-validation",
		},
	}
	for _, tg := range All() {
		if tg.Entitlements == nil {
			t.Errorf("target %s must declare an entitlement policy", tg.ID)
			continue
		}
		if !stringSlicesEqual(tg.Entitlements.Strip, wantStrip[tg.ID]) {
			t.Errorf("target %s Entitlements.Strip mismatch: got %v want %v", tg.ID, tg.Entitlements.Strip, wantStrip[tg.ID])
		}
		if !stringSlicesEqual(tg.Entitlements.RequiredBooleanEntitlements, wantRequired[tg.ID]) {
			t.Errorf("target %s RequiredBooleanEntitlements mismatch: got %v want %v", tg.ID, tg.Entitlements.RequiredBooleanEntitlements, wantRequired[tg.ID])
		}
	}
}

func TestUpdaterMetadataPerTarget(t *testing.T) {
	installFixture(t)
	cursor, err := lookupTarget("cursor")
	if err != nil {
		t.Fatalf("lookup cursor: %v", err)
	}
	if cursor.Updater.Kind != UpdaterHTTPPathJSONManifest {
		t.Fatalf("cursor updater kind = %q", cursor.Updater.Kind)
	}
	if !cursor.Updater.SupportsChannels() {
		t.Fatal("cursor updater should support channels")
	}
	if cursor.Updater.DefaultChannel != "dev" {
		t.Fatalf("cursor default channel = %q", cursor.Updater.DefaultChannel)
	}

	codex, err := lookupTarget("codex")
	if err != nil {
		t.Fatalf("lookup codex: %v", err)
	}
	if codex.Updater.Kind != UpdaterSparkleAppcast {
		t.Fatalf("codex updater kind = %q", codex.Updater.Kind)
	}
	if !codex.Updater.SupportsChannels() {
		t.Fatal("codex updater should support channels")
	}
	if codex.Updater.DefaultChannel != "beta" {
		t.Fatalf("codex default channel = %q", codex.Updater.DefaultChannel)
	}
	stableURL, err := codex.Updater.URLWithChannel("stable")
	if err != nil {
		t.Fatalf("codex stable URL: %v", err)
	}
	if stableURL == "" {
		t.Fatal("codex stable URL is empty")
	}

	claude, err := lookupTarget("claude")
	if err != nil {
		t.Fatalf("lookup claude: %v", err)
	}
	if claude.Updater.Kind != UpdaterSquirrelJSON {
		t.Fatalf("claude updater kind = %q", claude.Updater.Kind)
	}
	if claude.Updater.SupportsChannels() {
		t.Fatal("claude updater should not support channels")
	}
	if claude.Updater.URL == "" {
		t.Fatal("claude updater URL is empty")
	}
}

func TestNestedSignPathsPerTarget(t *testing.T) {
	installFixture(t)
	want := map[string][]string{
		"cursor": nil,
		"codex": {
			"Contents/Resources/codex",
			"Contents/Resources/codex_chronicle",
			"Contents/Resources/node",
			"Contents/Resources/node_repl",
			"Contents/Resources/rg",
			"Contents/Resources/native/bare-modifier-monitor",
			"Contents/Resources/native/browser-use-peer-authorization.node",
			"Contents/Resources/native/devicecheck.node",
			"Contents/Resources/native/launch-services-helper",
			"Contents/Resources/native/remote-control-device-key.node",
			"Contents/Resources/native/sky.node",
			"Contents/Resources/native/sparkle.node",
			"Contents/Frameworks/Codex Framework.framework/Helpers/Codex (Alerts).app",
			"Contents/Frameworks/Codex Framework.framework/Helpers/Codex (GPU).app",
			"Contents/Frameworks/Codex Framework.framework/Helpers/Codex (Service).app",
			"Contents/Frameworks/Codex Framework.framework/Helpers/Codex (Renderer).app",
			"Contents/Frameworks/Codex Framework.framework",
			"Contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Downloader.xpc",
			"Contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Installer.xpc",
			"Contents/Frameworks/Sparkle.framework/Versions/B/Updater.app",
			"Contents/Frameworks/Sparkle.framework/Versions/B/Autoupdate",
			"Contents/Frameworks/Sparkle.framework",
		},
		"claude": nil,
	}
	for _, tg := range All() {
		if !stringSlicesEqual(tg.NestedSignPaths, want[tg.ID]) {
			t.Errorf("target %s NestedSignPaths mismatch: got %v want %v", tg.ID, tg.NestedSignPaths, want[tg.ID])
		}
	}
}

func TestPreservedNestedCodePathsPerTarget(t *testing.T) {
	installFixture(t)
	want := map[string][]string{
		"cursor": nil,
		"codex":  nil,
		"claude": {"Contents/Frameworks/Squirrel.framework"},
	}
	for _, tg := range All() {
		if !stringSlicesEqual(tg.PreservedNestedCodePaths, want[tg.ID]) {
			t.Errorf("target %s PreservedNestedCodePaths mismatch: got %v want %v", tg.ID, tg.PreservedNestedCodePaths, want[tg.ID])
		}
	}
}

func TestComputerUsePolicyPerTarget(t *testing.T) {
	installFixture(t)
	for _, tg := range All() {
		if tg.ID != "codex" {
			if tg.Extensions.ComputerUse != nil {
				t.Errorf("target %s must not declare a Computer Use policy", tg.ID)
			}
			continue
		}
		if tg.Extensions.ComputerUse == nil {
			t.Fatal("codex must declare a Computer Use policy")
		}
		policy := tg.Extensions.ComputerUse
		if policy.HostAppPath != "/Applications/Codex.app" {
			t.Errorf("Codex Computer Use host app path mismatch: got %q", policy.HostAppPath)
		}
		if policy.BundledAppPath != "Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app" {
			t.Errorf("Codex Computer Use bundled path mismatch: got %q", policy.BundledAppPath)
		}
		if policy.AppPathFromHome != ".codex/computer-use/Codex Computer Use.app" {
			t.Errorf("Codex Computer Use path mismatch: got %q", policy.AppPathFromHome)
		}
		wantCacheGlobs := []string{".codex/plugins/cache/openai-bundled/computer-use/*/Codex Computer Use.app"}
		if !stringSlicesEqual(policy.CacheAppGlobsFromHome, wantCacheGlobs) {
			t.Errorf("Codex Computer Use cache globs mismatch: got %v want %v", policy.CacheAppGlobsFromHome, wantCacheGlobs)
		}
		if policy.AuthPluginPath != "/Library/Security/SecurityAgentPlugins/CodexComputerUseAuthorizationPlugin.bundle" {
			t.Errorf("Codex Computer Use authorization plugin path mismatch: got %q", policy.AuthPluginPath)
		}
		if policy.AuthPluginExecutable != "Contents/MacOS/CodexComputerUseAuthorizationPlugin" {
			t.Errorf("Codex Computer Use authorization plugin executable mismatch: got %q", policy.AuthPluginExecutable)
		}
		if policy.UpstreamTrustedTeamID != "2DC432GLL2" {
			t.Errorf("Codex Computer Use upstream team mismatch: got %q", policy.UpstreamTrustedTeamID)
		}
		wantPatch := []string{
			"Contents/MacOS/SkyComputerUseService",
			"Contents/SharedSupport/CUALockScreenGuardian.app/Contents/MacOS/CUALockScreenGuardian",
		}
		if !stringSlicesEqual(policy.TeamPatchBinaries, wantPatch) {
			t.Errorf("Codex Computer Use patch binaries mismatch: got %v want %v", policy.TeamPatchBinaries, wantPatch)
		}
		wantSign := []string{
			"Contents/SharedSupport/Codex Computer Use Installer.app/Contents/Resources/CodexComputerUseAuthorizationPlugin.bundle",
			"Contents/SharedSupport/Codex Computer Use Installer.app",
			"Contents/SharedSupport/SkyComputerUseClient.app",
			"Contents/SharedSupport/CUALockScreenGuardian.app",
			".",
		}
		gotSign := make([]string, 0, len(policy.SignTargets))
		for _, signTarget := range policy.SignTargets {
			gotSign = append(gotSign, signTarget.Path)
		}
		if !stringSlicesEqual(gotSign, wantSign) {
			t.Errorf("Codex Computer Use sign targets mismatch: got %v want %v", gotSign, wantSign)
		}
		mainHelper := policy.SignTargets[len(policy.SignTargets)-1]
		wantMainRequired := []string{
			"com.apple.security.automation.apple-events",
			"com.apple.security.device.audio-input",
		}
		if mainHelper.Entitlements == nil {
			t.Fatal("Codex Computer Use main helper must declare entitlements")
		}
		if !stringSlicesEqual(mainHelper.Entitlements.RequiredBooleanEntitlements, wantMainRequired) {
			t.Errorf("Codex Computer Use main helper required entitlements mismatch: got %v want %v", mainHelper.Entitlements.RequiredBooleanEntitlements, wantMainRequired)
		}
	}
}

func TestBundleIdentityMetadataPerTarget(t *testing.T) {
	installFixture(t)
	codex, err := lookupTarget("codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	if !stringSlicesEqual(codex.BundleIDAliases, []string{"com.openai.codex"}) {
		t.Fatalf("codex bundle aliases = %v", codex.BundleIDAliases)
	}
	for _, want := range []string{
		"com.openai.codex.framework",
		"com.openai.codex.framework.AlertNotificationService",
		"com.openai.codex.helper",
		"com.openai.codex.helper.renderer",
		"com.openai.sky.CUAService.AuthorizationPlugin",
		"com.openai.sky.CUAService",
	} {
		if !slices.Contains(codex.HelperBundleIDs, want) {
			t.Fatalf("codex helper bundle IDs missing %q: %v", want, codex.HelperBundleIDs)
		}
	}
	for _, targetID := range []string{"cursor", "codex", "claude"} {
		target, err := lookupTarget(targetID)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", targetID, err)
		}
		if !slices.Contains(target.HardResetServices, "ScreenCapture") {
			t.Fatalf("%s hard reset services missing ScreenCapture: %v", targetID, target.HardResetServices)
		}
		if !slices.Contains(target.HardResetServices, "SystemPolicyAllFiles") {
			t.Fatalf("%s hard reset services missing SystemPolicyAllFiles: %v", targetID, target.HardResetServices)
		}
	}
}

func TestCodexComputerUseTeamRequirementPlists(t *testing.T) {
	installFixture(t)
	tg, err := lookupTarget("codex")
	if err != nil {
		t.Fatalf("Lookup(%q) returned error: %v", "codex", err)
	}
	if tg.Extensions.ComputerUse == nil {
		t.Fatal("codex must declare a Computer Use policy")
	}
	want := []string{
		"Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement",
		"Contents/SharedSupport/CUALockScreenGuardian.app/Contents/Resources/CUALockScreenGuardian_Parent.coderequirement",
	}
	if !stringSlicesEqual(tg.Extensions.ComputerUse.TeamRequirementPlists, want) {
		t.Errorf("Codex Computer Use requirement plists mismatch: got %v want %v", tg.Extensions.ComputerUse.TeamRequirementPlists, want)
	}
}

func TestRegistryUsesConfiguredGenericIDs(t *testing.T) {
	config.SetCurrent(&spec.Config{
		Signing: spec.SigningSpec{Identity: "Test Identity", TeamID: "TEST123456"},
		Apps: map[string]spec.AppSpec{
			"zeta": {
				ID:       "zeta",
				AppPath:  "/Applications/Zeta.app",
				BundleID: "example.zeta",
				ExecName: "Zeta",
				Command:  spec.CommandSpec{Use: "zeta"},
				Operations: map[string]spec.OperationSpec{
					"status": {ID: "status", Use: "status", Capability: "app.status"},
				},
				LaunchPolicy: spec.LaunchPolicySpec{
					ProxyHost: "::1", ProxyPort: 1, CACertificate: "/tmp/ca", NoProxy: "localhost", LaunchWorkingDirectory: "/tmp",
				},
			},
			"alpha": {
				ID:       "alpha",
				AppPath:  "/Applications/Alpha.app",
				BundleID: "example.alpha",
				ExecName: "Alpha",
				Command:  spec.CommandSpec{Use: "alpha"},
				Operations: map[string]spec.OperationSpec{
					"status": {ID: "status", Use: "status", Capability: "app.status"},
				},
				LaunchPolicy: spec.LaunchPolicySpec{
					ProxyHost: "::1", ProxyPort: 1, CACertificate: "/tmp/ca", NoProxy: "localhost", LaunchWorkingDirectory: "/tmp",
				},
			},
		},
		CLIs: map[string]spec.CLISpec{
			"beta": {
				ID:         "beta",
				Command:    spec.CommandSpec{Use: "beta"},
				Operations: map[string]spec.OperationSpec{},
			},
		},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })

	all := All()
	if len(all) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(all))
	}
	if all[0].ID != "alpha" || all[1].ID != "zeta" {
		t.Fatalf("target ids = %q, %q", all[0].ID, all[1].ID)
	}
	found, err := Lookup("zeta")
	if err != nil {
		t.Fatalf("Lookup(zeta): %v", err)
	}
	if found.Command.Use != "zeta" {
		t.Fatalf("Lookup(zeta).Command.Use = %q", found.Command.Use)
	}
}

func stringSlicesEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func lookupTarget(id string) (Target, error) {
	for _, target := range All() {
		if target.ID == id {
			return target, nil
		}
	}
	return Target{}, fmt.Errorf("unknown target %q", id)
}

func installFixture(t *testing.T) {
	t.Helper()
	if err := registerFixtureCapabilities(); err != nil {
		t.Fatalf("RegisterFixtureCapabilities(): %v", err)
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
