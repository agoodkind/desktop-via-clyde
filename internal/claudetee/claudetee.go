// Package claudetee installs and removes a stdio-tee shim against the claude
// CLI binary that Claude Desktop spawns for tasks such as the /context slash
// command. Wrapping that binary lets the operator capture the exact SDK
// control protocol bytes that Desktop and the bundled CLI exchange.
//
// Install moves the bundled CLI to claude.real and writes the universal
// Mach-O tee shim embedded in shimembed at the original path. Uninstall
// restores the .real binary in place. Status reports which version is
// installed, whether the shim is in place, and where the tee logs land.
//
// The bundled CLI normally lives at
//
//	$HOME/Library/Application Support/Claude/claude-code/<version>/claude.app/Contents/MacOS/claude
//
// Version directories are version-sorted and the greatest is chosen unless an
// explicit version path is passed via Options.VersionDir.
package claudetee

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	shimembed "goodkind.io/desktop-via-clyde/internal/embed"
	"goodkind.io/desktop-via-clyde/internal/signing"
)

var claudeteeLog = slog.With("component", "desktop-via-clyde", "subcomponent", "claude-bundled-cli-tee")

// AppSupportRel is the path under $HOME where Claude Desktop installs its
// bundled claude-code CLI versions. Each version is a sibling directory under
// this root.
const AppSupportRel = "Library/Application Support/Claude/claude-code"

// BundledCLIRel is the path under a version directory to the actual claude
// executable. The "claude.app" wrapper is a tiny app bundle that holds the
// binary so Launch Services can spawn it cleanly.
const BundledCLIRel = "claude.app/Contents/MacOS/claude"

// DefaultLogDirRel is the default log directory under $HOME. The Go shim
// reads DVC_STDIO_TEE_DIR first; this is the fallback when it is unset.
const DefaultLogDirRel = ".local/state/desktop-via-clyde/stdio-tee"

// Options shapes a single tee install, uninstall, or status call.
type Options struct {
	// DryRun prints every step without modifying the filesystem.
	DryRun bool
	// VersionDir overrides the auto-detected version directory under
	// AppSupportRel. Useful when more than one version is installed and the
	// operator wants to wrap a specific one.
	VersionDir string
	// BundledCLIPath overrides the entire bundled CLI path. Highest priority;
	// when set VersionDir is ignored.
	BundledCLIPath string
	// LogDir overrides the default log directory shown in status output.
	LogDir string
	// HomeDir overrides $HOME for tests. Empty means use the real home dir.
	HomeDir string
	// Out receives human-readable progress. Defaults to os.Stdout.
	Out io.Writer
}

type versionPart struct {
	number   int
	text     string
	isNumber bool
}

func (o Options) writer() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return os.Stdout
}

func (o Options) home() (string, error) {
	if o.HomeDir != "" {
		return o.HomeDir, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		claudeteeLog.Error("claudetee.home.resolve_failed", "err", err)
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return homeDir, nil
}

func (o Options) logDir() (string, error) {
	if o.LogDir != "" {
		return o.LogDir, nil
	}
	home, err := o.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DefaultLogDirRel), nil
}

// ResolveBundledCLIPath returns the absolute path to the claude CLI inside
// Claude Desktop's bundled claude-code tree. When BundledCLIPath is set on
// opts it is returned as-is; otherwise the App Support tree is scanned for
// version directories and the version-sorted greatest is used unless
// VersionDir narrows the choice.
func ResolveBundledCLIPath(opts Options) (string, error) {
	claudeteeLog.Debug("claudetee.resolve_bundled_cli_path")
	if opts.BundledCLIPath != "" {
		return opts.BundledCLIPath, nil
	}
	home, err := opts.home()
	if err != nil {
		claudeteeLog.Error("claudetee.resolve_bundled_cli_path.home_failed", "err", err)
		return "", fmt.Errorf("resolve home: %w", err)
	}
	appSupport := filepath.Join(home, AppSupportRel)
	if opts.VersionDir != "" {
		return filepath.Join(appSupport, opts.VersionDir, BundledCLIRel), nil
	}
	versions, err := listVersionDirs(appSupport)
	if err != nil {
		claudeteeLog.Error("claudetee.resolve_bundled_cli_path.list_versions_failed", "path", appSupport, "err", err)
		return "", err
	}
	if len(versions) == 0 {
		claudeteeLog.Error("claudetee.resolve_bundled_cli_path.no_versions", "path", appSupport, "err", errors.New("no claude-code versions found"))
		return "", fmt.Errorf("no claude-code versions under %s; is Claude Desktop installed?", appSupport)
	}
	return filepath.Join(appSupport, versions[len(versions)-1], BundledCLIRel), nil
}

// listVersionDirs returns the version directories under appSupport sorted by
// natural version order (greatest last). Entries that look like dotfiles or
// non-directories are skipped. The sort uses semver-ish ordering by reading
// dot-separated numeric components when present and falling back to lex order
// for non-numeric segments.
func listVersionDirs(appSupport string) ([]string, error) {
	claudeteeLog.Debug("claudetee.list_version_dirs", "path", appSupport)
	entries, err := os.ReadDir(appSupport)
	if err != nil {
		claudeteeLog.Error("claudetee.list_version_dirs.read_failed", "path", appSupport, "err", err)
		return nil, fmt.Errorf("read %s: %w", appSupport, err)
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "" || name[0] == '.' {
			continue
		}
		dirs = append(dirs, name)
	}
	sort.Slice(dirs, func(i, j int) bool {
		return compareVersions(dirs[i], dirs[j]) < 0
	})
	return dirs, nil
}

// compareVersions returns -1, 0, or +1 comparing a and b as dot-separated
// version strings. Numeric components compare numerically; non-numeric
// components compare lexicographically; a shorter prefix-match version sorts
// before a longer one.
func compareVersions(a, b string) int {
	as := splitVersion(a)
	bs := splitVersion(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		switch {
		case as[i].isNumber && bs[i].isNumber:
			if as[i].number != bs[i].number {
				if as[i].number < bs[i].number {
					return -1
				}
				return 1
			}
		case as[i].isNumber && !bs[i].isNumber:
			return -1
		case !as[i].isNumber && bs[i].isNumber:
			return 1
		default:
			if as[i].text != bs[i].text {
				if as[i].text < bs[i].text {
					return -1
				}
				return 1
			}
		}
	}
	if len(as) != len(bs) {
		if len(as) < len(bs) {
			return -1
		}
		return 1
	}
	return 0
}

func splitVersion(s string) []versionPart {
	var parts []versionPart
	var currentPart strings.Builder
	flush := func() {
		value := currentPart.String()
		if value == "" {
			return
		}
		if n, err := parseUint(value); err == nil {
			parts = append(parts, versionPart{number: n, text: "", isNumber: true})
		} else {
			parts = append(parts, versionPart{number: 0, text: value, isNumber: false})
		}
		currentPart.Reset()
	}
	for _, ch := range s {
		if ch == '.' || ch == '-' || ch == '+' {
			flush()
			continue
		}
		currentPart.WriteRune(ch)
	}
	flush()
	return parts
}

func parseUint(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, errors.New("non-numeric")
		}
		n = n*10 + int(ch-'0')
	}
	return n, nil
}

func printInstallHeader(out io.Writer, bundled, realPath string, shimSize int, logDir string) {
	fmt.Fprintf(out, "target:       %s\n", bundled)
	fmt.Fprintf(out, "real sibling: %s\n", realPath)
	fmt.Fprintf(out, "shim size:    %d bytes\n", shimSize)
	fmt.Fprintf(out, "log dir:      %s\n", logDir)
}

// realSiblingPath returns the path to the renamed original binary that the
// shim execs. The shim itself resolves this name with realpath on its own
// executable path so the convention is fixed.
func realSiblingPath(bundled string) string {
	return bundled + ".real"
}

func resolveInstallTarget(ctx context.Context, opts Options, log *slog.Logger) (string, string, string, error) {
	bundled, err := ResolveBundledCLIPath(opts)
	if err != nil {
		log.ErrorContext(ctx, "claudetee.install.resolve_target_failed", "err", err)
		return "", "", "", err
	}
	realPath := realSiblingPath(bundled)

	if _, err := os.Stat(bundled); err != nil {
		log.ErrorContext(ctx, "claudetee.install.bundled_missing", "path", bundled, "err", err)
		return "", "", "", fmt.Errorf("bundled CLI missing at %s: %w", bundled, err)
	}
	if _, err := os.Stat(realPath); err == nil {
		err := fmt.Errorf("%s already exists; run uninstall first or remove it manually", realPath)
		log.ErrorContext(ctx, "claudetee.install.real_exists", "path", realPath, "err", err)
		return "", "", "", err
	} else if !errors.Is(err, fs.ErrNotExist) {
		log.ErrorContext(ctx, "claudetee.install.real_stat_failed", "path", realPath, "err", err)
		return "", "", "", fmt.Errorf("stat %s: %w", realPath, err)
	}

	logDirForDisplay, err := opts.logDir()
	if err != nil {
		log.ErrorContext(ctx, "claudetee.install.log_dir_failed", "err", err)
		return "", "", "", fmt.Errorf("resolve log dir: %w", err)
	}
	return bundled, realPath, logDirForDisplay, nil
}

func rollbackInstall(ctx context.Context, log *slog.Logger, bundled, realPath string) {
	log.WarnContext(ctx, "claudetee.install.rollback", "shim", bundled, "real", realPath)
	if err := os.Remove(bundled); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.WarnContext(ctx, "claudetee.install.rollback_remove_failed", "path", bundled, "err", err)
	}
	if err := os.Rename(realPath, bundled); err != nil {
		log.WarnContext(ctx, "claudetee.install.rollback_restore_failed", "from", realPath, "to", bundled, "err", err)
	}
}

func performInstall(ctx context.Context, out io.Writer, bundled, realPath string, log *slog.Logger) error {
	if err := stopDesktopAndBundledCLI(ctx, out); err != nil {
		log.ErrorContext(ctx, "claudetee.install.stop_failed", "err", err)
		return err
	}

	fmt.Fprintf(out, "renaming %s -> %s\n", bundled, realPath)
	if err := os.Rename(bundled, realPath); err != nil {
		log.ErrorContext(ctx, "claudetee.install.rename_failed", "from", bundled, "to", realPath, "err", err)
		return fmt.Errorf("rename original to .real: %w", err)
	}

	fmt.Fprintf(out, "writing shim to %s\n", bundled)
	if err := os.WriteFile(bundled, shimembed.StdioTeeShim, 0o600); err != nil {
		rollbackInstall(ctx, log, bundled, realPath)
		log.ErrorContext(ctx, "claudetee.install.write_shim_failed", "path", bundled, "err", err)
		return fmt.Errorf("write shim: %w", err)
	}
	if err := os.Chmod(bundled, 0o755); err != nil {
		rollbackInstall(ctx, log, bundled, realPath)
		log.ErrorContext(ctx, "claudetee.install.chmod_shim_failed", "path", bundled, "err", err)
		return fmt.Errorf("chmod shim: %w", err)
	}

	identity, err := signing.ResolveIdentity(ctx, false)
	if err != nil {
		rollbackInstall(ctx, log, bundled, realPath)
		log.ErrorContext(ctx, "claudetee.install.resolve_identity_failed", "err", err)
		return fmt.Errorf("resolve signing identity: %w", err)
	}

	fmt.Fprintf(out, "re-signing %s with %s (preserve entitlements)\n", realPath, identity)
	if err := codesignPreserveEntitlements(ctx, realPath, identity); err != nil {
		rollbackInstall(ctx, log, bundled, realPath)
		log.ErrorContext(ctx, "claudetee.install.codesign_real_failed", "path", realPath, "err", err)
		return fmt.Errorf("codesign .real: %w", err)
	}

	fmt.Fprintf(out, "signing shim %s with %s\n", bundled, identity)
	if err := codesignWithIdentity(ctx, bundled, identity); err != nil {
		rollbackInstall(ctx, log, bundled, realPath)
		log.ErrorContext(ctx, "claudetee.install.codesign_shim_failed", "path", bundled, "err", err)
		return fmt.Errorf("codesign shim: %w", err)
	}

	bundleDir := filepath.Dir(filepath.Dir(filepath.Dir(bundled)))
	fmt.Fprintf(out, "sealing bundle %s with %s\n", bundleDir, identity)
	if err := codesignWithIdentity(ctx, bundleDir, identity); err != nil {
		rollbackInstall(ctx, log, bundled, realPath)
		log.ErrorContext(ctx, "claudetee.install.codesign_bundle_failed", "path", bundleDir, "err", err)
		return fmt.Errorf("codesign bundle: %w", err)
	}
	return nil
}

func printInstallDone(out io.Writer, logDirForDisplay string) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "done. next steps:")
	fmt.Fprintln(out, "  1. start Claude Desktop")
	fmt.Fprintln(out, "  2. open the session you want to inspect")
	fmt.Fprintln(out, "  3. type /context and wait for the result")
	fmt.Fprintf(out, "  4. read the latest stdio log under %s/\n", logDirForDisplay)
	fmt.Fprintln(out, "  5. desktop-via-clyde claude bundled-cli-tee uninstall when done")
}

// Install wraps the bundled CLI with the tee shim. It is idempotent only
// when the sibling .real binary is already absent; if the .real already
// exists the install refuses to overwrite, on the principle that a stale
// .real likely means a previous install was not undone and rerunning would
// lose the real binary.
func Install(ctx context.Context, opts Options) error {
	log := claudeteeLog.With("operation", "install")
	log.InfoContext(ctx, "claudetee.install.start")
	out := opts.writer()
	bundled, realPath, logDirForDisplay, err := resolveInstallTarget(ctx, opts, log)
	if err != nil {
		return err
	}

	printInstallHeader(out, bundled, realPath, len(shimembed.StdioTeeShim), logDirForDisplay)
	if opts.DryRun {
		fmt.Fprintln(out, "dry-run: stopping Desktop processes would happen here")
		fmt.Fprintf(out, "dry-run: %s would be renamed to %s\n", bundled, realPath)
		fmt.Fprintf(out, "dry-run: shim would be written to %s and ad-hoc signed\n", bundled)
		return nil
	}

	if err := performInstall(ctx, out, bundled, realPath, log); err != nil {
		return err
	}

	printInstallDone(out, logDirForDisplay)
	return nil
}

// Uninstall restores the renamed original binary. It refuses to act when
// there is no .real sibling, since that means there is nothing to restore.
func Uninstall(ctx context.Context, opts Options) error {
	log := claudeteeLog.With("operation", "uninstall")
	log.InfoContext(ctx, "claudetee.uninstall.start")
	out := opts.writer()
	bundled, err := ResolveBundledCLIPath(opts)
	if err != nil {
		log.ErrorContext(ctx, "claudetee.uninstall.resolve_target_failed", "err", err)
		return err
	}
	realPath := realSiblingPath(bundled)
	if _, err := os.Stat(realPath); err != nil {
		log.ErrorContext(ctx, "claudetee.uninstall.real_missing", "path", realPath, "err", err)
		return fmt.Errorf("no .real sibling at %s; nothing to restore", realPath)
	}
	fmt.Fprintf(out, "target:       %s\n", bundled)
	fmt.Fprintf(out, "real sibling: %s\n", realPath)
	if opts.DryRun {
		fmt.Fprintln(out, "dry-run: stopping Desktop processes would happen here")
		fmt.Fprintf(out, "dry-run: %s would be moved back to %s\n", realPath, bundled)
		return nil
	}

	if err := stopDesktopAndBundledCLI(ctx, out); err != nil {
		log.ErrorContext(ctx, "claudetee.uninstall.stop_failed", "err", err)
		return err
	}

	fmt.Fprintf(out, "restoring %s -> %s\n", realPath, bundled)
	if err := os.Rename(realPath, bundled); err != nil {
		log.ErrorContext(ctx, "claudetee.uninstall.restore_failed", "from", realPath, "to", bundled, "err", err)
		return fmt.Errorf("restore original: %w", err)
	}
	fmt.Fprintln(out, "done.")
	return nil
}

// Status prints which bundled CLI path is targeted, whether the shim is
// installed, the embedded shim size, and the log directory the shim writes
// to. It is read-only and safe to run while Desktop is open.
func Status(ctx context.Context, opts Options) error {
	_ = ctx
	out := opts.writer()
	bundled, err := ResolveBundledCLIPath(opts)
	if err != nil {
		return err
	}
	realPath := realSiblingPath(bundled)
	logDir, _ := opts.logDir()

	state := "not wrapped"
	if _, err := os.Stat(realPath); err == nil {
		state = "wrapped (sibling .real present)"
	}

	fmt.Fprintf(out, "bundled cli: %s\n", bundled)
	fmt.Fprintf(out, "real path:   %s\n", realPath)
	fmt.Fprintf(out, "state:       %s\n", state)
	fmt.Fprintf(out, "shim size:   %d bytes (embedded)\n", len(shimembed.StdioTeeShim))
	fmt.Fprintf(out, "log dir:     %s\n", logDir)
	fmt.Fprintln(out, "log files per invocation: <stamp>-<pid>.{stdin.jsonl,stdout.jsonl,stderr.log,meta.log}")
	return nil
}

// stopDesktopAndBundledCLI sends best-effort SIGTERM to the Claude Desktop
// app process and any active bundled CLI processes so they release file
// locks on the binary we are about to rename. Failures are non-fatal because
// the targets may not be running; rename will fail loudly if a lock remains.
func stopDesktopAndBundledCLI(ctx context.Context, out io.Writer) error {
	claudeteeLog.DebugContext(ctx, "claudetee.stop_desktop_and_bundled_cli")
	fmt.Fprintln(out, "stopping Claude Desktop processes that hold the bundled binary open")
	_ = exec.CommandContext(ctx, "/usr/bin/pkill", "-x", "Claude").Run()
	_ = exec.CommandContext(ctx, "/usr/bin/pkill", "-f", "claude-code/.*/claude.app/Contents/MacOS/claude").Run()
	// pkill is best-effort and returns nonzero when no processes matched,
	// which is fine here.
	return nil
}

// codesignWithIdentity runs codesign with the local Developer ID identity
// resolved from the keychain. Used for the shim and for the parent bundle
// seal. The hardened runtime option is required so the resulting signature
// matches what amfid expects for the bundled claude-code app on macOS.
func codesignWithIdentity(ctx context.Context, path, identity string) error {
	claudeteeLog.DebugContext(ctx, "claudetee.codesign_with_identity", "path", path)
	args := signing.RuntimeArgs(identity, path)
	cmd := exec.CommandContext(ctx, "/usr/bin/codesign", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		claudeteeLog.ErrorContext(ctx, "claudetee.codesign_with_identity.failed", "path", path, "err", err)
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

// codesignPreserveEntitlements re-signs an existing binary under the local
// Developer ID while preserving its embedded entitlements. The bundled
// claude.real binary carries entitlements like
// com.apple.security.cs.disable-library-validation and
// com.apple.security.cs.allow-jit; the runtime needs those to stay set, so
// the re-sign uses --preserve-metadata=entitlements,requirements.
func codesignPreserveEntitlements(ctx context.Context, path, identity string) error {
	claudeteeLog.DebugContext(ctx, "claudetee.codesign_preserve_entitlements", "path", path)
	args := []string{
		"--force",
		"--sign", identity,
		"--options", "runtime",
		"--preserve-metadata=entitlements,requirements",
		path,
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/codesign", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		claudeteeLog.ErrorContext(ctx, "claudetee.codesign_preserve_entitlements.failed", "path", path, "err", err)
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}
