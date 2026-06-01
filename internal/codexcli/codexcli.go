// Package codexcli builds and installs a locally signed Codex CLI from source.
package codexcli

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/signing"
)

var codexcliLog = slog.With("component", "desktop-via-clyde", "subcomponent", "codex-cli")

const (
	StandaloneInstallCapability = "standalone-cli.install"
	StandaloneStatusCapability  = "standalone-cli.status"
)

// RegisterOperations links Codex CLI operation capabilities.
func RegisterOperations() error {
	if !catalog.HasOperationCapability(StandaloneInstallCapability) {
		if err := catalog.RegisterOperationCapability(StandaloneInstallCapability); err != nil {
			return err
		}
	}
	if err := operations.Register(StandaloneInstallCapability, InstallOperation); err != nil {
		return err
	}
	if !catalog.HasOperationCapability(StandaloneStatusCapability) {
		if err := catalog.RegisterOperationCapability(StandaloneStatusCapability); err != nil {
			return err
		}
	}
	return operations.Register(StandaloneStatusCapability, StatusOperation)
}

// BuildMode selects the Cargo profile tuning used for local Codex CLI builds.
type BuildMode string

const (
	// BuildModeRelease matches the upstream release build profile.
	BuildModeRelease BuildMode = "release"
	// BuildModeLocalFast keeps upstream release semantics but relaxes the slowest knobs for local iteration.
	BuildModeLocalFast BuildMode = "local-fast"
)

//go:embed scripts/resolve_codex_v8_env.py
var resolveCodexV8EnvScript string

// InstallOptions controls one Codex CLI source build and install.
type InstallOptions struct {
	DryRun            bool
	Repo              string
	SourceDir         string
	Ref               string
	PackageDir        string
	PackageVariant    string
	PackageBinaryPath string
	CommandName       string
	InstallDir        string
	PackageHome       string
	BuildMode         string
	NoSccache         bool
	ForceRebuild      bool
	Out               io.Writer
	Trace             *patch.Trace
}

// StatusOptions controls status output.
type StatusOptions struct {
	SourceDir         string
	InstallDir        string
	PackageHome       string
	CommandName       string
	PackageBinaryPath string
	Out               io.Writer
}

// InstallOperation installs the linked standalone CLI implementation.
func InstallOperation(ctx context.Context, req operations.Request) error {
	if err := Install(ctx, InstallOptions{
		DryRun:            req.Flags.Bool("dry-run"),
		Repo:              req.Flags.String("repo"),
		SourceDir:         req.Flags.String("source-dir"),
		Ref:               req.Flags.String("ref"),
		PackageDir:        req.Flags.String("package-dir"),
		PackageVariant:    req.Flags.String("package-variant"),
		PackageBinaryPath: req.Flags.String("package-binary-path"),
		CommandName:       req.Flags.String("command-name"),
		InstallDir:        req.Flags.String("install-dir"),
		PackageHome:       req.Flags.String("package-home"),
		BuildMode:         req.Flags.String("build-mode"),
		NoSccache:         req.Flags.Bool("no-sccache"),
		ForceRebuild:      req.Flags.Bool("force-rebuild"),
		Out:               req.Out,
		Trace:             nil,
	}); err != nil {
		return operations.Error(ctx, "operations.standalone_install_failed", "install standalone cli", err)
	}
	return nil
}

// StatusOperation prints status for the linked standalone CLI implementation.
func StatusOperation(ctx context.Context, req operations.Request) error {
	if err := Status(ctx, StatusOptions{
		SourceDir:         req.Flags.String("source-dir"),
		InstallDir:        req.Flags.String("install-dir"),
		PackageHome:       req.Flags.String("package-home"),
		CommandName:       req.Flags.String("command-name"),
		PackageBinaryPath: req.Flags.String("package-binary-path"),
		Out:               req.Out,
	}); err != nil {
		return operations.Error(ctx, "operations.standalone_status_failed", "print standalone cli status", err)
	}
	return nil
}

type packageMetadata struct {
	Version string `json:"version"`
	Target  string `json:"target"`
	Variant string `json:"variant"`
}

func validateInstallOptions(opts InstallOptions) error {
	missing := requiredOptionName(map[string]string{
		"repo":                opts.Repo,
		"source-dir":          opts.SourceDir,
		"ref":                 opts.Ref,
		"package-dir":         opts.PackageDir,
		"package-variant":     opts.PackageVariant,
		"package-binary-path": opts.PackageBinaryPath,
		"command-name":        opts.CommandName,
		"install-dir":         opts.InstallDir,
		"package-home":        opts.PackageHome,
		"build-mode":          opts.BuildMode,
	})
	if missing != "" {
		return fmt.Errorf("%s is required", missing)
	}
	return nil
}

func validateStatusOptions(opts StatusOptions) error {
	missing := requiredOptionName(map[string]string{
		"source-dir":          opts.SourceDir,
		"install-dir":         opts.InstallDir,
		"package-home":        opts.PackageHome,
		"command-name":        opts.CommandName,
		"package-binary-path": opts.PackageBinaryPath,
	})
	if missing != "" {
		return fmt.Errorf("%s is required", missing)
	}
	return nil
}

func requiredOptionName(values map[string]string) string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(values[name]) == "" {
			return name
		}
	}
	return ""
}

// Install clones or updates Codex, builds an upstream package layout, signs the
// entrypoint with the local Developer ID, and installs the package.
func Install(ctx context.Context, opts InstallOptions) error {
	log := codexcliLog.With("function", "Install")
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if err := validateInstallOptions(opts); err != nil {
		log.ErrorContext(ctx, "codexcli.install.invalid_options", "err", err)
		return err
	}
	r := patch.NewRunner(ctx, opts.DryRun, opts.Out)
	r.Trace = opts.Trace
	buildMode, err := parseBuildMode(opts.BuildMode)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.install.parse_build_mode_failed", "err", err)
		return err
	}

	target, err := hostTargetTriple()
	if err != nil {
		return err
	}
	notef(r, "codex-cli: source="+opts.SourceDir+" ref="+opts.Ref+" target="+target+" build-mode="+string(buildMode))
	notef(r, "codex-cli: update source checkout")
	if err := cloneOrUpdateSource(ctx, r, opts.Repo, opts.SourceDir, opts.Ref); err != nil {
		return err
	}
	head := "dryrun"
	if !opts.DryRun {
		headBytes, err := r.RunCaptureStdout(ctx, "git", "-C", opts.SourceDir, "rev-parse", "--short=12", "HEAD")
		if err != nil {
			return fmt.Errorf("read Codex source HEAD: %w", err)
		}
		head = strings.TrimSpace(string(headBytes))
		notef(r, "codex-cli: source checkout is at HEAD "+head)
	}
	if !opts.DryRun {
		reusedReleaseDir, reused, err := maybeReuseInstalledRelease(
			ctx,
			r,
			opts.PackageHome,
			opts.InstallDir,
			opts.PackageBinaryPath,
			opts.PackageVariant,
			opts.CommandName,
			head,
			target,
			buildMode,
			opts.ForceRebuild,
		)
		if err != nil {
			return err
		}
		if reused {
			notef(r, "codex-cli: install complete release="+reusedReleaseDir)
			return nil
		}
	}

	notef(r, "codex-cli: build upstream entrypoint")
	entrypointPath, err := buildEntrypoint(ctx, r, opts.SourceDir, opts.CommandName, target, buildMode, opts.NoSccache)
	if err != nil {
		return err
	}
	notef(r, "codex-cli: sign upstream entrypoint")
	if err := signBinary(ctx, r, opts.SourceDir, entrypointPath); err != nil {
		return err
	}
	notef(r, "codex-cli: build upstream package")
	if err := buildPackage(ctx, r, opts.SourceDir, opts.PackageDir, opts.PackageVariant, target, entrypointPath); err != nil {
		return err
	}

	notef(r, "codex-cli: read package metadata")
	metadata := packageMetadata{
		Version: "dryrun",
		Target:  target,
		Variant: opts.PackageVariant,
	}
	if !opts.DryRun {
		metadata, err = readPackageMetadata(opts.PackageDir, opts.PackageVariant)
		if err != nil {
			return err
		}
	}
	releaseDir := releaseDir(opts.PackageHome, metadata.Version, head, metadata.Target, buildMode)
	notef(r, "codex-cli: package version="+metadata.Version+" target="+metadata.Target+" release="+releaseDir)
	notef(r, "codex-cli: install standalone package")
	if err := installPackage(ctx, r, opts.PackageDir, releaseDir, opts.PackageHome, opts.InstallDir, opts.PackageBinaryPath, opts.CommandName); err != nil {
		return err
	}
	notef(r, "codex-cli: verify installed command")
	if err := verifyInstalledCommand(ctx, r, opts.InstallDir, opts.CommandName); err != nil {
		return err
	}
	notef(r, "codex-cli: install complete release="+releaseDir)
	return nil
}

// Status prints the local Codex CLI source, install, and signing state. It is
// best-effort so one missing surface does not hide the rest.
func Status(ctx context.Context, opts StatusOptions) error {
	log := codexcliLog.With("function", "Status")
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if err := validateStatusOptions(opts); err != nil {
		log.ErrorContext(ctx, "codexcli.status.invalid_options", "err", err)
		return err
	}
	out := opts.Out
	binPath := filepath.Join(opts.InstallDir, opts.CommandName)
	currentLink := filepath.Join(opts.PackageHome, "packages", "standalone", "current")

	fmt.Fprintf(out, "source dir: %s\n", opts.SourceDir)
	if isDir(filepath.Join(opts.SourceDir, ".git")) {
		printCommandValue(ctx, out, "source head", "git", "-C", opts.SourceDir, "rev-parse", "--short=12", "HEAD")
		printCommandValue(ctx, out, "source branch", "git", "-C", opts.SourceDir, "branch", "--show-current")
	} else {
		fmt.Fprintln(out, "source head: missing")
	}

	fmt.Fprintf(out, "codex home: %s\n", opts.PackageHome)
	printSymlink(out, "current release", currentLink)
	printSymlink(out, "visible command", binPath)
	if _, err := os.Stat(binPath); err == nil {
		printCommandValue(ctx, out, "version", binPath, "--version")
		printCommandValue(ctx, out, "codesign", "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", binPath)
		printCommandValue(ctx, out, "signature", "/usr/bin/codesign", "-dv", binPath)
	} else {
		fmt.Fprintf(out, "version: missing at %s\n", binPath)
	}
	printCommandValue(ctx, out, "which -a "+opts.CommandName, "/usr/bin/which", "-a", opts.CommandName)
	return nil
}

func notef(r *patch.Runner, message string) {
	prefix := "[run]"
	if r.DryRun {
		prefix = "[dry-run]"
	}
	fmt.Fprintf(r.Out, "%s %s\n", prefix, message)
}

func hostTargetTriple() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("codex-cli install only supports macOS, got %s", runtime.GOOS)
	}
	if runtime.GOARCH == "arm64" {
		return "aarch64-apple-darwin", nil
	}
	if runtime.GOARCH == "amd64" {
		return "x86_64-apple-darwin", nil
	}
	return "", fmt.Errorf("unsupported macOS architecture %s", runtime.GOARCH)
}

func cloneOrUpdateSource(ctx context.Context, r *patch.Runner, repo string, sourceDir string, ref string) error {
	log := codexcliLog.With("function", "cloneOrUpdateSource")
	if !r.DryRun {
		if err := os.MkdirAll(filepath.Dir(sourceDir), 0o755); err != nil {
			log.ErrorContext(ctx, "codexcli.clone_or_update_source.mkdir_failed", "err", err)
			return fmt.Errorf("create source parent: %w", err)
		}
	}
	if _, err := os.Stat(sourceDir); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.ErrorContext(ctx, "codexcli.clone_or_update_source.stat_failed", "err", err)
			return fmt.Errorf("stat source dir: %w", err)
		}
		notef(r, "codex-cli: source checkout missing, cloning "+repo+" with depth 1")
		if err := r.RunWithHeartbeat(ctx, "codex-cli: cloning Codex source", 30*time.Second, "gh", "repo", "clone", repo, sourceDir, "--", "--depth", "1"); err != nil {
			return fmt.Errorf("clone Codex source: %w", err)
		}
	} else if !isDir(filepath.Join(sourceDir, ".git")) && !r.DryRun {
		return fmt.Errorf("source dir exists but is not a git checkout: %s", sourceDir)
	} else {
		notef(r, "codex-cli: source checkout exists, updating "+sourceDir)
	}
	notef(r, "codex-cli: fetching "+ref+" from origin with depth 1")
	if err := r.RunWithHeartbeat(ctx, "codex-cli: fetching Codex source", 30*time.Second, "git", "-C", sourceDir, "fetch", "--depth", "1", "--prune", "origin", fetchRef(ref)); err != nil {
		return fmt.Errorf("fetch Codex source: %w", err)
	}
	notef(r, "codex-cli: checking out fetched commit")
	if err := r.Run(ctx, "git", "-C", sourceDir, "checkout", "--detach", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout Codex source: %w", err)
	}
	return nil
}

func fetchRef(ref string) string {
	trimmedRef, ok := strings.CutPrefix(ref, "origin/")
	if ok {
		return trimmedRef
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
	ctx context.Context,
	r *patch.Runner,
	sourceDir string,
	packageDir string,
	packageVariant string,
	target string,
	entrypointPath string,
) error {
	log := codexcliLog.With("function", "buildPackage")
	script := filepath.Join(sourceDir, "scripts", "build_codex_package.py")
	notef(r, "codex-cli: build package at "+packageDir)
	notef(r, "codex-cli: upstream package builder output follows")
	if err := r.RunWithHeartbeat(ctx,
		"codex-cli: building Codex package",
		30*time.Second,
		"python3",
		script,
		"--target",
		target,
		"--variant",
		packageVariant,
		"--package-dir",
		packageDir,
		"--cargo-profile",
		"release",
		"--entrypoint-bin",
		entrypointPath,
		"--force",
	); err != nil {
		log.ErrorContext(ctx, "codexcli.build_package.failed", "err", err)
		return fmt.Errorf("build Codex package: %w", err)
	}
	return nil
}

func buildEntrypoint(
	ctx context.Context,
	r *patch.Runner,
	sourceDir string,
	commandName string,
	target string,
	buildMode BuildMode,
	noSccache bool,
) (string, error) {
	log := codexcliLog.With("function", "buildEntrypoint")
	codexRoot := filepath.Join(sourceDir, "codex-rs")
	entrypointPath := filepath.Join(codexRoot, "target", target, "release", commandName)
	notef(r, "codex-cli: build entrypoint at "+entrypointPath)
	if r.DryRun {
		notef(r, "codex-cli: Cargo build will run from "+codexRoot+" so rustup honors upstream rust-toolchain.toml")
	} else {
		notef(r, "codex-cli: resolving upstream Rusty V8 artifact overrides")
	}
	describeBuildMode(r, buildMode)
	if err := ensureRustToolchain(ctx, r, codexRoot); err != nil {
		log.ErrorContext(ctx, "codexcli.build_entrypoint.rust_toolchain_failed", "err", err)
		return "", err
	}
	cargoEnv, sccachePath, sccacheUsed, err := cargoBuildEnv(ctx, r, sourceDir, target, noSccache)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.build_entrypoint.env_failed", "err", err)
		return "", err
	}
	cargoArgs := cargoBuildArgs(target, buildMode, commandName)
	if err := r.RunEnvInDirWithHeartbeat(ctx,
		"codex-cli: building Codex entrypoint",
		30*time.Second,
		cargoEnv,
		codexRoot,
		"cargo",
		cargoArgs...,
	); err != nil {
		log.ErrorContext(ctx, "codexcli.build_entrypoint.build_failed", "err", err)
		return "", fmt.Errorf("build Codex entrypoint: %w", err)
	}
	if sccacheUsed {
		notef(r, "codex-cli: sccache stats follow")
		if err := r.Run(ctx, sccachePath, "--show-stats"); err != nil {
			notef(r, "codex-cli: could not read sccache stats: "+err.Error())
		}
	}
	if !r.DryRun {
		if _, err := os.Stat(entrypointPath); err != nil {
			return "", fmt.Errorf("stat built Codex entrypoint: %w", err)
		}
	}
	return entrypointPath, nil
}

func signBinary(ctx context.Context, r *patch.Runner, sourceDir string, binaryPath string) error {
	log := codexcliLog.With("function", "signBinary")
	entitlementsPath := filepath.Join(
		sourceDir,
		".github",
		"actions",
		"macos-code-sign",
		"codex.entitlements.plist",
	)
	if !r.DryRun {
		if _, err := os.Stat(binaryPath); err != nil {
			log.ErrorContext(ctx, "codexcli.sign_binary.binary_missing", "err", err)
			return fmt.Errorf("stat Codex binary: %w", err)
		}
		if _, err := os.Stat(entitlementsPath); err != nil {
			log.ErrorContext(ctx, "codexcli.sign_binary.entitlements_missing", "err", err)
			return fmt.Errorf("stat upstream Codex entitlements: %w", err)
		}
	}
	notef(r, "codex-cli: resolving local signing identity "+strconv.Quote(paths.SignIdentity()))
	id, err := signing.ResolveIdentity(ctx, r.DryRun)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.sign_binary.resolve_identity_failed", "err", err)
		return fmt.Errorf("resolve local signing identity: %w", err)
	}
	notef(r, "codex-cli: using upstream entitlements "+entitlementsPath)
	notef(r, "codex-cli: sign "+binaryPath+" with "+strconv.Quote(paths.SignIdentity())+" (sha1="+id+")")
	if err := r.Run(ctx, "/usr/bin/codesign", signing.RuntimeTimestampEntitlementsArgs(id, entitlementsPath, binaryPath)...); err != nil {
		log.ErrorContext(ctx, "codexcli.sign_binary.codesign_failed", "err", err)
		return fmt.Errorf("sign Codex CLI: %w", err)
	}
	return nil
}

func ensureRustToolchain(
	ctx context.Context,
	r *patch.Runner,
	codexRoot string,
) error {
	log := codexcliLog.With("function", "ensureRustToolchain")
	if !r.DryRun {
		if _, err := exec.LookPath("rustup"); err != nil {
			log.ErrorContext(ctx, "codexcli.ensure_rust_toolchain.rustup_missing", "err", err)
			return fmt.Errorf("find rustup for upstream Rust toolchain: %w", err)
		}
	}
	notef(r, "codex-cli: installing or updating upstream Rust toolchain from "+filepath.Join(codexRoot, "rust-toolchain.toml"))
	if err := r.RunInDirWithHeartbeat(
		ctx,
		"codex-cli: installing upstream Rust toolchain",
		30*time.Second,
		codexRoot,
		"rustup",
		"toolchain",
		"install",
	); err != nil {
		log.ErrorContext(ctx, "codexcli.ensure_rust_toolchain.install_failed", "err", err)
		return fmt.Errorf("install upstream Rust toolchain: %w", err)
	}
	return nil
}

func cargoBuildEnv(
	ctx context.Context,
	r *patch.Runner,
	sourceDir string,
	target string,
	noSccache bool,
) (map[string]string, string, bool, error) {
	log := codexcliLog.With("function", "cargoBuildEnv")
	if r.DryRun {
		return nil, "", false, nil
	}
	scriptPath, cleanup, err := writeTempHelperScript("desktop-via-clyde-codex-v8-*.py", resolveCodexV8EnvScript)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.cargo_build_env.write_helper_failed", "err", err)
		return nil, "", false, fmt.Errorf("write Codex V8 helper script: %w", err)
	}
	defer cleanup()

	output, err := r.RunCaptureStdout(ctx, "python3", scriptPath, sourceDir, target)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.cargo_build_env.resolve_env_failed", "err", err)
		return nil, "", false, fmt.Errorf("resolve Codex V8 environment: %w", err)
	}
	env, err := parseEnvOutput(output)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.cargo_build_env.parse_env_failed", "err", err)
		return nil, "", false, fmt.Errorf("parse Codex V8 environment: %w", err)
	}
	if len(env) == 0 {
		notef(r, "codex-cli: upstream Codex did not require additional Rusty V8 env overrides")
		env = nil
	}
	for _, key := range []string{"RUSTY_V8_ARCHIVE", "RUSTY_V8_SRC_BINDING_PATH"} {
		value, ok := env[key]
		if ok {
			notef(r, "codex-cli: "+key+"="+value)
		}
	}
	existingWrapper := os.Getenv("RUSTC_WRAPPER")
	rustcWrapper, sccacheUsed := resolveRustcWrapper(existingWrapper, noSccache, exec.LookPath)
	switch {
	case existingWrapper != "":
		if sccacheUsed {
			notef(r, "codex-cli: using existing sccache wrapper "+existingWrapper)
		} else {
			notef(r, "codex-cli: respecting existing RUSTC_WRAPPER="+existingWrapper)
		}
	case noSccache:
		notef(r, "codex-cli: sccache disabled for this run")
	case rustcWrapper != "":
		notef(r, "codex-cli: using sccache wrapper "+rustcWrapper)
		if env == nil {
			env = map[string]string{}
		}
		env["RUSTC_WRAPPER"] = rustcWrapper
	default:
		notef(r, "codex-cli: sccache not found, building without compiler cache")
	}
	return env, rustcWrapper, sccacheUsed, nil
}

func writeTempHelperScript(pattern string, body string) (string, func(), error) {
	log := codexcliLog.With("function", "writeTempHelperScript")
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		log.Error("codexcli.write_temp_helper_script.create_failed", "err", err)
		return "", nil, fmt.Errorf("create temp helper script: %w", err)
	}
	if _, err := file.WriteString(body); err != nil {
		log.Error("codexcli.write_temp_helper_script.write_failed", "err", err)
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", nil, fmt.Errorf("write temp helper script body: %w", err)
	}
	if err := file.Close(); err != nil {
		log.Error("codexcli.write_temp_helper_script.close_failed", "err", err)
		_ = os.Remove(file.Name())
		return "", nil, fmt.Errorf("close temp helper script: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(file.Name())
	}
	return file.Name(), cleanup, nil
}

func parseEnvOutput(output []byte) (map[string]string, error) {
	log := codexcliLog.With("function", "parseEnvOutput")
	env := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			log.Error("codexcli.parse_env_output.invalid_line", "err", fmt.Errorf("invalid env line"), "line", line)
			return nil, fmt.Errorf("invalid env line %q", line)
		}
		env[parts[0]] = parts[1]
	}
	if err := scanner.Err(); err != nil {
		log.Error("codexcli.parse_env_output.scan_failed", "err", err)
		return nil, fmt.Errorf("scan environment output: %w", err)
	}
	return env, nil
}

func readPackageMetadata(packageDir string, packageVariant string) (packageMetadata, error) {
	log := codexcliLog.With("function", "readPackageMetadata")
	path := filepath.Join(packageDir, "codex-package.json")
	data, err := os.ReadFile(path)
	if err != nil {
		log.Error("codexcli.read_package_metadata.read_failed", "err", err)
		return packageMetadata{}, fmt.Errorf("read package metadata: %w", err)
	}
	var metadata packageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		log.Error("codexcli.read_package_metadata.parse_failed", "err", err)
		return packageMetadata{}, fmt.Errorf("parse package metadata: %w", err)
	}
	if metadata.Version == "" || metadata.Target == "" || metadata.Variant != packageVariant {
		return packageMetadata{}, fmt.Errorf("invalid package metadata in %s: %+v", path, metadata)
	}
	return metadata, nil
}

func releaseDir(packageHome string, version string, head string, target string, buildMode BuildMode) string {
	return filepath.Join(
		packageHome,
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
	ctx context.Context,
	r *patch.Runner,
	packageDir string,
	releaseDir string,
	packageHome string,
	installDir string,
	packageBinaryPath string,
	commandName string,
) error {
	log := codexcliLog.With("function", "installPackage")
	standaloneRoot := filepath.Join(packageHome, "packages", "standalone")
	currentLink := filepath.Join(standaloneRoot, "current")
	binPath := filepath.Join(installDir, commandName)
	stageDir := filepath.Join(filepath.Dir(releaseDir), ".staging."+filepath.Base(releaseDir))
	notef(r, "codex-cli: install package "+packageDir+" -> "+releaseDir)
	if r.DryRun {
		notef(r, "codex-cli: would stage package at "+stageDir)
		notef(r, "codex-cli: update "+currentLink+" -> "+releaseDir)
		notef(r, "codex-cli: update "+binPath+" -> "+filepath.Join(currentLink, filepath.FromSlash(packageBinaryPath)))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(releaseDir), 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.install_package.mkdir_release_failed", "err", err)
		return fmt.Errorf("create releases dir: %w", err)
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.install_package.mkdir_install_failed", "err", err)
		return fmt.Errorf("create install dir: %w", err)
	}
	notef(r, "codex-cli: preparing staging directory "+stageDir)
	if err := os.RemoveAll(stageDir); err != nil {
		log.ErrorContext(ctx, "codexcli.install_package.remove_stage_failed", "err", err)
		return fmt.Errorf("remove stale stage dir: %w", err)
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		log.ErrorContext(ctx, "codexcli.install_package.mkdir_stage_failed", "err", err)
		return fmt.Errorf("create stage dir: %w", err)
	}
	if err := r.Run(ctx, "/usr/bin/rsync", "-a", packageDir+"/", stageDir+"/"); err != nil {
		log.ErrorContext(ctx, "codexcli.install_package.rsync_failed", "err", err)
		return fmt.Errorf("copy package to release stage: %w", err)
	}
	notef(r, "codex-cli: creating release convenience symlink "+filepath.Join(stageDir, commandName))
	if err := os.Symlink(packageBinaryPath, filepath.Join(stageDir, commandName)); err != nil && !errors.Is(err, os.ErrExist) {
		log.ErrorContext(ctx, "codexcli.install_package.symlink_failed", "err", err)
		return fmt.Errorf("create release convenience symlink: %w", err)
	}
	notef(r, "codex-cli: promoting staged release to "+releaseDir)
	if err := os.RemoveAll(releaseDir); err != nil {
		log.ErrorContext(ctx, "codexcli.install_package.remove_release_failed", "err", err)
		return fmt.Errorf("remove old release dir: %w", err)
	}
	if err := os.Rename(stageDir, releaseDir); err != nil {
		log.ErrorContext(ctx, "codexcli.install_package.rename_release_failed", "err", err)
		return fmt.Errorf("move release into place: %w", err)
	}
	if err := replaceSymlink(currentLink, releaseDir); err != nil {
		return fmt.Errorf("update current release link: %w", err)
	}
	notef(r, "codex-cli: updating visible command symlink "+binPath)
	if err := replaceSymlink(binPath, filepath.Join(currentLink, filepath.FromSlash(packageBinaryPath))); err != nil {
		return fmt.Errorf("update visible command link: %w", err)
	}
	return nil
}

func replaceSymlink(linkPath string, target string) error {
	log := codexcliLog.With("function", "replaceSymlink")
	tmpLink := linkPath + ".tmp"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(target, tmpLink); err != nil {
		log.Error("codexcli.replace_symlink.create_failed", "err", err)
		return fmt.Errorf("create symlink %s -> %s: %w", tmpLink, target, err)
	}
	if err := os.RemoveAll(linkPath); err != nil {
		log.Error("codexcli.replace_symlink.remove_failed", "err", err)
		_ = os.Remove(tmpLink)
		return fmt.Errorf("remove existing link path %s: %w", linkPath, err)
	}
	if err := os.Rename(tmpLink, linkPath); err != nil {
		log.Error("codexcli.replace_symlink.rename_failed", "err", err)
		return fmt.Errorf("promote symlink %s -> %s: %w", tmpLink, linkPath, err)
	}
	return nil
}

func verifyInstalledCommand(ctx context.Context, r *patch.Runner, installDir string, commandName string) error {
	log := codexcliLog.With("function", "verifyInstalledCommand")
	binPath := filepath.Join(installDir, commandName)
	notef(r, "codex-cli: verifying signature for "+binPath)
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", binPath); err != nil {
		log.ErrorContext(ctx, "codexcli.verify_installed_command.codesign_failed", "err", err)
		return fmt.Errorf("verify Codex CLI signature: %w", err)
	}
	notef(r, "codex-cli: verifying executable by running "+binPath+" --version")
	if err := r.Run(ctx, binPath, "--version"); err != nil {
		log.ErrorContext(ctx, "codexcli.verify_installed_command.version_failed", "err", err)
		return fmt.Errorf("verify Codex CLI version: %w", err)
	}
	return nil
}

func printCommandValue(ctx context.Context, out io.Writer, label string, name string, args ...string) {
	cmd := exec.CommandContext(ctx, name, args...)
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
		notef(r, "codex-cli: local-fast mode overrides release settings with lto=false and codegen-units="+strconv.Itoa(localFastCodegenUnits()))
		return
	}
	notef(r, "codex-cli: release mode preserves the upstream release profile exactly")
}

func cargoBuildArgs(target string, buildMode BuildMode, commandName string) []string {
	args := []string{
		"build",
		"--target",
		target,
		"--release",
		"--timings",
		"--bin",
		commandName,
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
	ctx context.Context,
	r *patch.Runner,
	packageHome string,
	installDir string,
	packageBinaryPath string,
	packageVariant string,
	commandName string,
	head string,
	target string,
	buildMode BuildMode,
	forceRebuild bool,
) (string, bool, error) {
	if forceRebuild {
		notef(r, "codex-cli: force rebuild requested, skipping installed release reuse")
		return "", false, nil
	}
	releasesRoot := filepath.Join(packageHome, "packages", "standalone", "releases")
	matches, err := findMatchingReleaseDirs(releasesRoot, releaseNameSuffix(head, target, buildMode))
	if err != nil {
		return "", false, err
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	if len(matches) > 1 {
		notef(r, "codex-cli: found multiple matching installed releases, rebuilding instead")
		return "", false, nil
	}
	releasePath := matches[0]
	notef(r, "codex-cli: found matching installed release "+releasePath)
	reuseRejected, reuseReason := releaseReuseRejected(ctx, releasePath, target, packageBinaryPath, packageVariant)
	if reuseRejected {
		notef(r, "codex-cli: installed release reuse rejected: "+reuseReason)
		return "", false, nil
	}
	notef(r, "codex-cli: reusing verified installed release "+releasePath)
	if err := relinkInstalledRelease(ctx, r, packageHome, installDir, releasePath, packageBinaryPath, commandName); err != nil {
		return "", false, err
	}
	if err := verifyInstalledCommand(ctx, r, installDir, commandName); err != nil {
		return "", false, err
	}
	return releasePath, true, nil
}

func releaseReuseRejected(ctx context.Context, releasePath string, target string, packageBinaryPath string, packageVariant string) (bool, string) {
	verifyErr := verifyReleaseCandidate(ctx, releasePath, target, packageBinaryPath, packageVariant)
	if verifyErr == nil {
		return false, ""
	}
	return true, verifyErr.Error()
}

func findMatchingReleaseDirs(releasesRoot string, suffix string) ([]string, error) {
	log := codexcliLog.With("function", "findMatchingReleaseDirs")
	entries, err := os.ReadDir(releasesRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		log.Error("codexcli.find_matching_release_dirs.read_failed", "err", err)
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

func verifyReleaseCandidate(ctx context.Context, releasePath string, target string, packageBinaryPath string, packageVariant string) error {
	log := codexcliLog.With("function", "verifyReleaseCandidate")
	metadata, err := readPackageMetadata(releasePath, packageVariant)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.verify_release_candidate.metadata_failed", "err", err)
		return err
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
	_ = ctx
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
	notef(r, "codex-cli: update "+binPath+" -> "+filepath.Join(currentLink, filepath.FromSlash(packageBinaryPath)))
	if err := replaceSymlink(binPath, filepath.Join(currentLink, filepath.FromSlash(packageBinaryPath))); err != nil {
		log.ErrorContext(ctx, "codexcli.relink_installed_release.visible_link_failed", "err", err)
		return fmt.Errorf("update visible command link: %w", err)
	}
	return nil
}
