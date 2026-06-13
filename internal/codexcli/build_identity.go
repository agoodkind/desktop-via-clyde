package codexcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"goodkind.io/desktop-via-clyde/internal/patch"
)

const (
	buildHashLength = 12
)

type codexBuildIdentity struct {
	BaseVersion    string
	PackageVersion string
	TreeStamp      string
	Head           string
	Tree           string
	BuildHash      string
}

func newBuildIdentity(
	baseVersion string,
	head string,
	tree string,
	target string,
	buildMode BuildMode,
	packageVariant string,
	commandName string,
) codexBuildIdentity {
	treeStamp := shortTreeIdentifier(tree)
	buildHash := computeBuildHash(
		baseVersion,
		head,
		tree,
		target,
		string(buildMode),
		packageVariant,
		commandName,
	)
	packageVersion := fmt.Sprintf(
		"%s-main.%s+tree.%s.build.%s",
		baseVersion,
		head,
		treeStamp,
		buildHash,
	)
	return codexBuildIdentity{
		BaseVersion:    baseVersion,
		PackageVersion: packageVersion,
		TreeStamp:      treeStamp,
		Head:           head,
		Tree:           tree,
		BuildHash:      buildHash,
	}
}

func computeBuildHash(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))[:buildHashLength]
}

func latestStableRustVersion(ctx context.Context, r *patch.Runner, sourceDir string) (string, error) {
	log := codexcliLog.With("function", "latestStableRustVersion")
	output, err := r.RunCaptureStdout(ctx, "git", "-C", sourceDir, "ls-remote", "--tags", "origin", "refs/tags/rust-v*")
	if err != nil {
		log.ErrorContext(ctx, "codexcli.latest_stable_rust_version.tags_failed", "err", err)
		return "", fmt.Errorf("read Codex release tags: %w", err)
	}
	version, err := selectLatestStableRustVersion(output)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.latest_stable_rust_version.select_failed", "err", err)
		return "", err
	}
	return version.String(), nil
}

type rustVersion struct {
	Major int
	Minor int
	Patch int
}

func (version rustVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", version.Major, version.Minor, version.Patch)
}

func (version rustVersion) less(other rustVersion) bool {
	if version.Major != other.Major {
		return version.Major < other.Major
	}
	if version.Minor != other.Minor {
		return version.Minor < other.Minor
	}
	return version.Patch < other.Patch
}

func selectLatestStableRustVersion(output []byte) (rustVersion, error) {
	lines := strings.Split(string(output), "\n")
	latest := rustVersion{Major: 0, Minor: 0, Patch: 0}
	found := false
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		version, ok := parseStableRustTag(fields[1])
		if !ok {
			continue
		}
		if !found || latest.less(version) {
			latest = version
			found = true
		}
	}
	if !found {
		return rustVersion{Major: 0, Minor: 0, Patch: 0}, fmt.Errorf("no stable rust-v release tags found")
	}
	return latest, nil
}

func parseStableRustTag(ref string) (rustVersion, bool) {
	ref = strings.TrimSuffix(ref, "^{}")
	const prefix = "refs/tags/rust-v"
	tag, ok := strings.CutPrefix(ref, prefix)
	if !ok {
		return rustVersion{Major: 0, Minor: 0, Patch: 0}, false
	}
	parts := strings.Split(tag, ".")
	if len(parts) != 3 {
		return rustVersion{Major: 0, Minor: 0, Patch: 0}, false
	}
	major, ok := parseVersionPart(parts[0])
	if !ok {
		return rustVersion{Major: 0, Minor: 0, Patch: 0}, false
	}
	minor, ok := parseVersionPart(parts[1])
	if !ok {
		return rustVersion{Major: 0, Minor: 0, Patch: 0}, false
	}
	patch, ok := parseVersionPart(parts[2])
	if !ok {
		return rustVersion{Major: 0, Minor: 0, Patch: 0}, false
	}
	return rustVersion{Major: major, Minor: minor, Patch: patch}, true
}

func parseVersionPart(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, runeValue := range value {
		if !unicode.IsDigit(runeValue) {
			return 0, false
		}
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func prepareStampedBuildSource(
	ctx context.Context,
	r *patch.Runner,
	sourceDir string,
	identity codexBuildIdentity,
) (string, error) {
	log := codexcliLog.With("function", "prepareStampedBuildSource")
	buildRoot := codexBuildRoot(sourceDir)
	buildDir := filepath.Join(buildRoot, "work")
	targetCacheDir := filepath.Join(buildRoot, "target")
	notef(r, "codex-cli: prepare build source "+buildDir)
	if r.DryRun {
		return buildDir, nil
	}
	if err := os.MkdirAll(buildRoot, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.mkdir_build_root_failed", "err", err)
		return "", fmt.Errorf("create build root: %w", err)
	}
	if err := os.RemoveAll(buildDir); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.remove_build_dir_failed", "err", err)
		return "", fmt.Errorf("remove stale build source: %w", err)
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.mkdir_build_dir_failed", "err", err)
		return "", fmt.Errorf("create build source: %w", err)
	}
	archivePath := filepath.Join(buildRoot, ".source-"+identity.BuildHash+".tar")
	defer func() {
		_ = os.Remove(archivePath)
	}()
	if err := r.RunWithHeartbeat(ctx,
		"codex-cli: archiving Codex source",
		30*time.Second,
		"git",
		"-C",
		sourceDir,
		"archive",
		"--format=tar",
		"-o",
		archivePath,
		"HEAD",
	); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.archive_failed", "err", err)
		return "", fmt.Errorf("archive Codex source: %w", err)
	}
	if err := r.RunWithHeartbeat(ctx,
		"codex-cli: extracting Codex source",
		30*time.Second,
		"tar",
		"-xf",
		archivePath,
		"-C",
		buildDir,
	); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.extract_failed", "err", err)
		return "", fmt.Errorf("extract Codex source archive: %w", err)
	}
	if err := os.MkdirAll(targetCacheDir, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.mkdir_target_cache_failed", "err", err)
		return "", fmt.Errorf("create build target cache: %w", err)
	}
	targetPath := filepath.Join(buildDir, "codex-rs", "target")
	if err := os.RemoveAll(targetPath); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.remove_target_failed", "err", err)
		return "", fmt.Errorf("remove build target path: %w", err)
	}
	if err := os.Symlink(targetCacheDir, targetPath); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.symlink_target_failed", "err", err)
		return "", fmt.Errorf("link build target cache: %w", err)
	}
	if err := stampCodexBuildSource(buildDir, identity); err != nil {
		return "", err
	}
	return buildDir, nil
}

func stampCodexBuildSource(buildDir string, identity codexBuildIdentity) error {
	return writeWorkspacePackageVersion(
		filepath.Join(buildDir, "codex-rs", "Cargo.toml"),
		identity.PackageVersion,
	)
}

func writeWorkspacePackageVersion(path string, packageVersion string) error {
	log := codexcliLog.With("function", "writeWorkspacePackageVersion")
	cleanPath := filepath.Clean(path)
	log.Info("codexcli.write_workspace_package_version.boundary", "path", cleanPath, "package_version", packageVersion)
	if err := validateStampedPackageVersion(packageVersion); err != nil {
		log.Error("codexcli.write_workspace_package_version.invalid_version", "err", err)
		return err
	}
	info, err := os.Stat(cleanPath)
	if err != nil {
		log.Error("codexcli.write_workspace_package_version.stat_failed", "err", err)
		return fmt.Errorf("stat %s: %w", cleanPath, err)
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		log.Error("codexcli.write_workspace_package_version.read_failed", "err", err)
		return fmt.Errorf("read %s: %w", cleanPath, err)
	}
	contents := string(data)
	lines := strings.SplitAfter(contents, "\n")
	inWorkspacePackage := false
	foundWorkspacePackage := false
	stampedVersion := false
	for index, line := range lines {
		trimmedLine := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		trimmedLine = strings.TrimSuffix(trimmedLine, "\r")
		if strings.HasPrefix(trimmedLine, "[") && strings.HasSuffix(trimmedLine, "]") {
			if inWorkspacePackage && !stampedVersion {
				err := fmt.Errorf("stamp %s: workspace.package version was not found", cleanPath)
				log.Error("codexcli.write_workspace_package_version.missing_version", "err", err)
				return err
			}
			inWorkspacePackage = trimmedLine == "[workspace.package]"
			if inWorkspacePackage {
				foundWorkspacePackage = true
			}
			continue
		}
		if !inWorkspacePackage || !isTomlVersionAssignment(trimmedLine) {
			continue
		}
		lines[index] = stampedVersionLine(line, packageVersion)
		stampedVersion = true
		break
	}
	if !foundWorkspacePackage {
		err := fmt.Errorf("stamp %s: workspace.package table was not found", cleanPath)
		log.Error("codexcli.write_workspace_package_version.missing_table", "err", err)
		return err
	}
	if !stampedVersion {
		err := fmt.Errorf("stamp %s: workspace.package version was not found", cleanPath)
		log.Error("codexcli.write_workspace_package_version.missing_version", "err", err)
		return err
	}
	// #nosec G703 -- cleanPath is the fixed Cargo.toml path inside the local archived build copy.
	if err := os.WriteFile(cleanPath, []byte(strings.Join(lines, "")), info.Mode().Perm()); err != nil {
		log.Error("codexcli.write_workspace_package_version.write_failed", "err", err)
		return fmt.Errorf("write %s: %w", cleanPath, err)
	}
	return nil
}

func isTomlVersionAssignment(trimmedLine string) bool {
	left, _, ok := strings.Cut(trimmedLine, "=")
	return ok && strings.TrimSpace(left) == "version"
}

func stampedVersionLine(existingLine string, packageVersion string) string {
	ending := ""
	line := existingLine
	if strings.HasSuffix(line, "\n") {
		ending = "\n"
		line = strings.TrimSuffix(line, "\n")
	}
	if strings.HasSuffix(line, "\r") {
		ending = "\r" + ending
		line = strings.TrimSuffix(line, "\r")
	}
	indentLength := len(line) - len(strings.TrimLeft(line, " \t"))
	return line[:indentLength] + "version = " + strconv.Quote(packageVersion) + ending
}

func validateStampedPackageVersion(packageVersion string) error {
	baseVersion, rest, ok := strings.Cut(packageVersion, "-main.")
	if !ok {
		return fmt.Errorf("invalid stamped package version %q: missing -main prerelease", packageVersion)
	}
	if !isStableSemverCore(baseVersion) {
		return fmt.Errorf("invalid stamped package version %q: invalid base version", packageVersion)
	}
	head, metadata, ok := strings.Cut(rest, "+tree.")
	if !ok {
		return fmt.Errorf("invalid stamped package version %q: missing tree metadata", packageVersion)
	}
	if !isHexIdentifier(head, 12) {
		return fmt.Errorf("invalid stamped package version %q: invalid head prerelease", packageVersion)
	}
	tree, buildHash, ok := strings.Cut(metadata, ".build.")
	if !ok {
		return fmt.Errorf("invalid stamped package version %q: missing build hash metadata", packageVersion)
	}
	if !isHexIdentifier(tree, 12) {
		return fmt.Errorf("invalid stamped package version %q: invalid tree metadata", packageVersion)
	}
	if !isHexIdentifier(buildHash, buildHashLength) {
		return fmt.Errorf("invalid stamped package version %q: invalid build hash metadata", packageVersion)
	}
	return nil
}

func isStableSemverCore(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if _, ok := parseVersionPart(part); !ok {
			return false
		}
	}
	return true
}

func shortTreeIdentifier(tree string) string {
	if len(tree) >= 12 {
		return tree[:12]
	}
	return tree
}

func isHexIdentifier(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, runeValue := range value {
		if !isLowerHex(runeValue) {
			return false
		}
	}
	return true
}

func isLowerHex(runeValue rune) bool {
	return unicode.IsDigit(runeValue) || (runeValue >= 'a' && runeValue <= 'f')
}
