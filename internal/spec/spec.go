// Package spec defines the typed configuration and runtime contracts for
// desktop-via-clyde apps, CLIs, launch policies, and capabilities.
package spec

import "goodkind.io/desktop-via-clyde/internal/extensions"

// FlagType identifies one supported command flag type.
type FlagType string

const (
	// FlagTypeString is a string-valued flag.
	FlagTypeString FlagType = "string"
	// FlagTypeBool is a boolean flag.
	FlagTypeBool FlagType = "bool"
)

// UpdaterKind identifies one supported updater protocol.
type UpdaterKind string

const (
	// UpdaterKindHTTPPathJSONManifest fetches a JSON manifest from a path template.
	UpdaterKindHTTPPathJSONManifest UpdaterKind = "http_path_json_manifest"
	// UpdaterKindSparkleAppcast fetches a Sparkle XML appcast.
	UpdaterKindSparkleAppcast UpdaterKind = "sparkle_appcast"
	// UpdaterKindSquirrelJSON fetches a Squirrel-style JSON manifest.
	UpdaterKindSquirrelJSON UpdaterKind = "squirrel_json"
)

// PreflightKind identifies one supported launch preflight check.
type PreflightKind string

const (
	// PreflightKindFileExists checks that a required file exists.
	PreflightKindFileExists PreflightKind = "file_exists"
	// PreflightKindTCPReachable checks that a TCP endpoint is reachable.
	PreflightKindTCPReachable PreflightKind = "tcp_reachable"
)

// DryRunSignal identifies one signal name that dry-run verification may ignore.
type DryRunSignal string

const (
	// DryRunSignalSIGINT ignores SIGINT during dry-run verification.
	DryRunSignalSIGINT DryRunSignal = "SIGINT"
	// DryRunSignalSIGTERM ignores SIGTERM during dry-run verification.
	DryRunSignalSIGTERM DryRunSignal = "SIGTERM"
	// DryRunSignalSIGKILL ignores SIGKILL during dry-run verification.
	DryRunSignalSIGKILL DryRunSignal = "SIGKILL"
)

// Config is the fully typed desktop-via-clyde runtime configuration.
type Config struct {
	Signing SigningSpec        `toml:"signing"`
	Apps    map[string]AppSpec `toml:"apps"`
	CLIs    map[string]CLISpec `toml:"clis"`
}

// SigningSpec configures local codesigning identity metadata.
type SigningSpec struct {
	Identity string `toml:"identity"`
	TeamID   string `toml:"team_id"`
}

// CommandSpec describes one rendered Cobra command surface.
type CommandSpec struct {
	Use     string   `toml:"use"`
	Aliases []string `toml:"aliases"`
	Short   string   `toml:"short"`
	Long    string   `toml:"long"`
	Hidden  bool     `toml:"hidden"`
}

// OperationSpec describes one rendered Cobra subcommand surface.
type OperationSpec struct {
	ID         string     `toml:"-"`
	Use        string     `toml:"use"`
	Aliases    []string   `toml:"aliases"`
	Short      string     `toml:"short"`
	Long       string     `toml:"long"`
	Hidden     bool       `toml:"hidden"`
	Capability string     `toml:"capability"`
	Flags      []FlagSpec `toml:"flags"`
}

// FlagSpec describes one rendered command flag.
type FlagSpec struct {
	Name          string   `toml:"name"`
	Binding       string   `toml:"binding"`
	Type          FlagType `toml:"type"`
	Usage         string   `toml:"usage"`
	DefaultString string   `toml:"default_string"`
	DefaultBool   *bool    `toml:"default_bool"`
	ExpandPath    bool     `toml:"expand_path"`
	Hidden        bool     `toml:"hidden"`
}

// AppSpec configures one patchable desktop application and its command shape.
type AppSpec struct {
	ID                       string                   `toml:"-"`
	Command                  CommandSpec              `toml:"command"`
	Operations               map[string]OperationSpec `toml:"operations"`
	AppPath                  string                   `toml:"app_path"`
	BundleID                 string                   `toml:"bundle_id"`
	BundleIDAliases          []string                 `toml:"bundle_id_aliases"`
	HelperBundleIDs          []string                 `toml:"helper_bundle_ids"`
	ExecName                 string                   `toml:"exec_name"`
	KeychainServices         []string                 `toml:"keychain_services"`
	NestedSignPaths          []string                 `toml:"nested_sign_paths"`
	PreservedNestedCodePaths []string                 `toml:"preserved_nested_code_paths"`
	ProvisioningProfile      string                   `toml:"provisioning_profile"`
	Entitlements             EntitlementsSpec         `toml:"entitlements"`
	DevelopmentSigning       DevelopmentSigningSpec   `toml:"development_signing"`
	Updater                  UpdaterSpec              `toml:"updater"`
	LaunchPolicy             LaunchPolicySpec         `toml:"launch_policy"`
	Extensions               extensions.AppSpec
}

// DevelopmentSigningSpec opts one target into development-profile signing. When
// enabled, the patcher replaces the shim plus Developer ID re-sign with an Apple
// Development signature and an embedded wildcard MAC_APP_DEVELOPMENT provisioning
// profile, which is the only configuration that injects team-restricted
// entitlements (keychain-access-groups, application-identifier) into the running
// process so device-key enrollment (the "-34018" errSecMissingEntitlement
// failure) succeeds. All
// fields default to the zero value, so a target without a [development_signing]
// table stays on the standard shim path. Secrets are referenced by file path
// only; P12PasswordFile points at a file holding the p12 password, never the
// password itself.
type DevelopmentSigningSpec struct {
	Enabled           bool   `toml:"enabled"`
	ProfilePath       string `toml:"profile_path"`
	P12Path           string `toml:"p12_path"`
	P12PasswordFile   string `toml:"p12_password_file"`
	InjectorDylibPath string `toml:"injector_dylib_path"`
	ProxyInjection    bool   `toml:"proxy_injection"`
	// AutoGenerate lets the patch preflight mint the missing development-signing
	// assets through App Store Connect when credentials are present, instead of
	// only warning that they can be generated. It defaults to false so a target
	// never contacts Apple implicitly; the preflight stays non-blocking either way.
	AutoGenerate bool `toml:"auto_generate"`
}

// CLISpec configures one non-app CLI surface and its operations.
type CLISpec struct {
	ID         string                   `toml:"-"`
	Command    CommandSpec              `toml:"command"`
	Operations map[string]OperationSpec `toml:"operations"`
}

// EntitlementsSpec declares strip and required boolean entitlement lists.
type EntitlementsSpec struct {
	Strip           []string `toml:"strip"`
	RequiredBoolean []string `toml:"required_boolean"`
}

// UpdaterSpec configures one declarative app updater.
type UpdaterSpec struct {
	Kind              UpdaterKind      `toml:"kind"`
	URL               string           `toml:"url"`
	URLTemplate       string           `toml:"url_template"`
	UserAgent         string           `toml:"user_agent"`
	Platform          string           `toml:"platform"`
	Product           string           `toml:"product"`
	SparklePublicKey  string           `toml:"sparkle_public_key"`
	DeviceIDParamName string           `toml:"device_id_param_name"`
	DefaultChannel    string           `toml:"default_channel"`
	Channels          []UpdaterChannel `toml:"channels"`
}

// UpdaterChannel declares one named updater channel.
type UpdaterChannel struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

// LaunchPolicySpec declares the installed shim policy data.
type LaunchPolicySpec struct {
	ProxyHost              string          `toml:"proxy_host" json:"proxy_host"`
	ProxyPort              int             `toml:"proxy_port" json:"proxy_port"`
	CACertificate          string          `toml:"ca_certificate" json:"ca_certificate"`
	NoProxy                string          `toml:"no_proxy" json:"no_proxy"`
	LaunchWorkingDirectory string          `toml:"launch_working_directory" json:"launch_working_directory"`
	IgnoreDryRunSignal     DryRunSignal    `toml:"ignore_dry_run_signal" json:"ignore_dry_run_signal,omitempty"`
	Environment            []EnvActionSpec `toml:"environment" json:"environment"`
	Arguments              []ArgActionSpec `toml:"arguments" json:"arguments"`
	Preflights             []PreflightSpec `toml:"preflights" json:"preflights"`
}

// EnvActionSpec declares one environment mutation for the shim policy.
type EnvActionSpec struct {
	Action string `toml:"action" json:"action"`
	Key    string `toml:"key" json:"key"`
	Value  string `toml:"value" json:"value"`
}

// ArgActionSpec declares one argv mutation for the shim policy.
type ArgActionSpec struct {
	Action string `toml:"action" json:"action"`
	Value  string `toml:"value" json:"value"`
}

// PreflightSpec declares one preflight requirement for the shim policy.
type PreflightSpec struct {
	Kind    PreflightKind `toml:"kind" json:"kind"`
	Path    string        `toml:"path" json:"path"`
	Host    string        `toml:"host" json:"host"`
	Port    int           `toml:"port" json:"port"`
	Timeout int           `toml:"timeout_ms" json:"timeout_ms"`
}

// Clone returns a deep copy of the config.
func (cfg *Config) Clone() *Config {
	if cfg == nil {
		return nil
	}
	cloned := *cfg
	cloned.Apps = make(map[string]AppSpec, len(cfg.Apps))
	for id, app := range cfg.Apps {
		cloned.Apps[id] = cloneApp(app)
	}
	cloned.CLIs = make(map[string]CLISpec, len(cfg.CLIs))
	for id, cli := range cfg.CLIs {
		cloned.CLIs[id] = cloneCLI(cli)
	}
	return &cloned
}

func cloneApp(app AppSpec) AppSpec {
	cloned := app
	cloned.Command = cloneCommand(app.Command)
	cloned.Operations = cloneOperations(app.Operations)
	cloned.KeychainServices = cloneStrings(app.KeychainServices)
	cloned.BundleIDAliases = cloneStrings(app.BundleIDAliases)
	cloned.HelperBundleIDs = cloneStrings(app.HelperBundleIDs)
	cloned.NestedSignPaths = cloneStrings(app.NestedSignPaths)
	cloned.PreservedNestedCodePaths = cloneStrings(app.PreservedNestedCodePaths)
	cloned.Entitlements = cloneEntitlements(app.Entitlements)
	cloned.Updater = cloneUpdater(app.Updater)
	cloned.Extensions = extensions.CloneAppSpec(app.Extensions)
	cloned.LaunchPolicy = cloneLaunchPolicy(app.LaunchPolicy)
	return cloned
}

func cloneCLI(cli CLISpec) CLISpec {
	cloned := cli
	cloned.Command = cloneCommand(cli.Command)
	cloned.Operations = cloneOperations(cli.Operations)
	return cloned
}

func cloneCommand(command CommandSpec) CommandSpec {
	cloned := command
	cloned.Aliases = cloneStrings(command.Aliases)
	return cloned
}

func cloneOperations(operations map[string]OperationSpec) map[string]OperationSpec {
	if len(operations) == 0 {
		return nil
	}
	cloned := make(map[string]OperationSpec, len(operations))
	for id, operation := range operations {
		item := operation
		item.Aliases = cloneStrings(operation.Aliases)
		item.Flags = cloneFlags(operation.Flags)
		cloned[id] = item
	}
	return cloned
}

func cloneFlags(flags []FlagSpec) []FlagSpec {
	if len(flags) == 0 {
		return nil
	}
	cloned := make([]FlagSpec, 0, len(flags))
	for _, flag := range flags {
		item := flag
		if flag.DefaultBool != nil {
			value := *flag.DefaultBool
			item.DefaultBool = &value
		}
		cloned = append(cloned, item)
	}
	return cloned
}

func cloneEntitlements(entitlements EntitlementsSpec) EntitlementsSpec {
	return EntitlementsSpec{
		Strip:           cloneStrings(entitlements.Strip),
		RequiredBoolean: cloneStrings(entitlements.RequiredBoolean),
	}
}

func cloneUpdater(updater UpdaterSpec) UpdaterSpec {
	cloned := updater
	cloned.Channels = append([]UpdaterChannel(nil), updater.Channels...)
	return cloned
}

func cloneLaunchPolicy(policy LaunchPolicySpec) LaunchPolicySpec {
	cloned := policy
	cloned.Environment = append([]EnvActionSpec(nil), policy.Environment...)
	cloned.Arguments = append([]ArgActionSpec(nil), policy.Arguments...)
	cloned.Preflights = append([]PreflightSpec(nil), policy.Preflights...)
	return cloned
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
