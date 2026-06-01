// Package composition links the runtime capabilities compiled into the binary.
package composition

import (
	"fmt"
	"log/slog"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/bundledclitee"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/codexcli"
	"goodkind.io/desktop-via-clyde/internal/codexclishim"
	"goodkind.io/desktop-via-clyde/internal/computeruseext"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/stdioteeshim"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

var compositionLog = slog.With("component", "desktop-via-clyde", "subcomponent", "composition")

var (
	registerOnce sync.Once
	errRegister  error
)

// Register links every extension and core capability compiled into this binary.
func Register() error {
	registerOnce.Do(func() {
		errRegister = register()
	})
	return errRegister
}

func register() error {
	if err := stdioteeshim.RegisterHelper(); err != nil {
		return logCompositionRegistrationError("register stdio tee helper", err)
	}
	if err := codexclishim.RegisterHelper(); err != nil {
		return logCompositionRegistrationError("register Codex CLI helper", err)
	}
	if err := computeruseext.RegisterValidators(); err != nil {
		return logCompositionRegistrationError("register computer use validators", err)
	}
	if err := codexclishim.RegisterValidators(); err != nil {
		return logCompositionRegistrationError("register Codex CLI shim validators", err)
	}
	if err := bundledclitee.RegisterValidators(); err != nil {
		return logCompositionRegistrationError("register bundled CLI tee validators", err)
	}
	if err := upgrade.RegisterValidators(); err != nil {
		return logCompositionRegistrationError("register upgrade validators", err)
	}
	if err := operations.RegisterCoreHandlers(); err != nil {
		return logCompositionRegistrationError("register core operation handlers", err)
	}
	if err := patch.RegisterOperations(); err != nil {
		return logCompositionRegistrationError("register patch operations", err)
	}
	if err := computeruseext.RegisterLifecycleHooks(); err != nil {
		return logCompositionRegistrationError("register computer use lifecycle hooks", err)
	}
	if err := codexclishim.RegisterPreLaunchPolicyHooks(); err != nil {
		return logCompositionRegistrationError("register Codex CLI pre-launch-policy hooks", err)
	}
	if err := upgrade.RegisterOperations(); err != nil {
		return logCompositionRegistrationError("register upgrade operations", err)
	}
	if err := upgrade.RegisterBootstrapStrategies(); err != nil {
		return logCompositionRegistrationError("register upgrade bootstrap strategies", err)
	}
	if err := codexcli.RegisterOperations(); err != nil {
		return logCompositionRegistrationError("register Codex CLI operations", err)
	}
	if err := bundledclitee.RegisterPatchHooks(); err != nil {
		return logCompositionRegistrationError("register bundled CLI tee patch hooks", err)
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
	registeredPreLaunchPolicy := map[string]bool{}
	for _, capability := range patch.RegisteredPreLaunchPolicyHooks() {
		registeredPreLaunchPolicy[capability] = true
	}
	for _, capability := range catalog.PreLaunchPolicyHookCapabilities() {
		if !registeredPreLaunchPolicy[capability] {
			return fmt.Errorf("pre-launch-policy hook capability %q has no hook", capability)
		}
	}
	return nil
}

func logCompositionRegistrationError(message string, err error) error {
	compositionLog.Error("composition.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
