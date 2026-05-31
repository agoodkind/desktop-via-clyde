package codexcli

import (
	"bytes"
	"context"
	"io"
	"os"
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
	codexRoot := filepath.Join(sourceDir, "codex-rs")
	requireTraceCommand(t, trace, "gh", []string{"repo", "clone", "openai/codex", sourceDir, "--", "--depth", "1"})
	requireTraceCommand(t, trace, "git", []string{"-C", sourceDir, "fetch", "--depth", "1", "--prune", "origin", "main"})
	requireTraceCommand(t, trace, "git", []string{"-C", sourceDir, "checkout", "--detach", "FETCH_HEAD"})
	requireTraceCommand(t, trace, "rustup", []string{"toolchain", "install"})
	requireTraceCommand(t, trace, "cargo", []string{"build", "--target", "aarch64-apple-darwin", "--release", "--timings", "--bin", "codex", "-v"})
	requireTraceCommand(t, trace, "python3", []string{
		filepath.Join(sourceDir, "scripts", "build_codex_package.py"),
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

func TestReleaseDirUsesLocalFastSuffix(t *testing.T) {
	path := releaseDir("/tmp/codex-home", "0.0.0", "abcdef012345", "aarch64-apple-darwin", BuildModeLocalFast)
	if !strings.HasSuffix(path, "0.0.0-main-abcdef012345-aarch64-apple-darwin-local-fast") {
		t.Fatalf("local-fast release dir = %q", path)
	}
}

func TestFindMatchingReleaseDirsHonorsBuildModeSuffix(t *testing.T) {
	releasesRoot := t.TempDir()
	paths := []string{
		filepath.Join(releasesRoot, "0.0.0-main-abcdef-aarch64-apple-darwin"),
		filepath.Join(releasesRoot, "0.0.0-main-abcdef-aarch64-apple-darwin-local-fast"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", path, err)
		}
	}
	releaseMatches, err := findMatchingReleaseDirs(releasesRoot, releaseNameSuffix("abcdef", "aarch64-apple-darwin", BuildModeRelease))
	if err != nil {
		t.Fatalf("findMatchingReleaseDirs release: %v", err)
	}
	if len(releaseMatches) != 1 || releaseMatches[0] != paths[0] {
		t.Fatalf("release matches = %#v", releaseMatches)
	}
	fastMatches, err := findMatchingReleaseDirs(releasesRoot, releaseNameSuffix("abcdef", "aarch64-apple-darwin", BuildModeLocalFast))
	if err != nil {
		t.Fatalf("findMatchingReleaseDirs local-fast: %v", err)
	}
	if len(fastMatches) != 1 || fastMatches[0] != paths[1] {
		t.Fatalf("local-fast matches = %#v", fastMatches)
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
	cfg, err := config.LoadPath(filepath.Join("..", "testconfig", "testdata", "current-config.toml"))
	if err != nil {
		t.Fatalf("LoadPath(current-config.toml): %v", err)
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() {
		config.SetCurrent(nil)
	})
}
