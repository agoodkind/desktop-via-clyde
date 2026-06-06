// Package extensions models optional provider-owned target behavior loaded from
// runtime config.
package extensions

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/catalog"
)

// AppSpec stores optional app behavior owned by extension packages.
type AppSpec struct {
	ComputerUse   *ComputerUseSpec
	BundledCLITee *BundledCLITeeSpec
	CodexCLIShim  *CodexCLIShimSpec
}

// DecodedAppSpec captures extension-owned TOML declarations during config load.
type DecodedAppSpec struct {
	ComputerUse   *ComputerUseSpec   `toml:"computer_use"`
	BundledCLITee *BundledCLITeeSpec `toml:"bundled_cli_tee"`
	CodexCLIShim  *CodexCLIShimSpec  `toml:"codex_cli_shim"`
}

// EntitlementsSpec describes extension-owned entitlement mutations.
type EntitlementsSpec struct {
	Strip           []string `toml:"strip"`
	RequiredBoolean []string `toml:"required_boolean"`
}

// ComputerUseSpec configures local trust-policy repair surfaces.
type ComputerUseSpec struct {
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
	Path         string           `toml:"path"`
	Entitlements EntitlementsSpec `toml:"entitlements"`
}

// BundledCLITeeSpec configures a bundled CLI stdio tee selector.
type BundledCLITeeSpec struct {
	Capability               string   `toml:"capability"`
	AppSupportDir            string   `toml:"app_support_dir"`
	VersionDir               string   `toml:"version_dir"`
	BundledCLIRel            string   `toml:"bundled_cli_rel"`
	BundledCLIPath           string   `toml:"bundled_cli_path"`
	TerminateProcessNames    []string `toml:"terminate_process_names"`
	TerminateProcessPatterns []string `toml:"terminate_process_patterns"`
	CompletionSteps          []string `toml:"completion_steps"`
}

// CodexCLIShimSpec configures the bundled Codex CLI app-server wrapper.
type CodexCLIShimSpec struct {
	Capability     string `toml:"capability"`
	ChatGPTBaseURL string `toml:"chatgpt_base_url"`
}

// Target stores runtime optional behavior owned by extension packages.
type Target struct {
	ComputerUse   *ComputerUsePolicy
	BundledCLITee *BundledCLITeeSpec
	CodexCLIShim  *CodexCLIShimSpec
}

// PostPatchHookCapabilities returns optional post-patch hook capabilities.
func (t Target) PostPatchHookCapabilities() []string {
	if t.BundledCLITee == nil || t.BundledCLITee.Capability == "" {
		return nil
	}
	return []string{t.BundledCLITee.Capability}
}

// PreLaunchPolicyHookCapabilities returns optional pre-launch-policy hook capabilities.
func (t Target) PreLaunchPolicyHookCapabilities() []string {
	if t.CodexCLIShim == nil || t.CodexCLIShim.Capability == "" {
		return nil
	}
	return []string{t.CodexCLIShim.Capability}
}

// ToAppSpec converts one decoded extension block into the app extension model.
func (d DecodedAppSpec) ToAppSpec() AppSpec {
	return AppSpec(d)
}

// EntitlementsPolicy describes runtime entitlement mutations.
type EntitlementsPolicy struct {
	Strip                       []string
	RequiredBooleanEntitlements []string
}

// ComputerUsePolicy describes companion helper repair settings.
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
	SignTargets           []ComputerUseRuntimeSignTarget
}

// ComputerUseRuntimeSignTarget describes one helper that must be re-signed.
type ComputerUseRuntimeSignTarget struct {
	Path         string
	Entitlements *EntitlementsPolicy
}

// AppValidator validates one optional app extension declaration.
type AppValidator func(string, *AppSpec) error

var (
	validatorsMu sync.RWMutex
	validators   = map[string]AppValidator{}
)

// RegisterAppValidator links one app extension validator.
func RegisterAppValidator(name string, validator AppValidator) error {
	if name == "" {
		return fmt.Errorf("app validator name is required")
	}
	if validator == nil {
		return fmt.Errorf("app validator %q is required", name)
	}
	validatorsMu.Lock()
	defer validatorsMu.Unlock()
	if _, ok := validators[name]; ok {
		return fmt.Errorf("app validator %q is already registered", name)
	}
	validators[name] = validator
	return nil
}

// ValidateApp runs registered app extension validators in stable order.
func ValidateApp(path string, app *AppSpec) error {
	names := RegisteredAppValidators()
	validatorsMu.RLock()
	items := make([]AppValidator, 0, len(names))
	for _, name := range names {
		items = append(items, validators[name])
	}
	validatorsMu.RUnlock()
	for _, validator := range items {
		if err := validator(path, app); err != nil {
			return err
		}
	}
	return nil
}

// RegisteredAppValidators returns registered validator names in stable order.
func RegisteredAppValidators() []string {
	validatorsMu.RLock()
	defer validatorsMu.RUnlock()
	names := make([]string, 0, len(validators))
	for name := range validators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NormalizeAppSpec applies shared normalization to extension-owned app data.
func NormalizeAppSpec(
	app *AppSpec,
	cleanPath func(string) string,
	renderTokens func(string) string,
	normalizeStrings func([]string) []string,
) {
	if app == nil {
		return
	}
	if app.ComputerUse != nil {
		normalizeComputerUse(app.ComputerUse, cleanPath, normalizeStrings)
	}
	if app.BundledCLITee != nil {
		normalizeBundledCLITee(app.BundledCLITee, cleanPath, renderTokens, normalizeStrings)
	}
	if app.CodexCLIShim != nil {
		normalizeCodexCLIShim(app.CodexCLIShim)
	}
}

// BuildTarget converts app extension declarations into runtime extension data.
func BuildTarget(app AppSpec) Target {
	target := Target{
		ComputerUse:   nil,
		BundledCLITee: cloneBundledCLITee(app.BundledCLITee),
		CodexCLIShim:  cloneCodexCLIShim(app.CodexCLIShim),
	}
	if app.ComputerUse != nil {
		target.ComputerUse = buildComputerUse(*app.ComputerUse)
	}
	return target
}

// CloneAppSpec returns a deep copy of optional app extension data.
func CloneAppSpec(app AppSpec) AppSpec {
	cloned := app
	if app.ComputerUse != nil {
		policy := cloneComputerUse(*app.ComputerUse)
		cloned.ComputerUse = &policy
	}
	if app.BundledCLITee != nil {
		tee := cloneBundledCLITee(app.BundledCLITee)
		cloned.BundledCLITee = tee
	}
	if app.CodexCLIShim != nil {
		shim := cloneCodexCLIShim(app.CodexCLIShim)
		cloned.CodexCLIShim = shim
	}
	return cloned
}

func cloneComputerUse(policy ComputerUseSpec) ComputerUseSpec {
	cloned := policy
	cloned.CacheAppGlobsFromHome = cloneStrings(policy.CacheAppGlobsFromHome)
	cloned.TeamPatchBinaries = cloneStrings(policy.TeamPatchBinaries)
	cloned.TeamRequirementPlists = cloneStrings(policy.TeamRequirementPlists)
	if len(policy.SignTargets) > 0 {
		cloned.SignTargets = make([]ComputerUseSignTarget, 0, len(policy.SignTargets))
		for _, target := range policy.SignTargets {
			cloned.SignTargets = append(cloned.SignTargets, ComputerUseSignTarget{
				Path:         target.Path,
				Entitlements: cloneEntitlements(target.Entitlements),
			})
		}
	}
	return cloned
}

func cloneBundledCLITee(tee *BundledCLITeeSpec) *BundledCLITeeSpec {
	if tee == nil {
		return nil
	}
	cloned := *tee
	cloned.TerminateProcessNames = cloneStrings(tee.TerminateProcessNames)
	cloned.TerminateProcessPatterns = cloneStrings(tee.TerminateProcessPatterns)
	cloned.CompletionSteps = cloneStrings(tee.CompletionSteps)
	return &cloned
}

func cloneCodexCLIShim(shim *CodexCLIShimSpec) *CodexCLIShimSpec {
	if shim == nil {
		return nil
	}
	cloned := *shim
	return &cloned
}

func cloneEntitlements(entitlements EntitlementsSpec) EntitlementsSpec {
	return EntitlementsSpec{
		Strip:           cloneStrings(entitlements.Strip),
		RequiredBoolean: cloneStrings(entitlements.RequiredBoolean),
	}
}

func buildComputerUse(policy ComputerUseSpec) *ComputerUsePolicy {
	signTargets := make([]ComputerUseRuntimeSignTarget, 0, len(policy.SignTargets))
	for _, target := range policy.SignTargets {
		signTargets = append(signTargets, ComputerUseRuntimeSignTarget{
			Path: target.Path,
			Entitlements: &EntitlementsPolicy{
				Strip:                       cloneStrings(target.Entitlements.Strip),
				RequiredBooleanEntitlements: cloneStrings(target.Entitlements.RequiredBoolean),
			},
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

// ValidateBundledCLITee validates bundled CLI tee declarations.
func ValidateBundledCLITee(path string, app *AppSpec) error {
	tee := app.BundledCLITee
	if tee == nil {
		return nil
	}
	if tee.Capability == "" {
		return fmt.Errorf("%s.bundled_cli_tee.capability is required", path)
	}
	if !catalog.HasPatchHookCapability(tee.Capability) {
		return fmt.Errorf("%s.bundled_cli_tee.capability %q is unknown", path, tee.Capability)
	}
	if tee.BundledCLIPath == "" && tee.AppSupportDir == "" {
		return fmt.Errorf("%s.bundled_cli_tee.app_support_dir is required when bundled_cli_path is empty", path)
	}
	if tee.BundledCLIPath == "" && tee.BundledCLIRel == "" {
		return fmt.Errorf("%s.bundled_cli_tee.bundled_cli_rel is required when bundled_cli_path is empty", path)
	}
	if tee.AppSupportDir != "" && !isAbs(tee.AppSupportDir) {
		return fmt.Errorf("%s.bundled_cli_tee.app_support_dir must be an absolute path", path)
	}
	if tee.BundledCLIPath != "" && !isAbs(tee.BundledCLIPath) {
		return fmt.Errorf("%s.bundled_cli_tee.bundled_cli_path must be an absolute path", path)
	}
	return nil
}

// ValidateCodexCLIShim validates bundled Codex CLI shim declarations.
func ValidateCodexCLIShim(path string, app *AppSpec) error {
	shim := app.CodexCLIShim
	if shim == nil {
		return nil
	}
	if shim.Capability == "" {
		return fmt.Errorf("%s.codex_cli_shim.capability is required", path)
	}
	if !catalog.HasPreLaunchPolicyHookCapability(shim.Capability) {
		return fmt.Errorf("%s.codex_cli_shim.capability %q is unknown", path, shim.Capability)
	}
	if shim.ChatGPTBaseURL == "" {
		return fmt.Errorf("%s.codex_cli_shim.chatgpt_base_url is required", path)
	}
	if strings.Contains(shim.ChatGPTBaseURL, "{") || strings.Contains(shim.ChatGPTBaseURL, "}") {
		return fmt.Errorf("%s.codex_cli_shim.chatgpt_base_url must be fully resolved", path)
	}
	return nil
}

// ValidateComputerUse validates Computer Use declarations.
func ValidateComputerUse(path string, app *AppSpec) error {
	policy := app.ComputerUse
	if policy == nil {
		return nil
	}
	if policy.HostAppPath == "" {
		return fmt.Errorf("%s.computer_use.host_app_path is required", path)
	}
	if policy.BundledAppPath == "" {
		return fmt.Errorf("%s.computer_use.bundled_app_path is required", path)
	}
	if policy.AppPathFromHome == "" {
		return fmt.Errorf("%s.computer_use.app_path_from_home is required", path)
	}
	if policy.AuthPluginPath == "" {
		return fmt.Errorf("%s.computer_use.auth_plugin_path is required", path)
	}
	if !isAbs(policy.AuthPluginPath) {
		return fmt.Errorf("%s.computer_use.auth_plugin_path must be an absolute path", path)
	}
	if policy.AuthPluginExecutable == "" {
		return fmt.Errorf("%s.computer_use.auth_plugin_executable is required", path)
	}
	if policy.UpstreamTrustedTeamID == "" {
		return fmt.Errorf("%s.computer_use.upstream_trusted_team_id is required", path)
	}
	normalizedTargets := make([]ComputerUseSignTarget, 0, len(policy.SignTargets))
	for index, target := range policy.SignTargets {
		if target.Path == "" {
			return fmt.Errorf("%s.computer_use.sign_targets[%d].path is required", path, index)
		}
		normalizedTargets = append(normalizedTargets, target)
	}
	policy.SignTargets = normalizedTargets
	return nil
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func normalizeComputerUse(
	policy *ComputerUseSpec,
	cleanPath func(string) string,
	normalizeStrings func([]string) []string,
) {
	policy.HostAppPath = cleanPath(trimSpace(policy.HostAppPath))
	policy.BundledAppPath = trimSpace(policy.BundledAppPath)
	policy.AppPathFromHome = trimSpace(policy.AppPathFromHome)
	policy.CacheAppGlobsFromHome = normalizeStrings(policy.CacheAppGlobsFromHome)
	policy.AuthPluginPath = cleanPath(trimSpace(policy.AuthPluginPath))
	policy.AuthPluginExecutable = trimSpace(policy.AuthPluginExecutable)
	policy.UpstreamTrustedTeamID = trimSpace(policy.UpstreamTrustedTeamID)
	policy.TeamPatchBinaries = normalizeStrings(policy.TeamPatchBinaries)
	policy.TeamRequirementPlists = normalizeStrings(policy.TeamRequirementPlists)
	normalizedTargets := make([]ComputerUseSignTarget, 0, len(policy.SignTargets))
	for _, target := range policy.SignTargets {
		target.Path = trimSpace(target.Path)
		target.Entitlements.Strip = normalizeStrings(target.Entitlements.Strip)
		target.Entitlements.RequiredBoolean = normalizeStrings(target.Entitlements.RequiredBoolean)
		normalizedTargets = append(normalizedTargets, target)
	}
	policy.SignTargets = normalizedTargets
}

func normalizeBundledCLITee(
	tee *BundledCLITeeSpec,
	cleanPath func(string) string,
	renderTokens func(string) string,
	normalizeStrings func([]string) []string,
) {
	tee.Capability = trimSpace(tee.Capability)
	tee.AppSupportDir = cleanPath(renderTokens(trimSpace(tee.AppSupportDir)))
	tee.VersionDir = trimSpace(tee.VersionDir)
	tee.BundledCLIRel = trimSpace(tee.BundledCLIRel)
	tee.BundledCLIPath = cleanPath(renderTokens(trimSpace(tee.BundledCLIPath)))
	tee.TerminateProcessNames = normalizeStrings(tee.TerminateProcessNames)
	tee.TerminateProcessPatterns = normalizeStrings(tee.TerminateProcessPatterns)
	tee.CompletionSteps = normalizeStrings(tee.CompletionSteps)
}

func normalizeCodexCLIShim(shim *CodexCLIShimSpec) {
	shim.Capability = trimSpace(shim.Capability)
	shim.ChatGPTBaseURL = trimSpace(shim.ChatGPTBaseURL)
}

func trimSpace(value string) string {
	return strings.TrimSpace(value)
}

func isAbs(path string) bool {
	return filepath.IsAbs(path)
}
