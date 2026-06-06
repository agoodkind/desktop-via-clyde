// Package targets builds runtime app and CLI records from declared config.
package targets

import (
	"fmt"
	"sort"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/spec"
)

const (
	// UpdaterHTTPPathJSONManifest fetches a JSON manifest from a path template.
	UpdaterHTTPPathJSONManifest spec.UpdaterKind = spec.UpdaterKindHTTPPathJSONManifest
	// UpdaterSparkleAppcast fetches a Sparkle XML appcast.
	UpdaterSparkleAppcast spec.UpdaterKind = spec.UpdaterKindSparkleAppcast
	// UpdaterSquirrelJSON fetches a Squirrel-style JSON manifest.
	UpdaterSquirrelJSON spec.UpdaterKind = spec.UpdaterKindSquirrelJSON
)

// Updater describes the upstream updater endpoint for one target.
type Updater struct {
	Kind              spec.UpdaterKind
	URL               string
	URLTemplate       string
	UserAgent         string
	Platform          string
	Product           string
	SparklePublicKey  string
	DeviceIDParamName string
	DefaultChannel    string
	Channels          []spec.UpdaterChannel
}

// SupportsChannels reports whether the updater declares named channels.
func (u Updater) SupportsChannels() bool {
	return len(u.Channels) > 0
}

// ResolveChannel picks the effective update channel for one request.
func (u Updater) ResolveChannel(requested string) (string, error) {
	channel := strings.TrimSpace(requested)
	if !u.SupportsChannels() {
		if channel != "" {
			return "", fmt.Errorf("updater kind %q does not support channels", u.Kind)
		}
		return "", nil
	}
	if channel == "" {
		channel = strings.TrimSpace(u.DefaultChannel)
	}
	if channel == "" && len(u.Channels) > 0 {
		channel = u.Channels[0].Name
	}
	for _, candidate := range u.Channels {
		if candidate.Name == channel {
			return channel, nil
		}
	}
	return "", fmt.Errorf("unknown channel %q", channel)
}

// URLWithChannel resolves the updater URL for one channel name.
func (u Updater) URLWithChannel(channel string) (string, error) {
	if !u.SupportsChannels() {
		return u.URL, nil
	}
	resolved, err := u.ResolveChannel(channel)
	if err != nil {
		return "", err
	}
	for _, candidate := range u.Channels {
		if candidate.Name == resolved {
			if candidate.URL != "" {
				return candidate.URL, nil
			}
			return u.URL, nil
		}
	}
	return "", fmt.Errorf("unknown channel %q", resolved)
}

// EntitlementsPolicy describes how to normalize entitlements before signing.
type EntitlementsPolicy struct {
	Strip                       []string
	RequiredBooleanEntitlements []string
}

// DevelopmentSigningPolicy is the runtime view of opt-in development-profile
// signing for one target. A nil pointer or Enabled=false keeps the target on the
// standard shim plus Developer ID path; when Enabled and its assets are present,
// the patcher applies the Apple Development overlay instead.
type DevelopmentSigningPolicy struct {
	Enabled           bool
	ProfilePath       string
	P12Path           string
	P12PasswordFile   string
	InjectorDylibPath string
	ProxyInjection    bool
	AutoGenerate      bool
}

// ComputerUseSignTarget aliases extension-owned runtime helper metadata.
type ComputerUseSignTarget = extensions.ComputerUseRuntimeSignTarget

// ComputerUsePolicy aliases extension-owned companion helper settings.
type ComputerUsePolicy = extensions.ComputerUsePolicy

// Target is the runtime app target derived from validated config declarations.
type Target struct {
	ID                       string
	Command                  spec.CommandSpec
	Operations               map[string]spec.OperationSpec
	AppPath                  string
	BundleID                 string
	BundleIDAliases          []string
	HelperBundleIDs          []string
	ExecName                 string
	KeychainServices         []string
	NestedSignPaths          []string
	PreservedNestedCodePaths []string
	ProvisioningProfile      string
	Entitlements             *EntitlementsPolicy
	DevelopmentSigning       *DevelopmentSigningPolicy
	Updater                  Updater
	Extensions               extensions.Target
	LaunchPolicy             spec.LaunchPolicySpec
}

// CLIProgram is the runtime non-app CLI declaration derived from config.
type CLIProgram struct {
	ID         string
	Command    spec.CommandSpec
	Operations map[string]spec.OperationSpec
}

// All returns all configured app targets in stable command order.
func All() []Target {
	cfg := config.Current()
	ids := make([]string, 0, len(cfg.Apps))
	for id := range cfg.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := make([]Target, 0, len(ids))
	for _, id := range ids {
		results = append(results, buildTarget(cfg.Apps[id]))
	}
	return results
}

// AllCLIs returns all configured non-app CLI programs in stable command order.
func AllCLIs() []CLIProgram {
	cfg := config.Current()
	ids := make([]string, 0, len(cfg.CLIs))
	for id := range cfg.CLIs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := make([]CLIProgram, 0, len(ids))
	for _, id := range ids {
		results = append(results, CLIProgram{
			ID:         cfg.CLIs[id].ID,
			Command:    cfg.CLIs[id].Command,
			Operations: cloneOperations(cfg.CLIs[id].Operations),
		})
	}
	return results
}

func buildTarget(app spec.AppSpec) Target {
	target := Target{
		ID:                       app.ID,
		Command:                  app.Command,
		Operations:               cloneOperations(app.Operations),
		AppPath:                  app.AppPath,
		BundleID:                 app.BundleID,
		BundleIDAliases:          cloneStrings(app.BundleIDAliases),
		HelperBundleIDs:          cloneStrings(app.HelperBundleIDs),
		ExecName:                 app.ExecName,
		KeychainServices:         cloneStrings(app.KeychainServices),
		NestedSignPaths:          cloneStrings(app.NestedSignPaths),
		PreservedNestedCodePaths: cloneStrings(app.PreservedNestedCodePaths),
		ProvisioningProfile:      app.ProvisioningProfile,
		Entitlements:             buildEntitlements(app.Entitlements),
		DevelopmentSigning:       buildDevelopmentSigning(app.DevelopmentSigning),
		Updater:                  buildUpdater(app.Updater),
		Extensions:               extensions.BuildTarget(app.Extensions),
		LaunchPolicy:             app.LaunchPolicy,
	}
	return target
}

// PostPatchHookCapabilities returns optional post-patch hook capabilities.
func (t Target) PostPatchHookCapabilities() []string {
	return t.Extensions.PostPatchHookCapabilities()
}

// PreLaunchPolicyHookCapabilities returns optional pre-launch-policy hook capabilities.
func (t Target) PreLaunchPolicyHookCapabilities() []string {
	return t.Extensions.PreLaunchPolicyHookCapabilities()
}

func buildUpdater(updater spec.UpdaterSpec) Updater {
	return Updater{
		Kind:              updater.Kind,
		URL:               updater.URL,
		URLTemplate:       updater.URLTemplate,
		UserAgent:         updater.UserAgent,
		Platform:          updater.Platform,
		Product:           updater.Product,
		SparklePublicKey:  updater.SparklePublicKey,
		DeviceIDParamName: updater.DeviceIDParamName,
		DefaultChannel:    updater.DefaultChannel,
		Channels:          append([]spec.UpdaterChannel(nil), updater.Channels...),
	}
}

func buildEntitlements(entitlements spec.EntitlementsSpec) *EntitlementsPolicy {
	return &EntitlementsPolicy{
		Strip:                       cloneStrings(entitlements.Strip),
		RequiredBooleanEntitlements: cloneStrings(entitlements.RequiredBoolean),
	}
}

// buildDevelopmentSigning mirrors the decoded spec into the runtime policy. It
// always returns a non-nil pointer (matching buildEntitlements); a target without
// a [development_signing] table yields a policy with Enabled=false, so the patch
// pipeline keeps it on the standard shim plus Developer ID path.
func buildDevelopmentSigning(ds spec.DevelopmentSigningSpec) *DevelopmentSigningPolicy {
	return &DevelopmentSigningPolicy{
		Enabled:           ds.Enabled,
		ProfilePath:       ds.ProfilePath,
		P12Path:           ds.P12Path,
		P12PasswordFile:   ds.P12PasswordFile,
		InjectorDylibPath: ds.InjectorDylibPath,
		ProxyInjection:    ds.ProxyInjection,
		AutoGenerate:      ds.AutoGenerate,
	}
}

func cloneOperations(operations map[string]spec.OperationSpec) map[string]spec.OperationSpec {
	if len(operations) == 0 {
		return nil
	}
	cloned := make(map[string]spec.OperationSpec, len(operations))
	for id, operation := range operations {
		item := operation
		item.Aliases = cloneStrings(operation.Aliases)
		if len(operation.Flags) > 0 {
			item.Flags = append([]spec.FlagSpec(nil), operation.Flags...)
		}
		cloned[id] = item
	}
	return cloned
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
