// Package main is a self-locating stdio-tee shim. It replaces a binary that
// the parent process spawns over stdio, runs the original binary (renamed to
// <name>.real next to the shim) as a child, and tees the parent's stdin,
// the child's stdout, and the child's stderr to time-stamped log files. The
// shim forwards SIGINT, SIGTERM, SIGHUP, SIGUSR1, and SIGUSR2 to the child
// and exits with the child's termination status.
//
// Logs land under $HOME/.local/state/desktop-via-clyde/stdio-tee/ unless
// DVC_STDIO_TEE_DIR is set. Each invocation produces five files:
// <stamp>-<pid>.{stdin.jsonl, stdout.jsonl, stderr.log, meta.log,
// shim.jsonl}. The shim.jsonl stream carries shim-internal diagnostics
// (lifecycle events, signal forwards, child exit details) as JSON lines
// from log/slog. The parent's stderr is reserved for the child's bytes;
// the shim never writes its own diagnostics there, so the parent never
// confuses shim chatter with the child's output.
//
// Drop-in install path: rename the target binary to <name>.real and place
// the shim at <name>.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"goodkind.io/desktop-via-clyde/internal/paths"
)

const (
	logDirPerms    = 0o700
	logFilePerms   = 0o600
	signalForwards = "SIGINT SIGTERM SIGHUP SIGUSR1 SIGUSR2"
)

type invocationLogs struct {
	stdinLog  *os.File
	stdoutLog *os.File
	stderrLog *os.File
	metaLog   *os.File
}

func logDirPath() string {
	return paths.StdioTeeLogDir()
}

// resolveSelfRealPath returns the absolute, symlink-resolved path of the
// running shim binary. [os.Executable] may return a symlinked path on macOS;
// EvalSymlinks resolves to the install location so the .real sibling is
// always next to the actual binary on disk.
func resolveSelfRealPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		if shimLog != nil {
			shimLog.Error("resolve self executable", "err", err.Error())
		}
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	resolved, _ := filepath.EvalSymlinks(exe)
	if resolved == "" {
		// Fall back to the unresolved path rather than fail; the .real
		// sibling lookup may still succeed alongside the symlink.
		return exe, nil
	}
	return resolved, nil
}

// shimLog is the structured logger writing into the per-invocation
// shim.jsonl file. It is initialised after the log directory exists and
// the file can be opened; before that, errors flow through failFastInit.
var (
	shimLog      *slog.Logger
	bootstrapLog = slog.New(slog.DiscardHandler)
)

// failFastInit handles bootstrap errors that happen before the shim log
// file is open. The parent process needs SOME signal that the shim could
// not even initialise; stderr is the only available channel at this point.
// Once shim.jsonl is open, every other diagnostic goes through shimLog and
// the parent's stderr stays untouched.
func failFastInit(msg string, code int) int {
	fmt.Fprintf(os.Stderr, "dvc-stdio-tee-shim: %s\n", msg)
	return code
}

func timestampForFilename() string {
	return time.Now().UTC().Format("20060102T150405.000000000")
}

func writeMeta(w io.Writer, selfPath, realPath string, args []string) error {
	cwd, _ := os.Getwd()
	lines := []string{
		"argv0: " + filepath.Base(selfPath),
		"self:  " + selfPath,
		"real:  " + realPath,
		fmt.Sprintf("pid:   %d", os.Getpid()),
		fmt.Sprintf("ppid:  %d", os.Getppid()),
		"cwd:   " + cwd,
		"args:  " + strings.Join(args, " "),
		"start: " + time.Now().Format(time.RFC3339Nano),
		"signals_forwarded: " + signalForwards,
		"",
	}
	_, err := io.WriteString(w, strings.Join(lines, "\n"))
	if err != nil {
		if shimLog != nil {
			shimLog.Error("write meta log", "err", err.Error())
		}
		return fmt.Errorf("write meta log: %w", err)
	}
	return nil
}

// teePump copies bytes from src to dst, also writing every chunk to tee. It
// blocks until src returns EOF or a non-recoverable error, then closes the
// tee log and signals doneCh. When closeDst is true the dst is also closed,
// which is required for the parent-to-child direction so the child sees EOF
// on its stdin and can exit. The child-to-parent direction must leave dst
// open because dst is the parent's own stdout or stderr; closing them would
// break the Go runtime's exit-time flush and crash the shim.
func teePump(src io.Reader, dst io.Writer, tee io.WriteCloser, closeDst bool, doneCh chan<- struct{}) {
	defer func() {
		if closeDst {
			if c, ok := dst.(io.Closer); ok {
				_ = c.Close()
			}
		}
		_ = tee.Close()
		doneCh <- struct{}{}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			// Tee write is best-effort: if it fails we still must move the
			// real bytes to dst so the child does not stall.
			_, _ = tee.Write(buf[:n])
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func openLog(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, logFilePerms)
	if err != nil {
		if shimLog != nil {
			shimLog.Error("open log file", "path", path, "err", err.Error())
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return file, nil
}

func openInvocationLogs(base, selfPath, realPath string) (invocationLogs, error) {
	logs := invocationLogs{}

	var err error
	logs.stdinLog, err = openLog(base + ".stdin.jsonl")
	if err != nil {
		return logs, err
	}
	logs.stdoutLog, err = openLog(base + ".stdout.jsonl")
	if err != nil {
		return logs, err
	}
	logs.stderrLog, err = openLog(base + ".stderr.log")
	if err != nil {
		return logs, err
	}
	logs.metaLog, err = openLog(base + ".meta.log")
	if err != nil {
		return logs, err
	}

	if err := writeMeta(logs.metaLog, selfPath, realPath, os.Args[1:]); err != nil {
		shimLog.Warn("meta write failed", "err", err.Error())
	}
	return logs, nil
}

func startChildProcess(realPath string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	commandContext := context.Background()
	cmd := exec.CommandContext(commandContext, realPath)
	cmd.Args = append([]string{realPath}, os.Args[1:]...)
	cmd.Env = os.Environ()
	cwd, _ := os.Getwd()
	cmd.Dir = cwd

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		shimLog.Error("open child stdin pipe", "err", err.Error())
		return nil, nil, nil, nil, fmt.Errorf("open child stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		shimLog.Error("open child stdout pipe", "err", err.Error())
		return nil, nil, nil, nil, fmt.Errorf("open child stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		shimLog.Error("open child stderr pipe", "err", err.Error())
		return nil, nil, nil, nil, fmt.Errorf("open child stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		shimLog.Error("start child process", "real", realPath, "err", err.Error())
		return nil, nil, nil, nil, fmt.Errorf("start child process: %w", err)
	}
	return cmd, stdinPipe, stdoutPipe, stderrPipe, nil
}

func startSignalForwarding(cmd *exec.Cmd) chan os.Signal {
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh,
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP,
		syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				shimLog.Error("goroutine panic", "label", "signal.forwarding", "err", fmt.Sprintf("panic: %v", recovered))
			}
		}()
		for sig := range sigCh {
			if cmd.Process != nil {
				err := cmd.Process.Signal(sig)
				shimLog.Debug("signal.forwarded",
					"signal", sig.String(), "child_pid", cmd.Process.Pid,
					"err", errString(err))
			}
		}
	}()
	return sigCh
}

func startPumpGroup(
	stdinPipe io.WriteCloser,
	stdoutPipe io.ReadCloser,
	stderrPipe io.ReadCloser,
	logs invocationLogs,
) *sync.WaitGroup {
	var pumpWg sync.WaitGroup
	pumpWg.Add(3)
	doneStdin := make(chan struct{}, 1)
	doneStdout := make(chan struct{}, 1)
	doneStderr := make(chan struct{}, 1)

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				shimLog.Error("goroutine panic", "label", "stdin.pump", "err", fmt.Sprintf("panic: %v", recovered))
			}
		}()
		teePump(os.Stdin, stdinPipe, logs.stdinLog, true, doneStdin)
		pumpWg.Done()
	}()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				shimLog.Error("goroutine panic", "label", "stdout.pump", "err", fmt.Sprintf("panic: %v", recovered))
			}
		}()
		teePump(stdoutPipe, os.Stdout, logs.stdoutLog, false, doneStdout)
		pumpWg.Done()
	}()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				shimLog.Error("goroutine panic", "label", "stderr.pump", "err", fmt.Sprintf("panic: %v", recovered))
			}
		}()
		teePump(stderrPipe, os.Stderr, logs.stderrLog, false, doneStderr)
		pumpWg.Done()
	}()
	return &pumpWg
}

func main() {
	bootstrapLog.Info("shim.main")
	os.Exit(run())
}

func run() int {
	selfPath, err := resolveSelfRealPath()
	if err != nil {
		return failFastInit(fmt.Sprintf("resolve self: %v", err), 14)
	}
	realPath := selfPath + ".real"
	info, statErr := os.Stat(realPath)
	if statErr != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return failFastInit("missing or non-executable sibling at "+realPath, 13)
	}

	logDir := logDirPath()
	if err := os.MkdirAll(logDir, logDirPerms); err != nil {
		return failFastInit(fmt.Sprintf("mkdir %s: %v", logDir, err), 21)
	}
	base := filepath.Join(logDir, timestampForFilename()+"-"+strconv.Itoa(os.Getpid()))

	shimLogFile, err := openLog(base + ".shim.jsonl")
	if err != nil {
		return failFastInit(fmt.Sprintf("open shim log: %v", err), 22)
	}
	shimLog = slog.New(slog.NewJSONHandler(shimLogFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
	shimLog.Info("shim.start",
		"argv0", filepath.Base(selfPath),
		"self", selfPath,
		"real", realPath,
		"pid", os.Getpid(),
		"ppid", os.Getppid(),
		"args", os.Args[1:],
	)

	logs, err := openInvocationLogs(base, selfPath, realPath)
	if err != nil {
		shimLog.Error("open invocation log", "err", err.Error())
		return 22
	}
	cmd, stdinPipe, stdoutPipe, stderrPipe, err := startChildProcess(realPath)
	if err != nil {
		shimLog.Error("start child", "real", realPath, "err", err.Error())
		return 24
	}
	shimLog.Info("child.started", "pid", cmd.Process.Pid)

	// Forward a defined set of signals to the child. The shim ignores them
	// for itself so the child sees them through its own handlers; the child
	// gets to decide whether to honour or ignore. SIGKILL and SIGSTOP cannot
	// be caught, so listing them is pointless.
	sigCh := startSignalForwarding(cmd)
	pumpWg := startPumpGroup(stdinPipe, stdoutPipe, stderrPipe, logs)

	exitCode := waitForChildExit(cmd, pumpWg)
	signal.Stop(sigCh)
	_ = logs.metaLog.Close()
	shimLog.Info("shim.exit", "exit_code", exitCode)
	return exitCode
}

func waitForChildExit(cmd *exec.Cmd, pumpWg *sync.WaitGroup) int {
	shimLog.Info("child.wait.begin", "child_pid", cmd.Process.Pid)
	waitErr := cmd.Wait()
	shimLog.Info("child.wait.returned",
		"err", errString(waitErr),
		"process_state", fmt.Sprintf("%+v", cmd.ProcessState),
	)
	waitForPumps(pumpWg)
	return exitCodeForWaitError(waitErr)
}

func waitForPumps(pumpWg *sync.WaitGroup) {
	drained := make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				shimLog.Error("goroutine panic", "label", "pump.wait", "err", fmt.Sprintf("panic: %v", recovered))
			}
		}()
		pumpWg.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(500 * time.Millisecond):
		shimLog.Warn("pumps.drain.timeout")
	}
}

func exitCodeForWaitError(waitErr error) int {
	if waitErr == nil {
		return 0
	}

	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		shimLog.Error("wait error", "err", waitErr.Error())
		return 1
	}

	exitCode := exitErr.ExitCode()
	if exitCode != -1 {
		return exitCode
	}

	waitStatus, ok := exitErr.Sys().(syscall.WaitStatus)
	if ok && waitStatus.Signaled() {
		signalValue := waitStatus.Signal()
		exitCode = 128 + int(signalValue)
		shimLog.Warn("child.killed_by_signal",
			"signal", signalValue.String(), "exit_code", exitCode)
		return exitCode
	}

	return 128
}

// errString returns the empty string for nil errors so log/slog records do
// not pollute the JSON output with `null` entries; non-nil errors return
// their message text.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
