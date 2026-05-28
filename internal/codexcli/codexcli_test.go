package codexcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/patch"
)

func TestInstallDryRunUsesShallowGhCloneAndOriginMain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codex-cli install is macOS-only")
	}
	home := t.TempDir()
	cacheHome := filepath.Join(home, ".cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	var out bytes.Buffer
	err := Install(context.Background(), InstallOptions{
		DryRun:    true,
		BuildMode: string(BuildModeRelease),
		Out:       &out,
	})
	if err != nil {
		t.Fatalf("Install dry-run: %v", err)
	}
	log := out.String()
	sourceDir := filepath.Join(cacheHome, "desktop-via-clyde", "codex", "source")
	assertContains(t, log, "codex-cli step 1/7: update Codex source checkout")
	assertContains(t, log, "codex-cli: source checkout missing, cloning openai/codex with depth 1")
	assertContains(t, log, "gh repo clone openai/codex "+sourceDir+" -- --depth 1")
	assertContains(t, log, "codex-cli: fetching origin/main from origin with depth 1")
	assertContains(t, log, "git -C "+sourceDir+" fetch --depth 1 --prune origin main")
	assertContains(t, log, "git -C "+sourceDir+" checkout --detach FETCH_HEAD")
	assertContains(t, log, "codex-cli step 2/7: build upstream Codex entrypoint")
	codexRoot := filepath.Join(sourceDir, "codex-rs")
	assertContains(t, log, "codex-cli: Cargo build will run from "+codexRoot+" so rustup honors upstream rust-toolchain.toml")
	assertContains(t, log, "codex-cli: release mode preserves the upstream release profile exactly")
	assertContains(t, log, "codex-cli: installing or updating upstream Rust toolchain from "+filepath.Join(codexRoot, "rust-toolchain.toml"))
	assertContains(t, log, "cd "+codexRoot)
	assertContains(t, log, "rustup toolchain install")
	assertContains(t, log, "cargo build")
	assertContains(t, log, "--target aarch64-apple-darwin --release --timings --bin codex -v")
	assertContains(t, log, "codex-cli step 3/7: sign upstream Codex entrypoint")
	assertContains(t, log, "codex-cli: using upstream entitlements")
	assertContains(t, log, "/usr/bin/codesign --force --options runtime --timestamp --entitlements")
	assertContains(t, log, "codex-cli step 4/7: build upstream Codex package")
	assertContains(t, log, "codex-cli: upstream package builder output follows")
	assertContains(t, log, "python3 "+filepath.Join(sourceDir, "scripts", "build_codex_package.py"))
	assertContains(t, log, "--entrypoint-bin "+filepath.Join(sourceDir, "codex-rs", "target", "aarch64-apple-darwin", "release", "codex"))
	assertContains(t, log, "codex-cli step 6/7: install standalone package")
	assertContains(t, log, "codex-cli: would stage package at")
	assertContains(t, log, "codex-cli step 7/7: verify installed command")
	assertContains(t, log, "codex-cli: verifying executable by running")
}

func TestDefaultBuildModeIsLocalFast(t *testing.T) {
	if got := DefaultBuildMode(); got != string(BuildModeLocalFast) {
		t.Fatalf("DefaultBuildMode = %q, want %q", got, string(BuildModeLocalFast))
	}
}

func TestInstallDryRunDefaultsToLocalFastWhenBuildModeUnset(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codex-cli install is macOS-only")
	}
	home := t.TempDir()
	cacheHome := filepath.Join(home, ".cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	var out bytes.Buffer
	err := Install(context.Background(), InstallOptions{
		DryRun: true,
		Out:    &out,
	})
	if err != nil {
		t.Fatalf("Install dry-run with default build mode: %v", err)
	}
	log := out.String()
	assertContains(t, log, "build-mode=local-fast")
	assertContains(t, log, "codex-cli: local-fast mode overrides release settings with lto=false and codegen-units=")
	assertContains(t, log, "--config profile.release.lto=false")
}

func TestInstallDryRunLocalFastAddsCargoOverrides(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codex-cli install is macOS-only")
	}
	home := t.TempDir()
	cacheHome := filepath.Join(home, ".cache")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	var out bytes.Buffer
	err := Install(context.Background(), InstallOptions{
		DryRun:    true,
		BuildMode: string(BuildModeLocalFast),
		Out:       &out,
	})
	if err != nil {
		t.Fatalf("Install local-fast dry-run: %v", err)
	}
	log := out.String()
	assertContains(t, log, "build-mode=local-fast")
	assertContains(t, log, "codex-cli: local-fast mode overrides release settings with lto=false and codegen-units=")
	assertContains(t, log, "--config profile.release.lto=false")
	assertContains(t, log, "--config profile.release.codegen-units=")
	assertContains(t, log, "dryrun-main-dryrun-aarch64-apple-darwin-local-fast")
}

func TestReadPackageMetadata(t *testing.T) {
	packageDir := t.TempDir()
	body := []byte(`{"layoutVersion":1,"version":"0.133.0","target":"aarch64-apple-darwin","variant":"codex"}`)
	if err := os.WriteFile(filepath.Join(packageDir, "codex-package.json"), body, 0o644); err != nil {
		t.Fatalf("WriteFile metadata: %v", err)
	}
	got, err := readPackageMetadata(packageDir)
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
	codexHome := filepath.Join(root, "codex-home")
	installDir := filepath.Join(root, "bin")
	releaseDir := filepath.Join(codexHome, "packages", "standalone", "releases", "0.133.0-main-abcdef-aarch64-apple-darwin")
	if err := installPackage(context.Background(), patch.NewRunner(context.Background(), false, &bytes.Buffer{}), packageDir, releaseDir, codexHome, installDir); err != nil {
		t.Fatalf("installPackage: %v", err)
	}
	currentLink := filepath.Join(codexHome, "packages", "standalone", "current")
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

func assertContains(t *testing.T, haystack string, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected %q in:\n%s", needle, haystack)
	}
}
