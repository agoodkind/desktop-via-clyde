package daemon

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/paths"
)

//go:embed templates/updater.plist.tmpl
var updaterPlistTemplate string

// launchdLabel is the per-user LaunchAgent label for the updater daemon.
const launchdLabel = "io.goodkind.desktop-via-clyde.updater"

func launchAgentsDir() string {
	return filepath.Join(paths.Home(), "Library", "LaunchAgents")
}

func plistPath() string {
	return filepath.Join(launchAgentsDir(), launchdLabel+".plist")
}

func updaterLogPath() string {
	return filepath.Join(paths.Home(), "Library", "Logs", "desktop-via-clyde-updater.log")
}

func launchdDomain() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func launchdTarget() string {
	return launchdDomain() + "/" + launchdLabel
}

// Install renders the LaunchAgent plist for the running binary, writes it, and
// loads it through launchctl so launchd owns the daemon's lifecycle. It is
// idempotent: an existing agent is booted out before the fresh bootstrap.
func Install(ctx context.Context, out io.Writer) error {
	executablePath, err := os.Executable()
	if err != nil {
		daemonLog.ErrorContext(ctx, "daemon.install.executable_failed", "err", err)
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if err := os.MkdirAll(launchAgentsDir(), 0o755); err != nil {
		daemonLog.ErrorContext(ctx, "daemon.install.mkdir_agents_failed", "err", err, "path", launchAgentsDir())
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(updaterLogPath()), 0o755); err != nil {
		daemonLog.ErrorContext(ctx, "daemon.install.mkdir_log_failed", "err", err, "path", filepath.Dir(updaterLogPath()))
		return fmt.Errorf("create log dir: %w", err)
	}
	if err := os.WriteFile(plistPath(), []byte(renderPlist(executablePath)), 0o600); err != nil {
		daemonLog.ErrorContext(ctx, "daemon.install.write_plist_failed", "err", err, "path", plistPath())
		return fmt.Errorf("write launch agent %s: %w", plistPath(), err)
	}
	_, _ = fmt.Fprintf(out, "wrote launch agent %s\n", plistPath())

	// Boot out any prior instance best-effort, then bootstrap the fresh plist.
	_, _ = runLaunchctl(ctx, "bootout", launchdDomain(), plistPath())
	if output, err := runLaunchctl(ctx, "bootstrap", launchdDomain(), plistPath()); err != nil {
		daemonLog.ErrorContext(ctx, "daemon.install.bootstrap_failed", "err", err, "output", output)
		return fmt.Errorf("launchctl bootstrap %s: %w (%s)", launchdTarget(), err, output)
	}
	_, _ = fmt.Fprintf(out, "loaded updater daemon as %s\n", launchdTarget())
	return nil
}

// Status reports whether the LaunchAgent is loaded and whether the daemon's RPC
// socket is responding.
func Status(ctx context.Context, out io.Writer) error {
	if _, err := runLaunchctl(ctx, "print", launchdTarget()); err != nil {
		_, _ = fmt.Fprintf(out, "launch agent: not loaded target=%s\n", launchdTarget())
	} else {
		_, _ = fmt.Fprintf(out, "launch agent: loaded target=%s\n", launchdTarget())
	}
	if daemonReachable(ctx) {
		_, _ = fmt.Fprintf(out, "daemon rpc: responding socket=%s\n", paths.DaemonSocketPath())
	} else {
		_, _ = fmt.Fprintf(out, "daemon rpc: unavailable socket=%s\n", paths.DaemonSocketPath())
	}
	return nil
}

// Uninstall boots out the LaunchAgent and removes its plist.
func Uninstall(ctx context.Context, out io.Writer) error {
	_, _ = runLaunchctl(ctx, "bootout", launchdDomain(), plistPath())
	if err := os.Remove(plistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		daemonLog.ErrorContext(ctx, "daemon.uninstall.remove_plist_failed", "err", err, "path", plistPath())
		return fmt.Errorf("remove launch agent %s: %w", plistPath(), err)
	}
	_, _ = fmt.Fprintf(out, "removed launch agent %s\n", plistPath())
	return nil
}

func renderPlist(executablePath string) string {
	replacer := strings.NewReplacer(
		"@@BIN_PATH@@", executablePath,
		"@@HOME@@", paths.Home(),
		"@@LABEL@@", launchdLabel,
		"@@LOG_PATH@@", updaterLogPath(),
	)
	return replacer.Replace(updaterPlistTemplate)
}

// runLaunchctl runs one launchctl subcommand and returns its trimmed combined
// output. launchctl mixes stdout and stderr, so combining gives one diagnostic
// blob for the caller.
func runLaunchctl(ctx context.Context, args ...string) (string, error) {
	daemonLog.DebugContext(ctx, "daemon.launchctl.boundary", "args", strings.Join(args, " "))
	output, err := exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
