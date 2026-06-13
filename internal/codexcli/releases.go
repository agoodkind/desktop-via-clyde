package codexcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/patch"
)

func maybeReuseInstalledRelease(
	ctx context.Context,
	r *patch.Runner,
	packageHome string,
	installDir string,
	packageBinaryPath string,
	packageVariant string,
	commandName string,
	version string,
	target string,
	forceRebuild bool,
) (string, bool, error) {
	log := codexcliLog.With("function", "maybeReuseInstalledRelease")
	if forceRebuild {
		notef(r, "codex-cli: force rebuild requested, skipping installed release reuse")
		return "", false, nil
	}
	releasePath := latestMainReleaseDir(packageHome)
	if _, err := os.Stat(releasePath); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		log.ErrorContext(ctx, "codexcli.maybe_reuse_installed_release.stat_failed", "err", err)
		return "", false, fmt.Errorf("stat release dir %s: %w", releasePath, err)
	}
	notef(r, "codex-cli: found installed latest-main release "+releasePath)
	reuseRejected, reuseReason := releaseReuseRejected(ctx, releasePath, target, packageBinaryPath, packageVariant, version)
	if reuseRejected {
		notef(r, "codex-cli: installed release reuse rejected: "+reuseReason)
		return "", false, nil
	}
	notef(r, "codex-cli: reusing verified latest-main release "+releasePath)
	if err := relinkInstalledRelease(ctx, r, packageHome, installDir, releasePath, packageBinaryPath, commandName); err != nil {
		return "", false, err
	}
	if err := verifyInstalledCommand(ctx, r, installDir, commandName); err != nil {
		return "", false, err
	}
	return releasePath, true, nil
}

func releaseReuseRejected(
	ctx context.Context,
	releasePath string,
	target string,
	packageBinaryPath string,
	packageVariant string,
	version string,
) (bool, string) {
	verifyErr := verifyReleaseCandidate(ctx, releasePath, target, packageBinaryPath, packageVariant, version)
	if verifyErr == nil {
		return false, ""
	}
	return true, verifyErr.Error()
}

func verifyReleaseCandidate(
	ctx context.Context,
	releasePath string,
	target string,
	packageBinaryPath string,
	packageVariant string,
	version string,
) error {
	log := codexcliLog.With("function", "verifyReleaseCandidate")
	metadata, err := readPackageMetadata(releasePath, packageVariant)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.verify_release_candidate.metadata_failed", "err", err)
		return err
	}
	if metadata.Version != version {
		log.ErrorContext(ctx, "codexcli.verify_release_candidate.version_mismatch", "err", fmt.Errorf("release version mismatch"))
		return fmt.Errorf("release version mismatch: got %s want %s", metadata.Version, version)
	}
	if metadata.Target != target {
		log.ErrorContext(ctx, "codexcli.verify_release_candidate.target_mismatch", "err", fmt.Errorf("release target mismatch"))
		return fmt.Errorf("release target mismatch: got %s want %s", metadata.Target, target)
	}
	binaryPath := filepath.Join(releasePath, filepath.FromSlash(packageBinaryPath))
	if _, err := os.Stat(binaryPath); err != nil {
		log.ErrorContext(ctx, "codexcli.verify_release_candidate.binary_missing", "err", err)
		return fmt.Errorf("stat release binary: %w", err)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", binaryPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.ErrorContext(ctx, "codexcli.verify_release_candidate.codesign_failed", "err", err)
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("codesign verify release binary: %s", message)
	}
	return nil
}

func relinkInstalledRelease(
	ctx context.Context,
	r *patch.Runner,
	packageHome string,
	installDir string,
	releasePath string,
	packageBinaryPath string,
	commandName string,
) error {
	log := codexcliLog.With("function", "relinkInstalledRelease")
	standaloneRoot := filepath.Join(packageHome, "packages", "standalone")
	currentLink := filepath.Join(standaloneRoot, "current")
	binPath := filepath.Join(installDir, commandName)
	if err := os.MkdirAll(standaloneRoot, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.relink_installed_release.mkdir_standalone_failed", "err", err)
		return fmt.Errorf("create standalone root: %w", err)
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.relink_installed_release.mkdir_install_failed", "err", err)
		return fmt.Errorf("create install dir: %w", err)
	}
	notef(r, "codex-cli: update "+currentLink+" -> "+releasePath)
	if err := replaceSymlink(currentLink, releasePath); err != nil {
		log.ErrorContext(ctx, "codexcli.relink_installed_release.current_link_failed", "err", err)
		return fmt.Errorf("update current release link: %w", err)
	}
	visibleLinkTarget := filepath.Join(currentLink, filepath.FromSlash(packageBinaryPath))
	notef(r, "codex-cli: update "+binPath+" -> "+visibleLinkTarget)
	if err := replaceSymlink(binPath, visibleLinkTarget); err != nil {
		log.ErrorContext(ctx, "codexcli.relink_installed_release.visible_link_failed", "err", err)
		return fmt.Errorf("update visible command link: %w", err)
	}
	return nil
}
