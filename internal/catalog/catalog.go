// Package catalog tracks the capability names linked into this binary.
package catalog

import (
	"fmt"
	"sort"
	"sync"
)

var (
	capabilitiesMu                  sync.RWMutex
	operationCapabilities           = map[string]struct{}{}
	bootstrapCapabilities           = map[string]struct{}{}
	patchHookCapabilities           = map[string]struct{}{}
	preLaunchPolicyHookCapabilities = map[string]struct{}{}
)

// RegisterOperationCapability records one linked operation capability.
func RegisterOperationCapability(name string) error {
	return registerCapability("operation", name, operationCapabilities)
}

// RegisterBootstrapCapability records one linked bootstrap capability.
func RegisterBootstrapCapability(name string) error {
	return registerCapability("bootstrap", name, bootstrapCapabilities)
}

// RegisterPatchHookCapability records one linked patch hook capability.
func RegisterPatchHookCapability(name string) error {
	return registerCapability("patch hook", name, patchHookCapabilities)
}

// RegisterPreLaunchPolicyHookCapability records one linked pre-launch-policy hook capability.
func RegisterPreLaunchPolicyHookCapability(name string) error {
	return registerCapability("pre-launch-policy hook", name, preLaunchPolicyHookCapabilities)
}

// HasOperationCapability reports whether the operation capability is linked
// into this binary.
func HasOperationCapability(name string) bool {
	capabilitiesMu.RLock()
	defer capabilitiesMu.RUnlock()
	_, ok := operationCapabilities[name]
	return ok
}

// HasBootstrapCapability reports whether the bootstrap capability is linked
// into this binary.
func HasBootstrapCapability(name string) bool {
	capabilitiesMu.RLock()
	defer capabilitiesMu.RUnlock()
	_, ok := bootstrapCapabilities[name]
	return ok
}

// HasPatchHookCapability reports whether the patch hook capability is linked
// into this binary.
func HasPatchHookCapability(name string) bool {
	capabilitiesMu.RLock()
	defer capabilitiesMu.RUnlock()
	_, ok := patchHookCapabilities[name]
	return ok
}

// HasPreLaunchPolicyHookCapability reports whether the pre-launch-policy hook capability is linked
// into this binary.
func HasPreLaunchPolicyHookCapability(name string) bool {
	capabilitiesMu.RLock()
	defer capabilitiesMu.RUnlock()
	_, ok := preLaunchPolicyHookCapabilities[name]
	return ok
}

// OperationCapabilities returns linked operation capabilities in stable order.
func OperationCapabilities() []string {
	return capabilityNames(operationCapabilities)
}

// BootstrapCapabilities returns linked bootstrap capabilities in stable order.
func BootstrapCapabilities() []string {
	return capabilityNames(bootstrapCapabilities)
}

// PatchHookCapabilities returns linked patch hook capabilities in stable order.
func PatchHookCapabilities() []string {
	return capabilityNames(patchHookCapabilities)
}

// PreLaunchPolicyHookCapabilities returns linked pre-launch-policy hook capabilities in stable order.
func PreLaunchPolicyHookCapabilities() []string {
	return capabilityNames(preLaunchPolicyHookCapabilities)
}

func registerCapability(kind string, name string, capabilities map[string]struct{}) error {
	if name == "" {
		return fmt.Errorf("%s capability name is required", kind)
	}
	capabilitiesMu.Lock()
	defer capabilitiesMu.Unlock()
	if _, ok := capabilities[name]; ok {
		return fmt.Errorf("%s capability %q is already registered", kind, name)
	}
	capabilities[name] = struct{}{}
	return nil
}

func capabilityNames(capabilities map[string]struct{}) []string {
	capabilitiesMu.RLock()
	defer capabilitiesMu.RUnlock()
	names := make([]string, 0, len(capabilities))
	for name := range capabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
