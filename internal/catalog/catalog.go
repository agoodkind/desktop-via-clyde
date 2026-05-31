// Package catalog declares the capability names linked into this binary.
package catalog

const (
	// OperationAppPatch patches one configured desktop app.
	OperationAppPatch = "app.patch"
	// OperationAppUnpatch restores one configured desktop app from backup.
	OperationAppUnpatch = "app.unpatch"
	// OperationAppUpgrade upgrades one configured desktop app from upstream.
	OperationAppUpgrade = "app.upgrade"
	// OperationAppKeychainMigrate restores keychain access for one patched app.
	OperationAppKeychainMigrate = "app.keychain-migrate"
	// OperationAppStatus prints status for one configured desktop app.
	OperationAppStatus = "app.status"
	// OperationStandaloneInstall installs one configured standalone CLI.
	OperationStandaloneInstall = "standalone-cli.install"
	// OperationStandaloneStatus prints status for one configured standalone CLI.
	OperationStandaloneStatus = "standalone-cli.status"
	// OriginalDRBootstrapCleanMainBinary reads the upstream signature rule from a clean main binary.
	OriginalDRBootstrapCleanMainBinary = "clean-main-binary"
	// PatchHookBundledCLITee wraps and unwraps a bundled CLI with the stdio tee shim.
	PatchHookBundledCLITee = "bundled-cli-tee"
)

var operationCapabilities = map[string]struct{}{
	OperationAppPatch:           {},
	OperationAppUnpatch:         {},
	OperationAppUpgrade:         {},
	OperationAppKeychainMigrate: {},
	OperationAppStatus:          {},
	OperationStandaloneInstall:  {},
	OperationStandaloneStatus:   {},
}

var bootstrapCapabilities = map[string]struct{}{
	OriginalDRBootstrapCleanMainBinary: {},
}

var patchHookCapabilities = map[string]struct{}{
	PatchHookBundledCLITee: {},
}

// HasOperationCapability reports whether the operation capability is linked
// into this binary.
func HasOperationCapability(name string) bool {
	_, ok := operationCapabilities[name]
	return ok
}

// HasBootstrapCapability reports whether the bootstrap capability is linked
// into this binary.
func HasBootstrapCapability(name string) bool {
	_, ok := bootstrapCapabilities[name]
	return ok
}

// HasPatchHookCapability reports whether the patch hook capability is linked
// into this binary.
func HasPatchHookCapability(name string) bool {
	_, ok := patchHookCapabilities[name]
	return ok
}
