// Package codexcli builds and installs a locally signed Codex CLI from source.
package codexcli

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/signing"
)

const (
	defaultRef       = "origin/main"
	codexRepo        = "openai/codex"
	packageBinaryRel = "bin/codex"
)

type BuildMode string

const (
	BuildModeRelease   BuildMode = "release"
	BuildModeLocalFast BuildMode = "local-fast"
)

//go:embed scripts/resolve_codex_v8_env.py
var resolveCodexV8EnvScript string

// InstallOptions controls one Codex CLI source build and install.
type InstallOptions struct {
	DryRun       bool
	SourceDir    string
	Ref          string
	InstallDir   string
	CodexHome    string
	BuildMode    string
	NoSccache    bool
	ForceRebuild bool
	Out          io.Writer
}

// StatusOptions controls status output.
type StatusOptions struct {
	SourceDir  string
	InstallDir string
	CodexHome  string
	Out        io.Writer
}

type packageMetadata struct {
	Version string `json:"version"`
	Target  string `json:"target"`
	Variant string `json:"variant"`
}

// DefaultSourceDir returns the managed shallow Codex source checkout path.
func DefaultSourceDir() string {
	return filepath.Join(defaultCacheHome(), "desktop-via-clyde", "codex", "source")
}

// DefaultInstallDir returns the visible command directory.
func DefaultInstallDir() string {
	return filepath.Join(paths.Home(), ".local", "bin")
}

// DefaultCodexHome returns the Codex home used by upstream standalone installs.
func DefaultCodexHome() string {
	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		return codexHome
	}
	return filepath.Join(paths.Home(), ".codex")
}

// DefaultRef returns the default upstream source ref.
func DefaultRef() string {
	return defaultRef
}

// DefaultBuildMode returns the default build mode for Codex CLI installs.
func DefaultBuildMode() string {
	return string(BuildModeRelease)
}

// Install clones or updates Codex, builds an upstream package layout, signs the
// entrypoint with the local Developer ID, and installs the package.
func Install(opts InstallOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	opts = withInstallDefaults(opts)
	r := patch.NewRunner(opts.DryRun, opts.Out)
	buildMode, err := parseBuildMode(opts.BuildMode)
	if err != nil {
		return err
	}

	target, err := hostTargetTriple()
	if err != nil {
		return err
	}
	r.Note("codex-cli: source=%s ref=%s target=%s build-mode=%s", opts.SourceDir, opts.Ref, target, buildMode)
	r.Note("codex-cli step 1/7: update Codex source checkout")
	if err := cloneOrUpdateSource(r, opts.SourceDir, opts.Ref); err != nil {
		return err
	}
	head := "dryrun"
	if !opts.DryRun {
		headBytes, err := r.RunCaptureStdout("git", "-C", opts.SourceDir, "rev-parse", "--short=12", "HEAD")
		if err != nil {
			return fmt.Errorf("read Codex source HEAD: %w", err)
		}
		head = strings.TrimSpace(string(headBytes))
		r.Note("codex-cli: source checkout is at HEAD %s", head)
	}
	if !opts.DryRun {
		reusedReleaseDir, reused, err := maybeReuseInstalledRelease(
			r,
			opts.CodexHome,
			opts.InstallDir,
			head,
			target,
			buildMode,
			opts.ForceRebuild,
		)
		if err != nil {
			return err
		}
		if reused {
			r.Note("codex-cli: install complete release=%s", reusedReleaseDir)
			return nil
		}
	}

	packageDir := filepath.Join(defaultCacheHome(), "desktop-via-clyde", "codex", "package")
	r.Note("codex-cli step 2/7: build upstream Codex entrypoint")
	entrypointPath, err := buildEntrypoint(r, opts.SourceDir, target, buildMode, opts.NoSccache)
	if err != nil {
		return err
	}
	r.Note("codex-cli step 3/7: sign upstream Codex entrypoint")
	if err := signBinary(r, opts.SourceDir, entrypointPath); err != nil {
		return err
	}
	r.Note("codex-cli step 4/7: build upstream Codex package")
	if err := buildPackage(r, opts.SourceDir, packageDir, target, entrypointPath); err != nil {
		return err
	}

	r.Note("codex-cli step 5/7: read package metadata")
	metadata := packageMetadata{
		Version: "dryrun",
		Target:  target,
		Variant: "codex",
	}
	if !opts.DryRun {
		metadata, err = readPackageMetadata(packageDir)
		if err != nil {
			return err
		}
	}
	releaseDir := releaseDir(opts.CodexHome, metadata.Version, head, metadata.Target, buildMode)
	r.Note("codex-cli: package version=%s target=%s release=%s", metadata.Version, metadata.Target, releaseDir)
	r.Note("codex-cli step 6/7: install standalone package")
	if err := installPackage(r, packageDir, releaseDir, opts.CodexHome, opts.InstallDir); err != nil {
		return err
	}
	r.Note("codex-cli step 7/7: verify installed command")
	if err := verifyInstalledCommand(r, opts.InstallDir); err != nil {
		return err
	}
	r.Note("codex-cli: install complete release=%s", releaseDir)
	return nil
}

// Status prints the local Codex CLI source, install, and signing state. It is
// best-effort so one missing surface does not hide the rest.
func Status(opts StatusOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	opts = withStatusDefaults(opts)
	out := opts.Out
	binPath := filepath.Join(opts.InstallDir, "codex")
	currentLink := filepath.Join(opts.CodexHome, "packages", "standalone", "current")

	fmt.Fprintf(out, "source dir: %s\n", opts.SourceDir)
	if isDir(filepath.Join(opts.SourceDir, ".git")) {
		printCommandValue(out, "source head", "git", "-C", opts.SourceDir, "rev-parse", "--short=12", "HEAD")
		printCommandValue(out, "source branch", "git", "-C", opts.SourceDir, "branch", "--show-current")
	} else {
		fmt.Fprintln(out, "source head: missing")
	}

	fmt.Fprintf(out, "codex home: %s\n", opts.CodexHome)
	printSymlink(out, "current release", currentLink)
	printSymlink(out, "visible command", binPath)
	if _, err := os.Stat(binPath); err == nil {
		printCommandValue(out, "version", binPath, "--version")
		printCommandValue(out, "codesign", "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", binPath)
		printCommandValue(out, "signature", "/usr/bin/codesign", "-dv", binPath)
	} else {
		fmt.Fprintf(out, "version: missing at %s\n", binPath)
	}
	printCommandValue(out, "which -a codex", "/usr/bin/which", "-a", "codex")
	return nil
}

func withInstallDefaults(opts InstallOptions) InstallOptions {
	if opts.SourceDir == "" {
		opts.SourceDir = DefaultSourceDir()
	}
	if opts.Ref == "" {
		opts.Ref = DefaultRef()
	}
	if opts.InstallDir == "" {
		opts.InstallDir = DefaultInstallDir()
	}
	if opts.CodexHome == "" {
		opts.CodexHome = DefaultCodexHome()
	}
	if opts.BuildMode == "" {
		opts.BuildMode = DefaultBuildMode()
	}
	return opts
}

func withStatusDefaults(opts StatusOptions) StatusOptions {
	if opts.SourceDir == "" {
		opts.SourceDir = DefaultSourceDir()
	}
	if opts.InstallDir == "" {
		opts.InstallDir = DefaultInstallDir()
	}
	if opts.CodexHome == "" {
		opts.CodexHome = DefaultCodexHome()
	}
	return opts
}

func defaultCacheHome() string {
	if cacheHome := os.Getenv("XDG_CACHE_HOME"); cacheHome != "" {
		return cacheHome
	}
	return filepath.Join(paths.Home(), ".cache")
}

func hostTargetTriple() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("codex-cli install only supports macOS, got %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "arm64":
		return "aarch64-apple-darwin", nil
	case "amd64":
		return "x86_64-apple-darwin", nil
	default:
		return "", fmt.Errorf("unsupported macOS architecture %s", runtime.GOARCH)
	}
}

func cloneOrUpdateSource(r *patch.Runner, sourceDir string, ref string) error {
	if !r.DryRun {
		if err := os.MkdirAll(filepath.Dir(sourceDir), 0o755); err != nil {
			return fmt.Errorf("create source parent: %w", err)
		}
	}
	if _, err := os.Stat(sourceDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat source dir: %w", err)
		}
		r.Note("codex-cli: source checkout missing, cloning %s with depth 1", codexRepo)
		if err := r.RunWithHeartbeat("codex-cli: cloning Codex source", 30*time.Second, "gh", "repo", "clone", codexRepo, sourceDir, "--", "--depth", "1"); err != nil {
			return fmt.Errorf("clone Codex source: %w", err)
		}
	} else if !isDir(filepath.Join(sourceDir, ".git")) && !r.DryRun {
		return fmt.Errorf("source dir exists but is not a git checkout: %s", sourceDir)
	} else {
		r.Note("codex-cli: source checkout exists, updating %s", sourceDir)
	}
	r.Note("codex-cli: fetching %s from origin with depth 1", ref)
	if err := r.RunWithHeartbeat("codex-cli: fetching Codex source", 30*time.Second, "git", "-C", sourceDir, "fetch", "--depth", "1", "--prune", "origin", fetchRef(ref)); err != nil {
		return fmt.Errorf("fetch Codex source: %w", err)
	}
	r.Note("codex-cli: checking out fetched commit")
	if err := r.Run("git", "-C", sourceDir, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout Codex source: %w", err)
	}
	return nil
}

func fetchRef(ref string) string {
	if strings.HasPrefix(ref, "origin/") {
		return strings.TrimPrefix(ref, "origin/")
	}
	return ref
}

func parseBuildMode(value string) (BuildMode, error) {
	switch BuildMode(value) {
	case BuildModeRelease:
		return BuildModeRelease, nil
	case BuildModeLocalFast:
		return BuildModeLocalFast, nil
	default:
		return "", fmt.Errorf("unsupported build mode %q (expected %q or %q)", value, BuildModeRelease, BuildModeLocalFast)
	}
}

func buildPackage(
	r *patch.Runner,
	sourceDir string,
	packageDir string,
	target string,
	entrypointPath string,
) error {
	script := filepath.Join(sourceDir, "scripts", "build_codex_package.py")
	r.Note("codex-cli: build package at %s", packageDir)
	r.Note("codex-cli: upstream package builder output follows")
	if err := r.RunWithHeartbeat(
		"codex-cli: building Codex package",
		30*time.Second,
		"python3",
		script,
		"--target",
		target,
		"--variant",
		"codex",
		"--package-dir",
		packageDir,
		"--cargo-profile",
		"release",
		"--entrypoint-bin",
		entrypointPath,
		"--force",
	); err != nil {
		return fmt.Errorf("build Codex package: %w", err)
	}
	return nil
}

func buildEntrypoint(
	r *patch.Runner,
	sourceDir string,
	target string,
	buildMode BuildMode,
	noSccache bool,
) (string, error) {
	manifestPath := filepath.Join(sourceDir, "codex-rs", "Cargo.toml")
	entrypointPath := filepath.Join(sourceDir, "codex-rs", "target", target, "release", "codex")
	r.Note("codex-cli: build entrypoint at %s", entrypointPath)
	if r.DryRun {
		r.Note("codex-cli: Cargo build will resolve upstream Rusty V8 artifact overrides when needed")
	} else {
		r.Note("codex-cli: resolving upstream Rusty V8 artifact overrides")
	}
	describeBuildMode(r, buildMode)
	cargoEnv, sccachePath, sccacheUsed, err := cargoBuildEnv(r, sourceDir, target, noSccache)
	if err != nil {
		return "", err
	}
	cargoArgs := cargoBuildArgs(manifestPath, target, buildMode)
	if err := r.RunEnvWithHeartbeat(
		"codex-cli: building Codex entrypoint",
		30*time.Second,
		cargoEnv,
		"cargo",
		cargoArgs...,
	); err != nil {
		return "", fmt.Errorf("build Codex entrypoint: %w", err)
	}
	if sccacheUsed {
		r.Note("codex-cli: sccache stats follow")
		if err := r.Run(sccachePath, "--show-stats"); err != nil {
			r.Note("codex-cli: could not read sccache stats: %v", err)
		}
	}
	if !r.DryRun {
		if _, err := os.Stat(entrypointPath); err != nil {
			return "", fmt.Errorf("stat built Codex entrypoint: %w", err)
		}
	}
	return entrypointPath, nil
}

func signBinary(r *patch.Runner, sourceDir string, binaryPath string) error {
	entitlementsPath := filepath.Join(
		sourceDir,
		".github",
		"actions",
		"macos-code-sign",
		"codex.entitlements.plist",
	)
	if !r.DryRun {
		if _, err := os.Stat(binaryPath); err != nil {
			return fmt.Errorf("stat Codex binary: %w", err)
		}
		if _, err := os.Stat(entitlementsPath); err != nil {
			return fmt.Errorf("stat upstream Codex entitlements: %w", err)
		}
	}
	r.Note("codex-cli: resolving local signing identity %q", paths.SignIdentity)
	id, err := signing.ResolveIdentity(r.DryRun)
	if err != nil {
		return err
	}
	r.Note("codex-cli: using upstream entitlements %s", entitlementsPath)
	r.Note("codex-cli: sign %s with %q (sha1=%s)", binaryPath, paths.SignIdentity, id)
	if err := r.Run("/usr/bin/codesign", signing.RuntimeTimestampEntitlementsArgs(id, entitlementsPath, binaryPath)...); err != nil {
		return fmt.Errorf("sign Codex CLI: %w", err)
	}
	return nil
}

func cargoBuildEnv(
	r *patch.Runner,
	sourceDir string,
	target string,
	noSccache bool,
) (map[string]string, string, bool, error) {
	if r.DryRun {
		return nil, "", false, nil
	}
	scriptPath, cleanup, err := writeTempHelperScript("desktop-via-clyde-codex-v8-*.py", resolveCodexV8EnvScript)
	if err != nil {
		return nil, "", false, fmt.Errorf("write Codex V8 helper script: %w", err)
	}
	defer cleanup()

	output, err := r.RunCaptureStdout("python3", scriptPath, sourceDir, target)
	if err != nil {
		return nil, "", false, fmt.Errorf("resolve Codex V8 environment: %w", err)
	}
	env, err := parseEnvOutput(output)
	if err != nil {
		return nil, "", false, fmt.Errorf("parse Codex V8 environment: %w", err)
	}
	if len(env) == 0 {
		r.Note("codex-cli: upstream Codex did not require additional Rusty V8 env overrides")
		env = nil
	}
	for _, key := range []string{"RUSTY_V8_ARCHIVE", "RUSTY_V8_SRC_BINDING_PATH"} {
		value, ok := env[key]
		if ok {
			r.Note("codex-cli: %s=%s", key, value)
		}
	}
	existingWrapper := os.Getenv("RUSTC_WRAPPER")
	rustcWrapper, sccacheUsed := resolveRustcWrapper(existingWrapper, noSccache, exec.LookPath)
	switch {
	case existingWrapper != "":
		if sccacheUsed {
			r.Note("codex-cli: using existing sccache wrapper %s", existingWrapper)
		} else {
			r.Note("codex-cli: respecting existing RUSTC_WRAPPER=%s", existingWrapper)
		}
	case noSccache:
		r.Note("codex-cli: sccache disabled for this run")
	case rustcWrapper != "":
		r.Note("codex-cli: using sccache wrapper %s", rustcWrapper)
		if env == nil {
			env = map[string]string{}
		}
		env["RUSTC_WRAPPER"] = rustcWrapper
	default:
		r.Note("codex-cli: sccache not found, building without compiler cache")
	}
	return env, rustcWrapper, sccacheUsed, nil
}

func writeTempHelperScript(pattern string, body string) (string, func(), error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", nil, err
	}
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", nil, err
	}
	cleanup := func() {
		_ = os.Remove(file.Name())
	}
	return file.Name(), cleanup, nil
}

func parseEnvOutput(output []byte) (map[string]string, error) {
	env := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid env line %q", line)
		}
		env[parts[0]] = parts[1]
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return env, nil
}

func readPackageMetadata(packageDir string) (packageMetadata, error) {
	path := filepath.Join(packageDir, "codex-package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return packageMetadata{}, fmt.Errorf("read package metadata: %w", err)
	}
	var metadata packageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return packageMetadata{}, fmt.Errorf("parse package metadata: %w", err)
	}
	if metadata.Version == "" || metadata.Target == "" || metadata.Variant != "codex" {
		return packageMetadata{}, fmt.Errorf("invalid package metadata in %s: %+v", path, metadata)
	}
	return metadata, nil
}

func releaseDir(codexHome string, version string, head string, target string, buildMode BuildMode) string {
	return filepath.Join(
		codexHome,
		"packages",
		"standalone",
		"releases",
		fmt.Sprintf("%s-main-%s-%s%s", version, head, target, buildModeReleaseSuffix(buildMode)),
	)
}

func buildModeReleaseSuffix(buildMode BuildMode) string {
	if buildMode == BuildModeLocalFast {
		return "-local-fast"
	}
	return ""
}

func releaseNameSuffix(head string, target string, buildMode BuildMode) string {
	return fmt.Sprintf("-main-%s-%s%s", head, target, buildModeReleaseSuffix(buildMode))
}

func installPackage(
	r *patch.Runner,
	packageDir string,
	releaseDir string,
	codexHome string,
	installDir string,
) error {
	standaloneRoot := filepath.Join(codexHome, "packages", "standalone")
	currentLink := filepath.Join(standaloneRoot, "current")
	binPath := filepath.Join(installDir, "codex")
	stageDir := filepath.Join(filepath.Dir(releaseDir), ".staging."+filepath.Base(releaseDir))
	r.Note("codex-cli: install package %s -> %s", packageDir, releaseDir)
	if r.DryRun {
		r.Note("codex-cli: would stage package at %s", stageDir)
		r.Note("codex-cli: update %s -> %s", currentLink, releaseDir)
		r.Note("codex-cli: update %s -> %s", binPath, filepath.Join(currentLink, packageBinaryRel))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(releaseDir), 0o755); err != nil {
		return fmt.Errorf("create releases dir: %w", err)
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}
	r.Note("codex-cli: preparing staging directory %s", stageDir)
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("remove stale stage dir: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("create stage dir: %w", err)
	}
	if err := r.Run("/usr/bin/rsync", "-a", packageDir+"/", stageDir+"/"); err != nil {
		return fmt.Errorf("copy package to release stage: %w", err)
	}
	r.Note("codex-cli: creating release convenience symlink %s", filepath.Join(stageDir, "codex"))
	if err := os.Symlink(packageBinaryRel, filepath.Join(stageDir, "codex")); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create release convenience symlink: %w", err)
	}
	r.Note("codex-cli: promoting staged release to %s", releaseDir)
	if err := os.RemoveAll(releaseDir); err != nil {
		return fmt.Errorf("remove old release dir: %w", err)
	}
	if err := os.Rename(stageDir, releaseDir); err != nil {
		return fmt.Errorf("move release into place: %w", err)
	}
	if err := replaceSymlink(currentLink, releaseDir); err != nil {
		return fmt.Errorf("update current release link: %w", err)
	}
	r.Note("codex-cli: updating visible command symlink %s", binPath)
	if err := replaceSymlink(binPath, filepath.Join(currentLink, filepath.FromSlash(packageBinaryRel))); err != nil {
		return fmt.Errorf("update visible command link: %w", err)
	}
	return nil
}

func replaceSymlink(linkPath string, target string) error {
	tmpLink := linkPath + ".tmp"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(target, tmpLink); err != nil {
		return err
	}
	if err := os.RemoveAll(linkPath); err != nil {
		_ = os.Remove(tmpLink)
		return err
	}
	return os.Rename(tmpLink, linkPath)
}

func verifyInstalledCommand(r *patch.Runner, installDir string) error {
	binPath := filepath.Join(installDir, "codex")
	r.Note("codex-cli: verifying signature for %s", binPath)
	if err := r.Run("/usr/bin/codesign", "--verify", "--strict", "--verbose=2", binPath); err != nil {
		return fmt.Errorf("verify Codex CLI signature: %w", err)
	}
	r.Note("codex-cli: verifying executable by running %s --version", binPath)
	if err := r.Run(binPath, "--version"); err != nil {
		return fmt.Errorf("verify Codex CLI version: %w", err)
	}
	return nil
}

func printCommandValue(out io.Writer, label string, name string, args ...string) {
	cmd := exec.Command(name, args...)
	data, err := cmd.CombinedOutput()
	value := strings.TrimSpace(string(data))
	if err != nil {
		if value == "" {
			value = err.Error()
		} else {
			value = value + " (" + err.Error() + ")"
		}
	}
	fmt.Fprintf(out, "%s: %s\n", label, value)
}

func printSymlink(out io.Writer, label string, path string) {
	target, err := os.Readlink(path)
	if err != nil {
		fmt.Fprintf(out, "%s: missing at %s\n", label, path)
		return
	}
	fmt.Fprintf(out, "%s: %s -> %s\n", label, path, target)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func describeBuildMode(r *patch.Runner, buildMode BuildMode) {
	if buildMode == BuildModeLocalFast {
		r.Note(
			"codex-cli: local-fast mode overrides release settings with lto=false and codegen-units=%d",
			localFastCodegenUnits(),
		)
		return
	}
	r.Note("codex-cli: release mode preserves the upstream release profile exactly")
}

func cargoBuildArgs(manifestPath string, target string, buildMode BuildMode) []string {
	args := []string{
		"build",
		"--manifest-path",
		manifestPath,
		"--target",
		target,
		"--release",
		"--timings",
		"--bin",
		"codex",
		"-v",
	}
	if buildMode == BuildModeLocalFast {
		args = append(
			args,
			"--config",
			"profile.release.lto=false",
			"--config",
			"profile.release.codegen-units="+strconv.Itoa(localFastCodegenUnits()),
		)
	}
	return args
}

func localFastCodegenUnits() int {
	if runtime.NumCPU() < 1 {
		return 1
	}
	return runtime.NumCPU()
}

func resolveRustcWrapper(
	existingWrapper string,
	noSccache bool,
	lookPath func(string) (string, error),
) (string, bool) {
	if existingWrapper != "" {
		return existingWrapper, isSccachePath(existingWrapper)
	}
	if noSccache {
		return "", false
	}
	path, err := lookPath("sccache")
	if err != nil {
		return "", false
	}
	return path, true
}

func isSccachePath(path string) bool {
	return filepath.Base(path) == "sccache"
}

func maybeReuseInstalledRelease(
	r *patch.Runner,
	codexHome string,
	installDir string,
	head string,
	target string,
	buildMode BuildMode,
	forceRebuild bool,
) (string, bool, error) {
	if forceRebuild {
		r.Note("codex-cli: force rebuild requested, skipping installed release reuse")
		return "", false, nil
	}
	releasesRoot := filepath.Join(codexHome, "packages", "standalone", "releases")
	matches, err := findMatchingReleaseDirs(releasesRoot, releaseNameSuffix(head, target, buildMode))
	if err != nil {
		return "", false, err
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	if len(matches) > 1 {
		r.Note("codex-cli: found multiple matching installed releases, rebuilding instead")
		return "", false, nil
	}
	releasePath := matches[0]
	r.Note("codex-cli: found matching installed release %s", releasePath)
	if err := verifyReleaseCandidate(releasePath, target); err != nil {
		r.Note("codex-cli: installed release reuse rejected: %v", err)
		return "", false, nil
	}
	r.Note("codex-cli: reusing verified installed release %s", releasePath)
	if err := relinkInstalledRelease(r, codexHome, installDir, releasePath); err != nil {
		return "", false, err
	}
	if err := verifyInstalledCommand(r, installDir); err != nil {
		return "", false, err
	}
	return releasePath, true, nil
}

func findMatchingReleaseDirs(releasesRoot string, suffix string) ([]string, error) {
	entries, err := os.ReadDir(releasesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list release dir %s: %w", releasesRoot, err)
	}
	matches := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), suffix) {
			matches = append(matches, filepath.Join(releasesRoot, entry.Name()))
		}
	}
	sort.Strings(matches)
	return matches, nil
}

func verifyReleaseCandidate(releasePath string, target string) error {
	metadata, err := readPackageMetadata(releasePath)
	if err != nil {
		return err
	}
	if metadata.Target != target {
		return fmt.Errorf("release target mismatch: got %s want %s", metadata.Target, target)
	}
	binaryPath := filepath.Join(releasePath, filepath.FromSlash(packageBinaryRel))
	if _, err := os.Stat(binaryPath); err != nil {
		return fmt.Errorf("stat release binary: %w", err)
	}
	cmd := exec.Command("/usr/bin/codesign", "--verify", "--strict", "--verbose=2", binaryPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("codesign verify release binary: %s", message)
	}
	return nil
}

func relinkInstalledRelease(
	r *patch.Runner,
	codexHome string,
	installDir string,
	releasePath string,
) error {
	standaloneRoot := filepath.Join(codexHome, "packages", "standalone")
	currentLink := filepath.Join(standaloneRoot, "current")
	binPath := filepath.Join(installDir, "codex")
	if err := os.MkdirAll(standaloneRoot, 0o755); err != nil {
		return fmt.Errorf("create standalone root: %w", err)
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}
	r.Note("codex-cli: update %s -> %s", currentLink, releasePath)
	if err := replaceSymlink(currentLink, releasePath); err != nil {
		return fmt.Errorf("update current release link: %w", err)
	}
	r.Note("codex-cli: update %s -> %s", binPath, filepath.Join(currentLink, packageBinaryRel))
	if err := replaceSymlink(binPath, filepath.Join(currentLink, filepath.FromSlash(packageBinaryRel))); err != nil {
		return fmt.Errorf("update visible command link: %w", err)
	}
	return nil
}
