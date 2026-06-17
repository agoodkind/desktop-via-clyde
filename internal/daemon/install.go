package daemon

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/response"
	"goodkind.io/gklog/correlation"
)

//go:embed templates/updater.plist.tmpl
var updaterPlistTemplate string

// launchdLabel is the per-user LaunchAgent label for the updater daemon.
const launchdLabel = "io.goodkind.desktop-via-clyde.updater"

var (
	runLaunchctlStatus    = runLaunchctl
	daemonReachableStatus = daemonReachable
	fetchUpdaterStatusFn  = fetchUpdaterStatus
)

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

type launchAgentStatus struct {
	Loaded bool   `json:"loaded"`
	Target string `json:"target"`
}

type daemonRPCStatus struct {
	Responding bool   `json:"responding"`
	Socket     string `json:"socket"`
}

type statusSnapshot struct {
	LaunchAgent launchAgentStatus                           `json:"launch_agent"`
	DaemonRPC   daemonRPCStatus                             `json:"daemon_rpc"`
	Updater     *desktopviaclydev1.GetUpdaterStatusResponse `json:"updater,omitempty"`
}

// Status reports whether the LaunchAgent is loaded, whether the daemon's RPC
// socket is responding, and the daemon's current active runs.
func Status(ctx context.Context, out io.Writer, format clioutput.Format) error {
	loaded := true
	if _, err := runLaunchctlStatus(ctx, "print", launchdTarget()); err != nil {
		loaded = false
	}

	snapshot := statusSnapshot{
		LaunchAgent: launchAgentStatus{
			Loaded: loaded,
			Target: launchdTarget(),
		},
		DaemonRPC: daemonRPCStatus{
			Responding: daemonReachableStatus(ctx),
			Socket:     paths.DaemonSocketPath(),
		},
		Updater: nil,
	}

	if snapshot.DaemonRPC.Responding {
		updaterStatus, err := fetchUpdaterStatusFn(ctx)
		if err != nil {
			return err
		}
		snapshot.Updater = updaterStatus
	}

	if format == clioutput.FormatJSON {
		return writeStatusJSON(ctx, out, snapshot)
	}
	return writeStatusText(out, snapshot)
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

func fetchUpdaterStatus(ctx context.Context) (*desktopviaclydev1.GetUpdaterStatusResponse, error) {
	conn, client, err := dial()
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	resp, err := client.GetUpdaterStatus(correlation.NewOutgoingContext(ctx), &desktopviaclydev1.GetUpdaterStatusRequest{})
	if err != nil {
		daemonLog.WarnContext(ctx, "daemon.status.fetch_updater_status_failed", "err", err)
		return nil, fmt.Errorf("get updater status: %w", err)
	}
	return resp, nil
}

func writeStatusJSON(ctx context.Context, out io.Writer, snapshot statusSnapshot) error {
	body, err := json.Marshal(snapshot)
	if err != nil {
		daemonLog.WarnContext(ctx, "daemon.status.marshal_failed", "err", err)
		return fmt.Errorf("marshal updater status: %w", err)
	}
	if err := response.WriteJSON(ctx, out, body, response.JSONIndented); err != nil {
		daemonLog.WarnContext(ctx, "daemon.status.write_json_failed", "err", err)
		return fmt.Errorf("write updater status json: %w", err)
	}
	return nil
}

func writeStatusText(out io.Writer, snapshot statusSnapshot) error {
	if snapshot.LaunchAgent.Loaded {
		if err := writeStatusLine(out, fmt.Sprintf("launch agent: loaded target=%s\n", snapshot.LaunchAgent.Target)); err != nil {
			return err
		}
	} else {
		if err := writeStatusLine(out, fmt.Sprintf("launch agent: not loaded target=%s\n", snapshot.LaunchAgent.Target)); err != nil {
			return err
		}
	}

	if snapshot.DaemonRPC.Responding {
		if err := writeStatusLine(out, fmt.Sprintf("daemon rpc: responding socket=%s\n", snapshot.DaemonRPC.Socket)); err != nil {
			return err
		}
	} else {
		if err := writeStatusLine(out, fmt.Sprintf("daemon rpc: unavailable socket=%s\n", snapshot.DaemonRPC.Socket)); err != nil {
			return err
		}
	}

	if snapshot.Updater == nil {
		return nil
	}
	if len(snapshot.Updater.GetActiveRuns()) == 0 {
		return writeStatusLine(out, "active runs: none\n")
	}
	for _, run := range snapshot.Updater.GetActiveRuns() {
		if err := writeStatusLine(out, fmt.Sprintf("active run: target=%s operation=%s\n", run.GetTarget(), run.GetOperation())); err != nil {
			return err
		}
	}
	return nil
}

func writeStatusLine(out io.Writer, value string) error {
	if _, err := io.WriteString(out, value); err != nil {
		daemonLog.Warn("daemon.status.write_text_failed", "err", err)
		return fmt.Errorf("write status text: %w", err)
	}
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
