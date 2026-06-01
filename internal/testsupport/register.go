package testsupport

import (
	"sync"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/extensions"
)

var (
	registerFixtureOnce sync.Once
	registerFixtureErr  error
)

// RegisterFixtureCapabilities links the capability and validator set used by
// current-config.toml for package-level tests that should not import the full
// composition root.
func RegisterFixtureCapabilities() error {
	registerFixtureOnce.Do(func() {
		registerFixtureErr = registerFixtureCapabilities()
	})
	return registerFixtureErr
}

func registerFixtureCapabilities() error {
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
			return err
		}
	}
	if err := catalog.RegisterBootstrapCapability("clean-main-binary"); err != nil {
		return err
	}
	if err := catalog.RegisterPatchHookCapability("bundled-cli-tee"); err != nil {
		return err
	}
	if err := extensions.RegisterAppValidator("bundled_cli_tee", extensions.ValidateBundledCLITee); err != nil {
		return err
	}
	if err := extensions.RegisterAppValidator("computer_use", extensions.ValidateComputerUse); err != nil {
		return err
	}
	return extensions.RegisterAppValidator("original_dr_bootstrap_capability", extensions.ValidateOriginalDRBootstrapCapability)
}
