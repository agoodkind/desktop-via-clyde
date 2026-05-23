package targets

import "testing"

func TestRegistryHasThreeTargets(t *testing.T) {
	if len(Registry) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(Registry))
	}
}

func TestLookupKnown(t *testing.T) {
	for _, id := range []string{"cursor", "codex", "claude"} {
		tg, err := Lookup(id)
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
	if _, err := Lookup("nope"); err == nil {
		t.Fatal("expected error for unknown id")
	}
}

// Cursor declares Apple Events because upstream Cursor ships with that
// entitlement and appshot capture can target Cursor. Claude declares the shared
// runtime entitlement required by the shim. Codex and Claude both ship
// Team-bound entitlements that are invalid after local signing. Codex's
// original signature includes
// Team-bound keys (application-identifier, developer.team-identifier,
// keychain-access-groups) that AMFI refuses to honor cross-Team and that block
// launch entirely (verified empirically: AppleMobileFileIntegrityError
// Code=-413). Codex must strip exactly those three and must preserve Apple
// Events for appshot capture.
func TestEntitlementsPolicyPerTarget(t *testing.T) {
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
	for _, tg := range Registry {
		if tg.Entitlements == nil {
			t.Errorf("target %s must declare an entitlement policy", tg.ID)
			continue
		}
		expStrip, ok := wantStrip[tg.ID]
		if !ok {
			t.Errorf("unexpected target id %q in registry", tg.ID)
			continue
		}
		if !stringSlicesEqual(tg.Entitlements.Strip, expStrip) {
			t.Errorf("target %s Entitlements.Strip mismatch: got %v want %v", tg.ID, tg.Entitlements.Strip, expStrip)
		}
		expRequired := wantRequired[tg.ID]
		if !stringSlicesEqual(tg.Entitlements.RequiredBooleanEntitlements, expRequired) {
			t.Errorf(
				"target %s Entitlements.RequiredBooleanEntitlements mismatch: got %v want %v",
				tg.ID,
				tg.Entitlements.RequiredBooleanEntitlements,
				expRequired,
			)
		}
	}
}

func TestUpdaterMetadataPerTarget(t *testing.T) {
	want := map[string]UpdaterKind{
		"cursor": UpdaterCursorManifest,
		"codex":  UpdaterSparkleAppcast,
		"claude": UpdaterClaudeSquirrel,
	}
	for _, tg := range Registry {
		exp, ok := want[tg.ID]
		if !ok {
			t.Errorf("unexpected target id %q in registry", tg.ID)
			continue
		}
		if tg.Updater.Kind != exp {
			t.Errorf("target %s updater kind mismatch: got %q want %q", tg.ID, tg.Updater.Kind, exp)
		}
		if tg.ID != "cursor" && tg.Updater.URL == "" {
			t.Errorf("target %s updater URL is empty", tg.ID)
		}
	}
}

func TestNestedSignPathsPerTarget(t *testing.T) {
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
			"Contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Downloader.xpc",
			"Contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Installer.xpc",
			"Contents/Frameworks/Sparkle.framework/Versions/B/Updater.app",
			"Contents/Frameworks/Sparkle.framework/Versions/B/Autoupdate",
			"Contents/Frameworks/Sparkle.framework",
		},
		"claude": {
			"Contents/Frameworks/Squirrel.framework/Versions/A/Resources/ShipIt",
			"Contents/Frameworks/Squirrel.framework",
		},
	}
	for _, tg := range Registry {
		exp, ok := want[tg.ID]
		if !ok {
			t.Errorf("unexpected target id %q in registry", tg.ID)
			continue
		}
		if !stringSlicesEqual(tg.NestedSignPaths, exp) {
			t.Errorf("target %s NestedSignPaths mismatch: got %v want %v", tg.ID, tg.NestedSignPaths, exp)
		}
	}
}

func TestComputerUsePolicyPerTarget(t *testing.T) {
	for _, tg := range Registry {
		if tg.ID != "codex" {
			if tg.ComputerUse != nil {
				t.Errorf("target %s must not declare a Computer Use policy", tg.ID)
			}
			continue
		}
		if tg.ComputerUse == nil {
			t.Fatal("codex must declare a Computer Use policy")
		}
		policy := tg.ComputerUse
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
		wantRequirements := []string{
			"Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement",
		}
		if !stringSlicesEqual(policy.TeamRequirementPlists, wantRequirements) {
			t.Errorf("Codex Computer Use requirement plists mismatch: got %v want %v", policy.TeamRequirementPlists, wantRequirements)
		}
		wantSign := []string{
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
			t.Errorf(
				"Codex Computer Use main helper required entitlements mismatch: got %v want %v",
				mainHelper.Entitlements.RequiredBooleanEntitlements,
				wantMainRequired,
			)
		}
	}
}

func stringSlicesEqual(a, b []string) bool {
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
