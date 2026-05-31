// Package targets defines the configured macOS apps that desktop-via-clyde can patch.
package targets

import (
	"fmt"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/config"
)

// UpdaterKind identifies the upstream update protocol used by a target.
type UpdaterKind string

const (
	// UpdaterCursorManifest selects Cursor's JSON manifest endpoint.
	UpdaterCursorManifest UpdaterKind = "cursor-manifest"
	// UpdaterSparkleAppcast selects Sparkle's XML appcast format.
	UpdaterSparkleAppcast UpdaterKind = "sparkle-appcast"
	// UpdaterClaudeSquirrel selects Claude's Squirrel-style JSON endpoint.
	UpdaterClaudeSquirrel UpdaterKind = "claude-squirrel"
)

// UpdaterChannel declares one named updater channel.
type UpdaterChannel struct {
	Name string
	URL  string
}

// Updater describes the upstream updater endpoint for one target.
type Updater struct {
	Kind              UpdaterKind
	URL               string
	Platform          string
	Product           string
	SparklePublicKey  string
	DeviceIDParamName string
	DefaultChannel    string
	Channels          []UpdaterChannel
}

// SupportsChannels reports whether the updater exposes named channels.
func (u Updater) SupportsChannels() bool {
	return len(u.Channels) > 0
}

// ResolveChannel returns the requested channel or the configured default.
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

// URLWithChannel returns the updater URL for the resolved channel.
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
			if candidate.URL == "" {
				return u.URL, nil
			}
			return candidate.URL, nil
		}
	}
	return "", fmt.Errorf("unknown channel %q", resolved)
}

// EntitlementsPolicy describes how a target's extracted entitlements are normalized before re-signing.
type EntitlementsPolicy struct {
	Strip                       []string
	RequiredBooleanEntitlements []string
}

// ComputerUseSignTarget describes one app bundle inside Codex Computer Use that must be re-signed after local trust-policy repair.
type ComputerUseSignTarget struct {
	Path         string
	Entitlements *EntitlementsPolicy
}

// ComputerUsePolicy describes the Codex companion helper bundle whose native IPC trust policy must match the locally re-signed Codex app.
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

// Target describes one patchable bundle.
type Target struct {
	ID                       string
	AppPath                  string
	BundleID                 string
	ExecName                 string
	Entitlements             *EntitlementsPolicy
	KeychainServices         []string
	NestedSignPaths          []string
	PreservedNestedCodePaths []string
	ComputerUse              *ComputerUsePolicy
	Updater                  Updater
	BundledCLITee            config.BundledCLITeeConfig
	TargetPolicy             string
}

// All returns the configured targets in stable CLI order.
func All() []Target {
	cfg := config.Current()
	return []Target{
		buildTarget("cursor", cfg.Apps.Cursor),
		buildTarget("codex", cfg.Apps.Codex),
		buildTarget("claude", cfg.Apps.Claude),
	}
}

func buildTarget(id string, app config.AppConfig) Target {
	target := Target{
		ID:                       id,
		AppPath:                  app.AppPath,
		BundleID:                 app.BundleID,
		ExecName:                 app.ExecName,
		Entitlements:             buildEntitlements(app.Entitlements),
		KeychainServices:         cloneStrings(app.KeychainServices),
		NestedSignPaths:          cloneStrings(app.NestedSignPaths),
		PreservedNestedCodePaths: cloneStrings(app.PreservedNestedCodePaths),
		ComputerUse:              nil,
		Updater:                  buildUpdater(app.Updater),
		BundledCLITee:            app.BundledCLITee,
		TargetPolicy:             string(app.TargetPolicy),
	}
	if app.ComputerUse.HostAppPath != "" {
		target.ComputerUse = buildComputerUse(app.ComputerUse)
	}
	return target
}

func buildUpdater(updater config.UpdaterConfig) Updater {
	channels := make([]UpdaterChannel, 0, len(updater.Channels))
	for _, channel := range updater.Channels {
		channels = append(channels, UpdaterChannel{Name: channel.Name, URL: channel.URL})
	}
	return Updater{
		Kind:              mapUpdaterKind(updater.Kind),
		URL:               updater.URL,
		Platform:          updater.Platform,
		Product:           updater.Product,
		SparklePublicKey:  updater.SparklePublicKey,
		DeviceIDParamName: updater.DeviceIDParamName,
		DefaultChannel:    updater.DefaultChannel,
		Channels:          channels,
	}
}

func mapUpdaterKind(kind config.UpdaterKind) UpdaterKind {
	switch kind {
	case config.UpdaterKindCursorManifest:
		return UpdaterCursorManifest
	case config.UpdaterKindSparkleAppcast:
		return UpdaterSparkleAppcast
	case config.UpdaterKindClaudeSquirrel:
		return UpdaterClaudeSquirrel
	default:
		return UpdaterKind(kind)
	}
}

func buildEntitlements(entitlements config.EntitlementsConfig) *EntitlementsPolicy {
	return &EntitlementsPolicy{
		Strip:                       cloneStrings(entitlements.Strip),
		RequiredBooleanEntitlements: cloneStrings(entitlements.RequiredBoolean),
	}
}

func buildComputerUse(policy config.ComputerUseConfig) *ComputerUsePolicy {
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

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
