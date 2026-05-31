package config

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

var configLog = slog.With("component", "desktop-via-clyde", "subcomponent", "config")

// LoadRequired loads the required desktop-via-clyde XDG config file.
func LoadRequired() (*Config, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		wrappedErr := fmt.Errorf("read %s: %w", path, err)
		configLog.Error("config.load.read_failed", "path", path, "err", wrappedErr)
		return nil, wrappedErr
	}

	cfg := Default()
	if err := toml.Unmarshal(data, cfg); err != nil {
		wrappedErr := fmt.Errorf("parse %s: %w", path, err)
		configLog.Error("config.load.parse_failed", "path", path, "err", wrappedErr)
		return nil, wrappedErr
	}

	if err := applyUpdaterChannelOverrides(cfg, string(data)); err != nil {
		wrappedErr := fmt.Errorf("invalid %s: %w", path, err)
		configLog.Error("config.load.channels_failed", "path", path, "err", wrappedErr)
		return nil, wrappedErr
	}
	if err := normalizeAndValidate(cfg); err != nil {
		wrappedErr := fmt.Errorf("invalid %s: %w", path, err)
		configLog.Error("config.load.validate_failed", "path", path, "err", wrappedErr)
		return nil, wrappedErr
	}
	return cfg, nil
}

func normalizeAndValidate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	if err := validateSigning(cfg); err != nil {
		return err
	}
	if err := validateProxy(cfg); err != nil {
		return err
	}
	if err := validateCodexCLI(cfg); err != nil {
		return err
	}
	if err := normalizeAndValidateApp("apps.cursor", &cfg.Apps.Cursor, true, false, false); err != nil {
		return err
	}
	if err := normalizeAndValidateApp("apps.codex", &cfg.Apps.Codex, true, true, false); err != nil {
		return err
	}
	if err := normalizeAndValidateApp("apps.claude", &cfg.Apps.Claude, false, false, true); err != nil {
		return err
	}
	return nil
}

func validateSigning(cfg *Config) error {
	cfg.Signing.Identity = strings.TrimSpace(cfg.Signing.Identity)
	cfg.Signing.TeamID = strings.TrimSpace(cfg.Signing.TeamID)
	if cfg.Signing.Identity == "" {
		return fmt.Errorf("signing.identity is required")
	}
	if cfg.Signing.TeamID == "" {
		return fmt.Errorf("signing.team_id is required")
	}
	return nil
}

func validateProxy(cfg *Config) error {
	cfg.Proxy.Host = strings.TrimSpace(cfg.Proxy.Host)
	cfg.Proxy.CACertificate = cleanExpandedPath(strings.TrimSpace(cfg.Proxy.CACertificate))
	cfg.Proxy.NoProxy = strings.TrimSpace(cfg.Proxy.NoProxy)
	cfg.Proxy.LaunchWorkingDirectory = cleanExpandedPath(strings.TrimSpace(cfg.Proxy.LaunchWorkingDirectory))
	if cfg.Proxy.Host == "" {
		return fmt.Errorf("proxy.host is required")
	}
	if cfg.Proxy.Port < 1 || cfg.Proxy.Port > 65535 {
		return fmt.Errorf("proxy.port must be between 1 and 65535")
	}
	if cfg.Proxy.CACertificate == "" {
		return fmt.Errorf("proxy.ca_certificate is required")
	}
	if !filepath.IsAbs(cfg.Proxy.CACertificate) {
		return fmt.Errorf("proxy.ca_certificate must be an absolute path")
	}
	if cfg.Proxy.NoProxy == "" {
		return fmt.Errorf("proxy.no_proxy is required")
	}
	if cfg.Proxy.LaunchWorkingDirectory == "" {
		return fmt.Errorf("proxy.launch_working_directory is required")
	}
	if !filepath.IsAbs(cfg.Proxy.LaunchWorkingDirectory) {
		return fmt.Errorf("proxy.launch_working_directory must be an absolute path")
	}
	return nil
}

func validateCodexCLI(cfg *Config) error {
	cfg.CLI.Codex.SourceRef = strings.TrimSpace(cfg.CLI.Codex.SourceRef)
	cfg.CLI.Codex.BuildMode = strings.TrimSpace(cfg.CLI.Codex.BuildMode)
	cfg.CLI.Codex.InstallDir = cleanExpandedPath(strings.TrimSpace(cfg.CLI.Codex.InstallDir))
	cfg.CLI.Codex.CodexHome = cleanExpandedPath(strings.TrimSpace(cfg.CLI.Codex.CodexHome))
	if cfg.CLI.Codex.SourceRef == "" {
		return fmt.Errorf("cli.codex.source_ref is required")
	}
	if cfg.CLI.Codex.BuildMode != "local-fast" && cfg.CLI.Codex.BuildMode != "release" {
		return fmt.Errorf("cli.codex.build_mode must be one of local-fast|release")
	}
	if cfg.CLI.Codex.InstallDir == "" {
		return fmt.Errorf("cli.codex.install_dir is required")
	}
	if !filepath.IsAbs(cfg.CLI.Codex.InstallDir) {
		return fmt.Errorf("cli.codex.install_dir must be an absolute path")
	}
	if cfg.CLI.Codex.CodexHome == "" {
		return fmt.Errorf("cli.codex.codex_home is required")
	}
	if !filepath.IsAbs(cfg.CLI.Codex.CodexHome) {
		return fmt.Errorf("cli.codex.codex_home must be an absolute path")
	}
	return nil
}

func normalizeAndValidateApp(path string, app *AppConfig, allowChannels bool, requireComputerUse bool, allowBundledCLITee bool) error {
	app.AppPath = cleanExpandedPath(strings.TrimSpace(app.AppPath))
	app.BundleID = strings.TrimSpace(app.BundleID)
	app.ExecName = strings.TrimSpace(app.ExecName)
	app.TargetPolicy = TargetPolicy(strings.TrimSpace(string(app.TargetPolicy)))
	app.KeychainServices = normalizeStringSlice(app.KeychainServices)
	app.NestedSignPaths = normalizeStringSlice(app.NestedSignPaths)
	app.PreservedNestedCodePaths = normalizeStringSlice(app.PreservedNestedCodePaths)
	app.Entitlements.Strip = normalizeStringSlice(app.Entitlements.Strip)
	app.Entitlements.RequiredBoolean = normalizeStringSlice(app.Entitlements.RequiredBoolean)
	app.BundledCLITee.VersionDir = strings.TrimSpace(app.BundledCLITee.VersionDir)
	app.BundledCLITee.BundledCLIPath = cleanExpandedPath(strings.TrimSpace(app.BundledCLITee.BundledCLIPath))

	if app.AppPath == "" {
		return fmt.Errorf("%s.app_path is required", path)
	}
	if app.BundleID == "" {
		return fmt.Errorf("%s.bundle_id is required", path)
	}
	if app.ExecName == "" {
		return fmt.Errorf("%s.exec_name is required", path)
	}
	if app.TargetPolicy != TargetPolicyDefault && app.TargetPolicy != TargetPolicyCodex {
		return fmt.Errorf("%s.target_policy must be one of default|codex", path)
	}
	if !allowBundledCLITee && (app.BundledCLITee.VersionDir != "" || app.BundledCLITee.BundledCLIPath != "") {
		return fmt.Errorf("%s.bundled_cli_tee is not supported", path)
	}
	if app.BundledCLITee.BundledCLIPath != "" && !filepath.IsAbs(app.BundledCLITee.BundledCLIPath) {
		return fmt.Errorf("%s.bundled_cli_tee.bundled_cli_path must be an absolute path", path)
	}
	if err := normalizeAndValidateUpdater(path+".updater", &app.Updater, allowChannels); err != nil {
		return err
	}
	if requireComputerUse {
		if err := normalizeAndValidateComputerUse(path+".computer_use", &app.ComputerUse); err != nil {
			return err
		}
	}
	return nil
}

func normalizeAndValidateUpdater(path string, updater *UpdaterConfig, allowChannels bool) error {
	normalizeUpdaterFields(updater)
	channelNames, err := normalizeUpdaterChannels(path, updater)
	if err != nil {
		return err
	}
	if len(updater.Channels) > 0 && !allowChannels {
		return fmt.Errorf("%s.channels is not supported", path)
	}
	if updater.Kind == UpdaterKindCursorManifest {
		return validateCursorUpdater(path, updater, channelNames)
	}
	if updater.Kind == UpdaterKindSparkleAppcast {
		return validateSparkleUpdater(path, updater, channelNames)
	}
	if updater.Kind == UpdaterKindClaudeSquirrel {
		return validateClaudeUpdater(path, updater)
	}
	return fmt.Errorf("%s.kind must be one of cursor_manifest|sparkle_appcast|claude_squirrel", path)
}

func normalizeUpdaterFields(updater *UpdaterConfig) {
	updater.Kind = UpdaterKind(strings.TrimSpace(string(updater.Kind)))
	updater.URL = strings.TrimSpace(updater.URL)
	updater.Platform = strings.TrimSpace(updater.Platform)
	updater.Product = strings.TrimSpace(updater.Product)
	updater.SparklePublicKey = strings.TrimSpace(updater.SparklePublicKey)
	updater.DeviceIDParamName = strings.TrimSpace(updater.DeviceIDParamName)
	updater.DefaultChannel = strings.TrimSpace(updater.DefaultChannel)
}

func normalizeUpdaterChannels(path string, updater *UpdaterConfig) (map[string]bool, error) {
	channels := make([]UpdaterChannel, 0, len(updater.Channels))
	channelNames := map[string]bool{}
	for _, channel := range updater.Channels {
		normalizedName := strings.TrimSpace(channel.Name)
		normalizedURL := strings.TrimSpace(channel.URL)
		if normalizedName == "" {
			return nil, fmt.Errorf("%s.channels contains a channel without name", path)
		}
		if channelNames[normalizedName] {
			return nil, fmt.Errorf("%s.channels.%s is duplicated", path, normalizedName)
		}
		channelNames[normalizedName] = true
		channels = append(channels, UpdaterChannel{Name: normalizedName, URL: normalizedURL})
	}
	updater.Channels = channels
	return channelNames, nil
}

func validateCursorUpdater(path string, updater *UpdaterConfig, channelNames map[string]bool) error {
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
	for _, channel := range updater.Channels {
		if channel.URL != "" {
			return fmt.Errorf("%s.channels.%s.url is not supported for cursor_manifest", path, channel.Name)
		}
	}
	if !channelNames[updater.DefaultChannel] {
		return fmt.Errorf("%s.default_channel %q is not declared in channels", path, updater.DefaultChannel)
	}
	return nil
}

func validateSparkleUpdater(path string, updater *UpdaterConfig, channelNames map[string]bool) error {
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
	if !channelNames[updater.DefaultChannel] {
		return fmt.Errorf("%s.default_channel %q is not declared in channels", path, updater.DefaultChannel)
	}
	return nil
}

func validateClaudeUpdater(path string, updater *UpdaterConfig) error {
	if updater.URL == "" {
		return fmt.Errorf("%s.url is required", path)
	}
	if updater.DeviceIDParamName == "" {
		return fmt.Errorf("%s.device_id_param_name is required", path)
	}
	if len(updater.Channels) > 0 || updater.DefaultChannel != "" {
		return fmt.Errorf("%s does not support channels", path)
	}
	return nil
}

func normalizeAndValidateComputerUse(path string, policy *ComputerUseConfig) error {
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
	normalizedTargets := make([]ComputerUseSignTarget, 0, len(policy.SignTargets))
	for index, target := range policy.SignTargets {
		target.Path = strings.TrimSpace(target.Path)
		if target.Path == "" {
			return fmt.Errorf("%s.sign_targets[%d].path is required", path, index)
		}
		target.Entitlements.Strip = normalizeStringSlice(target.Entitlements.Strip)
		target.Entitlements.RequiredBoolean = normalizeStringSlice(target.Entitlements.RequiredBoolean)
		normalizedTargets = append(normalizedTargets, target)
	}
	policy.SignTargets = normalizedTargets
	return nil
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

func applyUpdaterChannelOverrides(cfg *Config, data string) error {
	parser := updaterChannelParser{
		cfg:               cfg,
		currentTable:      "",
		currentChannelApp: "",
		currentChannel:    UpdaterChannel{Name: "", URL: ""},
		seenChannelTables: map[string]bool{},
	}
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		if err := parser.consumeLine(scanner.Text()); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		configLog.Error("config.load.channels_scan_failed", "err", err)
		return fmt.Errorf("scan config channels: %w", err)
	}
	return parser.finish()
}

type updaterChannelParser struct {
	cfg               *Config
	currentTable      string
	currentChannelApp string
	currentChannel    UpdaterChannel
	seenChannelTables map[string]bool
}

func (p *updaterChannelParser) consumeLine(rawLine string) error {
	line := strings.TrimSpace(stripLineComment(rawLine))
	if line == "" {
		return nil
	}
	if isArrayTableLine(line) {
		return p.handleArrayTable(line)
	}
	if isTableLine(line) {
		return p.handleTable(line)
	}
	key, value, ok := parseKeyValue(line)
	if !ok {
		return nil
	}
	if p.currentChannelApp != "" {
		p.applyChannelField(key, value)
		return nil
	}
	return p.handleUpdaterTableValue(key, value)
}

func (p *updaterChannelParser) finish() error {
	return p.flushCurrentChannel()
}

func (p *updaterChannelParser) handleArrayTable(line string) error {
	if err := p.flushCurrentChannel(); err != nil {
		return err
	}
	p.currentChannel = UpdaterChannel{Name: "", URL: ""}
	p.currentTable = strings.TrimSpace(line[2 : len(line)-2])
	appID, ok := parseUpdaterChannelTableName(p.currentTable)
	if !ok {
		p.currentChannelApp = ""
		return nil
	}
	p.currentChannelApp = appID
	if p.seenChannelTables[appID] {
		return nil
	}
	if err := replaceAppChannels(p.cfg, appID, nil); err != nil {
		return err
	}
	p.seenChannelTables[appID] = true
	return nil
}

func (p *updaterChannelParser) handleTable(line string) error {
	if err := p.flushCurrentChannel(); err != nil {
		return err
	}
	p.currentChannel = UpdaterChannel{Name: "", URL: ""}
	p.currentChannelApp = ""
	p.currentTable = strings.TrimSpace(line[1 : len(line)-1])
	return nil
}

func (p *updaterChannelParser) applyChannelField(key string, value string) {
	if key == "name" {
		p.currentChannel.Name = parseQuotedString(value)
	}
	if key == "url" {
		p.currentChannel.URL = parseQuotedString(value)
	}
}

func (p *updaterChannelParser) handleUpdaterTableValue(key string, value string) error {
	appID, ok := parseUpdaterTableName(p.currentTable)
	if !ok || key != "channels" {
		return nil
	}
	channels, err := parseStringChannels(appID, value)
	if err != nil {
		return err
	}
	return replaceAppChannels(p.cfg, appID, channels)
}

func (p *updaterChannelParser) flushCurrentChannel() error {
	if p.currentChannelApp == "" {
		return nil
	}
	if p.currentChannel.Name == "" && p.currentChannel.URL == "" {
		return nil
	}
	return appendAppChannel(p.cfg, p.currentChannelApp, p.currentChannel)
}

func parseUpdaterTableName(table string) (string, bool) {
	if !strings.HasPrefix(table, "apps.") || !strings.HasSuffix(table, ".updater") {
		return "", false
	}
	appID := strings.TrimSuffix(strings.TrimPrefix(table, "apps."), ".updater")
	if strings.Contains(appID, ".") || appID == "" {
		return "", false
	}
	return appID, true
}

func parseUpdaterChannelTableName(table string) (string, bool) {
	if !strings.HasPrefix(table, "apps.") || !strings.HasSuffix(table, ".updater.channels") {
		return "", false
	}
	appID := strings.TrimSuffix(strings.TrimPrefix(table, "apps."), ".updater.channels")
	if strings.Contains(appID, ".") || appID == "" {
		return "", false
	}
	return appID, true
}

func parseKeyValue(line string) (string, string, bool) {
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

func parseQuotedString(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "\"")
	trimmed = strings.TrimSuffix(trimmed, "\"")
	return strings.ReplaceAll(trimmed, "\\\"", "\"")
}

func parseStringChannels(appID string, value string) ([]UpdaterChannel, error) {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return nil, fmt.Errorf("apps.%s.updater.channels must use the TOML array form", appID)
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if inner == "" {
		return []UpdaterChannel{}, nil
	}
	parts := strings.Split(inner, ",")
	channels := make([]UpdaterChannel, 0, len(parts))
	for _, part := range parts {
		name := parseQuotedString(part)
		if name == "" {
			return nil, fmt.Errorf("apps.%s.updater.channels contains an empty channel name", appID)
		}
		channels = append(channels, UpdaterChannel{Name: name, URL: ""})
	}
	return channels, nil
}

func replaceAppChannels(cfg *Config, appID string, channels []UpdaterChannel) error {
	if appID == "cursor" {
		cfg.Apps.Cursor.Updater.Channels = append([]UpdaterChannel(nil), channels...)
		return nil
	}
	if appID == "codex" {
		cfg.Apps.Codex.Updater.Channels = append([]UpdaterChannel(nil), channels...)
		return nil
	}
	if appID == "claude" {
		cfg.Apps.Claude.Updater.Channels = append([]UpdaterChannel(nil), channels...)
		return nil
	}
	return fmt.Errorf("apps.%s.updater.channels references an unknown app", appID)
}

func appendAppChannel(cfg *Config, appID string, channel UpdaterChannel) error {
	if appID == "cursor" {
		cfg.Apps.Cursor.Updater.Channels = append(cfg.Apps.Cursor.Updater.Channels, channel)
		return nil
	}
	if appID == "codex" {
		cfg.Apps.Codex.Updater.Channels = append(cfg.Apps.Codex.Updater.Channels, channel)
		return nil
	}
	if appID == "claude" {
		cfg.Apps.Claude.Updater.Channels = append(cfg.Apps.Claude.Updater.Channels, channel)
		return nil
	}
	return fmt.Errorf("apps.%s.updater.channels references an unknown app", appID)
}

func isArrayTableLine(line string) bool {
	return strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]")
}

func isTableLine(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")
}

func stripLineComment(line string) string {
	var builder strings.Builder
	inQuotes := false
	escaped := false
	for _, character := range line {
		if character == '\\' && inQuotes {
			escaped = !escaped
			builder.WriteRune(character)
			continue
		}
		if character == '"' && !escaped {
			inQuotes = !inQuotes
			builder.WriteRune(character)
			continue
		}
		escaped = false
		if character == '#' && !inQuotes {
			break
		}
		builder.WriteRune(character)
	}
	return builder.String()
}
