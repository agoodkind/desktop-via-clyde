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
	"time"

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

func TestStampCodexBuildSourceSplitsWorkspaceAndAppCrateVersions(t *testing.T) {
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
	codexRoot := filepath.Join(buildDir, "codex-rs")
	workspacePath := filepath.Join(codexRoot, "Cargo.toml")
	writeFixtureFile(t, workspacePath, "[workspace]\nmembers = []\n\n[workspace.package]\nedition = \"2024\"\nversion = \"0.0.0\"\n")
	// App-facing crates inherit the workspace version via version.workspace = true.
	appCrate := filepath.Join(codexRoot, "cli", "Cargo.toml")
	tuiCrate := filepath.Join(codexRoot, "tui", "Cargo.toml")
	writeFixtureFile(t, appCrate, "[package]\nname = \"codex-cli\"\nversion.workspace = true\nedition.workspace = true\n")
	writeFixtureFile(t, tuiCrate, "[package]\nname = \"codex-tui\"\nversion.workspace = true\n")
	// A non-app crate stays on the stable base version (left inheriting the workspace).
	coreCrate := filepath.Join(codexRoot, "core", "Cargo.toml")
	writeFixtureFile(t, coreCrate, "[package]\nname = \"codex-core\"\nversion.workspace = true\n")
	// Source files must never be modified by stamping.
	versionPath := filepath.Join(codexRoot, "tui", "src", "version.rs")
	writeFixtureFile(t, versionPath, "pub const CODEX_CLI_VERSION: &str = env!(\"CARGO_PKG_VERSION\");\n")

	if err := stampCodexBuildSource(buildDir, identity, ""); err != nil {
		t.Fatalf("stampCodexBuildSource: %v", err)
	}

	if got := readFixtureFile(t, workspacePath); !strings.Contains(got, `version = "`+identity.BaseVersion+`"`) {
		t.Fatalf("workspace Cargo.toml should hold the stable base version:\n%s", got)
	}
	for _, cratePath := range []string{appCrate, tuiCrate} {
		got := readFixtureFile(t, cratePath)
		if !strings.Contains(got, `version = "`+identity.PackageVersion+`"`) {
			t.Fatalf("app crate %s missing full per-commit version:\n%s", cratePath, got)
		}
	}
	if got := readFixtureFile(t, coreCrate); !strings.Contains(got, "version.workspace = true") {
		t.Fatalf("non-app crate core should keep inheriting the workspace version:\n%s", got)
	}
	if got := readFixtureFile(t, versionPath); got != "pub const CODEX_CLI_VERSION: &str = env!(\"CARGO_PKG_VERSION\");\n" {
		t.Fatalf("version.rs was modified:\n%s", got)
	}
}

func TestStampPackageVersionInTableMtimeBehavior(t *testing.T) {
	past := time.Unix(1_700_000_000, 0)

	// Workspace manifest: preserveMtime keeps the source mtime so cargo does not
	// recheck the whole workspace when the base version is unchanged.
	wsPath := filepath.Join(t.TempDir(), "Cargo.toml")
	writeFixtureFile(t, wsPath, "[workspace.package]\nversion = \"0.0.0\"\n")
	setMtime(t, wsPath, past)
	if err := stampPackageVersionInTable(wsPath, "[workspace.package]", "0.141.0", true); err != nil {
		t.Fatalf("stamp workspace: %v", err)
	}
	wsInfo, err := os.Stat(wsPath)
	if err != nil {
		t.Fatalf("stat workspace: %v", err)
	}
	if !wsInfo.ModTime().Truncate(time.Second).Equal(past.Truncate(time.Second)) {
		t.Fatalf("workspace mtime not preserved: got %v want %v", wsInfo.ModTime(), past)
	}
	if got := readFixtureFile(t, wsPath); !strings.Contains(got, `version = "0.141.0"`) {
		t.Fatalf("workspace version not stamped:\n%s", got)
	}

	// App crate manifest: mtime must advance so cargo recompiles when the
	// per-commit version changes.
	appPath := filepath.Join(t.TempDir(), "Cargo.toml")
	writeFixtureFile(t, appPath, "[package]\nname = \"codex-cli\"\nversion.workspace = true\n")
	setMtime(t, appPath, past)
	full := "0.141.0-main.64bdeed9f7ad+tree.53a0b16bef4b.build.a9b9299c326b"
	if err := stampPackageVersionInTable(appPath, "[package]", full, false); err != nil {
		t.Fatalf("stamp app crate: %v", err)
	}
	appInfo, err := os.Stat(appPath)
	if err != nil {
		t.Fatalf("stat app crate: %v", err)
	}
	if appInfo.ModTime().Truncate(time.Second).Equal(past.Truncate(time.Second)) {
		t.Fatalf("app crate mtime should advance, still %v", appInfo.ModTime())
	}
	if got := readFixtureFile(t, appPath); !strings.Contains(got, `version = "`+full+`"`) {
		t.Fatalf("app crate version not stamped:\n%s", got)
	}
}

func TestStampCodexBuildSourceWorkspaceMtimeGatedOnBase(t *testing.T) {
	past := time.Unix(1_700_000_000, 0)
	newID := func() codexBuildIdentity {
		return newBuildIdentity(
			"0.141.0", "80b65e994573", "abcdef0123456789abcdef0123456789abcdef01",
			"aarch64-apple-darwin", BuildModeLocalFast, "codex", "codex",
		)
	}
	stampWorkspace := func(t *testing.T, previousBase string) time.Time {
		t.Helper()
		buildDir := t.TempDir()
		wsPath := filepath.Join(buildDir, "codex-rs", "Cargo.toml")
		writeFixtureFile(t, wsPath, "[workspace.package]\nversion = \"0.0.0\"\n")
		setMtime(t, wsPath, past)
		if err := stampCodexBuildSource(buildDir, newID(), previousBase); err != nil {
			t.Fatalf("stampCodexBuildSource: %v", err)
		}
		info, err := os.Stat(wsPath)
		if err != nil {
			t.Fatalf("stat workspace: %v", err)
		}
		return info.ModTime()
	}

	// Same base as the previous build: mtime preserved so cargo skips the recheck.
	if got := stampWorkspace(t, "0.141.0"); !got.Truncate(time.Second).Equal(past.Truncate(time.Second)) {
		t.Fatalf("same-base workspace mtime should be preserved, got %v", got)
	}
	// Base bump: mtime advances so cargo rebuilds non-app crates with the new base.
	if got := stampWorkspace(t, "0.140.0"); got.Truncate(time.Second).Equal(past.Truncate(time.Second)) {
		t.Fatalf("base-bump workspace mtime should advance, still %v", got)
	}
}

func TestOverwriteCodexPackageVersionPreservesOtherFields(t *testing.T) {
	packageDir := t.TempDir()
	path := filepath.Join(packageDir, "codex-package.json")
	writeFixtureFile(t, path, `{"layoutVersion":1,"version":"0.137.0","target":"aarch64-apple-darwin","variant":"codex"}`)
	full := "0.137.0-main.80b65e994573+tree.abcdef012345.build.0123456789ab"

	if err := overwriteCodexPackageVersion(packageDir, full); err != nil {
		t.Fatalf("overwriteCodexPackageVersion: %v", err)
	}

	metadata, err := readPackageMetadata(packageDir, "codex")
	if err != nil {
		t.Fatalf("readPackageMetadata: %v", err)
	}
	if metadata.Version != full {
		t.Fatalf("version = %q, want %q", metadata.Version, full)
	}
	if metadata.Target != "aarch64-apple-darwin" || metadata.Variant != "codex" {
		t.Fatalf("other fields not preserved: %+v", metadata)
	}
	if got := readFixtureFile(t, path); !strings.Contains(got, `"layoutVersion":1`) {
		t.Fatalf("layoutVersion not preserved:\n%s", got)
	}
}

func TestPrepareStampedBuildSourceMirrorsSourceWithSharedTargetCache(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not available")
	}
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	writeFixtureFile(t, filepath.Join(sourceDir, "codex-rs", "Cargo.toml"),
		"[workspace]\nmembers = []\n\n[workspace.package]\nedition = \"2024\"\nversion = \"0.0.0\"\n")
	writeFixtureFile(t, filepath.Join(sourceDir, "codex-rs", "core", "src", "lib.rs"), "pub fn run() {}\n")
	// An in-place build target in the source must never be copied into the work tree.
	writeFixtureFile(t, filepath.Join(sourceDir, "codex-rs", "target", "junk.bin"), "stale build output\n")

	identity := newBuildIdentity(
		"0.137.0", "80b65e994573", "abcdef0123456789abcdef0123456789abcdef01",
		"aarch64-apple-darwin", BuildModeLocalFast, "codex", "codex",
	)
	r := patch.NewRunner(context.Background(), false, &bytes.Buffer{})
	buildDir, err := prepareStampedBuildSource(context.Background(), r, sourceDir, identity)
	if err != nil {
		t.Fatalf("prepareStampedBuildSource: %v", err)
	}

	wantBuildDir := filepath.Join(root, "build", "work")
	if buildDir != wantBuildDir {
		t.Fatalf("buildDir = %q, want %q", buildDir, wantBuildDir)
	}
	cargoToml := readFixtureFile(t, filepath.Join(buildDir, "codex-rs", "Cargo.toml"))
	if !strings.Contains(cargoToml, `version = "`+identity.BaseVersion+`"`) {
		t.Fatalf("workspace Cargo.toml missing stable base version:\n%s", cargoToml)
	}
	if got := readFixtureFile(t, filepath.Join(buildDir, "codex-rs", "core", "src", "lib.rs")); got != "pub fn run() {}\n" {
		t.Fatalf("lib.rs not mirrored: %q", got)
	}
	targetLink, err := os.Readlink(filepath.Join(buildDir, "codex-rs", "target"))
	if err != nil {
		t.Fatalf("Readlink work target: %v", err)
	}
	wantCache := filepath.Join(root, "build", "target")
	if targetLink != wantCache {
		t.Fatalf("work target symlink = %q, want %q", targetLink, wantCache)
	}
	if _, err := os.Stat(filepath.Join(wantCache, "junk.bin")); !os.IsNotExist(err) {
		t.Fatalf("in-place source target was copied into shared cache: err=%v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "build"))
	if err != nil {
		t.Fatalf("ReadDir build: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		leaked := strings.HasPrefix(name, "source-") ||
			strings.HasPrefix(name, ".source-") ||
			(strings.HasPrefix(name, "target-") && name != "target")
		if leaked {
			t.Fatalf("unexpected leaked build dir/file: %q", name)
		}
	}
}

func TestPrepareStampedBuildSourceReusesCacheAndPreservesMtimes(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not available")
	}
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	writeFixtureFile(t, filepath.Join(sourceDir, "codex-rs", "Cargo.toml"),
		"[workspace]\nmembers = []\n\n[workspace.package]\nedition = \"2024\"\nversion = \"0.0.0\"\n")
	libPath := filepath.Join(sourceDir, "codex-rs", "core", "src", "lib.rs")
	writeFixtureFile(t, libPath, "pub fn run() {}\n")
	// Pin an unchanged source file to a fixed past mtime; rsync -a must preserve it,
	// unlike the old git-archive path that rewrote every file to the commit time.
	pastTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(libPath, pastTime, pastTime); err != nil {
		t.Fatalf("Chtimes source lib.rs: %v", err)
	}

	identity := newBuildIdentity(
		"0.137.0", "80b65e994573", "abcdef0123456789abcdef0123456789abcdef01",
		"aarch64-apple-darwin", BuildModeLocalFast, "codex", "codex",
	)
	r := patch.NewRunner(context.Background(), false, &bytes.Buffer{})
	buildDir, err := prepareStampedBuildSource(context.Background(), r, sourceDir, identity)
	if err != nil {
		t.Fatalf("prepareStampedBuildSource run 1: %v", err)
	}
	workLib := filepath.Join(buildDir, "codex-rs", "core", "src", "lib.rs")
	info, err := os.Stat(workLib)
	if err != nil {
		t.Fatalf("Stat work lib.rs: %v", err)
	}
	if !info.ModTime().Truncate(time.Second).Equal(pastTime.Truncate(time.Second)) {
		t.Fatalf("work lib.rs mtime = %v, want preserved %v", info.ModTime(), pastTime)
	}

	// A sentinel in the shared cache must survive a second build (cache is reused, not wiped).
	sentinel := filepath.Join(root, "build", "target", "SENTINEL")
	writeFixtureFile(t, sentinel, "warm\n")
	// Change a different file so the second sync has work to do.
	writeFixtureFile(t, filepath.Join(sourceDir, "codex-rs", "core", "src", "extra.rs"), "pub fn extra() {}\n")

	if _, err := prepareStampedBuildSource(context.Background(), r, sourceDir, identity); err != nil {
		t.Fatalf("prepareStampedBuildSource run 2: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("shared cache sentinel lost across builds: %v", err)
	}
	info2, err := os.Stat(workLib)
	if err != nil {
		t.Fatalf("Stat work lib.rs run 2: %v", err)
	}
	if !info2.ModTime().Truncate(time.Second).Equal(pastTime.Truncate(time.Second)) {
		t.Fatalf("work lib.rs mtime changed on rebuild = %v, want preserved %v", info2.ModTime(), pastTime)
	}
	if got := readFixtureFile(t, filepath.Join(buildDir, "codex-rs", "core", "src", "extra.rs")); got != "pub fn extra() {}\n" {
		t.Fatalf("new source file not mirrored on rebuild: %q", got)
	}
}

func setMtime(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}

func TestPruneTargetCacheArtifactsKeepsNewestVariant(t *testing.T) {
	root := t.TempDir()
	profile := filepath.Join(root, "aarch64-apple-darwin", "release")
	deps := filepath.Join(profile, "deps")
	oldHash := "1111111111111111"
	newHash := "2222222222222222"
	extHash := "abababababababab"
	oldTime := time.Unix(1_700_000_000, 0)
	newTime := time.Unix(1_700_100_000, 0)

	depFiles := map[string]time.Time{
		"libcodex_core-" + oldHash + ".rlib":  oldTime,
		"libcodex_core-" + oldHash + ".rmeta": oldTime,
		"libcodex_core-" + newHash + ".rlib":  newTime,
		"libcodex_core-" + newHash + ".rmeta": newTime,
		"codex-" + oldHash:                    oldTime,
		"codex-" + oldHash + ".d":             oldTime,
		"codex-" + newHash:                    newTime,
		"codex-" + newHash + ".d":             newTime,
		"libserde-" + extHash + ".rlib":       oldTime,
		"CACHEDIR.TAG":                        oldTime,
	}
	for name, when := range depFiles {
		path := filepath.Join(deps, name)
		writeFixtureFile(t, path, name)
		setMtime(t, path, when)
	}
	// .fingerprint and build are dirs named <crate>-<hash>; set the dir mtime after
	// writing children so the test controls which variant is newest.
	fpOld := filepath.Join(profile, ".fingerprint", "codex-"+oldHash)
	fpNew := filepath.Join(profile, ".fingerprint", "codex-"+newHash)
	buildOld := filepath.Join(profile, "build", "somecrate-"+oldHash)
	buildNew := filepath.Join(profile, "build", "somecrate-"+newHash)
	for _, dir := range []string{fpOld, fpNew, buildOld, buildNew} {
		writeFixtureFile(t, filepath.Join(dir, "out"), "x")
	}
	setMtime(t, fpOld, oldTime)
	setMtime(t, fpNew, newTime)
	setMtime(t, buildOld, oldTime)
	setMtime(t, buildNew, newTime)

	r := patch.NewRunner(context.Background(), false, &bytes.Buffer{})
	pruneTargetCacheArtifacts(context.Background(), r, root, 1)

	mustExist := []string{
		filepath.Join(deps, "libcodex_core-"+newHash+".rlib"),
		filepath.Join(deps, "libcodex_core-"+newHash+".rmeta"),
		filepath.Join(deps, "codex-"+newHash),
		filepath.Join(deps, "codex-"+newHash+".d"),
		filepath.Join(deps, "libserde-"+extHash+".rlib"),
		filepath.Join(deps, "CACHEDIR.TAG"),
		fpNew,
		buildNew,
	}
	for _, path := range mustExist {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected kept path missing: %s: %v", path, err)
		}
	}
	mustGone := []string{
		filepath.Join(deps, "libcodex_core-"+oldHash+".rlib"),
		filepath.Join(deps, "libcodex_core-"+oldHash+".rmeta"),
		filepath.Join(deps, "codex-"+oldHash),
		filepath.Join(deps, "codex-"+oldHash+".d"),
		fpOld,
		buildOld,
	}
	for _, path := range mustGone {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected pruned path still present: %s: err=%v", path, err)
		}
	}
}

func TestPruneCargoArtifactDirKeepsTwoNewest(t *testing.T) {
	deps := t.TempDir()
	base := "libcodex_core"
	hashes := []struct {
		hash string
		when time.Time
	}{
		{"1111111111111111", time.Unix(1_700_000_000, 0)},
		{"2222222222222222", time.Unix(1_700_100_000, 0)},
		{"3333333333333333", time.Unix(1_700_200_000, 0)},
	}
	for _, h := range hashes {
		path := filepath.Join(deps, base+"-"+h.hash+".rlib")
		writeFixtureFile(t, path, h.hash)
		setMtime(t, path, h.when)
	}

	removed := pruneCargoArtifactDir(context.Background(), deps, 2)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(filepath.Join(deps, base+"-1111111111111111.rlib")); !os.IsNotExist(err) {
		t.Fatalf("oldest variant should be pruned: err=%v", err)
	}
	for _, hash := range []string{"2222222222222222", "3333333333333333"} {
		if _, err := os.Stat(filepath.Join(deps, base+"-"+hash+".rlib")); err != nil {
			t.Fatalf("newer variant %s should be kept: %v", hash, err)
		}
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
