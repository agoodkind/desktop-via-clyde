package codexcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.mkdir_build_dir_failed", "err", err)
		return "", fmt.Errorf("create build source: %w", err)
	}
	if err := os.MkdirAll(targetCacheDir, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.mkdir_target_cache_failed", "err", err)
		return "", fmt.Errorf("create build target cache: %w", err)
	}
	// Capture the base version the previous build stamped before rsync overwrites the
	// work manifest. The workspace mtime is only preserved when the base is unchanged,
	// so a base bump (new release tag) advances the mtime and forces cargo to rebuild
	// the non-app crates with the new base.
	previousBaseVersion, _ := readTomlPackageVersion(filepath.Join(buildDir, "codex-rs", "Cargo.toml"), "[workspace.package]")
	// Mirror the source working tree into a reused build dir with rsync rather than
	// re-extracting a git archive. git archive rewrites every file mtime to the commit
	// time, so each commit invalidates Cargo's fingerprints in the shared target cache
	// and forces a near-full recompile. rsync -a preserves mtimes, so unchanged files
	// keep their cache entries warm, while --delete drops files removed upstream. The
	// target excludes keep the symlinked cache and any in-place build output from being
	// copied into the work tree.
	if err := r.RunWithHeartbeat(ctx,
		"codex-cli: syncing Codex source",
		30*time.Second,
		"rsync",
		"-a",
		"--delete",
		"--exclude",
		".git/",
		"--exclude",
		"target/",
		sourceDir+"/",
		buildDir+"/",
	); err != nil {
		log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.sync_failed", "err", err)
		return "", fmt.Errorf("sync Codex source: %w", err)
	}
	// Point the work tree's Cargo target at the shared cache. The sync preserves a
	// correct symlink across builds, so only replace the path when it is missing or wrong.
	targetPath := filepath.Join(buildDir, "codex-rs", "target")
	if existing, linkErr := os.Readlink(targetPath); linkErr != nil || existing != targetCacheDir {
		if err := os.RemoveAll(targetPath); err != nil {
			log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.remove_target_failed", "err", err)
			return "", fmt.Errorf("remove build target path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.mkdir_target_parent_failed", "err", err)
			return "", fmt.Errorf("create build target parent: %w", err)
		}
		if err := os.Symlink(targetCacheDir, targetPath); err != nil {
			log.ErrorContext(ctx, "codexcli.prepare_stamped_build_source.symlink_target_failed", "err", err)
			return "", fmt.Errorf("link build target cache: %w", err)
		}
	}
	if err := stampCodexBuildSource(buildDir, identity, previousBaseVersion); err != nil {
		return "", err
	}
	return buildDir, nil
}

// appFacingVersionCrates carry the full per-commit version so the app and
// `codex --version` show the exact build. Every other crate, and the workspace
// base version, stays at the stable release version so cargo caches the heavy
// crates (core) and the external dependency wall across commits; a new commit
// then recompiles only these few crates plus the final link instead of the
// whole 91-crate workspace.
var appFacingVersionCrates = []string{
	"cli",
	"app-server",
	"app-server-daemon",
	"tui",
	"exec",
	"mcp-server",
}

func stampCodexBuildSource(buildDir string, identity codexBuildIdentity, previousBaseVersion string) error {
	log := codexcliLog.With("function", "stampCodexBuildSource")
	if err := validateStampedPackageVersion(identity.PackageVersion); err != nil {
		log.Error("codexcli.stamp_codex_build_source.invalid_version", "err", err)
		return err
	}
	if !isStableSemverCore(identity.BaseVersion) {
		err := fmt.Errorf("invalid base version %q", identity.BaseVersion)
		log.Error("codexcli.stamp_codex_build_source.invalid_base_version", "err", err)
		return err
	}
	codexRoot := filepath.Join(buildDir, "codex-rs")
	// Preserve the workspace manifest's mtime (the rsync-copied source mtime) only when
	// the base version is unchanged from the previous build, so across normal commits the
	// identical manifest keeps cargo from rechecking all 91 members. On a base bump the
	// mtime advances, forcing cargo to rebuild the non-app crates with the new base. The
	// app crate manifests never preserve their mtime: their per-commit version changes, so
	// cargo must see the change and recompile them.
	preserveWorkspaceMtime := previousBaseVersion == identity.BaseVersion
	if err := stampPackageVersionInTable(filepath.Join(codexRoot, "Cargo.toml"), "[workspace.package]", identity.BaseVersion, preserveWorkspaceMtime); err != nil {
		return err
	}
	for _, crate := range appFacingVersionCrates {
		cratePath := filepath.Join(codexRoot, crate, "Cargo.toml")
		if _, err := os.Stat(cratePath); errors.Is(err, os.ErrNotExist) {
			log.Warn("codexcli.stamp_codex_build_source.crate_missing", "crate", crate)
			continue
		}
		if err := stampPackageVersionInTable(cratePath, "[package]", identity.PackageVersion, false); err != nil {
			return err
		}
	}
	return nil
}

func stampPackageVersionInTable(path string, tableHeader string, version string, preserveMtime bool) error {
	log := codexcliLog.With("function", "stampPackageVersionInTable")
	cleanPath := filepath.Clean(path)
	log.Info("codexcli.stamp_package_version_in_table.boundary", "path", cleanPath, "table", tableHeader, "version", version)
	info, err := os.Stat(cleanPath)
	if err != nil {
		log.Error("codexcli.stamp_package_version_in_table.stat_failed", "err", err)
		return fmt.Errorf("stat %s: %w", cleanPath, err)
	}
	sourceModTime := info.ModTime()
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		log.Error("codexcli.stamp_package_version_in_table.read_failed", "err", err)
		return fmt.Errorf("read %s: %w", cleanPath, err)
	}
	lines := strings.SplitAfter(string(data), "\n")
	inTable := false
	foundTable := false
	stamped := false
	for index, line := range lines {
		trimmedLine := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		if strings.HasPrefix(trimmedLine, "[") && strings.HasSuffix(trimmedLine, "]") {
			if inTable && !stamped {
				err := fmt.Errorf("stamp %s: %s version was not found", cleanPath, tableHeader)
				log.Error("codexcli.stamp_package_version_in_table.missing_version", "err", err)
				return err
			}
			inTable = trimmedLine == tableHeader
			if inTable {
				foundTable = true
			}
			continue
		}
		if !inTable || !isTomlVersionAssignment(trimmedLine) {
			continue
		}
		lines[index] = stampedVersionLine(line, version)
		stamped = true
		break
	}
	if !foundTable {
		err := fmt.Errorf("stamp %s: %s table was not found", cleanPath, tableHeader)
		log.Error("codexcli.stamp_package_version_in_table.missing_table", "err", err)
		return err
	}
	if !stamped {
		err := fmt.Errorf("stamp %s: %s version was not found", cleanPath, tableHeader)
		log.Error("codexcli.stamp_package_version_in_table.missing_version", "err", err)
		return err
	}
	// #nosec G703 -- cleanPath is a fixed Cargo.toml path inside the local build copy.
	if err := os.WriteFile(cleanPath, []byte(strings.Join(lines, "")), info.Mode().Perm()); err != nil {
		log.Error("codexcli.stamp_package_version_in_table.write_failed", "err", err)
		return fmt.Errorf("write %s: %w", cleanPath, err)
	}
	if preserveMtime {
		if err := os.Chtimes(cleanPath, sourceModTime, sourceModTime); err != nil {
			log.Error("codexcli.stamp_package_version_in_table.chtimes_failed", "err", err)
			return fmt.Errorf("restore mtime %s: %w", cleanPath, err)
		}
	}
	return nil
}

func isTomlVersionAssignment(trimmedLine string) bool {
	left, _, ok := strings.Cut(trimmedLine, "=")
	if !ok {
		return false
	}
	key := strings.TrimSpace(left)
	return key == "version" || key == "version.workspace"
}

// readTomlPackageVersion returns the quoted version assigned in the given table,
// e.g. `version = "0.141.0"` under [workspace.package]. It returns ("", false)
// when the file is missing, the table is absent, or the version inherits the
// workspace (version.workspace = true), so a prior build's stamped base can be
// compared against the current one.
func readTomlPackageVersion(path string, tableHeader string) (string, bool) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", false
	}
	inTable := false
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmedLine := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(trimmedLine, "[") && strings.HasSuffix(trimmedLine, "]") {
			inTable = trimmedLine == tableHeader
			continue
		}
		if !inTable {
			continue
		}
		left, right, ok := strings.Cut(trimmedLine, "=")
		if !ok || strings.TrimSpace(left) != "version" {
			continue
		}
		if value, err := strconv.Unquote(strings.TrimSpace(right)); err == nil {
			return value, true
		}
		return "", false
	}
	return "", false
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

// targetCacheKeepVariants is how many fingerprinted variants of each crate to
// retain in the shared Cargo target cache. Every scheduled build is a new commit
// that is never revisited, so keeping only the newest discards nothing useful.
const targetCacheKeepVariants = 1

// cargoArtifactPattern splits a cargo output name into a crate base and its
// trailing metadata hash, e.g. "libcodex_core-1a2b3c4d5e6f7a8b.rlib" ->
// ("libcodex_core", "1a2b3c4d5e6f7a8b"). The greedy prefix ensures the LAST
// "-<hex>" segment (cargo's metadata hash) is taken as the hash.
var cargoArtifactPattern = regexp.MustCompile(`^(.*)-([0-9a-fA-F]{8,})(\..*)?$`)

type cargoArtifactVariant struct {
	paths   []string
	modTime time.Time
}

// pruneTargetCacheArtifacts bounds the shared Cargo target cache by keeping only
// the newest `keep` fingerprinted variants of each crate base name. cargo never
// evicts superseded artifacts, so a scheduler that builds a new commit on every
// run otherwise grows target/deps without bound. What this primarily reclaims is
// the per-commit churn of the app-facing workspace crates, whose hash changes
// every commit. Variants are grouped by base name, so a crate present at multiple
// versions (some external deps) may have an older-mtime variant pruned; cargo and
// sccache rebuild it cheaply on the next build, so the cache stays correct but not
// strictly minimal. It is best-effort: failures are logged and never abort the
// build.
func pruneTargetCacheArtifacts(ctx context.Context, r *patch.Runner, targetCacheDir string, keep int) {
	log := codexcliLog.With("function", "pruneTargetCacheArtifacts")
	log.InfoContext(ctx, "codexcli.prune_target_cache.boundary", "target_cache_dir", targetCacheDir, "keep", keep)
	if r.DryRun {
		return
	}
	if keep < 1 {
		keep = 1
	}
	removed := 0
	for _, profileDir := range discoverCargoProfileDirs(targetCacheDir) {
		for _, sub := range []string{"deps", ".fingerprint", "build", "incremental"} {
			removed += pruneCargoArtifactDir(ctx, filepath.Join(profileDir, sub), keep)
		}
	}
	if removed > 0 {
		notef(r, fmt.Sprintf("codex-cli: pruned %d superseded build artifact variant(s) from target cache", removed))
		log.InfoContext(ctx, "codexcli.prune_target_cache.completed", "removed_variants", removed)
	}
}

// discoverCargoProfileDirs finds cargo profile directories under the target
// cache, both host (target/<profile>) and target-triple (target/<triple>/<profile>).
// A profile dir is any directory that contains a "deps" subdir, so profile and
// triple names are not hardcoded.
func discoverCargoProfileDirs(targetCacheDir string) []string {
	var dirs []string
	topEntries, err := os.ReadDir(targetCacheDir)
	if err != nil {
		return dirs
	}
	for _, top := range topEntries {
		if !top.IsDir() {
			continue
		}
		topPath := filepath.Join(targetCacheDir, top.Name())
		if isDir(filepath.Join(topPath, "deps")) {
			dirs = append(dirs, topPath)
		}
		subEntries, err := os.ReadDir(topPath)
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() {
				continue
			}
			subPath := filepath.Join(topPath, sub.Name())
			if isDir(filepath.Join(subPath, "deps")) {
				dirs = append(dirs, subPath)
			}
		}
	}
	return dirs
}

// pruneCargoArtifactDir keeps the newest `keep` hash variants of each crate base
// in dir and removes the rest. It returns the number of variants removed.
func pruneCargoArtifactDir(ctx context.Context, dir string, keep int) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	groups := map[string]map[string]*cargoArtifactVariant{}
	for _, entry := range entries {
		base, hash, ok := parseCargoArtifactName(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		byHash := groups[base]
		if byHash == nil {
			byHash = map[string]*cargoArtifactVariant{}
			groups[base] = byHash
		}
		variant := byHash[hash]
		if variant == nil {
			variant = &cargoArtifactVariant{paths: nil, modTime: time.Time{}}
			byHash[hash] = variant
		}
		variant.paths = append(variant.paths, filepath.Join(dir, entry.Name()))
		if info.ModTime().After(variant.modTime) {
			variant.modTime = info.ModTime()
		}
	}
	removed := 0
	for _, byHash := range groups {
		if len(byHash) <= keep {
			continue
		}
		removed += removeStaleVariants(ctx, byHash, keep)
	}
	return removed
}

// removeStaleVariants deletes all but the newest `keep` hash variants in byHash
// and returns how many variants it removed.
func removeStaleVariants(ctx context.Context, byHash map[string]*cargoArtifactVariant, keep int) int {
	log := codexcliLog.With("function", "removeStaleVariants")
	variants := make([]*cargoArtifactVariant, 0, len(byHash))
	for _, variant := range byHash {
		variants = append(variants, variant)
	}
	sort.Slice(variants, func(i, j int) bool {
		return variants[i].modTime.After(variants[j].modTime)
	})
	removed := 0
	for _, variant := range variants[keep:] {
		failed := false
		for _, path := range variant.paths {
			if err := os.RemoveAll(path); err != nil {
				log.ErrorContext(ctx, "codexcli.prune_target_cache.remove_failed", "err", err, "path", path)
				failed = true
			}
		}
		if !failed {
			removed++
		}
	}
	return removed
}

func parseCargoArtifactName(name string) (string, string, bool) {
	match := cargoArtifactPattern.FindStringSubmatch(name)
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}
