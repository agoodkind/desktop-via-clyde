// Package config loads and holds the desktop-via-clyde XDG configuration.
package config

import (
	"path/filepath"
	"sync"
)

// Config is the fully typed desktop-via-clyde runtime configuration.
type Config struct {
	Signing SigningConfig `toml:"signing"`
	Proxy   ProxyConfig   `toml:"proxy"`
	CLI     CLIConfig     `toml:"cli"`
	Apps    AppsConfig    `toml:"apps"`
}

// TargetPolicy selects the shim environment policy for one app target.
type TargetPolicy string

const (
	// TargetPolicyDefault applies the standard Electron proxy environment.
	TargetPolicyDefault TargetPolicy = "default"
	// TargetPolicyCodex applies the Codex-specific CA and SSL environment.
	TargetPolicyCodex TargetPolicy = "codex"
)

// UpdaterKind identifies the declarative updater kind from config.toml.
type UpdaterKind string

const (
	// UpdaterKindCursorManifest selects the Cursor JSON manifest endpoint.
	UpdaterKindCursorManifest UpdaterKind = "cursor_manifest"
	// UpdaterKindSparkleAppcast selects a Sparkle XML appcast.
	UpdaterKindSparkleAppcast UpdaterKind = "sparkle_appcast"
	// UpdaterKindClaudeSquirrel selects Claude's Squirrel JSON endpoint.
	UpdaterKindClaudeSquirrel UpdaterKind = "claude_squirrel"
)

// SigningConfig configures local codesigning identity metadata.
type SigningConfig struct {
	Identity string `toml:"identity"`
	TeamID   string `toml:"team_id"`
}

// ProxyConfig configures the Clyde MITM proxy settings the harnesses use.
type ProxyConfig struct {
	Host                   string `toml:"host"`
	Port                   int    `toml:"port"`
	CACertificate          string `toml:"ca_certificate"`
	NoProxy                string `toml:"no_proxy"`
	LaunchWorkingDirectory string `toml:"launch_working_directory"`
}

// CLIConfig configures desktop-via-clyde managed CLI surfaces.
type CLIConfig struct {
	Codex  CodexCLIConfig  `toml:"codex"`
	Claude ClaudeCLIConfig `toml:"claude"`
}

// CodexCLIConfig configures the managed Codex CLI build and install defaults.
type CodexCLIConfig struct {
	SourceRef    string `toml:"source_ref"`
	BuildMode    string `toml:"build_mode"`
	InstallDir   string `toml:"install_dir"`
	CodexHome    string `toml:"codex_home"`
	UseSccache   bool   `toml:"use_sccache"`
	ForceRebuild bool   `toml:"force_rebuild"`
}

// ClaudeCLIConfig holds placeholder knobs for future Claude CLI support.
type ClaudeCLIConfig struct {
	Placeholder bool `toml:"placeholder"`
}

// AppsConfig holds the declarative app target configuration.
type AppsConfig struct {
	Cursor AppConfig `toml:"cursor"`
	Codex  AppConfig `toml:"codex"`
	Claude AppConfig `toml:"claude"`
}

// AppConfig configures one patchable Electron desktop application.
type AppConfig struct {
	AppPath                  string              `toml:"app_path"`
	BundleID                 string              `toml:"bundle_id"`
	ExecName                 string              `toml:"exec_name"`
	KeychainServices         []string            `toml:"keychain_services"`
	TargetPolicy             TargetPolicy        `toml:"target_policy"`
	NestedSignPaths          []string            `toml:"nested_sign_paths"`
	PreservedNestedCodePaths []string            `toml:"preserved_nested_code_paths"`
	Entitlements             EntitlementsConfig  `toml:"entitlements"`
	Updater                  UpdaterConfig       `toml:"updater"`
	ComputerUse              ComputerUseConfig   `toml:"computer_use"`
	BundledCLITee            BundledCLITeeConfig `toml:"bundled_cli_tee"`
}

// EntitlementsConfig declares strip and required boolean entitlement lists.
type EntitlementsConfig struct {
	Strip           []string `toml:"strip"`
	RequiredBoolean []string `toml:"required_boolean"`
}

// BundledCLITeeConfig configures the Claude Desktop bundled CLI tee selector.
type BundledCLITeeConfig struct {
	VersionDir     string `toml:"version_dir"`
	BundledCLIPath string `toml:"bundled_cli_path"`
}

// UpdaterConfig configures one declarative app updater.
type UpdaterConfig struct {
	Kind              UpdaterKind      `toml:"kind"`
	URL               string           `toml:"url"`
	Platform          string           `toml:"platform"`
	Product           string           `toml:"product"`
	SparklePublicKey  string           `toml:"sparkle_public_key"`
	DeviceIDParamName string           `toml:"device_id_param_name"`
	DefaultChannel    string           `toml:"default_channel"`
	Channels          []UpdaterChannel `toml:"-"`
}

// UpdaterChannel declares one named updater channel.
type UpdaterChannel struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

// ComputerUseConfig configures the Codex Computer Use repair surfaces.
type ComputerUseConfig struct {
	HostAppPath           string                  `toml:"host_app_path"`
	BundledAppPath        string                  `toml:"bundled_app_path"`
	AppPathFromHome       string                  `toml:"app_path_from_home"`
	CacheAppGlobsFromHome []string                `toml:"cache_app_globs_from_home"`
	AuthPluginPath        string                  `toml:"auth_plugin_path"`
	AuthPluginExecutable  string                  `toml:"auth_plugin_executable"`
	UpstreamTrustedTeamID string                  `toml:"upstream_trusted_team_id"`
	TeamPatchBinaries     []string                `toml:"team_patch_binaries"`
	TeamRequirementPlists []string                `toml:"team_requirement_plists"`
	SignTargets           []ComputerUseSignTarget `toml:"sign_targets"`
}

// ComputerUseSignTarget declares one helper bundle or binary to re-sign.
type ComputerUseSignTarget struct {
	Path         string             `toml:"path"`
	Entitlements EntitlementsConfig `toml:"entitlements"`
}

var (
	currentMu     sync.RWMutex
	currentConfig *Config
)

// Current returns the active runtime config, or the compiled defaults when no
// explicit config has been installed for the current process.
func Current() *Config {
	currentMu.RLock()
	defer currentMu.RUnlock()
	if currentConfig == nil {
		return Default()
	}
	return cloneConfig(currentConfig)
}

// SetCurrent installs the active runtime config for the current process.
func SetCurrent(cfg *Config) {
	currentMu.Lock()
	defer currentMu.Unlock()
	if cfg == nil {
		currentConfig = nil
		return
	}
	currentConfig = cloneConfig(cfg)
}

// Default returns the compiled fallback config used before XDG config is
// loaded and by tests that do not install a temporary config file.
func Default() *Config {
	return &Config{
		Signing: defaultSigningConfig(),
		Proxy:   defaultProxyConfig(),
		CLI:     defaultCLIConfig(),
		Apps:    defaultAppsConfig(),
	}
}

func defaultSigningConfig() SigningConfig {
	return SigningConfig{
		Identity: "Developer ID Application: Alex Goodkind (H3BMXM4W7H)",
		TeamID:   "H3BMXM4W7H",
	}
}

func defaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		Host:                   "::1",
		Port:                   48723,
		CACertificate:          StateRoot() + "/mitm/ca/clyde-mitm-ca.crt",
		NoProxy:                "localhost,127.0.0.1,::1,[::1]",
		LaunchWorkingDirectory: homeRelativeRoot(""),
	}
}

func defaultCLIConfig() CLIConfig {
	return CLIConfig{
		Codex: CodexCLIConfig{
			SourceRef:    "origin/main",
			BuildMode:    "local-fast",
			InstallDir:   homeRelativeRoot(filepath.Join(".local", "bin")),
			CodexHome:    homeRelativeRoot(".codex"),
			UseSccache:   true,
			ForceRebuild: false,
		},
		Claude: ClaudeCLIConfig{
			Placeholder: true,
		},
	}
}

func defaultAppsConfig() AppsConfig {
	return AppsConfig{
		Cursor: defaultCursorAppConfig(),
		Codex:  defaultCodexAppConfig(),
		Claude: defaultClaudeAppConfig(),
	}
}

func defaultCursorAppConfig() AppConfig {
	return AppConfig{
		AppPath:                  "/Applications/Cursor.app",
		BundleID:                 "com.todesktop.230313mzl4w4u92",
		ExecName:                 "Cursor",
		KeychainServices:         []string{"Cursor Safe Storage"},
		TargetPolicy:             TargetPolicyDefault,
		NestedSignPaths:          nil,
		PreservedNestedCodePaths: nil,
		Entitlements: EntitlementsConfig{
			Strip: nil,
			RequiredBoolean: []string{
				"com.apple.security.automation.apple-events",
				"com.apple.security.cs.disable-library-validation",
			},
		},
		Updater: UpdaterConfig{
			Kind:              UpdaterKindCursorManifest,
			URL:               "",
			Platform:          "darwin-arm64",
			Product:           "cursor",
			SparklePublicKey:  "",
			DeviceIDParamName: "",
			DefaultChannel:    "dev",
			Channels: []UpdaterChannel{
				{Name: "stable", URL: ""},
				{Name: "dev", URL: ""},
			},
		},
		ComputerUse: defaultEmptyComputerUseConfig(),
		BundledCLITee: BundledCLITeeConfig{
			VersionDir:     "",
			BundledCLIPath: "",
		},
	}
}

func defaultCodexAppConfig() AppConfig {
	return AppConfig{
		AppPath:          "/Applications/Codex.app",
		BundleID:         "com.openai.codex",
		ExecName:         "Codex",
		KeychainServices: []string{"Codex Safe Storage", "Codex Auth", "Codex MCP Credentials"},
		TargetPolicy:     TargetPolicyCodex,
		NestedSignPaths: []string{
			"Contents/Resources/codex",
			"Contents/Resources/codex_chronicle",
			"Contents/Resources/node",
			"Contents/Resources/node_repl",
			"Contents/Resources/rg",
			"Contents/Resources/native/bare-modifier-monitor",
			"Contents/Resources/native/browser-use-peer-authorization.node",
			"Contents/Resources/native/devicecheck.node",
			"Contents/Resources/native/launch-services-helper",
			"Contents/Resources/native/remote-control-device-key.node",
			"Contents/Resources/native/sky.node",
			"Contents/Resources/native/sparkle.node",
			"Contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Downloader.xpc",
			"Contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Installer.xpc",
			"Contents/Frameworks/Sparkle.framework/Versions/B/Updater.app",
			"Contents/Frameworks/Sparkle.framework/Versions/B/Autoupdate",
			"Contents/Frameworks/Sparkle.framework",
		},
		PreservedNestedCodePaths: nil,
		Entitlements: EntitlementsConfig{
			Strip: []string{
				"com.apple.application-identifier",
				"com.apple.developer.team-identifier",
				"keychain-access-groups",
			},
			RequiredBoolean: []string{
				"com.apple.security.automation.apple-events",
				"com.apple.security.cs.disable-library-validation",
			},
		},
		Updater:       defaultCodexUpdaterConfig(),
		ComputerUse:   defaultCodexComputerUseConfig(),
		BundledCLITee: defaultEmptyBundledCLITeeConfig(),
	}
}

func defaultCodexUpdaterConfig() UpdaterConfig {
	return UpdaterConfig{
		Kind:              UpdaterKindSparkleAppcast,
		URL:               "",
		Platform:          "",
		Product:           "",
		SparklePublicKey:  "rhcBvttuqDFriyNqwTQJR3L4UT1WjIK4QxtwtwusVic=",
		DeviceIDParamName: "",
		DefaultChannel:    "beta",
		Channels: []UpdaterChannel{
			{Name: "stable", URL: "https://persistent.oaistatic.com/codex-app-prod/appcast.xml"},
			{Name: "beta", URL: "https://persistent.oaistatic.com/codex-app-beta/appcast.xml"},
		},
	}
}

func defaultCodexComputerUseConfig() ComputerUseConfig {
	return ComputerUseConfig{
		HostAppPath:           "/Applications/Codex.app",
		BundledAppPath:        "Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app",
		AppPathFromHome:       ".codex/computer-use/Codex Computer Use.app",
		CacheAppGlobsFromHome: []string{".codex/plugins/cache/openai-bundled/computer-use/*/Codex Computer Use.app"},
		AuthPluginPath:        "/Library/Security/SecurityAgentPlugins/CodexComputerUseAuthorizationPlugin.bundle",
		AuthPluginExecutable:  "Contents/MacOS/CodexComputerUseAuthorizationPlugin",
		UpstreamTrustedTeamID: "2DC432GLL2",
		TeamPatchBinaries: []string{
			"Contents/MacOS/SkyComputerUseService",
			"Contents/SharedSupport/CUALockScreenGuardian.app/Contents/MacOS/CUALockScreenGuardian",
		},
		TeamRequirementPlists: []string{
			"Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement",
			"Contents/SharedSupport/CUALockScreenGuardian.app/Contents/Resources/CUALockScreenGuardian_Parent.coderequirement",
		},
		SignTargets: []ComputerUseSignTarget{
			{
				Path: "Contents/SharedSupport/Codex Computer Use Installer.app",
				Entitlements: EntitlementsConfig{
					Strip:           nil,
					RequiredBoolean: nil,
				},
			},
			{
				Path: "Contents/SharedSupport/SkyComputerUseClient.app",
				Entitlements: EntitlementsConfig{
					Strip:           []string{"com.apple.security.application-groups"},
					RequiredBoolean: []string{"com.apple.security.automation.apple-events"},
				},
			},
			{
				Path: "Contents/SharedSupport/CUALockScreenGuardian.app",
				Entitlements: EntitlementsConfig{
					Strip:           []string{"com.apple.security.application-groups"},
					RequiredBoolean: nil,
				},
			},
			{
				Path: ".",
				Entitlements: EntitlementsConfig{
					Strip: []string{"com.apple.security.application-groups"},
					RequiredBoolean: []string{
						"com.apple.security.automation.apple-events",
						"com.apple.security.device.audio-input",
					},
				},
			},
		},
	}
}

func defaultClaudeAppConfig() AppConfig {
	return AppConfig{
		AppPath:                  "/Applications/Claude.app",
		BundleID:                 "com.anthropic.claudefordesktop",
		ExecName:                 "Claude",
		KeychainServices:         []string{"Claude Safe Storage"},
		TargetPolicy:             TargetPolicyDefault,
		NestedSignPaths:          nil,
		PreservedNestedCodePaths: []string{"Contents/Frameworks/Squirrel.framework"},
		Entitlements: EntitlementsConfig{
			Strip: []string{
				"com.apple.application-identifier",
				"com.apple.developer.team-identifier",
				"keychain-access-groups",
			},
			RequiredBoolean: []string{"com.apple.security.cs.disable-library-validation"},
		},
		Updater: UpdaterConfig{
			Kind:              UpdaterKindClaudeSquirrel,
			URL:               "https://api.anthropic.com/api/desktop/darwin/universal/squirrel/update",
			Platform:          "",
			Product:           "",
			SparklePublicKey:  "",
			DeviceIDParamName: "device_id",
			DefaultChannel:    "",
			Channels:          nil,
		},
		ComputerUse:   defaultEmptyComputerUseConfig(),
		BundledCLITee: defaultEmptyBundledCLITeeConfig(),
	}
}

func defaultEmptyComputerUseConfig() ComputerUseConfig {
	return ComputerUseConfig{
		HostAppPath:           "",
		BundledAppPath:        "",
		AppPathFromHome:       "",
		CacheAppGlobsFromHome: nil,
		AuthPluginPath:        "",
		AuthPluginExecutable:  "",
		UpstreamTrustedTeamID: "",
		TeamPatchBinaries:     nil,
		TeamRequirementPlists: nil,
		SignTargets:           nil,
	}
}

func defaultEmptyBundledCLITeeConfig() BundledCLITeeConfig {
	return BundledCLITeeConfig{
		VersionDir:     "",
		BundledCLIPath: "",
	}
}

func cloneConfig(cfg *Config) *Config {
	if cfg == nil {
		return Default()
	}
	cloned := *cfg
	cloned.Apps.Cursor = cloneAppConfig(cfg.Apps.Cursor)
	cloned.Apps.Codex = cloneAppConfig(cfg.Apps.Codex)
	cloned.Apps.Claude = cloneAppConfig(cfg.Apps.Claude)
	return &cloned
}

func cloneAppConfig(app AppConfig) AppConfig {
	cloned := app
	cloned.KeychainServices = cloneStrings(app.KeychainServices)
	cloned.NestedSignPaths = cloneStrings(app.NestedSignPaths)
	cloned.PreservedNestedCodePaths = cloneStrings(app.PreservedNestedCodePaths)
	cloned.Entitlements = EntitlementsConfig{
		Strip:           cloneStrings(app.Entitlements.Strip),
		RequiredBoolean: cloneStrings(app.Entitlements.RequiredBoolean),
	}
	cloned.Updater = cloneUpdater(app.Updater)
	cloned.ComputerUse = cloneComputerUse(app.ComputerUse)
	cloned.BundledCLITee = BundledCLITeeConfig{
		VersionDir:     app.BundledCLITee.VersionDir,
		BundledCLIPath: app.BundledCLITee.BundledCLIPath,
	}
	return cloned
}

func cloneUpdater(updater UpdaterConfig) UpdaterConfig {
	cloned := updater
	cloned.Channels = append([]UpdaterChannel(nil), updater.Channels...)
	return cloned
}

func cloneComputerUse(policy ComputerUseConfig) ComputerUseConfig {
	cloned := policy
	cloned.CacheAppGlobsFromHome = cloneStrings(policy.CacheAppGlobsFromHome)
	cloned.TeamPatchBinaries = cloneStrings(policy.TeamPatchBinaries)
	cloned.TeamRequirementPlists = cloneStrings(policy.TeamRequirementPlists)
	cloned.SignTargets = make([]ComputerUseSignTarget, 0, len(policy.SignTargets))
	for _, target := range policy.SignTargets {
		cloned.SignTargets = append(cloned.SignTargets, ComputerUseSignTarget{
			Path: target.Path,
			Entitlements: EntitlementsConfig{
				Strip:           cloneStrings(target.Entitlements.Strip),
				RequiredBoolean: cloneStrings(target.Entitlements.RequiredBoolean),
			},
		})
	}
	return cloned
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
