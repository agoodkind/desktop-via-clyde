package patch_test

import (
	"fmt"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/extensions"
)

var (
	externalFixtureCapabilitiesOnce sync.Once
	errExternalFixtureCapabilities  error
)

func registerFixtureCapabilities() error {
	externalFixtureCapabilitiesOnce.Do(func() {
		errExternalFixtureCapabilities = registerFixtureCapabilitiesOnce()
	})
	return errExternalFixtureCapabilities
}

func registerFixtureCapabilitiesOnce() error {
	for _, capability := range []string{
		"app.patch",
		"app.upgrade",
		"app.keychain-migrate",
		"app.hard-reset",
		"app.status",
		"standalone-cli.install",
		"standalone-cli.status",
	} {
		if catalog.HasOperationCapability(capability) {
			continue
		}
		if err := catalog.RegisterOperationCapability(capability); err != nil {
			return fmt.Errorf("register operation capability %s: %w", capability, err)
		}
	}
	if !catalog.HasBootstrapCapability("clean-main-binary") {
		if err := catalog.RegisterBootstrapCapability("clean-main-binary"); err != nil {
			return fmt.Errorf("register clean-main-binary bootstrap capability: %w", err)
		}
	}
	if !catalog.HasPatchHookCapability("bundled-cli-tee") {
		if err := catalog.RegisterPatchHookCapability("bundled-cli-tee"); err != nil {
			return fmt.Errorf("register bundled-cli-tee patch hook capability: %w", err)
		}
	}
	if !catalog.HasPreLaunchPolicyHookCapability("codex-cli-shim") {
		if err := catalog.RegisterPreLaunchPolicyHookCapability("codex-cli-shim"); err != nil {
			return fmt.Errorf("register codex-cli-shim pre-launch-policy capability: %w", err)
		}
	}
	return registerFixtureValidators()
}

func registerFixtureValidators() error {
	if isFixtureValidatorRegistered("bundled_cli_tee") {
		return nil
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

func isFixtureValidatorRegistered(name string) bool {
	for _, candidate := range extensions.RegisteredAppValidators() {
		if candidate == name {
			return true
		}
	}
	return false
}
