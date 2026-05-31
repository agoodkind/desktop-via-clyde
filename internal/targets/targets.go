// Package targets builds runtime app and CLI records from declared config.
package targets

import (
	"fmt"
	"sort"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/config"
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

// ComputerUseSignTarget describes one helper that must be re-signed.
type ComputerUseSignTarget struct {
	Path         string
	Entitlements *EntitlementsPolicy
}

// ComputerUsePolicy describes the companion helper repair settings for one app.
type ComputerUsePolicy struct {
	HostAppPath           string
	BundledAppPath        string
	AppPathFromHome       string
	CacheAppGlobsFromHome []string
	AuthPluginPath        string
	AuthPluginExecutable  string
	UpstreamTrustedTeamID string
	TeamPatchBinaries     []string
	TeamRequirementPlists []string
	SignTargets           []ComputerUseSignTarget
}

// Target is the runtime app target derived from validated config declarations.
type Target struct {
	ID                            string
	Command                       spec.CommandSpec
	Operations                    map[string]spec.OperationSpec
	AppPath                       string
	BundleID                      string
	ExecName                      string
	KeychainServices              []string
	NestedSignPaths               []string
	PreservedNestedCodePaths      []string
	Entitlements                  *EntitlementsPolicy
	Updater                       Updater
	ComputerUse                   *ComputerUsePolicy
	BundledCLITee                 *spec.BundledCLITeeSpec
	LaunchPolicy                  spec.LaunchPolicySpec
	OriginalDRBootstrapCapability string
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

// Lookup returns one configured app target by declaration id.
func Lookup(id string) (Target, error) {
	app, ok := config.Current().Apps[id]
	if !ok {
		return Target{}, fmt.Errorf("unknown target %q", id)
	}
	return buildTarget(app), nil
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
		ID:                            app.ID,
		Command:                       app.Command,
		Operations:                    cloneOperations(app.Operations),
		AppPath:                       app.AppPath,
		BundleID:                      app.BundleID,
		ExecName:                      app.ExecName,
		KeychainServices:              cloneStrings(app.KeychainServices),
		NestedSignPaths:               cloneStrings(app.NestedSignPaths),
		PreservedNestedCodePaths:      cloneStrings(app.PreservedNestedCodePaths),
		Entitlements:                  buildEntitlements(app.Entitlements),
		Updater:                       buildUpdater(app.Updater),
		BundledCLITee:                 cloneBundledCLITee(app.BundledCLITee),
		LaunchPolicy:                  app.LaunchPolicy,
		ComputerUse:                   nil,
		OriginalDRBootstrapCapability: app.OriginalDRBootstrapCapability,
	}
	if app.ComputerUse != nil {
		target.ComputerUse = buildComputerUse(*app.ComputerUse)
	}
	return target
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

func buildComputerUse(policy spec.ComputerUseSpec) *ComputerUsePolicy {
	signTargets := make([]ComputerUseSignTarget, 0, len(policy.SignTargets))
	for _, target := range policy.SignTargets {
		signTargets = append(signTargets, ComputerUseSignTarget{
			Path:         target.Path,
			Entitlements: buildEntitlements(target.Entitlements),
		})
	}
	return &ComputerUsePolicy{
		HostAppPath:           policy.HostAppPath,
		BundledAppPath:        policy.BundledAppPath,
		AppPathFromHome:       policy.AppPathFromHome,
		CacheAppGlobsFromHome: cloneStrings(policy.CacheAppGlobsFromHome),
		AuthPluginPath:        policy.AuthPluginPath,
		AuthPluginExecutable:  policy.AuthPluginExecutable,
		UpstreamTrustedTeamID: policy.UpstreamTrustedTeamID,
		TeamPatchBinaries:     cloneStrings(policy.TeamPatchBinaries),
		TeamRequirementPlists: cloneStrings(policy.TeamRequirementPlists),
		SignTargets:           signTargets,
	}
}

func cloneBundledCLITee(tee *spec.BundledCLITeeSpec) *spec.BundledCLITeeSpec {
	if tee == nil {
		return nil
	}
	cloned := *tee
	return &cloned
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
