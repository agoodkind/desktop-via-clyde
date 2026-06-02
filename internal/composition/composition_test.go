package composition

import (
	"testing"

	"goodkind.io/desktop-via-clyde/internal/bundledclitee"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/codexcli"
	"goodkind.io/desktop-via-clyde/internal/codexclishim"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

func TestRegisterLinksEveryCatalogCapability(t *testing.T) {
	if err := Register(); err != nil {
		t.Fatalf("Register(): %v", err)
	}
	wantOperations := []string{
		patch.AppKeychainMigrateCapability,
		patch.AppPatchCapability,
		operations.AppStatusCapability,
		upgrade.AppUpgradeCapability,
		codexcli.StandaloneInstallCapability,
		codexcli.StandaloneStatusCapability,
	}
	assertEqualStrings(t, catalog.OperationCapabilities(), wantOperations)
	for _, capability := range wantOperations {
		if _, ok := operations.Lookup(capability); !ok {
			t.Fatalf("operation %q has no handler", capability)
		}
	}
	assertEqualStrings(t, upgrade.RegisteredBootstrapStrategies(), []string{
		upgrade.CleanMainBinaryBootstrapCapability,
	})
	assertEqualStrings(t, catalog.BootstrapCapabilities(), upgrade.RegisteredBootstrapStrategies())
	assertEqualStrings(t, patch.RegisteredPostPatchHooks(), []string{
		bundledclitee.HookCapability,
	})
	assertEqualStrings(t, catalog.PatchHookCapabilities(), patch.RegisteredPostPatchHooks())
	assertEqualStrings(t, patch.RegisteredPreLaunchPolicyHooks(), []string{
		codexclishim.HookCapability,
	})
	assertEqualStrings(t, catalog.PreLaunchPolicyHookCapabilities(), patch.RegisteredPreLaunchPolicyHooks())
	assertEqualStrings(t, extensions.RegisteredAppValidators(), []string{
		"bundled_cli_tee",
		"codex_cli_shim",
		"computer_use",
		"original_dr_bootstrap_capability",
	})
}

func assertEqualStrings(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("values mismatch: got %v want %v", got, want)
		}
	}
}
