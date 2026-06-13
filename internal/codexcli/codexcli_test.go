package codexcli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/patch"
)

func TestInstallDryRunUsesShallowGhCloneAndOriginMain(t *testing.T) {
	installFixture(t)
	if runtime.GOOS != "darwin" {
		t.Skip("codex-cli install is macOS-only")
	}
	home := t.TempDir()
	cacheHome := filepath.Join(home, ".cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	var out bytes.Buffer
	trace := &patch.Trace{}
	opts := testInstallOptions(home, cacheHome)
	opts.BuildMode = string(BuildModeRelease)
	opts.Out = &out
	opts.Trace = trace
	err := Install(context.Background(), opts)
	if err != nil {
		t.Fatalf("Install dry-run: %v", err)
	}
	sourceDir := filepath.Join(cacheHome, "clyde", "desktop-via-clyde", "codex", "source")
	buildDir := filepath.Join(filepath.Dir(sourceDir), "build", "work")
	codexRoot := filepath.Join(buildDir, "codex-rs")
	requireTraceCommand(t, trace, "gh", []string{"repo", "clone", "openai/codex", sourceDir, "--", "--depth", "1"})
	requireTraceCommand(t, trace, "git", []string{"-C", sourceDir, "fetch", "--depth", "1", "--prune", "origin", "main"})
	requireTraceCommand(t, trace, "git", []string{"-C", sourceDir, "checkout", "--detach", "FETCH_HEAD"})
	requireTraceCommand(t, trace, "rustup", []string{"toolchain", "install"})
	requireTraceCommand(t, trace, "cargo", []string{"build", "--target", "aarch64-apple-darwin", "--release", "--timings", "--bin", "codex", "-v"})
	requireTraceCommand(t, trace, "python3", []string{
		filepath.Join(buildDir, "scripts", "build_codex_package.py"),
		"--target",
		"aarch64-apple-darwin",
		"--variant",
		"codex",
		"--package-dir",
		filepath.Join(cacheHome, "clyde", "desktop-via-clyde", "codex", "package"),
		"--cargo-profile",
		"release",
		"--entrypoint-bin",
		filepath.Join(codexRoot, "target", "aarch64-apple-darwin", "release", "codex"),
		"--force",
	})
}

func TestInstallRejectsMissingBuildMode(t *testing.T) {
	installFixture(t)
	opts := testInstallOptions(t.TempDir(), t.TempDir())
	opts.BuildMode = ""
	err := Install(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "build-mode is required") {
		t.Fatalf("Install missing build mode err = %v", err)
	}
}

func TestInstallDryRunUsesConfiguredLocalFastBuildMode(t *testing.T) {
	installFixture(t)
	if runtime.GOOS != "darwin" {
		t.Skip("codex-cli install is macOS-only")
	}
	home := t.TempDir()
	cacheHome := filepath.Join(home, ".cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	trace := &patch.Trace{}
	opts := testInstallOptions(home, cacheHome)
	opts.BuildMode = string(BuildModeLocalFast)
	opts.Out = io.Discard
	opts.Trace = trace
	err := Install(context.Background(), opts)
	if err != nil {
		t.Fatalf("Install dry-run with local-fast build mode: %v", err)
	}
	requireTraceCommandContains(t, trace, "cargo", "--config", "profile.release.lto=false")
}

func TestInstallDryRunLocalFastAddsCargoOverrides(t *testing.T) {
	installFixture(t)
	if runtime.GOOS != "darwin" {
		t.Skip("codex-cli install is macOS-only")
	}
	home := t.TempDir()
	cacheHome := filepath.Join(home, ".cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	trace := &patch.Trace{}
	opts := testInstallOptions(home, cacheHome)
	opts.BuildMode = string(BuildModeLocalFast)
	opts.Out = io.Discard
	opts.Trace = trace
	err := Install(context.Background(), opts)
	if err != nil {
		t.Fatalf("Install local-fast dry-run: %v", err)
	}
	requireTraceCommandContains(t, trace, "cargo", "--config", "profile.release.codegen-units=")
}

func TestReadPackageMetadata(t *testing.T) {
	packageDir := t.TempDir()
	body := []byte(`{"layoutVersion":1,"version":"0.133.0","target":"aarch64-apple-darwin","variant":"codex"}`)
	if err := os.WriteFile(filepath.Join(packageDir, "codex-package.json"), body, 0o644); err != nil {
		t.Fatalf("WriteFile metadata: %v", err)
	}
	got, err := readPackageMetadata(packageDir, "codex")
	if err != nil {
		t.Fatalf("readPackageMetadata: %v", err)
	}
	if got.Version != "0.133.0" || got.Target != "aarch64-apple-darwin" || got.Variant != "codex" {
		t.Fatalf("metadata = %+v", got)
	}
}

func TestFetchRefStripsOriginPrefix(t *testing.T) {
	if got := fetchRef("origin/main"); got != "main" {
		t.Fatalf("fetchRef origin/main = %q, want main", got)
	}
	if got := fetchRef("rust-v0.133.0"); got != "rust-v0.133.0" {
		t.Fatalf("fetchRef tag = %q, want rust-v0.133.0", got)
	}
}

func TestReadCodexPythonRequirement(t *testing.T) {
	sourceDir := t.TempDir()
	writeFixtureFile(
		t,
		filepath.Join(sourceDir, "scripts", "pyproject.toml"),
		"[project]\nrequires-python = \">=3.10\"\n",
	)
	got, err := readCodexPythonRequirement(sourceDir)
	if err != nil {
		t.Fatalf("readCodexPythonRequirement: %v", err)
	}
	if got != (pythonVersion{Major: 3, Minor: 10}) {
		t.Fatalf("python requirement = %+v", got)
	}
}

func TestFindCodexEntitlementsPath(t *testing.T) {
	sourceDir := t.TempDir()
	want := filepath.Join(sourceDir, ".github", "scripts", "macos-signing", "codex.entitlements.plist")
	writeFixtureFile(t, want, "<plist/>")
	got, err := findCodexEntitlementsPath(sourceDir)
	if err != nil {
		t.Fatalf("findCodexEntitlementsPath: %v", err)
	}
	if got != want {
		t.Fatalf("entitlements path = %q, want %q", got, want)
	}
}

func TestResolveCompatiblePythonCommandPrefersCompatiblePython3(t *testing.T) {
	versionLookup := func(_ context.Context, name string) (pythonVersion, error) {
		switch name {
		case "python3":
			return pythonVersion{Major: 3, Minor: 11}, nil
		case "python3.13":
			return pythonVersion{Major: 3, Minor: 13}, nil
		default:
			return pythonVersion{}, exec.ErrNotFound
		}
	}
	got, err := resolveCompatiblePythonCommandFromCandidates(
		context.Background(),
		pythonVersion{Major: 3, Minor: 10},
		[]string{"python3", "python3.13"},
		versionLookup,
	)
	if err != nil {
		t.Fatalf("resolveCompatiblePythonCommandFromCandidates: %v", err)
	}
	if got != "python3" {
		t.Fatalf("python command = %q, want python3", got)
	}
}

func TestResolveCompatiblePythonCommandChoosesNewestCompatibleFallback(t *testing.T) {
	versionLookup := func(_ context.Context, name string) (pythonVersion, error) {
		switch name {
		case "python3":
			return pythonVersion{Major: 3, Minor: 9}, nil
		case "python3.11":
			return pythonVersion{Major: 3, Minor: 11}, nil
		case "python3.13":
			return pythonVersion{Major: 3, Minor: 13}, nil
		default:
			return pythonVersion{}, exec.ErrNotFound
		}
	}
	candidates := []string{"python3", "python3.11", "python3.13"}
	got, err := resolveCompatiblePythonCommandFromCandidates(
		context.Background(),
		pythonVersion{Major: 3, Minor: 10},
		candidates,
		versionLookup,
	)
	if err != nil {
		t.Fatalf("resolveCompatiblePythonCommandFromCandidates: %v", err)
	}
	if got != "python3.13" {
		t.Fatalf("python command = %q, want python3.13", got)
	}
}

func TestResolveCompatiblePythonCommandErrorsWithoutCompatibleInterpreter(t *testing.T) {
	versionLookup := func(_ context.Context, name string) (pythonVersion, error) {
		if name == "python3" {
			return pythonVersion{Major: 3, Minor: 9}, nil
		}
		return pythonVersion{}, exec.ErrNotFound
	}
	_, err := resolveCompatiblePythonCommandFromCandidates(
		context.Background(),
		pythonVersion{Major: 3, Minor: 10},
		[]string{"python3"},
		versionLookup,
	)
	if err == nil || !strings.Contains(err.Error(), "Python 3.10 or newer") {
		t.Fatalf("resolveCompatiblePythonCommandFromCandidates err = %v", err)
	}
}

func TestDiscoverPythonCommandCandidates(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.MkdirAll(first, 0o755); err != nil {
		t.Fatalf("MkdirAll first: %v", err)
	}
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatalf("MkdirAll second: %v", err)
	}
	writeFixtureFile(t, filepath.Join(first, "python3"), "")
	writeFixtureFile(t, filepath.Join(first, "python3.11"), "")
	writeFixtureFile(t, filepath.Join(second, "python3.13"), "")
	writeFixtureFile(t, filepath.Join(second, "python3-config"), "")
	got := discoverPythonCommandCandidates(first + string(os.PathListSeparator) + second)
	want := []string{"python3", "python3.11", "python3.13"}
	if !equalStrings(got, want) {
		t.Fatalf("python candidates = %v, want %v", got, want)
	}
}

func TestSelectLatestStableRustVersion(t *testing.T) {
	output := []byte(strings.Join([]string{
		"1111111111111111111111111111111111111111\trefs/tags/rust-v0.136.0",
		"2222222222222222222222222222222222222222\trefs/tags/rust-v0.137.0",
		"3333333333333333333333333333333333333333\trefs/tags/rust-v0.138.0-beta.1",
		"4444444444444444444444444444444444444444\trefs/tags/v0.999.0",
	}, "\n"))
	got, err := selectLatestStableRustVersion(output)
	if err != nil {
		t.Fatalf("selectLatestStableRustVersion: %v", err)
	}
	if got.String() != "0.137.0" {
		t.Fatalf("latest stable rust version = %s, want 0.137.0", got.String())
	}
}

func TestBuildIdentityUsesDeterministicSemverPackageVersion(t *testing.T) {
	tree := "abcdef0123456789abcdef0123456789abcdef01"
	identity := newBuildIdentity(
		"0.137.0",
		"80b65e994573",
		tree,
		"aarch64-apple-darwin",
		BuildModeLocalFast,
		"codex",
		"codex",
	)
	wantHash := computeBuildHash(
		"0.137.0",
		"80b65e994573",
		tree,
		"aarch64-apple-darwin",
		string(BuildModeLocalFast),
		"codex",
		"codex",
	)
	wantVersion := "0.137.0-main.80b65e994573+tree.abcdef012345.build." + wantHash
	if identity.TreeStamp != "abcdef012345" {
		t.Fatalf("TreeStamp = %q", identity.TreeStamp)
	}
	if identity.PackageVersion != wantVersion {
		t.Fatalf("PackageVersion = %q, want %q", identity.PackageVersion, wantVersion)
	}
	if err := validateStampedPackageVersion(identity.PackageVersion); err != nil {
		t.Fatalf("validateStampedPackageVersion: %v", err)
	}
	if len(identity.BuildHash) != buildHashLength {
		t.Fatalf("BuildHash len = %d, want %d", len(identity.BuildHash), buildHashLength)
	}
}

func TestBuildHashChangesWithBuildInputs(t *testing.T) {
	baseValues := []string{
		"0.137.0",
		"80b65e994573",
		"abcdef0123456789abcdef0123456789abcdef01",
		"aarch64-apple-darwin",
		string(BuildModeLocalFast),
		"codex",
		"codex",
	}
	baseHash := computeBuildHash(baseValues...)
	for index := range baseValues {
		mutatedValues := append([]string{}, baseValues...)
		mutatedValues[index] = mutatedValues[index] + "x"
		if got := computeBuildHash(mutatedValues...); got == baseHash {
			t.Fatalf("hash did not change when input %d changed", index)
		}
	}
}

func TestStampCodexBuildSourceWritesOnlyCargoToml(t *testing.T) {
	buildDir := t.TempDir()
	identity := newBuildIdentity(
		"0.137.0",
		"80b65e994573",
		"abcdef0123456789abcdef0123456789abcdef01",
		"aarch64-apple-darwin",
		BuildModeLocalFast,
		"codex",
		"codex",
	)
	cargoPath := filepath.Join(buildDir, "codex-rs", "Cargo.toml")
	versionPath := filepath.Join(buildDir, "codex-rs", "tui", "src", "version.rs")
	mainPath := filepath.Join(buildDir, "codex-rs", "cli", "src", "main.rs")
	writeFixtureFile(t, cargoPath, "[workspace]\nmembers = []\n\n[workspace.package]\nedition = \"2024\"\nversion = \"0.0.0\"\n")
	writeFixtureFile(t, versionPath, "pub const CODEX_CLI_VERSION: &str = env!(\"CARGO_PKG_VERSION\");\n")
	writeFixtureFile(t, mainPath, "mod doctor;\n")

	if err := stampCodexBuildSource(buildDir, identity); err != nil {
		t.Fatalf("stampCodexBuildSource: %v", err)
	}

	cargoToml := readFixtureFile(t, cargoPath)
	if !strings.Contains(cargoToml, `version = "`+identity.PackageVersion+`"`) {
		t.Fatalf("Cargo.toml missing stamped package version:\n%s", cargoToml)
	}
	if got := readFixtureFile(t, versionPath); got != "pub const CODEX_CLI_VERSION: &str = env!(\"CARGO_PKG_VERSION\");\n" {
		t.Fatalf("version.rs was modified:\n%s", got)
	}
	if got := readFixtureFile(t, mainPath); got != "mod doctor;\n" {
		t.Fatalf("main.rs was modified:\n%s", got)
	}
}

func TestInstallPackageCreatesStandaloneLinks(t *testing.T) {
	root := t.TempDir()
	packageDir := filepath.Join(root, "package")
	binDir := filepath.Join(packageDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "codex-package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile package metadata: %v", err)
	}
	packageHome := filepath.Join(root, "package-home")
	installDir := filepath.Join(root, "bin")
	releaseDir := filepath.Join(packageHome, "packages", "standalone", "releases", "0.133.0-main-abcdef-aarch64-apple-darwin")
	if err := installPackage(context.Background(), patch.NewRunner(context.Background(), false, &bytes.Buffer{}), packageDir, releaseDir, packageHome, installDir, "bin/codex", "codex"); err != nil {
		t.Fatalf("installPackage: %v", err)
	}
	currentLink := filepath.Join(packageHome, "packages", "standalone", "current")
	currentTarget, err := os.Readlink(currentLink)
	if err != nil {
		t.Fatalf("Readlink current: %v", err)
	}
	if currentTarget != releaseDir {
		t.Fatalf("current link = %q, want %q", currentTarget, releaseDir)
	}
	binTarget, err := os.Readlink(filepath.Join(installDir, "codex"))
	if err != nil {
		t.Fatalf("Readlink visible command: %v", err)
	}
	wantBinTarget := filepath.Join(currentLink, "bin", "codex")
	if binTarget != wantBinTarget {
		t.Fatalf("visible command link = %q, want %q", binTarget, wantBinTarget)
	}
	releaseCodex, err := os.Readlink(filepath.Join(releaseDir, "codex"))
	if err != nil {
		t.Fatalf("Readlink release codex: %v", err)
	}
	if releaseCodex != "bin/codex" {
		t.Fatalf("release codex link = %q, want bin/codex", releaseCodex)
	}
}

func TestParseEnvOutput(t *testing.T) {
	env, err := parseEnvOutput([]byte("RUSTY_V8_ARCHIVE=/tmp/archive\nRUSTY_V8_SRC_BINDING_PATH=/tmp/binding\n"))
	if err != nil {
		t.Fatalf("parseEnvOutput: %v", err)
	}
	if env["RUSTY_V8_ARCHIVE"] != "/tmp/archive" {
		t.Fatalf("RUSTY_V8_ARCHIVE = %q", env["RUSTY_V8_ARCHIVE"])
	}
	if env["RUSTY_V8_SRC_BINDING_PATH"] != "/tmp/binding" {
		t.Fatalf("RUSTY_V8_SRC_BINDING_PATH = %q", env["RUSTY_V8_SRC_BINDING_PATH"])
	}
}

func testInstallOptions(home string, cacheHome string) InstallOptions {
	return InstallOptions{
		DryRun:            true,
		Repo:              "openai/codex",
		SourceDir:         filepath.Join(cacheHome, "clyde", "desktop-via-clyde", "codex", "source"),
		Ref:               "origin/main",
		PackageDir:        filepath.Join(cacheHome, "clyde", "desktop-via-clyde", "codex", "package"),
		PackageVariant:    "codex",
		PackageBinaryPath: "bin/codex",
		CommandName:       "codex",
		InstallDir:        filepath.Join(home, ".local", "bin"),
		PackageHome:       filepath.Join(home, ".codex"),
		BuildMode:         string(BuildModeLocalFast),
		NoSccache:         false,
		ForceRebuild:      false,
		Out:               nil,
	}
}

func TestLatestMainReleaseDirUsesSingleReleasePath(t *testing.T) {
	path := latestMainReleaseDir("/tmp/codex-home")
	if !strings.HasSuffix(path, "packages/standalone/releases/latest-main") {
		t.Fatalf("latest-main release dir = %q", path)
	}
}

func TestCodexInstallLockPathUsesSharedBuildRoot(t *testing.T) {
	sourceDir := filepath.Join("/tmp", "cache", "clyde", "desktop-via-clyde", "codex", "source")
	path := codexInstallLockPath(sourceDir)
	want := filepath.Join("/tmp", "cache", "clyde", "desktop-via-clyde", "codex", "build", ".install.lock")
	if path != want {
		t.Fatalf("codexInstallLockPath = %q, want %q", path, want)
	}
}

func TestVerifyReleaseCandidateRejectsVersionMismatchBeforeReuse(t *testing.T) {
	releasePath := t.TempDir()
	metadata := `{"layoutVersion":1,"version":"0.0.0","target":"aarch64-apple-darwin","variant":"codex"}`
	if err := os.WriteFile(filepath.Join(releasePath, "codex-package.json"), []byte(metadata), 0o644); err != nil {
		t.Fatalf("WriteFile metadata: %v", err)
	}
	err := verifyReleaseCandidate(
		context.Background(),
		releasePath,
		"aarch64-apple-darwin",
		"bin/codex",
		"codex",
		"0.137.0-main.abcdef012345+tree.abcdef012345.build.123456789abc",
	)
	if err == nil || !strings.Contains(err.Error(), "release version mismatch") {
		t.Fatalf("verifyReleaseCandidate err = %v", err)
	}
}

func TestResolveRustcWrapper(t *testing.T) {
	lookups := 0
	lookupPath := func(name string) (string, error) {
		lookups++
		if name != "sccache" {
			t.Fatalf("lookupPath called with %q", name)
		}
		return "/opt/homebrew/bin/sccache", nil
	}
	path, used := resolveRustcWrapper("/custom/wrapper", false, lookupPath)
	if path != "/custom/wrapper" || used {
		t.Fatalf("existing wrapper = %q used=%v", path, used)
	}
	if lookups != 0 {
		t.Fatalf("lookupPath called %d times, want 0", lookups)
	}

	path, used = resolveRustcWrapper("/opt/homebrew/bin/sccache", false, lookupPath)
	if path != "/opt/homebrew/bin/sccache" || !used {
		t.Fatalf("existing sccache wrapper = %q used=%v", path, used)
	}

	path, used = resolveRustcWrapper("", true, lookupPath)
	if path != "" || used {
		t.Fatalf("disabled sccache wrapper = %q used=%v", path, used)
	}

	path, used = resolveRustcWrapper("", false, lookupPath)
	if path != "/opt/homebrew/bin/sccache" || !used {
		t.Fatalf("discovered sccache wrapper = %q used=%v", path, used)
	}
}

func requireTraceCommand(t *testing.T, trace *patch.Trace, command string, args []string) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Command != command {
			continue
		}
		if equalStrings(event.Args, args) {
			return
		}
	}
	t.Fatalf("trace missing command=%s args=%v events=%#v", command, args, trace.Events)
}

func requireTraceCommandContains(t *testing.T, trace *patch.Trace, command string, needles ...string) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Command != command {
			continue
		}
		if argsContainAll(event.Args, needles) {
			return
		}
	}
	t.Fatalf("trace missing command=%s args containing %v events=%#v", command, needles, trace.Events)
}

func argsContainAll(args []string, needles []string) bool {
	for _, needle := range needles {
		found := false
		for _, arg := range args {
			if arg == needle || strings.HasPrefix(arg, needle) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func installFixture(t *testing.T) {
	t.Helper()
	if err := registerFixtureCapabilities(); err != nil {
		t.Fatalf("RegisterFixtureCapabilities(): %v", err)
	}
	cfg, err := config.LoadPath(filepath.Join("..", "testconfig", "testdata", "current-config.toml"))
	if err != nil {
		t.Fatalf("LoadPath(current-config.toml): %v", err)
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() {
		config.SetCurrent(nil)
	})
}

func writeFixtureFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func readFixtureFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(body)
}
