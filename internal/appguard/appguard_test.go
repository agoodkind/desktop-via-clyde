package appguard

import (
	"context"
	"strings"
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

func TestEnsureClosedFailsWhenProcessRemains(t *testing.T) {
	target := targets.Target{
		ID:       "codex",
		AppPath:  "/Applications/Codex.app",
		BundleID: "com.openai.codex.beta",
		ExecName: "Codex (Beta)",
	}
	originalListProcessOutput := listProcessOutput
	originalRequestQuit := requestQuit
	originalSleep := sleep
	listProcessOutput = func(context.Context) ([]byte, error) {
		return []byte("101 /Applications/Codex.app/Contents/MacOS/Codex (Beta)\n"), nil
	}
	requestQuit = func(context.Context, targets.Target) error { return nil }
	sleep = func(time.Duration) {}
	t.Cleanup(func() {
		listProcessOutput = originalListProcessOutput
		requestQuit = originalRequestQuit
		sleep = originalSleep
	})

	err := EnsureClosed(context.Background(), target, Options{
		Out:     &strings.Builder{},
		Timeout: time.Nanosecond,
	})
	if err == nil {
		t.Fatal("EnsureClosed succeeded, want timeout")
	}
	if !strings.Contains(err.Error(), "still has running processes") {
		t.Fatalf("error = %v", err)
	}
}
