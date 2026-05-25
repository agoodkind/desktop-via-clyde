package patch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"howett.net/plist"
)

var signIdentityTeamRE = regexp.MustCompile(`\(([A-Za-z0-9]{10})\)\s*$`)

type teamRequirementPlist struct {
	TeamIdentifier string `plist:"team-identifier"`
}

func stepPatchBundledComputerUse(ctx context.Context, r *Runner, t targets.Target) error {
	if t.ComputerUse == nil {
		return nil
	}
	policy := *t.ComputerUse
	localTeamID, err := validateComputerUsePolicy(policy)
	if err != nil {
		return err
	}
	appPath := bundledComputerUseAppPath(t.AppPath, policy)
	notef(r, fmt.Sprintf("target=%s step 5b: repair bundled Codex Computer Use helper at %s", t.ID, appPath))
	if !r.DryRun {
		if err := ensureComputerUseAppPath(appPath); err != nil {
			return err
		}
	}
	return patchComputerUseBundle(ctx, r, t, appPath, policy, localTeamID, false)
}

func stepPatchComputerUse(ctx context.Context, r *Runner, t targets.Target) error {
	if t.ComputerUse == nil {
		return nil
	}
	policy := *t.ComputerUse
	localTeamID, err := validateComputerUsePolicy(policy)
	if err != nil {
		return err
	}
	if filepath.Clean(t.AppPath) != filepath.Clean(policy.HostAppPath) {
		notef(r, fmt.Sprintf("target=%s step 7b: skipped helper repair for non-canonical app path %s", t.ID, t.AppPath))
		return nil
	}
	appPath := computerUseAppPath(policy)
	notef(r, fmt.Sprintf("target=%s step 7b: repair Codex Computer Use helper at %s", t.ID, appPath))
	if r.DryRun {
		if err := patchComputerUseBundle(ctx, r, t, appPath, policy, localTeamID, true); err != nil {
			return err
		}
		return stepPatchComputerUseCache(ctx, r, t, policy, localTeamID)
	}

	if err := ensureComputerUseAppPath(appPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			notef(r, fmt.Sprintf("target=%s step 7b: helper bundle not found, skipping", t.ID))
			return stepPatchComputerUseCache(ctx, r, t, policy, localTeamID)
		}
		return err
	}
	if err := patchComputerUseBundle(ctx, r, t, appPath, policy, localTeamID, true); err != nil {
		return err
	}
	return stepPatchComputerUseCache(ctx, r, t, policy, localTeamID)
}

func validateComputerUsePolicy(policy targets.ComputerUsePolicy) (string, error) {
	if policy.BundledAppPath == "" {
		return "", fmt.Errorf("codex computer use policy missing bundled app path")
	}
	if policy.AppPathFromHome == "" {
		return "", fmt.Errorf("codex computer use policy missing installed app path")
	}
	localTeamID, err := teamIDFromSignIdentity(paths.SignIdentity)
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

func stepPatchComputerUseCache(
	ctx context.Context,
	r *Runner,
	t targets.Target,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relGlob := range policy.CacheAppGlobsFromHome {
		pattern := filepath.Join(paths.Home(), filepath.FromSlash(relGlob))
		notef(r, fmt.Sprintf("target=%s step 7c: scan Codex Computer Use cache helpers at %s", t.ID, pattern))
		if r.DryRun {
			continue
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_cache_glob_failed", fmt.Errorf("glob helper cache %s: %w", pattern, err))
		}
		if len(matches) == 0 {
			notef(r, fmt.Sprintf("target=%s step 7c: no cached helper bundles matched %s", t.ID, pattern))
			continue
		}
		sort.Strings(matches)
		for _, appPath := range matches {
			notef(r, fmt.Sprintf("target=%s step 7c: repair cached Codex Computer Use helper at %s", t.ID, appPath))
			if err := ensureComputerUseAppPath(appPath); err != nil {
				return err
			}
			if err := patchComputerUseBundle(ctx, r, t, appPath, policy, localTeamID, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureComputerUseAppPath(appPath string) error {
	info, err := os.Stat(appPath)
	if err != nil {
		return logPatchErrorNoContext("patch.computer_use_app_stat_failed", fmt.Errorf("stat helper bundle %s: %w", appPath, err))
	}
	if !info.IsDir() {
		return fmt.Errorf("helper path is not a directory: %s", appPath)
	}
	return nil
}

func patchComputerUseBundle(
	ctx context.Context,
	r *Runner,
	t targets.Target,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
	backup bool,
) error {
	if backup {
		if err := backupComputerUseHelper(ctx, r, t, appPath); err != nil {
			return err
		}
	}
	if err := patchComputerUseTrustedTeam(ctx, r, appPath, policy, localTeamID); err != nil {
		return err
	}
	if err := patchComputerUseTeamRequirements(ctx, r, appPath, policy, localTeamID); err != nil {
		return err
	}
	id, err := resolveSignIdentity(ctx, r.DryRun)
	if err != nil {
		return err
	}
	if err := signComputerUseHelper(ctx, r, appPath, policy, id); err != nil {
		return err
	}
	if err := verifyComputerUseHelper(ctx, r, appPath, policy, localTeamID); err != nil {
		return err
	}
	return nil
}

func backupComputerUseHelper(ctx context.Context, r *Runner, t targets.Target, appPath string) error {
	backup := computerUseBackupBundle(t, appPath)
	if !r.DryRun {
		if _, err := os.Stat(backup); err == nil {
			notef(r, fmt.Sprintf("target=%s step 7b: helper backup exists at %s, skipping", t.ID, backup))
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return logPatchError(ctx, "patch.computer_use_backup_dir_failed", fmt.Errorf("create helper backup dir %s: %w", filepath.Dir(backup), err))
		}
	}
	notef(r, fmt.Sprintf("target=%s step 7b: backup helper %s -> %s", t.ID, appPath, backup))
	return r.Run(ctx, "/usr/bin/rsync", "-a", appPath+"/", backup+"/")
}

func computerUseBackupBundle(t targets.Target, appPath string) string {
	return filepath.Join(paths.BackupDir(t), "computer-use", filepath.Base(appPath))
}

func patchComputerUseTrustedTeam(
	ctx context.Context,
	r *Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relPath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		notef(r, "computer-use: repair trusted sender team in "+binaryPath)
		if r.DryRun {
			continue
		}
		info, err := os.Stat(binaryPath)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_binary_stat_failed", fmt.Errorf("stat helper binary %s: %w", binaryPath, err))
		}
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_binary_read_failed", fmt.Errorf("read helper binary %s: %w", binaryPath, err))
		}
		updated, replacements, alreadyPatched, err := replaceStandaloneTeamID(
			data,
			policy.UpstreamTrustedTeamID,
			localTeamID,
		)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_binary_repair_failed", fmt.Errorf("repair helper binary %s: %w", binaryPath, err))
		}
		if replacements == 0 && alreadyPatched {
			notef(r, fmt.Sprintf("computer-use: %s already trusts team %s", binaryPath, localTeamID))
			continue
		}
		if replacements == 0 {
			return fmt.Errorf("helper binary %s contained neither trusted team %s nor %s",
				binaryPath, policy.UpstreamTrustedTeamID, localTeamID)
		}
		if err := writeExistingFile(binaryPath, info.Mode().Perm(), updated); err != nil {
			return logPatchError(ctx, "patch.computer_use_binary_write_failed", fmt.Errorf("write helper binary %s: %w", binaryPath, err))
		}
		notef(r, fmt.Sprintf("computer-use: replaced %d trusted sender team occurrence(s) in %s", replacements, binaryPath))
	}
	return nil
}

func patchComputerUseTeamRequirements(
	ctx context.Context,
	r *Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relPath := range policy.TeamRequirementPlists {
		plistPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		notef(r, "computer-use: repair trusted parent team in "+plistPath)
		if r.DryRun {
			continue
		}
		info, err := os.Stat(plistPath)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_requirement_plist_stat_failed", fmt.Errorf("stat helper requirement plist %s: %w", plistPath, err))
		}
		data, err := os.ReadFile(plistPath)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_requirement_plist_read_failed", fmt.Errorf("read helper requirement plist %s: %w", plistPath, err))
		}
		updated, changed, alreadyPatched, err := replaceTeamRequirementPlist(
			data,
			policy.UpstreamTrustedTeamID,
			localTeamID,
		)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_requirement_plist_repair_failed", fmt.Errorf("repair helper requirement plist %s: %w", plistPath, err))
		}
		if !changed && alreadyPatched {
			notef(r, fmt.Sprintf("computer-use: %s already trusts parent team %s", plistPath, localTeamID))
			continue
		}
		if !changed {
			return fmt.Errorf("helper requirement plist %s contained neither trusted team %s nor %s",
				plistPath, policy.UpstreamTrustedTeamID, localTeamID)
		}
		if err := writeExistingFile(plistPath, info.Mode().Perm(), updated); err != nil {
			return logPatchError(ctx, "patch.computer_use_requirement_plist_write_failed", fmt.Errorf("write helper requirement plist %s: %w", plistPath, err))
		}
		notef(r, "computer-use: replaced trusted parent team in "+plistPath)
	}
	return nil
}

func signComputerUseHelper(
	ctx context.Context,
	r *Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	id string,
) error {
	for _, target := range policy.SignTargets {
		codePath := computerUseSignTargetPath(appPath, target.Path)
		if !r.DryRun {
			if _, err := os.Stat(codePath); err != nil {
				return logPatchError(ctx, "patch.computer_use_sign_target_stat_failed", fmt.Errorf("stat helper code target %s: %w", codePath, err))
			}
		}
		if target.Entitlements == nil {
			notef(r, fmt.Sprintf("computer-use: sign %s without entitlements", codePath))
			if err := r.Run(ctx, "/usr/bin/codesign", codesignRuntimeArgs(id, codePath)...); err != nil {
				return logPatchError(ctx, "patch.computer_use_sign_target_failed", fmt.Errorf("sign helper code target %s: %w", codePath, err))
			}
			continue
		}
		entFile, err := writeAugmentedEntitlementsFileAllowEmpty(
			ctx,
			r,
			"computer-use-"+target.Path,
			codePath,
			*target.Entitlements,
		)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_entitlements_failed", fmt.Errorf("helper entitlements for %s: %w", codePath, err))
		}
		notef(r, fmt.Sprintf("computer-use: sign %s with repaired entitlements", codePath))
		if err := r.Run(ctx, "/usr/bin/codesign", codesignRuntimeEntitlementsArgs(id, entFile, codePath)...); err != nil {
			return logPatchError(ctx, "patch.computer_use_sign_target_failed", fmt.Errorf("sign helper code target %s: %w", codePath, err))
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
	r *Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	if r.DryRun {
		return nil
	}
	notef(r, "computer-use: verify helper signature")
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", appPath); err != nil {
		return logPatchError(ctx, "patch.computer_use_verify_bundle_failed", fmt.Errorf("verify helper bundle: %w", err))
	}
	for _, target := range policy.SignTargets {
		if target.Entitlements == nil {
			continue
		}
		codePath := computerUseSignTargetPath(appPath, target.Path)
		if err := verifyBooleanEntitlements(ctx, r, codePath, target.Entitlements.RequiredBooleanEntitlements); err != nil {
			return err
		}
		if err := verifyAbsentEntitlements(ctx, r, codePath, target.Entitlements.Strip); err != nil {
			return err
		}
	}
	for _, relPath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_verify_binary_read_failed", fmt.Errorf("read helper binary %s: %w", binaryPath, err))
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
			return logPatchError(ctx, "patch.computer_use_verify_requirement_plist_read_failed", fmt.Errorf("read helper requirement plist %s: %w", plistPath, err))
		}
		teamID, err := teamRequirementPlistTeamID(data)
		if err != nil {
			return logPatchError(ctx, "patch.computer_use_verify_requirement_team_read_failed", fmt.Errorf("read helper requirement plist team %s: %w", plistPath, err))
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
		return nil, false, false, logPatchErrorNoContext("patch.computer_use_requirement_plist_unmarshal_failed", fmt.Errorf("unmarshal requirement plist: %w", err))
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
		return nil, false, false, logPatchErrorNoContext("patch.computer_use_requirement_plist_marshal_failed", fmt.Errorf("marshal requirement plist: %w", err))
	}
	return out, true, false, nil
}

func teamRequirementPlistTeamID(data []byte) (string, error) {
	var requirement teamRequirementPlist
	if _, err := plist.Unmarshal(data, &requirement); err != nil {
		return "", logPatchErrorNoContext("patch.computer_use_requirement_team_unmarshal_failed", fmt.Errorf("unmarshal requirement plist: %w", err))
	}
	if requirement.TeamIdentifier == "" {
		return "", fmt.Errorf("missing team-identifier")
	}
	return requirement.TeamIdentifier, nil
}

func writeExistingFile(path string, permissions os.FileMode, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, permissions)
	if err != nil {
		return logPatchErrorNoContext("patch.write_existing_file_open_failed", fmt.Errorf("open %s for rewrite: %w", path, err))
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(data); err != nil {
		return logPatchErrorNoContext("patch.write_existing_file_write_failed", fmt.Errorf("write %s: %w", path, err))
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
