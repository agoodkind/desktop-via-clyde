package patch

import (
	"bytes"
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

func stepPatchBundledComputerUse(r *Runner, t targets.Target) error {
	if t.ComputerUse == nil {
		return nil
	}
	policy := *t.ComputerUse
	localTeamID, err := validateComputerUsePolicy(policy)
	if err != nil {
		return err
	}
	appPath := bundledComputerUseAppPath(t.AppPath, policy)
	r.Note("target=%s step 5b: repair bundled Codex Computer Use helper at %s", t.ID, appPath)
	if !r.DryRun {
		if err := ensureComputerUseAppPath(appPath); err != nil {
			return err
		}
	}
	return patchComputerUseBundle(r, t, appPath, policy, localTeamID, false)
}

func stepPatchComputerUse(r *Runner, t targets.Target) error {
	if t.ComputerUse == nil {
		return nil
	}
	policy := *t.ComputerUse
	localTeamID, err := validateComputerUsePolicy(policy)
	if err != nil {
		return err
	}
	if filepath.Clean(t.AppPath) != filepath.Clean(policy.HostAppPath) {
		r.Note("target=%s step 7b: skipped helper repair for non-canonical app path %s", t.ID, t.AppPath)
		return nil
	}
	appPath := computerUseAppPath(policy)
	r.Note("target=%s step 7b: repair Codex Computer Use helper at %s", t.ID, appPath)
	if !r.DryRun {
		if err := ensureComputerUseAppPath(appPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				r.Note("target=%s step 7b: helper bundle not found, skipping", t.ID)
			} else {
				return err
			}
		} else if err := patchComputerUseBundle(r, t, appPath, policy, localTeamID, true); err != nil {
			return err
		}
	} else if err := patchComputerUseBundle(r, t, appPath, policy, localTeamID, true); err != nil {
		return err
	}
	return stepPatchComputerUseCache(r, t, policy, localTeamID)
}

func validateComputerUsePolicy(policy targets.ComputerUsePolicy) (string, error) {
	if policy.BundledAppPath == "" {
		return "", fmt.Errorf("Codex Computer Use policy missing bundled app path")
	}
	if policy.AppPathFromHome == "" {
		return "", fmt.Errorf("Codex Computer Use policy missing installed app path")
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
	r *Runner,
	t targets.Target,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relGlob := range policy.CacheAppGlobsFromHome {
		pattern := filepath.Join(paths.Home(), filepath.FromSlash(relGlob))
		r.Note("target=%s step 7c: scan Codex Computer Use cache helpers at %s", t.ID, pattern)
		if r.DryRun {
			continue
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("glob helper cache %s: %w", pattern, err)
		}
		if len(matches) == 0 {
			r.Note("target=%s step 7c: no cached helper bundles matched %s", t.ID, pattern)
			continue
		}
		sort.Strings(matches)
		for _, appPath := range matches {
			r.Note("target=%s step 7c: repair cached Codex Computer Use helper at %s", t.ID, appPath)
			if err := ensureComputerUseAppPath(appPath); err != nil {
				return err
			}
			if err := patchComputerUseBundle(r, t, appPath, policy, localTeamID, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureComputerUseAppPath(appPath string) error {
	info, err := os.Stat(appPath)
	if err != nil {
		return fmt.Errorf("stat helper bundle %s: %w", appPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("helper path is not a directory: %s", appPath)
	}
	return nil
}

func patchComputerUseBundle(
	r *Runner,
	t targets.Target,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
	backup bool,
) error {
	if backup {
		if err := backupComputerUseHelper(r, t, appPath); err != nil {
			return err
		}
	}
	if err := patchComputerUseTrustedTeam(r, appPath, policy, localTeamID); err != nil {
		return err
	}
	if err := patchComputerUseTeamRequirements(r, appPath, policy, localTeamID); err != nil {
		return err
	}
	id, err := resolveSignIdentity(r.DryRun)
	if err != nil {
		return err
	}
	if err := signComputerUseHelper(r, appPath, policy, id); err != nil {
		return err
	}
	if err := verifyComputerUseHelper(r, appPath, policy, localTeamID); err != nil {
		return err
	}
	return nil
}

func backupComputerUseHelper(r *Runner, t targets.Target, appPath string) error {
	backup := computerUseBackupBundle(t, appPath)
	if !r.DryRun {
		if _, err := os.Stat(backup); err == nil {
			r.Note("target=%s step 7b: helper backup exists at %s, skipping", t.ID, backup)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return err
		}
	}
	r.Note("target=%s step 7b: backup helper %s -> %s", t.ID, appPath, backup)
	return r.Run("/usr/bin/rsync", "-a", appPath+"/", backup+"/")
}

func computerUseBackupBundle(t targets.Target, appPath string) string {
	return filepath.Join(paths.BackupDir(t), "computer-use", filepath.Base(appPath))
}

func patchComputerUseTrustedTeam(
	r *Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relPath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		r.Note("computer-use: repair trusted sender team in %s", binaryPath)
		if r.DryRun {
			continue
		}
		info, err := os.Stat(binaryPath)
		if err != nil {
			return fmt.Errorf("stat helper binary %s: %w", binaryPath, err)
		}
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			return fmt.Errorf("read helper binary %s: %w", binaryPath, err)
		}
		updated, replacements, alreadyPatched, err := replaceStandaloneTeamID(
			data,
			policy.UpstreamTrustedTeamID,
			localTeamID,
		)
		if err != nil {
			return fmt.Errorf("repair helper binary %s: %w", binaryPath, err)
		}
		if replacements == 0 && alreadyPatched {
			r.Note("computer-use: %s already trusts team %s", binaryPath, localTeamID)
			continue
		}
		if replacements == 0 {
			return fmt.Errorf("helper binary %s contained neither trusted team %s nor %s",
				binaryPath, policy.UpstreamTrustedTeamID, localTeamID)
		}
		if err := os.WriteFile(binaryPath, updated, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write helper binary %s: %w", binaryPath, err)
		}
		r.Note("computer-use: replaced %d trusted sender team occurrence(s) in %s", replacements, binaryPath)
	}
	return nil
}

func patchComputerUseTeamRequirements(
	r *Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	for _, relPath := range policy.TeamRequirementPlists {
		plistPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		r.Note("computer-use: repair trusted parent team in %s", plistPath)
		if r.DryRun {
			continue
		}
		info, err := os.Stat(plistPath)
		if err != nil {
			return fmt.Errorf("stat helper requirement plist %s: %w", plistPath, err)
		}
		data, err := os.ReadFile(plistPath)
		if err != nil {
			return fmt.Errorf("read helper requirement plist %s: %w", plistPath, err)
		}
		updated, changed, alreadyPatched, err := replaceTeamRequirementPlist(
			data,
			policy.UpstreamTrustedTeamID,
			localTeamID,
		)
		if err != nil {
			return fmt.Errorf("repair helper requirement plist %s: %w", plistPath, err)
		}
		if !changed && alreadyPatched {
			r.Note("computer-use: %s already trusts parent team %s", plistPath, localTeamID)
			continue
		}
		if !changed {
			return fmt.Errorf("helper requirement plist %s contained neither trusted team %s nor %s",
				plistPath, policy.UpstreamTrustedTeamID, localTeamID)
		}
		if err := os.WriteFile(plistPath, updated, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write helper requirement plist %s: %w", plistPath, err)
		}
		r.Note("computer-use: replaced trusted parent team in %s", plistPath)
	}
	return nil
}

func signComputerUseHelper(
	r *Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	id string,
) error {
	for _, target := range policy.SignTargets {
		codePath := computerUseSignTargetPath(appPath, target.Path)
		if !r.DryRun {
			if _, err := os.Stat(codePath); err != nil {
				return fmt.Errorf("stat helper code target %s: %w", codePath, err)
			}
		}
		if target.Entitlements == nil {
			r.Note("computer-use: sign %s without entitlements", codePath)
			if err := r.Run("/usr/bin/codesign", codesignRuntimeArgs(id, codePath)...); err != nil {
				return fmt.Errorf("sign helper code target %s: %w", codePath, err)
			}
			continue
		}
		entFile, err := writeAugmentedEntitlementsFileAllowEmpty(
			r,
			"computer-use-"+target.Path,
			codePath,
			*target.Entitlements,
		)
		if err != nil {
			return fmt.Errorf("helper entitlements for %s: %w", codePath, err)
		}
		r.Note("computer-use: sign %s with repaired entitlements", codePath)
		if err := r.Run("/usr/bin/codesign", codesignRuntimeEntitlementsArgs(id, entFile, codePath)...); err != nil {
			return fmt.Errorf("sign helper code target %s: %w", codePath, err)
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
	r *Runner,
	appPath string,
	policy targets.ComputerUsePolicy,
	localTeamID string,
) error {
	if r.DryRun {
		return nil
	}
	r.Note("computer-use: verify helper signature")
	if err := r.Run("/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", appPath); err != nil {
		return fmt.Errorf("verify helper bundle: %w", err)
	}
	for _, target := range policy.SignTargets {
		if target.Entitlements == nil {
			continue
		}
		codePath := computerUseSignTargetPath(appPath, target.Path)
		if err := verifyBooleanEntitlements(r, codePath, target.Entitlements.RequiredBooleanEntitlements); err != nil {
			return err
		}
		if err := verifyAbsentEntitlements(r, codePath, target.Entitlements.Strip); err != nil {
			return err
		}
	}
	for _, relPath := range policy.TeamPatchBinaries {
		binaryPath := filepath.Join(appPath, filepath.FromSlash(relPath))
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			return fmt.Errorf("read helper binary %s: %w", binaryPath, err)
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
			return fmt.Errorf("read helper requirement plist %s: %w", plistPath, err)
		}
		teamID, err := teamRequirementPlistTeamID(data)
		if err != nil {
			return fmt.Errorf("read helper requirement plist team %s: %w", plistPath, err)
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
	for i := 0; i < len(teamID); i++ {
		b := teamID[i]
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
		return nil, false, false, err
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
		return nil, false, false, err
	}
	return out, true, false, nil
}

func teamRequirementPlistTeamID(data []byte) (string, error) {
	var requirement teamRequirementPlist
	if _, err := plist.Unmarshal(data, &requirement); err != nil {
		return "", err
	}
	if requirement.TeamIdentifier == "" {
		return "", fmt.Errorf("missing team-identifier")
	}
	return requirement.TeamIdentifier, nil
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
