package claudetee

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testAppSupportRel = "Library/Application Support/Claude/claude-code"
	testBundledCLIRel = "claude.app/Contents/MacOS/claude"
)

func TestCompareVersionsNumericOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.1", -1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.0", "1.0.0", -1},
		{"1.0.0", "1.0.0", 0},
		{"2.1.149", "2.1.150", -1},
		{"2.1.150", "2.1.149", 1},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if got != c.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolveBundledCLIPathPicksGreatestVersion(t *testing.T) {
	home := t.TempDir()
	appSupport := filepath.Join(home, testAppSupportRel)
	// Three versions; the resolver must pick the greatest by version sort.
	for _, v := range []string{"2.1.99", "2.1.150", "2.1.149"} {
		dir := filepath.Join(appSupport, v, "claude.app", "Contents", "MacOS")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("fake"), 0o755); err != nil {
			t.Fatalf("write claude: %v", err)
		}
	}
	got, err := ResolveBundledCLIPath(Options{
		DryRun:                   false,
		AppSupportDir:            appSupport,
		VersionDir:               "",
		BundledCLIRel:            testBundledCLIRel,
		BundledCLIPath:           "",
		TerminateProcessNames:    nil,
		TerminateProcessPatterns: nil,
		CompletionSteps:          nil,
		LogDir:                   "",
		Out:                      nil,
		Trace:                    nil,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(home, testAppSupportRel, "2.1.150", testBundledCLIRel)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveBundledCLIPathHonorsVersionDirOverride(t *testing.T) {
	home := t.TempDir()
	appSupport := filepath.Join(home, testAppSupportRel)
	got, err := ResolveBundledCLIPath(Options{
		DryRun:                   false,
		AppSupportDir:            appSupport,
		VersionDir:               "9.9.9",
		BundledCLIRel:            testBundledCLIRel,
		BundledCLIPath:           "",
		TerminateProcessNames:    nil,
		TerminateProcessPatterns: nil,
		CompletionSteps:          nil,
		LogDir:                   "",
		Out:                      nil,
		Trace:                    nil,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(home, testAppSupportRel, "9.9.9", testBundledCLIRel)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveBundledCLIPathHonorsBundledCLIPathOverride(t *testing.T) {
	override := "/some/explicit/path/claude"
	got, err := ResolveBundledCLIPath(Options{
		DryRun:                   false,
		AppSupportDir:            "",
		VersionDir:               "should-be-ignored",
		BundledCLIRel:            "",
		BundledCLIPath:           override,
		TerminateProcessNames:    nil,
		TerminateProcessPatterns: nil,
		CompletionSteps:          nil,
		LogDir:                   "",
		Out:                      nil,
		Trace:                    nil,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != override {
		t.Fatalf("got %q, want %q", got, override)
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	opts, bundled := setupFakeBundledCLI(t)
	originalBytes := mustRead(t, bundled)

	var out bytes.Buffer
	trace := &Trace{}
	opts.DryRun = true
	opts.LogDir = "/tmp/should-show-up"
	opts.TerminateProcessNames = []string{"ExampleApp"}
	opts.TerminateProcessPatterns = []string{"example-helper/.*/child"}
	opts.Out = &out
	opts.Trace = trace
	err := Install(context.Background(), opts)
	if err != nil {
		t.Fatalf("dry-run install: %v", err)
	}

	if got := mustRead(t, bundled); !bytes.Equal(got, originalBytes) {
		t.Fatalf("dry-run install mutated %s", bundled)
	}
	if _, err := os.Stat(bundled + ".real"); err == nil {
		t.Fatalf(".real sibling unexpectedly created by dry-run install")
	}
	requireTeeTraceTarget(t, trace, actionResolveInstallTarget, bundled, "/tmp/should-show-up")
	requireTeeTraceProcessName(t, trace, "ExampleApp")
	requireTeeTraceProcessPattern(t, trace, "example-helper/.*/child")
	requireTeeTraceRename(t, trace, actionRenameBundledCLI, bundled, bundled+".real")
	requireTeeTracePath(t, trace, actionWriteShim, bundled)
}

func TestRenderCompletionStepSubstitutesLogDir(t *testing.T) {
	got := renderCompletionStep("read logs under {log_dir}/", "/tmp/example-logs")
	if got != "read logs under /tmp/example-logs/" {
		t.Fatalf("renderCompletionStep = %q", got)
	}
}

func TestInstallRefusesWhenRealExists(t *testing.T) {
	opts, bundled := setupFakeBundledCLI(t)
	if err := os.WriteFile(bundled+".real", []byte("pre-existing"), 0o755); err != nil {
		t.Fatalf("seed .real: %v", err)
	}
	var out bytes.Buffer
	opts.Out = &out
	err := Install(context.Background(), opts)
	if err == nil {
		t.Fatalf("expected install to refuse when .real already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error, got: %v", err)
	}
}

func TestInstallDefersWhenTerminateIsDisallowedAndMatchingProcessIsRunning(t *testing.T) {
	opts, _ := setupFakeBundledCLI(t)
	var out bytes.Buffer
	opts.Out = &out
	opts.AllowTerminate = false
	opts.TerminateProcessNames = []string{"Claude"}

	originalNameMatches := processNameMatches
	originalKillName := killProcessName
	processNameMatches = func(context.Context, string) (bool, error) {
		return true, nil
	}
	killed := false
	killProcessName = func(context.Context, string) error {
		killed = true
		return nil
	}
	t.Cleanup(func() {
		processNameMatches = originalNameMatches
		killProcessName = originalKillName
	})

	err := Install(context.Background(), opts)
	if !errors.Is(err, ErrProcessesRunning) {
		t.Fatalf("Install error = %v, want ErrProcessesRunning", err)
	}
	if killed {
		t.Fatal("pkill ran even though termination was disallowed")
	}
}

func TestStopConfiguredProcessesTerminatesMatchingProcessesWhenAllowed(t *testing.T) {
	var out bytes.Buffer
	opts := Options{
		AllowTerminate:           true,
		TerminateProcessNames:    []string{"Claude"},
		TerminateProcessPatterns: []string{"claude-helper"},
	}

	originalKillName := killProcessName
	originalKillPattern := killProcessPattern
	killedName := false
	killedPattern := false
	killProcessName = func(context.Context, string) error {
		killedName = true
		return nil
	}
	killProcessPattern = func(context.Context, string) error {
		killedPattern = true
		return nil
	}
	t.Cleanup(func() {
		killProcessName = originalKillName
		killProcessPattern = originalKillPattern
	})

	if err := stopConfiguredProcesses(context.Background(), opts, &out); err != nil {
		t.Fatalf("stopConfiguredProcesses: %v", err)
	}
	if !killedName {
		t.Fatal("name-based kill did not run")
	}
	if !killedPattern {
		t.Fatal("pattern-based kill did not run")
	}
}

// setupFakeBundledCLI builds a fake claude-code/<version>/claude.app tree
// under a temp HOME and returns the home dir plus the bundled CLI path. The
// fake claude binary is just a few bytes; tests that need a working real
// binary use a shell script when needed.
func setupFakeBundledCLI(t *testing.T) (Options, string) {
	t.Helper()
	home := t.TempDir()
	appSupport := filepath.Join(home, testAppSupportRel)
	dir := filepath.Join(appSupport, "2.1.149", "claude.app", "Contents", "MacOS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bundled := filepath.Join(dir, "claude")
	if err := os.WriteFile(bundled, []byte("fake-original-claude"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return Options{
		DryRun:                   false,
		AppSupportDir:            appSupport,
		VersionDir:               "",
		BundledCLIRel:            testBundledCLIRel,
		BundledCLIPath:           "",
		TerminateProcessNames:    nil,
		TerminateProcessPatterns: nil,
		CompletionSteps:          nil,
		LogDir:                   "",
		Out:                      nil,
		Trace:                    nil,
	}, bundled
}

func requireTeeTraceTarget(t *testing.T, trace *Trace, action Action, path string, logDir string) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Action == action && event.Path == path && event.LogDir == logDir {
			return
		}
	}
	t.Fatalf("trace missing action=%s path=%s logDir=%s events=%#v", action, path, logDir, trace.Events)
}

func requireTeeTraceRename(t *testing.T, trace *Trace, action Action, from string, to string) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Action == action && event.From == from && event.To == to {
			return
		}
	}
	t.Fatalf("trace missing action=%s from=%s to=%s events=%#v", action, from, to, trace.Events)
}

func requireTeeTraceProcessName(t *testing.T, trace *Trace, name string) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Action == actionStopProcessName && event.Name == name {
			return
		}
	}
	t.Fatalf("trace missing process name=%s events=%#v", name, trace.Events)
}

func requireTeeTraceProcessPattern(t *testing.T, trace *Trace, pattern string) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Action == actionStopProcessPattern && event.Pattern == pattern {
			return
		}
	}
	t.Fatalf("trace missing process pattern=%s events=%#v", pattern, trace.Events)
}

func requireTeeTracePath(t *testing.T, trace *Trace, action Action, path string) {
	t.Helper()
	for _, event := range trace.Events {
		if event.Action == action && event.Path == path {
			return
		}
	}
	t.Fatalf("trace missing action=%s path=%s events=%#v", action, path, trace.Events)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
