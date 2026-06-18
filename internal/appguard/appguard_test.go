package appguard

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestProcessesMatchesBundlePathAndExecName(t *testing.T) {
	target := targets.Target{
		ID:       "codex",
		AppPath:  "/Applications/Codex.app",
		ExecName: "Codex (Beta)",
	}
	output := []byte(`
  101 /Applications/Codex.app/Contents/MacOS/Codex (Beta)
  102 /Applications/Codex.app/Contents/Frameworks/Codex Framework.framework/Helpers/Codex (Renderer).app/Contents/MacOS/Codex (Renderer)
  103 Other
`)

	processes := parseProcesses(output, target)
	if len(processes) != 2 {
		t.Fatalf("process count = %d, want 2: %#v", len(processes), processes)
	}
}

func TestProcessesIgnoresCommandsThatOnlyMentionBundlePath(t *testing.T) {
	target := targets.Target{
		ID:       "codex",
		AppPath:  "/Applications/Codex.app",
		ExecName: "Codex (Beta)",
	}
	output := []byte(`
  101 /Users/agoodkind/.codex/computer-use/SkyComputerUseClient --note=/Applications/Codex.app/Contents/MacOS/Codex
  102 /bin/zsh -lc test -e /Applications/Codex.app/Contents/MacOS/Codex
  103 /Applications/Codex.app/Contents/MacOS/Codex (Beta)
`)

	processes := parseProcesses(output, target)
	if len(processes) != 1 {
		t.Fatalf("process count = %d, want 1: %#v", len(processes), processes)
	}
	if processes[0].PID != 103 {
		t.Fatalf("matched PID = %d, want 103", processes[0].PID)
	}
}

func TestEnsureClosedRequestsQuitAndWaits(t *testing.T) {
	target := targets.Target{
		ID:       "codex",
		AppPath:  "/Applications/Codex.app",
		BundleID: "com.openai.codex.beta",
		ExecName: "Codex (Beta)",
	}
	running := true
	quitRequested := false
	originalListProcessOutput := listProcessOutput
	originalRequestQuit := requestQuit
	originalSleep := sleep
	listProcessOutput = func(context.Context) ([]byte, error) {
		if !running {
			return []byte(""), nil
		}
		return []byte("101 /Applications/Codex.app/Contents/MacOS/Codex (Beta)\n"), nil
	}
	requestQuit = func(context.Context, targets.Target) error {
		quitRequested = true
		running = false
		return nil
	}
	sleep = func(time.Duration) {}
	t.Cleanup(func() {
		listProcessOutput = originalListProcessOutput
		requestQuit = originalRequestQuit
		sleep = originalSleep
	})

	var out strings.Builder
	err := EnsureClosed(context.Background(), target, Options{Out: &out})
	if err != nil {
		t.Fatalf("EnsureClosed: %v", err)
	}
	if !quitRequested {
		t.Fatal("quit was not requested")
	}
	if !strings.Contains(out.String(), "close running app before bundle mutation") {
		t.Fatalf("output missing close notice: %s", out.String())
	}
}

func TestEnsureClosedTerminatesWhenQuitDoesNotExit(t *testing.T) {
	target := targets.Target{
		ID:       "codex",
		AppPath:  "/Applications/Codex.app",
		BundleID: "com.openai.codex.beta",
		ExecName: "Codex (Beta)",
	}
	running := true
	quitRequested := false
	terminated := false
	killed := false
	originalListProcessOutput := listProcessOutput
	originalRequestQuit := requestQuit
	originalSignalProcess := signalProcess
	originalSleep := sleep
	listProcessOutput = func(context.Context) ([]byte, error) {
		if !running {
			return []byte(""), nil
		}
		return []byte("101 /Applications/Codex.app/Contents/MacOS/Codex (Beta)\n"), nil
	}
	requestQuit = func(context.Context, targets.Target) error {
		quitRequested = true
		return nil
	}
	signalProcess = func(process Process, sig os.Signal) error {
		if process.PID != 101 {
			t.Fatalf("signal PID = %d, want 101", process.PID)
		}
		switch sig {
		case syscall.SIGTERM:
			terminated = true
			running = false
		case syscall.SIGKILL:
			killed = true
		default:
			t.Fatalf("unexpected signal %v", sig)
		}
		return nil
	}
	sleep = func(time.Duration) {}
	t.Cleanup(func() {
		listProcessOutput = originalListProcessOutput
		requestQuit = originalRequestQuit
		signalProcess = originalSignalProcess
		sleep = originalSleep
	})

	var out strings.Builder
	err := EnsureClosed(context.Background(), target, Options{
		Out:     &out,
		Timeout: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("EnsureClosed: %v", err)
	}
	if !quitRequested {
		t.Fatal("quit was not requested")
	}
	if !terminated {
		t.Fatal("process was not terminated")
	}
	if killed {
		t.Fatal("process was killed after terminate succeeded")
	}
	if !strings.Contains(out.String(), "terminate running app processes") {
		t.Fatalf("output missing terminate notice: %s", out.String())
	}
}

func TestEnsureClosedFailsWhenProcessRemains(t *testing.T) {
	target := targets.Target{
		ID:       "codex",
		AppPath:  "/Applications/Codex.app",
		BundleID: "com.openai.codex.beta",
		ExecName: "Codex (Beta)",
	}
	originalListProcessOutput := listProcessOutput
	originalRequestQuit := requestQuit
	originalSignalProcess := signalProcess
	originalSleep := sleep
	listProcessOutput = func(context.Context) ([]byte, error) {
		return []byte("101 /Applications/Codex.app/Contents/MacOS/Codex (Beta)\n"), nil
	}
	requestQuit = func(context.Context, targets.Target) error { return nil }
	signals := make([]os.Signal, 0)
	signalProcess = func(_ Process, sig os.Signal) error {
		signals = append(signals, sig)
		return nil
	}
	sleep = func(time.Duration) {}
	t.Cleanup(func() {
		listProcessOutput = originalListProcessOutput
		requestQuit = originalRequestQuit
		signalProcess = originalSignalProcess
		sleep = originalSleep
	})

	var out strings.Builder
	err := EnsureClosed(context.Background(), target, Options{
		Out:     &out,
		Timeout: time.Nanosecond,
	})
	if err == nil {
		t.Fatal("EnsureClosed succeeded, want timeout")
	}
	if !strings.Contains(err.Error(), "still has running processes") {
		t.Fatalf("error = %v", err)
	}
	if len(signals) != 2 || signals[0] != syscall.SIGTERM || signals[1] != syscall.SIGKILL {
		t.Fatalf("signals = %#v, want SIGTERM then SIGKILL", signals)
	}
	text := out.String()
	for _, want := range []string{
		"terminate running app processes",
		"kill running app processes",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q: %s", want, text)
		}
	}
}
