// Package computeruseext links Computer Use helper repair behavior into patch
// lifecycle hooks.
package computeruseext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"howett.net/plist"
)

var (
	signIdentityTeamRE = regexp.MustCompile(`\(([A-Za-z0-9]{10})\)\s*$`)
	computerUseLog     = slog.With("component", "desktop-via-clyde", "subcomponent", "computer-use")
)

const (
	// ActionRepairBundledComputerUse records repair of a bundled Computer Use helper.
	ActionRepairBundledComputerUse patch.Action = "repair_bundled_computer_use"
	// ActionRepairComputerUseAuthPlugin records repair of the authorization plugin.
	ActionRepairComputerUseAuthPlugin patch.Action = "repair_computer_use_auth_plugin"
	// ActionRepairComputerUseTrustedTeam records trusted team replacement.
	ActionRepairComputerUseTrustedTeam patch.Action = "repair_computer_use_trusted_team"
	// ActionRepairComputerUseRequirement records code requirement replacement.
	ActionRepairComputerUseRequirement patch.Action = "repair_computer_use_requirement"
	// ActionScanComputerUseCache records scanning cached Computer Use helpers.
	ActionScanComputerUseCache patch.Action = "scan_computer_use_cache"
	// ActionSignComputerUseHelper records Computer Use helper signing.
	ActionSignComputerUseHelper patch.Action = "sign_computer_use_helper"
)

type teamRequirementPlist struct {
	TeamIdentifier string `plist:"team-identifier"`
}

type computerUseAuthPluginRepair struct {
	Updated      []byte
	Permissions  os.FileMode
	Replacements int
}

// RegisterLifecycleHooks links Computer Use patch lifecycle hooks.
func RegisterLifecycleHooks() error {
	if err := patch.RegisterPreResignHook("computer-use-bundled", BundledLifecycleHook); err != nil {
		return logComputerUseRegistrationError("register bundled Computer Use lifecycle hook", err)
	}
	if err := patch.RegisterPostBundleHook("computer-use", LifecycleHook); err != nil {
		return logComputerUseRegistrationError("register Computer Use lifecycle hook", err)
	}
	return nil
}

// RegisterValidators links Computer Use config validation.
func RegisterValidators() error {
	if err := extensions.RegisterAppValidator("computer_use", extensions.ValidateComputerUse); err != nil {
		return logComputerUseRegistrationError("register Computer Use validator", err)
	}
	return nil
}

// BundledLifecycleHook repairs a bundled Computer Use helper before app signing.
func BundledLifecycleHook(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	_ patch.Options,
) error {
	if t.Extensions.ComputerUse == nil {
		return nil
	}
	policy := *t.Extensions.ComputerUse
	localTeamID, err := validateComputerUsePolicy(policy)
	if err != nil {
		return err
	}
	appPath := bundledComputerUseAppPath(t.AppPath, policy)
	patch.RecordTrace(r, ActionRepairBundledComputerUse, t.ID, appPath)
	patch.Note(r, fmt.Sprintf("target=%s repair bundled Computer Use helper at %s", t.ID, appPath))
	if !r.DryRun {
		if err := ensureComputerUseAppPath(appPath); err != nil {
			return err
		}
	}
	return patchComputerUseBundle(ctx, r, t, appPath, policy, localTeamID)
}

// LifecycleHook repairs installed and cached Computer Use helpers.
func LifecycleHook(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	_ patch.Options,
) error {
	if t.Extensions.ComputerUse == nil {
		return nil
	}
	policy := *t.Extensions.ComputerUse
	localTeamID, err := validateComputerUsePolicy(policy)
	if err != nil {
		return err
	}
	if filepath.Clean(t.AppPath) != filepath.Clean(policy.HostAppPath) {
		patch.Note(r, fmt.Sprintf("target=%s skipped helper repair for non-canonical app path %s", t.ID, t.AppPath))
		return nil
	}
	appPath := computerUseAppPath(policy)
	patch.RecordTrace(r, ActionRepairBundledComputerUse, t.ID, appPath)
	patch.Note(r, fmt.Sprintf("target=%s repair Computer Use helper at %s", t.ID, appPath))
	if r.DryRun {
		if err := patchComputerUseBundle(ctx, r, t, appPath, policy, localTeamID); err != nil {
			return err
		}
		if err := patchComputerUseCache(ctx, r, t, policy, localTeamID); err != nil {
			return err
		}
		return patchComputerUseAuthPluginStep(ctx, r, t, policy, localTeamID)
	}

	if err := ensureComputerUseAppPath(appPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			patch.Note(r, fmt.Sprintf("target=%s helper bundle not found, skipping", t.ID))
			if err := patchComputerUseCache(ctx, r, t, policy, localTeamID); err != nil {
				return err
			}
			return patchComputerUseAuthPluginStep(ctx, r, t, policy, localTeamID)
		}
		return err
	}
	if err := patchComputerUseBundle(ctx, r, t, appPath, policy, localTeamID); err != nil {
		return err
	}
	if err := patchComputerUseCache(ctx, r, t, policy, localTeamID); err != nil {
		return err
	}
	return patchComputerUseAuthPluginStep(ctx, r, t, policy, localTeamID)
}

func validateComputerUsePolicy(policy targets.ComputerUsePolicy) (string, error) {
	if policy.BundledAppPath == "" {
		return "", fmt.Errorf("computer use policy missing bundled app path")
	}
	if policy.AppPathFromHome == "" {
		return "", fmt.Errorf("computer use policy missing installed app path")
	}
	if policy.AuthPluginPath == "" {
		return "", fmt.Errorf("computer use policy missing authorization plugin path")
	}
	if policy.AuthPluginExecutable == "" {
		return "", fmt.Errorf("computer use policy missing authorization plugin executable")
	}
	localTeamID, err := teamIDFromSignIdentity(paths.SignIdentity())
	if err != nil {
		return "", err
	}
	if err := validateTeamID(policy.UpstreamTrustedTeamID, "upstream trusted team ID"); err != nil {
		return "", err
	}
	return localTeamID, nil
}

func bundledComputerUseAppPath(hostAppPath string, policy targets.ComputerUsePolicy) string {
	return filepath.Join(hostAppPath, filepath.FromSlash(policy.BundledAppPath))
}

func computerUseAppPath(policy targets.ComputerUsePolicy) string {
	return filepath.Join(paths.Home(), filepath.FromSlash(policy.AppPathFromHome))
}

func patchComputerUseCache(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relGlob := range policy.CacheAppGlobsFromHome {
		pattern := filepath.Join(paths.Home(), filepath.FromSlash(relGlob))
		patch.RecordTrace(r, ActionScanComputerUseCache, t.ID, pattern)
		patch.Note(r, fmt.Sprintf("target=%s scan Computer Use cache helpers at %s", t.ID, pattern))
		if r.DryRun {
			continue
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_cache_glob_failed",
				fmt.Errorf("glob helper cache %s: %w", pattern, err))
		}
		if len(matches) == 0 {
			patch.Note(r, fmt.Sprintf("target=%s no cached helper bundles matched %s", t.ID, pattern))
			continue
		}
		sort.Strings(matches)
		for _, appPath := range matches {
			patch.Note(r, fmt.Sprintf("target=%s repair cached Computer Use helper at %s", t.ID, appPath))
			if err := ensureComputerUseAppPath(appPath); err != nil {
				return err
			}
			if err := patchComputerUseBundle(ctx, r, t, appPath, policy, localTeamID); err != nil {
				return err
			}
		}
	}
	return nil
}

func patchComputerUseAuthPluginStep(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	pluginPath := policy.AuthPluginPath
	executablePath := filepath.Join(pluginPath, filepath.FromSlash(policy.AuthPluginExecutable))
	r.Log.InfoContext(ctx, "patch.computer_use_auth_plugin.start",
		"target", t.ID,
		"plugin_path", pluginPath,
		"executable_path", executablePath,
		"dry_run", r.DryRun)
	patch.RecordTrace(r, ActionRepairComputerUseAuthPlugin, t.ID, pluginPath)
	patch.Note(r, fmt.Sprintf("target=%s repair Computer Use authorization plugin at %s", t.ID, pluginPath))

	if r.DryRun {
		return dryRunPatchComputerUseAuthPlugin(ctx, r, t, pluginPath, executablePath, policy, localTeamID)
	}

	return patchComputerUseAuthPlugin(ctx, r, t, pluginPath, executablePath, policy, localTeamID)
}

func dryRunPatchComputerUseAuthPlugin(
	ctx context.Context,
	r *patch.Runner,
	_ targets.Target,
	pluginPath string,
	executablePath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	patch.RecordTrace(r, ActionRepairComputerUseTrustedTeam, "", executablePath)
	patch.Note(r, "computer-use: repair login authorization trusted service team in "+executablePath)
	stagingPath := computerUseAuthPluginDryRunStagingPath(pluginPath)
	if err := r.Run(ctx, "/usr/bin/rsync", "-a", pluginPath+"/", stagingPath+"/"); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_stage_failed",
			fmt.Errorf("stage authorization plugin: %w", err))
	}
	id, err := patch.ResolveSignIdentity(ctx, true)
	if err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_identity_failed",
			fmt.Errorf("resolve signing identity: %w", err))
	}
	if err := signAndVerifyComputerUseAuthPlugin(ctx, r, stagingPath, id); err != nil {
		return err
	}
	if err := installComputerUseAuthPlugin(ctx, r, stagingPath, pluginPath); err != nil {
		return err
	}
	return verifyComputerUseAuthPlugin(ctx, r, pluginPath, policy, localTeamID)
}

func patchComputerUseAuthPlugin(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	pluginPath string,
	executablePath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	info, err := os.Stat(pluginPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			patch.Note(r, fmt.Sprintf("target=%s authorization plugin not found at %s, skipping", t.ID, pluginPath))
			return nil
		}
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_stat_failed", fmt.Errorf("stat authorization plugin %s: %w", pluginPath, err))
	}
	if !info.IsDir() {
		return fmt.Errorf("authorization plugin path is not a directory: %s", pluginPath)
	}

	repair, err := readComputerUseAuthPluginRepair(ctx, executablePath, policy, localTeamID)
	if err != nil {
		return err
	}
	if err := stageInstallComputerUseAuthPlugin(ctx, r, pluginPath, executablePath, policy, repair); err != nil {
		return err
	}
	return verifyComputerUseAuthPlugin(ctx, r, pluginPath, policy, localTeamID)
}

func readComputerUseAuthPluginRepair(
	ctx context.Context,
	executablePath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) (computerUseAuthPluginRepair, error) {
	executableInfo, err := os.Stat(executablePath)
	if err != nil {
		return computerUseAuthPluginRepair{}, logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_executable_stat_failed", fmt.Errorf("stat authorization plugin executable %s: %w", executablePath, err))
	}
	data, err := os.ReadFile(executablePath)
	if err != nil {
		return computerUseAuthPluginRepair{}, logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_executable_read_failed", fmt.Errorf("read authorization plugin executable %s: %w", executablePath, err))
	}
	updated, replacements, alreadyPatched, err := replaceStandaloneTeamID(
		data,
		policy.UpstreamTrustedTeamID,
		localTeamID,
	)
	if err != nil {
		return computerUseAuthPluginRepair{}, logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_executable_repair_failed", fmt.Errorf("repair authorization plugin executable %s: %w", executablePath, err))
	}
	if replacements == 0 && !alreadyPatched {
		return computerUseAuthPluginRepair{}, fmt.Errorf("authorization plugin executable %s contained neither trusted team %s nor %s",
			executablePath, policy.UpstreamTrustedTeamID, localTeamID)
	}
	return computerUseAuthPluginRepair{
		Updated:      updated,
		Permissions:  executableInfo.Mode().Perm(),
		Replacements: replacements,
	}, nil
}

func stageInstallComputerUseAuthPlugin(
	ctx context.Context,
	r *patch.Runner,
	pluginPath string,
	executablePath string,
	policy targets.ComputerUsePolicy,
	repair computerUseAuthPluginRepair,
) error {
	r.Log.InfoContext(ctx, "patch.computer_use_auth_plugin.stage_install",
		"plugin_path", pluginPath,
		"executable_path", executablePath,
		"replacements", repair.Replacements)
	tempDir, err := os.MkdirTemp("", "desktop-via-clyde-auth-plugin-*")
	if err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_temp_dir_failed", fmt.Errorf("create authorization plugin staging dir: %w", err))
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	stagingPath := filepath.Join(tempDir, filepath.Base(pluginPath))
	if err := r.Run(ctx, "/usr/bin/rsync", "-a", pluginPath+"/", stagingPath+"/"); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_stage_failed", fmt.Errorf("stage authorization plugin: %w", err))
	}
	stagedExecutablePath := filepath.Join(stagingPath, filepath.FromSlash(policy.AuthPluginExecutable))
	if err := writeExistingFile(stagedExecutablePath, repair.Permissions, repair.Updated); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_executable_write_failed", fmt.Errorf("write authorization plugin executable %s: %w", stagedExecutablePath, err))
	}
	if repair.Replacements > 0 {
		patch.Note(r, fmt.Sprintf("computer-use: replaced %d login authorization trusted service team occurrence(s) in %s", repair.Replacements, executablePath))
	} else {
		patch.Note(r, "computer-use: "+executablePath+" already trusts login authorization service team; refreshing signature")
	}

	id, err := patch.ResolveSignIdentity(ctx, false)
	if err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_identity_failed",
			fmt.Errorf("resolve signing identity: %w", err))
	}
	if err := signAndVerifyComputerUseAuthPlugin(ctx, r, stagingPath, id); err != nil {
		return err
	}
	return installComputerUseAuthPlugin(ctx, r, stagingPath, pluginPath)
}

func signAndVerifyComputerUseAuthPlugin(ctx context.Context, r *patch.Runner, stagingPath string, id string) error {
	patch.Note(r, "computer-use: sign authorization plugin "+stagingPath)
	if err := r.Run(ctx, "/usr/bin/codesign", patch.CodesignRuntimeArgs(id, stagingPath)...); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_sign_failed",
			fmt.Errorf("sign authorization plugin: %w", err))
	}
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", stagingPath); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_stage_verify_failed",
			fmt.Errorf("verify staged authorization plugin: %w", err))
	}
	return nil
}

func installComputerUseAuthPlugin(ctx context.Context, r *patch.Runner, stagingPath string, pluginPath string) error {
	patch.Note(r, fmt.Sprintf("computer-use: install authorization plugin %s -> %s with sudo", stagingPath, pluginPath))
	if err := r.Run(ctx, "/usr/bin/sudo", "/usr/bin/rsync", "-rltp", "--delete", stagingPath+"/", pluginPath+"/"); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_install_failed",
			fmt.Errorf("install authorization plugin: %w", err))
	}
	return nil
}

func computerUseAuthPluginDryRunStagingPath(pluginPath string) string {
	return filepath.Join(os.TempDir(), "desktop-via-clyde-auth-plugin", filepath.Base(pluginPath))
}

func ensureComputerUseAppPath(appPath string) error {
	info, err := os.Stat(appPath)
	if err != nil {
		return logComputerUsePatchErrorNoContext("patch.computer_use_app_stat_failed",
			fmt.Errorf("stat helper bundle %s: %w", appPath, err))
	}
	if !info.IsDir() {
		return fmt.Errorf("helper path is not a directory: %s", appPath)
	}
	return nil
}

func verifyComputerUseAuthPlugin(
	ctx context.Context,
	r *patch.Runner,
	pluginPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	if r.DryRun {
		return nil
	}
	patch.Note(r, "computer-use: verify authorization plugin signature")
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", pluginPath); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_verify_failed", fmt.Errorf("verify authorization plugin: %w", err))
	}
	executablePath := filepath.Join(pluginPath, filepath.FromSlash(policy.AuthPluginExecutable))
	data, err := os.ReadFile(executablePath)
	if err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_auth_plugin_verify_read_failed", fmt.Errorf("read authorization plugin executable %s: %w", executablePath, err))
	}
	if countStandaloneToken(data, policy.UpstreamTrustedTeamID) > 0 {
		return fmt.Errorf("%s still contains upstream trusted team %s", executablePath, policy.UpstreamTrustedTeamID)
	}
	if countStandaloneToken(data, localTeamID) == 0 {
		return fmt.Errorf("%s does not contain local trusted team %s", executablePath, localTeamID)
	}
	return nil
}

func patchComputerUseBundle(
	ctx context.Context,
	r *patch.Runner,
	_ targets.Target,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	if err := patchComputerUseTrustedTeam(ctx, r, appPath, policy, localTeamID); err != nil {
		return err
	}
	if err := patchComputerUseTeamRequirements(ctx, r, appPath, policy, localTeamID); err != nil {
		return err
	}
	id, err := patch.ResolveSignIdentity(ctx, r.DryRun)
	if err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_helper_identity_failed",
			fmt.Errorf("resolve signing identity: %w", err))
	}
	if err := signComputerUseHelper(ctx, r, appPath, policy, id); err != nil {
		return err
	}
	if err := verifyComputerUseHelper(ctx, r, appPath, policy, localTeamID); err != nil {
		return err
	}
	return nil
}

func patchComputerUseTrustedTeam(
	ctx context.Context,
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relPath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		patch.RecordTrace(r, ActionRepairComputerUseTrustedTeam, "", binaryPath)
		patch.Note(r, "computer-use: repair trusted sender team in "+binaryPath)
		if r.DryRun {
			continue
		}
		info, err := os.Stat(binaryPath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_binary_stat_failed", fmt.Errorf("stat helper binary %s: %w", binaryPath, err))
		}
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_binary_read_failed", fmt.Errorf("read helper binary %s: %w", binaryPath, err))
		}
		updated, replacements, alreadyPatched, err := replaceStandaloneTeamID(
			data,
			policy.UpstreamTrustedTeamID,
			localTeamID,
		)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_binary_repair_failed", fmt.Errorf("repair helper binary %s: %w", binaryPath, err))
		}
		if replacements == 0 && alreadyPatched {
			patch.Note(r, fmt.Sprintf("computer-use: %s already trusts team %s", binaryPath, localTeamID))
			continue
		}
		if replacements == 0 {
			return fmt.Errorf("helper binary %s contained neither trusted team %s nor %s",
				binaryPath, policy.UpstreamTrustedTeamID, localTeamID)
		}
		if err := writeExistingFile(binaryPath, info.Mode().Perm(), updated); err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_binary_write_failed", fmt.Errorf("write helper binary %s: %w", binaryPath, err))
		}
		patch.Note(r, fmt.Sprintf("computer-use: replaced %d trusted sender team occurrence(s) in %s", replacements, binaryPath))
	}
	return nil
}

func patchComputerUseTeamRequirements(
	ctx context.Context,
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relPath := range policy.TeamRequirementPlists {
		plistPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		patch.RecordTrace(r, ActionRepairComputerUseRequirement, "", plistPath)
		patch.Note(r, "computer-use: repair trusted parent team in "+plistPath)
		if r.DryRun {
			continue
		}
		info, err := os.Stat(plistPath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_requirement_plist_stat_failed", fmt.Errorf("stat helper requirement plist %s: %w", plistPath, err))
		}
		data, err := os.ReadFile(plistPath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_requirement_plist_read_failed", fmt.Errorf("read helper requirement plist %s: %w", plistPath, err))
		}
		updated, changed, alreadyPatched, err := replaceTeamRequirementPlist(
			data,
			policy.UpstreamTrustedTeamID,
			localTeamID,
		)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_requirement_plist_repair_failed", fmt.Errorf("repair helper requirement plist %s: %w", plistPath, err))
		}
		if !changed && alreadyPatched {
			patch.Note(r, fmt.Sprintf("computer-use: %s already trusts parent team %s", plistPath, localTeamID))
			continue
		}
		if !changed {
			return fmt.Errorf("helper requirement plist %s contained neither trusted team %s nor %s",
				plistPath, policy.UpstreamTrustedTeamID, localTeamID)
		}
		if err := writeExistingFile(plistPath, info.Mode().Perm(), updated); err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_requirement_plist_write_failed", fmt.Errorf("write helper requirement plist %s: %w", plistPath, err))
		}
		patch.Note(r, "computer-use: replaced trusted parent team in "+plistPath)
	}
	return nil
}

func signComputerUseHelper(
	ctx context.Context,
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	id string,
) error {
	for _, target := range policy.SignTargets {
		codePath := computerUseSignTargetPath(appPath, target.Path)
		patch.RecordTrace(r, ActionSignComputerUseHelper, "", codePath)
		if !r.DryRun {
			if _, err := os.Stat(codePath); err != nil {
				return logComputerUsePatchError(ctx, "patch.computer_use_sign_target_stat_failed", fmt.Errorf("stat helper code target %s: %w", codePath, err))
			}
		}
		if target.Entitlements == nil {
			patch.Note(r, fmt.Sprintf("computer-use: sign %s without entitlements", codePath))
			if err := r.Run(ctx, "/usr/bin/codesign", patch.CodesignRuntimeArgs(id, codePath)...); err != nil {
				return logComputerUsePatchError(ctx, "patch.computer_use_sign_target_failed", fmt.Errorf("sign helper code target %s: %w", codePath, err))
			}
			continue
		}
		entFile, err := patch.WriteAugmentedEntitlementsFileAllowEmpty(
			ctx,
			r,
			"computer-use-"+target.Path,
			codePath,
			targets.EntitlementsPolicy{
				Strip:                       append([]string(nil), target.Entitlements.Strip...),
				RequiredBooleanEntitlements: append([]string(nil), target.Entitlements.RequiredBooleanEntitlements...),
			},
		)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_entitlements_failed", fmt.Errorf("helper entitlements for %s: %w", codePath, err))
		}
		patch.Note(r, fmt.Sprintf("computer-use: sign %s with repaired entitlements", codePath))
		if err := r.Run(ctx, "/usr/bin/codesign", patch.CodesignRuntimeEntitlementsArgs(id, entFile, codePath)...); err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_sign_target_failed", fmt.Errorf("sign helper code target %s: %w", codePath, err))
		}
	}
	return nil
}

func computerUseSignTargetPath(appPath string, relPath string) string {
	if relPath == "." || relPath == "" {
		return appPath
	}
	return filepath.Join(appPath, filepath.FromSlash(relPath))
}

func verifyComputerUseHelper(
	ctx context.Context,
	r *patch.Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	if r.DryRun {
		return nil
	}
	patch.Note(r, "computer-use: verify helper signature")
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", appPath); err != nil {
		return logComputerUsePatchError(ctx, "patch.computer_use_verify_bundle_failed", fmt.Errorf("verify helper bundle: %w", err))
	}
	for _, target := range policy.SignTargets {
		if target.Entitlements == nil {
			continue
		}
		codePath := computerUseSignTargetPath(appPath, target.Path)
		if err := patch.VerifyBooleanEntitlements(ctx, r, codePath, target.Entitlements.RequiredBooleanEntitlements); err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_required_entitlements_failed",
				fmt.Errorf("verify required entitlements %s: %w", codePath, err))
		}
		if err := patch.VerifyAbsentEntitlements(ctx, r, codePath, target.Entitlements.Strip); err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_absent_entitlements_failed",
				fmt.Errorf("verify absent entitlements %s: %w", codePath, err))
		}
	}
	for _, relPath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_binary_read_failed",
				fmt.Errorf("read helper binary %s: %w", binaryPath, err))
		}
		if countStandaloneToken(data, policy.UpstreamTrustedTeamID) > 0 {
			return fmt.Errorf("%s still contains upstream trusted team %s", binaryPath, policy.UpstreamTrustedTeamID)
		}
		if countStandaloneToken(data, localTeamID) == 0 {
			return fmt.Errorf("%s does not contain local trusted team %s", binaryPath, localTeamID)
		}
	}
	for _, relPath := range policy.TeamRequirementPlists {
		plistPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		data, err := os.ReadFile(plistPath)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_requirement_plist_read_failed", fmt.Errorf("read helper requirement plist %s: %w", plistPath, err))
		}
		teamID, err := teamRequirementPlistTeamID(data)
		if err != nil {
			return logComputerUsePatchError(ctx, "patch.computer_use_verify_requirement_team_read_failed", fmt.Errorf("read helper requirement plist team %s: %w", plistPath, err))
		}
		if teamID != localTeamID {
			return fmt.Errorf("%s trusts parent team %s, want %s", plistPath, teamID, localTeamID)
		}
	}
	return nil
}

func teamIDFromSignIdentity(identity string) (string, error) {
	m := signIdentityTeamRE.FindStringSubmatch(identity)
	if m == nil {
		return "", fmt.Errorf("could not parse Team ID from signing identity %q", identity)
	}
	teamID := m[1]
	if err := validateTeamID(teamID, "local signing team ID"); err != nil {
		return "", err
	}
	return teamID, nil
}

func validateTeamID(teamID string, label string) error {
	if len(teamID) != 10 {
		return fmt.Errorf("%s %q must be exactly 10 ASCII alphanumeric characters", label, teamID)
	}
	for _, b := range teamID {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' {
			continue
		}
		return fmt.Errorf("%s %q must be exactly 10 ASCII alphanumeric characters", label, teamID)
	}
	return nil
}

func replaceStandaloneTeamID(data []byte, oldID string, newID string) ([]byte, int, bool, error) {
	if err := validateTeamID(oldID, "old team ID"); err != nil {
		return nil, 0, false, err
	}
	if err := validateTeamID(newID, "new team ID"); err != nil {
		return nil, 0, false, err
	}
	if len(oldID) != len(newID) {
		return nil, 0, false, fmt.Errorf("team IDs must have equal byte length")
	}
	out := append([]byte(nil), data...)
	oldBytes := []byte(oldID)
	newBytes := []byte(newID)
	replacements := 0
	for offset := 0; ; {
		idx := bytes.Index(out[offset:], oldBytes)
		if idx < 0 {
			break
		}
		start := offset + idx
		end := start + len(oldBytes)
		if hasStandaloneTokenBoundary(out, start, end) {
			copy(out[start:end], newBytes)
			replacements++
		}
		offset = end
	}
	alreadyPatched := replacements == 0 && countStandaloneToken(out, newID) > 0
	return out, replacements, alreadyPatched, nil
}

func replaceTeamRequirementPlist(data []byte, oldID string, newID string) ([]byte, bool, bool, error) {
	if err := validateTeamID(oldID, "old team ID"); err != nil {
		return nil, false, false, err
	}
	if err := validateTeamID(newID, "new team ID"); err != nil {
		return nil, false, false, err
	}
	var requirement teamRequirementPlist
	if _, err := plist.Unmarshal(data, &requirement); err != nil {
		return nil, false, false, logComputerUsePatchErrorNoContext("patch.computer_use_requirement_plist_unmarshal_failed", fmt.Errorf("unmarshal requirement plist: %w", err))
	}
	if requirement.TeamIdentifier == newID {
		return data, false, true, nil
	}
	if requirement.TeamIdentifier != oldID {
		return nil, false, false, fmt.Errorf("team-identifier %q is neither %q nor %q",
			requirement.TeamIdentifier, oldID, newID)
	}
	requirement.TeamIdentifier = newID
	out, err := plist.MarshalIndent(requirement, plist.XMLFormat, "\t")
	if err != nil {
		return nil, false, false, logComputerUsePatchErrorNoContext("patch.computer_use_requirement_plist_marshal_failed", fmt.Errorf("marshal requirement plist: %w", err))
	}
	return out, true, false, nil
}

func teamRequirementPlistTeamID(data []byte) (string, error) {
	var requirement teamRequirementPlist
	if _, err := plist.Unmarshal(data, &requirement); err != nil {
		return "", logComputerUsePatchErrorNoContext("patch.computer_use_requirement_team_unmarshal_failed", fmt.Errorf("unmarshal requirement plist: %w", err))
	}
	if requirement.TeamIdentifier == "" {
		return "", fmt.Errorf("missing team-identifier")
	}
	return requirement.TeamIdentifier, nil
}

func writeExistingFile(path string, permissions os.FileMode, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, permissions)
	if err != nil {
		return logComputerUsePatchErrorNoContext("patch.write_existing_file_open_failed", fmt.Errorf("open %s for rewrite: %w", path, err))
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(data); err != nil {
		return logComputerUsePatchErrorNoContext("patch.write_existing_file_write_failed", fmt.Errorf("write %s: %w", path, err))
	}
	return nil
}

func countStandaloneToken(data []byte, token string) int {
	tokenBytes := []byte(token)
	count := 0
	for offset := 0; ; {
		idx := bytes.Index(data[offset:], tokenBytes)
		if idx < 0 {
			return count
		}
		start := offset + idx
		end := start + len(tokenBytes)
		if hasStandaloneTokenBoundary(data, start, end) {
			count++
		}
		offset = end
	}
}

func hasStandaloneTokenBoundary(data []byte, start int, end int) bool {
	if start > 0 && isTeamTokenByte(data[start-1]) {
		return false
	}
	if end < len(data) && isTeamTokenByte(data[end]) {
		return false
	}
	return true
}

func isTeamTokenByte(b byte) bool {
	if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' {
		return true
	}
	return b == '.' || b == '_' || b == '-'
}

func logComputerUseRegistrationError(message string, err error) error {
	computerUseLog.Error("computeruse.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}

func logComputerUsePatchError(ctx context.Context, event string, err error) error {
	_ = patch.LogError(ctx, event, err)
	return err
}

func logComputerUsePatchErrorNoContext(event string, err error) error {
	_ = patch.LogErrorNoContext(event, err)
	return err
}
