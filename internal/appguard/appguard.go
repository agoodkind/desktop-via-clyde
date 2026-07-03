// Package appguard detects and closes target app processes before foreground
// bundle mutation.
package appguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

const (
	defaultCloseTimeout = 20 * time.Second
	forceCloseTimeout   = 5 * time.Second
	closePollInterval   = 250 * time.Millisecond
)

var (
	appguardLog       = slog.With("component", "desktop-via-clyde", "subcomponent", "appguard")
	listProcessOutput = defaultListProcessOutput
	requestQuit       = defaultRequestQuit
	signalProcess     = defaultSignalProcess
	sleep             = time.Sleep
)

// Options controls foreground close behavior.
type Options struct {
	DryRun  bool
	Out     io.Writer
	Timeout time.Duration
}

// Process describes one running process that appears to belong to a target app.
type Process struct {
	PID     int
	Command string
}

// ErrAppRunning reports that a bundle mutation was skipped because target
// app processes are live.
var ErrAppRunning = errors.New("target app is running; deferring bundle mutation")

// Running reports whether any target process appears live.
func Running(ctx context.Context, target targets.Target) bool {
	processes, err := Processes(ctx, target)
	if err != nil {
		return false
	}
	return len(processes) > 0
}

// Processes returns live processes whose command appears under the app bundle.
func Processes(ctx context.Context, target targets.Target) ([]Process, error) {
	output, err := listProcessOutput(ctx)
	if err != nil {
		return nil, logAppguardError(ctx, "appguard.list_processes_failed", fmt.Errorf("list processes: %w", err))
	}
	return parseProcesses(output, target), nil
}

// EnsureClosed asks a foreground target app to quit and waits until its app
// processes exit. The background updater should call Running and defer instead.
func EnsureClosed(ctx context.Context, target targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultCloseTimeout
	}
	processes, err := Processes(ctx, target)
	if err != nil {
		return err
	}
	if len(processes) == 0 {
		return nil
	}
	note(opts, fmt.Sprintf("target=%s close running app before bundle mutation", target.ID))
	if opts.DryRun {
		return nil
	}
	if err := requestQuit(ctx, target); err != nil {
		return logAppguardError(ctx, "appguard.request_quit_failed", fmt.Errorf("request %s quit: %w", target.ID, err))
	}
	deadline := clock.Now().Add(timeout)
	for {
		processes, err = Processes(ctx, target)
		if err != nil {
			return err
		}
		if len(processes) == 0 {
			return nil
		}
		if !clock.Now().Before(deadline) {
			return terminateProcesses(ctx, target, opts, processes, forceTimeout(timeout))
		}
		sleep(closePollInterval)
	}
}

func defaultListProcessOutput(ctx context.Context) ([]byte, error) {
	appguardLog.DebugContext(ctx, "appguard.list_processes.boundary")
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,command=")
	output, err := cmd.Output()
	if err != nil {
		return nil, logAppguardError(ctx, "appguard.ps_failed", fmt.Errorf("run ps process list: %w", err))
	}
	return output, nil
}

func defaultRequestQuit(ctx context.Context, target targets.Target) error {
	appguardLog.DebugContext(ctx, "appguard.request_quit.boundary", "target", target.ID, "bundle_id", target.BundleID)
	ids := append([]string{target.BundleID}, target.BundleIDAliases...)
	var lastErr error
	for _, id := range ids {
		trimmedID := strings.TrimSpace(id)
		if trimmedID == "" {
			continue
		}
		script := fmt.Sprintf(`tell application id "%s" to quit`, escapeAppleScript(trimmedID))
		cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
		if err := cmd.Run(); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("target %s has no bundle id", target.ID)
}

func defaultSignalProcess(process Process, sig os.Signal) error {
	osProcess, err := os.FindProcess(process.PID)
	if err != nil {
		appguardLog.Error("appguard.find_process_failed", "pid", process.PID, "err", err)
		return fmt.Errorf("find process %d: %w", process.PID, err)
	}
	if err := osProcess.Signal(sig); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		appguardLog.Warn("appguard.signal_process_failed", "pid", process.PID, "signal", sig.String(), "err", err)
		return fmt.Errorf("signal process %d: %w", process.PID, err)
	}
	return nil
}

func parseProcesses(output []byte, target targets.Target) []Process {
	results := make([]Process, 0)
	for line := range strings.SplitSeq(string(output), "\n") {
		process, ok := parseProcessLine(line)
		if !ok {
			continue
		}
		if processMatchesTarget(process.Command, target) {
			results = append(results, process)
		}
	}
	return results
}

func parseProcessLine(line string) (Process, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return Process{PID: 0, Command: ""}, false
	}
	pidText, command, ok := strings.Cut(trimmed, " ")
	if !ok {
		return Process{PID: 0, Command: ""}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(pidText))
	if err != nil {
		return Process{PID: 0, Command: ""}, false
	}
	return Process{PID: pid, Command: strings.TrimSpace(command)}, true
}

func processMatchesTarget(command string, target targets.Target) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	appPath := strings.TrimSpace(target.AppPath)
	if appPath != "" && matchesCommandPrefix(command, filepath.Clean(appPath)) {
		return true
	}
	if matchesCommandPrefix(command, target.ExecName) {
		return true
	}
	return false
}

func matchesCommandPrefix(command string, prefix string) bool {
	if prefix == "" {
		return false
	}
	return command == prefix ||
		strings.HasPrefix(command, prefix+string(filepath.Separator)) ||
		strings.HasPrefix(command, prefix+" ")
}

func terminateProcesses(ctx context.Context, target targets.Target, opts Options, processes []Process, timeout time.Duration) error {
	note(opts, fmt.Sprintf("target=%s terminate running app processes after graceful close timeout", target.ID))
	if err := signalProcesses(processes, syscall.SIGTERM); err != nil {
		return logAppguardError(ctx, "appguard.terminate_failed", err)
	}
	remaining, err := waitForExit(ctx, target, timeout)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return nil
	}
	note(opts, fmt.Sprintf("target=%s kill running app processes after terminate timeout", target.ID))
	if err := signalProcesses(remaining, syscall.SIGKILL); err != nil {
		return logAppguardError(ctx, "appguard.kill_failed", err)
	}
	remaining, err = waitForExit(ctx, target, timeout)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return nil
	}
	return logAppguardError(ctx, "appguard.close_timeout",
		fmt.Errorf("%s still has running processes after forced close: %s", target.ID, formatProcesses(remaining)))
}

func signalProcesses(processes []Process, sig os.Signal) error {
	for _, process := range processes {
		if err := signalProcess(process, sig); err != nil {
			return err
		}
	}
	return nil
}

func waitForExit(ctx context.Context, target targets.Target, timeout time.Duration) ([]Process, error) {
	deadline := clock.Now().Add(timeout)
	for {
		processes, err := Processes(ctx, target)
		if err != nil {
			return nil, err
		}
		if len(processes) == 0 {
			return nil, nil
		}
		if !clock.Now().Before(deadline) {
			return processes, nil
		}
		sleep(closePollInterval)
	}
}

func forceTimeout(timeout time.Duration) time.Duration {
	if timeout < forceCloseTimeout {
		return timeout
	}
	return forceCloseTimeout
}

func formatProcesses(processes []Process) string {
	parts := make([]string, 0, len(processes))
	for _, process := range processes {
		parts = append(parts, fmt.Sprintf("%d %s", process.PID, process.Command))
	}
	return strings.Join(parts, "; ")
}

func escapeAppleScript(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(escaped, `"`, `\"`)
}

func logAppguardError(ctx context.Context, event string, err error) error {
	appguardLog.ErrorContext(ctx, event, "err", err)
	return err
}

func note(opts Options, message string) {
	if opts.Out == nil {
		return
	}
	prefix := "[run]"
	if opts.DryRun {
		prefix = "[dry-run]"
	}
	fmt.Fprintf(opts.Out, "%s %s\n", prefix, message)
}
