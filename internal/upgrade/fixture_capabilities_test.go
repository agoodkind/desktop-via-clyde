package upgrade

import (
	"fmt"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/extensions"
)

var (
	fixtureCapabilitiesOnce sync.Once
	errFixtureCapabilities  error
)

func registerFixtureCapabilities() error {
	fixtureCapabilitiesOnce.Do(func() {
		errFixtureCapabilities = registerFixtureCapabilitiesOnce()
	})
	return errFixtureCapabilities
}

func registerFixtureCapabilitiesOnce() error {
	for _, capability := range []string{
		"app.patch",
		"app.unpatch",
		"app.upgrade",
		"app.keychain-migrate",
		"app.status",
		"standalone-cli.install",
		"standalone-cli.status",
	} {
		if err := catalog.RegisterOperationCapability(capability); err != nil {
			return fmt.Errorf("register operation capability %s: %w", capability, err)
		}
	}
	if err := catalog.RegisterBootstrapCapability("clean-main-binary"); err != nil {
		return fmt.Errorf("register clean-main-binary bootstrap capability: %w", err)
	}
	if err := catalog.RegisterPatchHookCapability("bundled-cli-tee"); err != nil {
		return fmt.Errorf("register bundled-cli-tee patch hook capability: %w", err)
	}
	if err := catalog.RegisterPreLaunchPolicyHookCapability("codex-cli-shim"); err != nil {
		return fmt.Errorf("register codex-cli-shim pre-launch-policy capability: %w", err)
	}
	if err := extensions.RegisterAppValidator("bundled_cli_tee", extensions.ValidateBundledCLITee); err != nil {
		return fmt.Errorf("register bundled_cli_tee validator: %w", err)
	}
	if err := extensions.RegisterAppValidator("codex_cli_shim", extensions.ValidateCodexCLIShim); err != nil {
		return fmt.Errorf("register codex_cli_shim validator: %w", err)
	}
	if err := extensions.RegisterAppValidator("computer_use", extensions.ValidateComputerUse); err != nil {
		return fmt.Errorf("register computer_use validator: %w", err)
	}
	if err := extensions.RegisterAppValidator("original_dr_bootstrap_capability", extensions.ValidateOriginalDRBootstrapCapability); err != nil {
		return fmt.Errorf("register original_dr_bootstrap_capability validator: %w", err)
	}
	return nil
}
