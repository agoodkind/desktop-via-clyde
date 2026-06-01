// Package composition links the runtime capabilities compiled into the binary.
package composition

import (
	"fmt"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/bundledclitee"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/codexcli"
	"goodkind.io/desktop-via-clyde/internal/computeruseext"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

var (
	registerOnce sync.Once
	registerErr  error
)

// Register links every extension and core capability compiled into this binary.
func Register() error {
	registerOnce.Do(func() {
		registerErr = register()
	})
	return registerErr
}

func register() error {
	if err := computeruseext.RegisterValidators(); err != nil {
		return err
	}
	if err := bundledclitee.RegisterValidators(); err != nil {
		return err
	}
	if err := upgrade.RegisterValidators(); err != nil {
		return err
	}
	if err := operations.RegisterCoreHandlers(); err != nil {
		return err
	}
	if err := patch.RegisterOperations(); err != nil {
		return err
	}
	if err := computeruseext.RegisterLifecycleHooks(); err != nil {
		return err
	}
	if err := upgrade.RegisterOperations(); err != nil {
		return err
	}
	if err := upgrade.RegisterBootstrapStrategies(); err != nil {
		return err
	}
	if err := codexcli.RegisterOperations(); err != nil {
		return err
	}
	if err := bundledclitee.RegisterPatchHooks(); err != nil {
		return err
	}
	return validateLinkedCapabilities()
}

func validateLinkedCapabilities() error {
	for _, capability := range catalog.OperationCapabilities() {
		if _, ok := operations.Lookup(capability); !ok {
			return fmt.Errorf("operation capability %q has no registered handler", capability)
		}
	}
	registeredBootstrap := map[string]bool{}
	for _, capability := range upgrade.RegisteredBootstrapStrategies() {
		registeredBootstrap[capability] = true
	}
	for _, capability := range catalog.BootstrapCapabilities() {
		if !registeredBootstrap[capability] {
			return fmt.Errorf("bootstrap capability %q has no registered strategy", capability)
		}
	}
	registeredPostPatch := map[string]bool{}
	for _, capability := range patch.RegisteredPostPatchHooks() {
		registeredPostPatch[capability] = true
	}
	registeredPreUnpatch := map[string]bool{}
	for _, capability := range patch.RegisteredPreUnpatchHooks() {
		registeredPreUnpatch[capability] = true
	}
	for _, capability := range catalog.PatchHookCapabilities() {
		if !registeredPostPatch[capability] {
			return fmt.Errorf("patch hook capability %q has no post-patch hook", capability)
		}
		if !registeredPreUnpatch[capability] {
			return fmt.Errorf("patch hook capability %q has no pre-unpatch hook", capability)
		}
	}
	return nil
}
