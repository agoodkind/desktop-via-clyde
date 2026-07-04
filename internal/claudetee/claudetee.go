// Package claudetee installs and removes a stdio-tee shim against a configured
// bundled CLI binary. Wrapping that binary lets the operator capture the exact
// stdio protocol bytes that the parent app and bundled CLI exchange.
//
// Install moves the bundled CLI to a .real sibling and writes the universal
// desktop-via-clyde monolith at the original path. Uninstall
// restores the .real binary in place. Status reports which version is
// installed, whether the shim is in place, and where the tee logs land.
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

	"goodkind.io/desktop-via-clyde/internal/monolith"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/signing"
)

var claudeteeLog = slog.With("component", "desktop-via-clyde", "subcomponent", "claude-bundled-cli-tee")

// Options shapes a single tee install, uninstall, or status call.
type Options struct {
	// DryRun prints every step without modifying the filesystem.
	DryRun bool
	// AppSupportDir is the absolute directory that contains bundled CLI version directories.
	AppSupportDir string
	// VersionDir overrides the auto-detected version directory under
	// AppSupportDir.
	VersionDir string
	// BundledCLIRel is the CLI executable path under a selected version directory.
	BundledCLIRel string
	// BundledCLIPath overrides the entire bundled CLI path. Highest priority;
	// when set VersionDir is ignored.
	BundledCLIPath string
	// TerminateProcessNames lists executable names to stop before mutation.
	TerminateProcessNames []string
	// TerminateProcessPatterns lists full-command patterns to stop before mutation.
	TerminateProcessPatterns []string
	// AllowTerminate permits pkill when matching processes are live.
	AllowTerminate bool
	// CompletionSteps lists operator follow-up lines to print after install.
	CompletionSteps []string
	// LogDir overrides the default log directory shown in status output.
	LogDir string
	// Out receives human-readable progress. Defaults to os.Stdout.
	Out io.Writer
	// Trace receives structured workflow events for tests.
	Trace *Trace
}

// Action names one bundled CLI tee workflow action in the structured trace.
type Action string

const (
	actionResolveInstallTarget Action = "resolve_install_target"
	actionStopProcesses        Action = "stop_processes"
	actionStopProcessName      Action = "stop_process_name"
	actionStopProcessPattern   Action = "stop_process_pattern"
	actionRenameBundledCLI     Action = "rename_bundled_cli"
	actionWriteShim            Action = "write_shim"
)

// Trace records structured bundled CLI tee workflow events for tests.
type Trace struct {
	Events []TraceEvent
}

// TraceEvent records one structured bundled CLI tee workflow event.
type TraceEvent struct {
	Action  Action
	Path    string
	From    string
	To      string
	LogDir  string
	Size    int
	Name    string
	Pattern string
}

type versionPart struct {
	number   int
	text     string
	isNumber bool
}

// ErrProcessesRunning reports that installation was deferred because matching
// processes are still live and termination is disallowed.
var ErrProcessesRunning = errors.New("configured processes are running; deferring tee install")

var (
	processNameMatches    = defaultProcessNameMatches
	processPatternMatches = defaultProcessPatternMatches
	killProcessName       = defaultKillProcessName
	killProcessPattern    = defaultKillProcessPattern
)

func (o Options) writer() io.Writer {
	if o.Out != nil {
		return o.Out
	}
	return os.Stdout
}

func (o Options) logDir() string {
	if o.LogDir != "" {
		return o.LogDir
	}
	return paths.StdioTeeLogDir()
}

// ResolveBundledCLIPath returns the absolute path to the configured bundled CLI.
func ResolveBundledCLIPath(opts Options) (string, error) {
	claudeteeLog.Debug("claudetee.resolve_bundled_cli_path")
	if opts.BundledCLIPath != "" {
		return opts.BundledCLIPath, nil
	}
	if opts.AppSupportDir == "" {
		return "", fmt.Errorf("app support directory is required")
	}
	if opts.BundledCLIRel == "" {
		return "", fmt.Errorf("bundled CLI relative path is required")
	}
	if opts.VersionDir != "" {
		return filepath.Join(opts.AppSupportDir, opts.VersionDir, opts.BundledCLIRel), nil
	}
	versions, err := listVersionDirs(opts.AppSupportDir)
	if err != nil {
		claudeteeLog.Error("claudetee.resolve_bundled_cli_path.list_versions_failed", "path", opts.AppSupportDir, "err", err)
		return "", err
	}
	if len(versions) == 0 {
		claudeteeLog.Error("claudetee.resolve_bundled_cli_path.no_versions", "path", opts.AppSupportDir, "err", errors.New("no bundled CLI versions found"))
		return "", fmt.Errorf("no bundled CLI versions under %s", opts.AppSupportDir)
	}
	return filepath.Join(opts.AppSupportDir, versions[len(versions)-1], opts.BundledCLIRel), nil
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

	logDirForDisplay := opts.logDir()
	event := newTraceEvent(actionResolveInstallTarget)
	event.Path = bundled
	event.LogDir = logDirForDisplay
	event.Size = int(monolith.Size())
	traceEvent(opts, event)
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

func performInstall(ctx context.Context, opts Options, out io.Writer, bundled, realPath string, log *slog.Logger) error {
	if err := stopConfiguredProcesses(ctx, opts, out); err != nil {
		if errors.Is(err, ErrProcessesRunning) {
			log.InfoContext(ctx, "claudetee.install.deferred_processes_running", "bundled", bundled)
			return err
		}
		log.ErrorContext(ctx, "claudetee.install.stop_failed", "err", err)
		return err
	}

	fmt.Fprintf(out, "renaming %s -> %s\n", bundled, realPath)
	if err := os.Rename(bundled, realPath); err != nil {
		log.ErrorContext(ctx, "claudetee.install.rename_failed", "from", bundled, "to", realPath, "err", err)
		return fmt.Errorf("rename original to .real: %w", err)
	}

	fmt.Fprintf(out, "writing shim to %s\n", bundled)
	if err := monolith.CopyTo(bundled); err != nil {
		rollbackInstall(ctx, log, bundled, realPath)
		log.ErrorContext(ctx, "claudetee.install.write_shim_failed", "path", bundled, "err", err)
		return fmt.Errorf("write shim: %w", err)
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

func printInstallDone(out io.Writer, opts Options, logDirForDisplay string) {
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "done.")
	if len(opts.CompletionSteps) == 0 {
		return
	}
	fmt.Fprintln(out, "next steps:")
	for index, step := range opts.CompletionSteps {
		fmt.Fprintf(out, "  %d. %s\n", index+1, renderCompletionStep(step, logDirForDisplay))
	}
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

	printInstallHeader(out, bundled, realPath, int(monolith.Size()), logDirForDisplay)
	if opts.DryRun {
		traceConfiguredProcessStops(opts)
		renameEvent := newTraceEvent(actionRenameBundledCLI)
		renameEvent.From = bundled
		renameEvent.To = realPath
		traceEvent(opts, renameEvent)
		writeEvent := newTraceEvent(actionWriteShim)
		writeEvent.Path = bundled
		writeEvent.Size = int(monolith.Size())
		traceEvent(opts, writeEvent)
		fmt.Fprintln(out, "dry-run: configured process stops would happen here")
		fmt.Fprintf(out, "dry-run: %s would be renamed to %s\n", bundled, realPath)
		fmt.Fprintf(out, "dry-run: shim would be written to %s and ad-hoc signed\n", bundled)
		return nil
	}

	if err := performInstall(ctx, opts, out, bundled, realPath, log); err != nil {
		return err
	}

	printInstallDone(out, opts, logDirForDisplay)
	return nil
}

func newTraceEvent(action Action) TraceEvent {
	return TraceEvent{
		Action:  action,
		Path:    "",
		From:    "",
		To:      "",
		LogDir:  "",
		Size:    0,
		Name:    "",
		Pattern: "",
	}
}

func traceEvent(opts Options, event TraceEvent) {
	if opts.Trace == nil {
		return
	}
	opts.Trace.Events = append(opts.Trace.Events, event)
}

// stopConfiguredProcesses records configured process-stop intent and, when
// termination is allowed, sends best-effort SIGTERM to processes so they
// release file locks on the binary we are about to rename.
func stopConfiguredProcesses(ctx context.Context, opts Options, out io.Writer) error {
	claudeteeLog.DebugContext(ctx, "claudetee.stop_desktop_and_bundled_cli")
	traceConfiguredProcessStops(opts)
	if len(opts.TerminateProcessNames) == 0 && len(opts.TerminateProcessPatterns) == 0 {
		fmt.Fprintln(out, "no configured processes to stop")
		return nil
	}
	if !opts.AllowTerminate {
		running, err := configuredProcessesRunning(ctx, opts)
		if err != nil {
			return err
		}
		if running {
			return ErrProcessesRunning
		}
		return nil
	}
	fmt.Fprintln(out, "stopping configured processes that hold the bundled binary open")
	for _, name := range opts.TerminateProcessNames {
		if err := killProcessName(ctx, name); err != nil {
			return err
		}
	}
	for _, pattern := range opts.TerminateProcessPatterns {
		if err := killProcessPattern(ctx, pattern); err != nil {
			return err
		}
	}
	return nil
}

func configuredProcessesRunning(ctx context.Context, opts Options) (bool, error) {
	for _, name := range opts.TerminateProcessNames {
		matched, err := processNameMatches(ctx, name)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	for _, pattern := range opts.TerminateProcessPatterns {
		matched, err := processPatternMatches(ctx, pattern)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func defaultProcessNameMatches(ctx context.Context, name string) (bool, error) {
	return processMatches(ctx, "/usr/bin/pgrep", "-x", name)
}

func defaultProcessPatternMatches(ctx context.Context, pattern string) (bool, error) {
	return processMatches(ctx, "/usr/bin/pgrep", "-f", pattern)
}

func processMatches(ctx context.Context, command string, args ...string) (bool, error) {
	claudeteeLog.DebugContext(ctx, "claudetee.process_matches.boundary", "command", command, "args", args)
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	wrappedErr := fmt.Errorf("run %s %s: %w (output: %s)", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	claudeteeLog.WarnContext(ctx, "claudetee.process_matches.failed", "command", command, "args", args, "err", wrappedErr)
	return false, wrappedErr
}

func defaultKillProcessName(ctx context.Context, name string) error {
	return killConfiguredProcess(ctx, "/usr/bin/pkill", "-x", name)
}

func defaultKillProcessPattern(ctx context.Context, pattern string) error {
	return killConfiguredProcess(ctx, "/usr/bin/pkill", "-f", pattern)
}

func killConfiguredProcess(ctx context.Context, command string, args ...string) error {
	claudeteeLog.DebugContext(ctx, "claudetee.kill_configured_process.boundary", "command", command, "args", args)
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil
	}
	wrappedErr := fmt.Errorf("run %s %s: %w (output: %s)", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	claudeteeLog.WarnContext(ctx, "claudetee.kill_configured_process.failed", "command", command, "args", args, "err", wrappedErr)
	return wrappedErr
}

func traceConfiguredProcessStops(opts Options) {
	traceEvent(opts, newTraceEvent(actionStopProcesses))
	for _, name := range opts.TerminateProcessNames {
		event := newTraceEvent(actionStopProcessName)
		event.Name = name
		traceEvent(opts, event)
	}
	for _, pattern := range opts.TerminateProcessPatterns {
		event := newTraceEvent(actionStopProcessPattern)
		event.Pattern = pattern
		traceEvent(opts, event)
	}
}

func renderCompletionStep(step string, logDir string) string {
	return strings.ReplaceAll(step, "{log_dir}", logDir)
}

// codesignWithIdentity runs codesign with the local Developer ID identity
// resolved from the keychain. Used for the shim and for the parent bundle
// seal. The hardened runtime option is required so the resulting signature
// matches what amfid expects for the configured bundled app on macOS.
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
// original binary can carry entitlements like
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
