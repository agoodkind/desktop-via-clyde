package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/spec"
)

var configLog = slog.With("component", "desktop-via-clyde", "subcomponent", "config")

// LoadPath reads and validates a config file at an explicit path.
func LoadPath(path string) (*spec.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		configLog.Error("config.load.read_failed", "path", path, "err", err)
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var cfg spec.Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		configLog.Error("config.load.parse_failed", "path", path, "err", err)
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := normalizeAndValidate(&cfg); err != nil {
		configLog.Error("config.load.validate_failed", "path", path, "err", err)
		return nil, fmt.Errorf("invalid %s: %w", path, err)
	}
	return cfg.Clone(), nil
}

func normalizeAndValidate(cfg *spec.Config) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}

	cfg.Signing.Identity = strings.TrimSpace(cfg.Signing.Identity)
	cfg.Signing.TeamID = strings.TrimSpace(cfg.Signing.TeamID)
	if cfg.Signing.Identity == "" {
		return fmt.Errorf("signing.identity is required")
	}
	if cfg.Signing.TeamID == "" {
		return fmt.Errorf("signing.team_id is required")
	}

	if len(cfg.Apps) == 0 {
		return fmt.Errorf("at least one app must be declared")
	}
	if len(cfg.CLIs) == 0 {
		return fmt.Errorf("at least one cli must be declared")
	}

	appIDs := sortedAppKeys(cfg.Apps)
	for _, id := range appIDs {
		app := cfg.Apps[id]
		if err := normalizeAndValidateApp(id, &app); err != nil {
			return err
		}
		cfg.Apps[id] = app
	}

	cliIDs := sortedCLIKeys(cfg.CLIs)
	for _, id := range cliIDs {
		cli := cfg.CLIs[id]
		if err := normalizeAndValidateCLI(id, &cli); err != nil {
			return err
		}
		cfg.CLIs[id] = cli
	}

	return nil
}

func normalizeAndValidateApp(id string, app *spec.AppSpec) error {
	app.ID = strings.TrimSpace(id)
	if app.ID == "" {
		return fmt.Errorf("apps contains an empty id")
	}
	normalizeCommand(&app.Command)
	app.Command.Use = strings.TrimSpace(app.Command.Use)
	if app.Command.Use == "" {
		return fmt.Errorf("apps.%s.command.use is required", app.ID)
	}
	if err := normalizeAndValidateOperations("apps."+app.ID+".operations", app.Operations); err != nil {
		return err
	}

	app.AppPath = cleanExpandedPath(strings.TrimSpace(app.AppPath))
	app.BundleID = strings.TrimSpace(app.BundleID)
	app.ExecName = strings.TrimSpace(app.ExecName)
	app.KeychainServices = normalizeStringSlice(app.KeychainServices)
	app.NestedSignPaths = normalizeStringSlice(app.NestedSignPaths)
	app.PreservedNestedCodePaths = normalizeStringSlice(app.PreservedNestedCodePaths)
	app.Entitlements.Strip = normalizeStringSlice(app.Entitlements.Strip)
	app.Entitlements.RequiredBoolean = normalizeStringSlice(app.Entitlements.RequiredBoolean)
	app.OriginalDRBootstrapCapability = strings.TrimSpace(app.OriginalDRBootstrapCapability)

	if app.AppPath == "" {
		return fmt.Errorf("apps.%s.app_path is required", app.ID)
	}
	if app.BundleID == "" {
		return fmt.Errorf("apps.%s.bundle_id is required", app.ID)
	}
	if app.ExecName == "" {
		return fmt.Errorf("apps.%s.exec_name is required", app.ID)
	}
	if err := normalizeAndValidateUpdater("apps."+app.ID+".updater", &app.Updater); err != nil {
		return err
	}
	if app.OriginalDRBootstrapCapability != "" && !catalog.HasBootstrapCapability(app.OriginalDRBootstrapCapability) {
		return fmt.Errorf("apps.%s.original_dr_bootstrap_capability %q is unknown", app.ID, app.OriginalDRBootstrapCapability)
	}
	if app.ComputerUse != nil {
		if err := normalizeAndValidateComputerUse("apps."+app.ID+".computer_use", app.ComputerUse); err != nil {
			return err
		}
	}
	if app.BundledCLITee != nil {
		if err := normalizeAndValidateBundledCLITee("apps."+app.ID+".bundled_cli_tee", app.BundledCLITee); err != nil {
			return err
		}
	}
	if err := normalizeAndValidateLaunchPolicy("apps."+app.ID+".launch_policy", &app.LaunchPolicy); err != nil {
		return err
	}
	return nil
}

func normalizeAndValidateCLI(id string, cli *spec.CLISpec) error {
	cli.ID = strings.TrimSpace(id)
	if cli.ID == "" {
		return fmt.Errorf("clis contains an empty id")
	}
	normalizeCommand(&cli.Command)
	if strings.TrimSpace(cli.Command.Use) == "" {
		return fmt.Errorf("clis.%s.command.use is required", cli.ID)
	}
	return normalizeAndValidateOperations("clis."+cli.ID+".operations", cli.Operations)
}

func normalizeAndValidateOperations(path string, operations map[string]spec.OperationSpec) error {
	if len(operations) == 0 {
		return fmt.Errorf("%s must declare at least one operation", path)
	}
	for _, id := range sortedOperationKeys(operations) {
		operation := operations[id]
		operation.ID = strings.TrimSpace(id)
		operation.Use = strings.TrimSpace(operation.Use)
		operation.Short = strings.TrimSpace(operation.Short)
		operation.Long = strings.TrimSpace(operation.Long)
		operation.Aliases = normalizeStringSlice(operation.Aliases)
		operation.Capability = strings.TrimSpace(operation.Capability)
		if operation.Use == "" {
			return fmt.Errorf("%s.%s.use is required", path, id)
		}
		if operation.Capability == "" {
			return fmt.Errorf("%s.%s.capability is required", path, id)
		}
		if !catalog.HasOperationCapability(operation.Capability) {
			return fmt.Errorf("%s.%s.capability %q is unknown", path, id, operation.Capability)
		}
		normalizedFlags := make([]spec.FlagSpec, 0, len(operation.Flags))
		seenFlags := map[string]bool{}
		seenBindings := map[string]bool{}
		for index, flag := range operation.Flags {
			normalized, err := normalizeAndValidateFlag(path+"."+id, index, flag)
			if err != nil {
				return err
			}
			if seenFlags[normalized.Name] {
				return fmt.Errorf("%s.%s.flags contains duplicate %q", path, id, normalized.Name)
			}
			seenFlags[normalized.Name] = true
			if seenBindings[normalized.Binding] {
				return fmt.Errorf("%s.%s.flags contains duplicate binding %q", path, id, normalized.Binding)
			}
			seenBindings[normalized.Binding] = true
			normalizedFlags = append(normalizedFlags, normalized)
		}
		operation.Flags = normalizedFlags
		operations[id] = operation
	}
	return nil
}

func normalizeAndValidateFlag(path string, index int, flag spec.FlagSpec) (spec.FlagSpec, error) {
	flag.Name = strings.TrimSpace(flag.Name)
	flag.Binding = strings.TrimSpace(flag.Binding)
	flag.Type = spec.FlagType(strings.TrimSpace(string(flag.Type)))
	flag.Usage = strings.TrimSpace(flag.Usage)
	if flag.Name == "" {
		return spec.FlagSpec{}, fmt.Errorf("%s.flags[%d].name is required", path, index)
	}
	if flag.Binding == "" {
		flag.Binding = flag.Name
	}
	switch flag.Type {
	case spec.FlagTypeBool:
		if flag.DefaultBool == nil {
			value := false
			flag.DefaultBool = &value
		}
		flag.DefaultString = ""
	case spec.FlagTypeString:
		if flag.DefaultBool != nil {
			return spec.FlagSpec{}, fmt.Errorf("%s.flags[%d].default_bool is not supported for string flags", path, index)
		}
		flag.DefaultString = strings.TrimSpace(flag.DefaultString)
		if flag.ExpandPath {
			flag.DefaultString = cleanExpandedPath(renderPathTokens(flag.DefaultString))
		}
	default:
		return spec.FlagSpec{}, fmt.Errorf("%s.flags[%d].type must be one of bool|string", path, index)
	}
	return flag, nil
}

func renderPathTokens(value string) string {
	replacer := strings.NewReplacer(
		"{cache_root}", CacheRoot(),
		"{config_root}", filepath.Dir(Path()),
		"{home}", homeRelativeRoot(""),
		"{state_root}", StateRoot(),
	)
	return replacer.Replace(value)
}

func normalizeAndValidateUpdater(path string, updater *spec.UpdaterSpec) error {
	updater.Kind = spec.UpdaterKind(strings.TrimSpace(string(updater.Kind)))
	updater.URL = strings.TrimSpace(updater.URL)
	updater.URLTemplate = strings.TrimSpace(updater.URLTemplate)
	updater.UserAgent = strings.TrimSpace(updater.UserAgent)
	updater.Platform = strings.TrimSpace(updater.Platform)
	updater.Product = strings.TrimSpace(updater.Product)
	updater.SparklePublicKey = strings.TrimSpace(updater.SparklePublicKey)
	updater.DeviceIDParamName = strings.TrimSpace(updater.DeviceIDParamName)
	updater.DefaultChannel = strings.TrimSpace(updater.DefaultChannel)

	switch updater.Kind {
	case spec.UpdaterKindHTTPPathJSONManifest, spec.UpdaterKindSparkleAppcast, spec.UpdaterKindSquirrelJSON:
	default:
		return fmt.Errorf("%s.kind must be one of http_path_json_manifest|sparkle_appcast|squirrel_json", path)
	}

	channels := make([]spec.UpdaterChannel, 0, len(updater.Channels))
	seen := map[string]bool{}
	for _, channel := range updater.Channels {
		channel.Name = strings.TrimSpace(channel.Name)
		channel.URL = strings.TrimSpace(channel.URL)
		if channel.Name == "" {
			return fmt.Errorf("%s.channels contains a channel without name", path)
		}
		if seen[channel.Name] {
			return fmt.Errorf("%s.channels.%s is duplicated", path, channel.Name)
		}
		seen[channel.Name] = true
		channels = append(channels, channel)
	}
	updater.Channels = channels

	switch updater.Kind {
	case spec.UpdaterKindHTTPPathJSONManifest:
		if err := validateHTTPPathJSONManifestUpdater(path, updater); err != nil {
			return err
		}
	case spec.UpdaterKindSparkleAppcast:
		if err := validateSparkleUpdater(path, updater); err != nil {
			return err
		}
	case spec.UpdaterKindSquirrelJSON:
		if err := validateSquirrelUpdater(path, updater); err != nil {
			return err
		}
	}

	if updater.DefaultChannel != "" && !seen[updater.DefaultChannel] {
		return fmt.Errorf("%s.default_channel %q is not declared in channels", path, updater.DefaultChannel)
	}
	return nil
}

func normalizeAndValidateComputerUse(path string, policy *spec.ComputerUseSpec) error {
	policy.HostAppPath = cleanExpandedPath(strings.TrimSpace(policy.HostAppPath))
	policy.BundledAppPath = strings.TrimSpace(policy.BundledAppPath)
	policy.AppPathFromHome = strings.TrimSpace(policy.AppPathFromHome)
	policy.CacheAppGlobsFromHome = normalizeStringSlice(policy.CacheAppGlobsFromHome)
	policy.AuthPluginPath = cleanExpandedPath(strings.TrimSpace(policy.AuthPluginPath))
	policy.AuthPluginExecutable = strings.TrimSpace(policy.AuthPluginExecutable)
	policy.UpstreamTrustedTeamID = strings.TrimSpace(policy.UpstreamTrustedTeamID)
	policy.TeamPatchBinaries = normalizeStringSlice(policy.TeamPatchBinaries)
	policy.TeamRequirementPlists = normalizeStringSlice(policy.TeamRequirementPlists)

	if policy.HostAppPath == "" {
		return fmt.Errorf("%s.host_app_path is required", path)
	}
	if policy.BundledAppPath == "" {
		return fmt.Errorf("%s.bundled_app_path is required", path)
	}
	if policy.AppPathFromHome == "" {
		return fmt.Errorf("%s.app_path_from_home is required", path)
	}
	if policy.AuthPluginPath == "" {
		return fmt.Errorf("%s.auth_plugin_path is required", path)
	}
	if !filepath.IsAbs(policy.AuthPluginPath) {
		return fmt.Errorf("%s.auth_plugin_path must be an absolute path", path)
	}
	if policy.AuthPluginExecutable == "" {
		return fmt.Errorf("%s.auth_plugin_executable is required", path)
	}
	if policy.UpstreamTrustedTeamID == "" {
		return fmt.Errorf("%s.upstream_trusted_team_id is required", path)
	}
	normalizedTargets := make([]spec.ComputerUseSignTarget, 0, len(policy.SignTargets))
	for index, target := range policy.SignTargets {
		target.Path = strings.TrimSpace(target.Path)
		target.Entitlements.Strip = normalizeStringSlice(target.Entitlements.Strip)
		target.Entitlements.RequiredBoolean = normalizeStringSlice(target.Entitlements.RequiredBoolean)
		if target.Path == "" {
			return fmt.Errorf("%s.sign_targets[%d].path is required", path, index)
		}
		normalizedTargets = append(normalizedTargets, target)
	}
	policy.SignTargets = normalizedTargets
	return nil
}

func normalizeAndValidateBundledCLITee(path string, tee *spec.BundledCLITeeSpec) error {
	tee.Capability = strings.TrimSpace(tee.Capability)
	tee.AppSupportDir = cleanExpandedPath(renderPathTokens(strings.TrimSpace(tee.AppSupportDir)))
	tee.VersionDir = strings.TrimSpace(tee.VersionDir)
	tee.BundledCLIRel = strings.TrimSpace(tee.BundledCLIRel)
	tee.BundledCLIPath = cleanExpandedPath(renderPathTokens(strings.TrimSpace(tee.BundledCLIPath)))
	tee.TerminateProcessNames = normalizeStringSlice(tee.TerminateProcessNames)
	tee.TerminateProcessPatterns = normalizeStringSlice(tee.TerminateProcessPatterns)
	tee.CompletionSteps = normalizeStringSlice(tee.CompletionSteps)

	if tee.Capability == "" {
		return fmt.Errorf("%s.capability is required", path)
	}
	if !catalog.HasPatchHookCapability(tee.Capability) {
		return fmt.Errorf("%s.capability %q is unknown", path, tee.Capability)
	}
	if err := validateBundledCLITeePaths(path, tee); err != nil {
		return err
	}
	return nil
}

func validateBundledCLITeePaths(path string, tee *spec.BundledCLITeeSpec) error {
	if tee.BundledCLIPath == "" && tee.AppSupportDir == "" {
		return fmt.Errorf("%s.app_support_dir is required when bundled_cli_path is empty", path)
	}
	if tee.BundledCLIPath == "" && tee.BundledCLIRel == "" {
		return fmt.Errorf("%s.bundled_cli_rel is required when bundled_cli_path is empty", path)
	}
	if tee.AppSupportDir != "" && !filepath.IsAbs(tee.AppSupportDir) {
		return fmt.Errorf("%s.app_support_dir must be an absolute path", path)
	}
	if tee.BundledCLIPath != "" && !filepath.IsAbs(tee.BundledCLIPath) {
		return fmt.Errorf("%s.bundled_cli_path must be an absolute path", path)
	}
	return nil
}

func normalizeAndValidateLaunchPolicy(path string, policy *spec.LaunchPolicySpec) error {
	policy.ProxyHost = strings.TrimSpace(policy.ProxyHost)
	policy.CACertificate = cleanExpandedPath(strings.TrimSpace(policy.CACertificate))
	policy.NoProxy = strings.TrimSpace(policy.NoProxy)
	policy.LaunchWorkingDirectory = cleanExpandedPath(strings.TrimSpace(policy.LaunchWorkingDirectory))
	policy.IgnoreDryRunSignal = spec.DryRunSignal(strings.TrimSpace(string(policy.IgnoreDryRunSignal)))

	if policy.ProxyHost == "" {
		return fmt.Errorf("%s.proxy_host is required", path)
	}
	if policy.ProxyPort < 1 || policy.ProxyPort > 65535 {
		return fmt.Errorf("%s.proxy_port must be between 1 and 65535", path)
	}
	if policy.CACertificate == "" {
		return fmt.Errorf("%s.ca_certificate is required", path)
	}
	if !filepath.IsAbs(policy.CACertificate) {
		return fmt.Errorf("%s.ca_certificate must be an absolute path", path)
	}
	if policy.NoProxy == "" {
		return fmt.Errorf("%s.no_proxy is required", path)
	}
	if policy.LaunchWorkingDirectory == "" {
		return fmt.Errorf("%s.launch_working_directory is required", path)
	}
	if !filepath.IsAbs(policy.LaunchWorkingDirectory) {
		return fmt.Errorf("%s.launch_working_directory must be an absolute path", path)
	}
	if err := normalizeLaunchPolicyEnvironment(path, policy); err != nil {
		return err
	}
	if err := normalizeLaunchPolicyArguments(path, policy); err != nil {
		return err
	}
	if err := normalizeLaunchPolicyPreflights(path, policy); err != nil {
		return err
	}
	return nil
}

func normalizeCommand(command *spec.CommandSpec) {
	command.Use = strings.TrimSpace(command.Use)
	command.Short = strings.TrimSpace(command.Short)
	command.Long = strings.TrimSpace(command.Long)
	command.Aliases = normalizeStringSlice(command.Aliases)
}

func normalizeStringSlice(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func sortedAppKeys(values map[string]spec.AppSpec) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedCLIKeys(values map[string]spec.CLISpec) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedOperationKeys(values map[string]spec.OperationSpec) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateHTTPPathJSONManifestUpdater(path string, updater *spec.UpdaterSpec) error {
	if updater.URLTemplate == "" {
		return fmt.Errorf("%s.url_template is required", path)
	}
	if updater.UserAgent == "" {
		return fmt.Errorf("%s.user_agent is required", path)
	}
	if updater.Platform == "" {
		return fmt.Errorf("%s.platform is required", path)
	}
	if updater.Product == "" {
		return fmt.Errorf("%s.product is required", path)
	}
	if len(updater.Channels) == 0 {
		return fmt.Errorf("%s.channels is required", path)
	}
	if updater.DefaultChannel == "" {
		return fmt.Errorf("%s.default_channel is required", path)
	}
	return nil
}

func validateSparkleUpdater(path string, updater *spec.UpdaterSpec) error {
	if updater.UserAgent == "" {
		return fmt.Errorf("%s.user_agent is required", path)
	}
	if updater.SparklePublicKey == "" {
		return fmt.Errorf("%s.sparkle_public_key is required", path)
	}
	if len(updater.Channels) == 0 && updater.URL == "" {
		return fmt.Errorf("%s.url or %s.channels is required", path, path)
	}
	if len(updater.Channels) == 0 {
		return nil
	}
	if updater.DefaultChannel == "" {
		return fmt.Errorf("%s.default_channel is required", path)
	}
	for _, channel := range updater.Channels {
		if channel.URL == "" {
			return fmt.Errorf("%s.channels.%s.url is required", path, channel.Name)
		}
	}
	return nil
}

func validateSquirrelUpdater(path string, updater *spec.UpdaterSpec) error {
	if updater.URL == "" {
		return fmt.Errorf("%s.url is required", path)
	}
	if updater.UserAgent == "" {
		return fmt.Errorf("%s.user_agent is required", path)
	}
	if updater.DeviceIDParamName == "" {
		return fmt.Errorf("%s.device_id_param_name is required", path)
	}
	if len(updater.Channels) > 0 || updater.DefaultChannel != "" {
		return fmt.Errorf("%s does not support channels", path)
	}
	return nil
}

func normalizeLaunchPolicyEnvironment(path string, policy *spec.LaunchPolicySpec) error {
	for index, action := range policy.Environment {
		action.Action = strings.TrimSpace(action.Action)
		action.Key = strings.TrimSpace(action.Key)
		if action.Action != "set" && action.Action != "unset" {
			return fmt.Errorf("%s.environment[%d].action must be one of set|unset", path, index)
		}
		if action.Key == "" {
			return fmt.Errorf("%s.environment[%d].key is required", path, index)
		}
		if action.Value != "" && hasLaunchPolicyTemplate(action.Value) {
			return fmt.Errorf("%s.environment[%d].value must be fully resolved", path, index)
		}
		policy.Environment[index] = action
	}
	return nil
}

func normalizeLaunchPolicyArguments(path string, policy *spec.LaunchPolicySpec) error {
	for index, action := range policy.Arguments {
		action.Action = strings.TrimSpace(action.Action)
		action.Value = strings.TrimSpace(action.Value)
		if action.Action != "append" && action.Action != "prepend" {
			return fmt.Errorf("%s.arguments[%d].action must be one of append|prepend", path, index)
		}
		if action.Value == "" {
			return fmt.Errorf("%s.arguments[%d].value is required", path, index)
		}
		if hasLaunchPolicyTemplate(action.Value) {
			return fmt.Errorf("%s.arguments[%d].value must be fully resolved", path, index)
		}
		policy.Arguments[index] = action
	}
	return nil
}

func normalizeLaunchPolicyPreflights(path string, policy *spec.LaunchPolicySpec) error {
	for index, preflight := range policy.Preflights {
		preflight.Kind = spec.PreflightKind(strings.TrimSpace(string(preflight.Kind)))
		preflight.Path = cleanExpandedPath(strings.TrimSpace(preflight.Path))
		preflight.Host = strings.TrimSpace(preflight.Host)
		switch preflight.Kind {
		case spec.PreflightKindFileExists:
			if preflight.Path == "" {
				return fmt.Errorf("%s.preflights[%d].path is required", path, index)
			}
			if hasLaunchPolicyTemplate(preflight.Path) {
				return fmt.Errorf("%s.preflights[%d].path must be fully resolved", path, index)
			}
		case spec.PreflightKindTCPReachable:
			if preflight.Host == "" {
				return fmt.Errorf("%s.preflights[%d].host is required", path, index)
			}
			if preflight.Port < 1 || preflight.Port > 65535 {
				return fmt.Errorf("%s.preflights[%d].port must be between 1 and 65535", path, index)
			}
			if preflight.Timeout < 1 {
				return fmt.Errorf("%s.preflights[%d].timeout_ms must be positive", path, index)
			}
		default:
			return fmt.Errorf("%s.preflights[%d].kind must be one of file_exists|tcp_reachable", path, index)
		}
		policy.Preflights[index] = preflight
	}
	return nil
}

func hasLaunchPolicyTemplate(value string) bool {
	return strings.Contains(value, "{") || strings.Contains(value, "}")
}
