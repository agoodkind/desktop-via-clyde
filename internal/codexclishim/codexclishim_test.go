package codexclishim

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestRewrittenArgsInjectsChatGPTBaseBeforeAppServer(t *testing.T) {
	got, err := RewrittenArgs([]string{"app-server", "--port", "0"}, "http://localhost:48730/backend-api")
	if err != nil {
		t.Fatalf("RewrittenArgs: %v", err)
	}
	want := []string{
		"-c",
		"chatgpt_base_url=http://localhost:48730/backend-api",
		"app-server",
		"--port",
		"0",
	}
	if !equalStrings(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestRewrittenArgsLeavesNonAppServerInvocationUnchanged(t *testing.T) {
	got, err := RewrittenArgs([]string{"exec", "--help"}, "http://localhost:48730/backend-api")
	if err != nil {
		t.Fatalf("RewrittenArgs: %v", err)
	}
	want := []string{"exec", "--help"}
	if !equalStrings(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestRunWithExecsRealCLIWithRewrittenArgs(t *testing.T) {
	var gotPath string
	var gotArgv []string
	var gotEnv []string
	getenv := func(key string) string {
		switch key {
		case EnvRealCLI:
			return "/tmp/Codex.app/Contents/Resources/codex"
		case EnvChatGPTBaseURL:
			return "http://localhost:48730/backend-api"
		default:
			return ""
		}
	}
	execve := func(path string, argv []string, env []string) error {
		gotPath = path
		gotArgv = append([]string(nil), argv...)
		gotEnv = append([]string(nil), env...)
		return nil
	}
	err := RunWith([]string{"app-server"}, getenv, execve, []string{"A=B"})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if gotPath != "/tmp/Codex.app/Contents/Resources/codex" {
		t.Fatalf("path = %q", gotPath)
	}
	wantArgv := []string{
		"/tmp/Codex.app/Contents/Resources/codex",
		"-c",
		"chatgpt_base_url=http://localhost:48730/backend-api",
		"app-server",
	}
	if !equalStrings(gotArgv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", gotArgv, wantArgv)
	}
	if !equalStrings(gotEnv, []string{"A=B"}) {
		t.Fatalf("env = %#v", gotEnv)
	}
}

func TestPreLaunchPolicyHookInstallsWrapperAndAppendsResolvedEnvironment(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	target := targets.Target{
		ID:      "codex",
		AppPath: appPath,
		Extensions: extensions.Target{
			CodexCLIShim: &extensions.CodexCLIShimSpec{
				Capability:     HookCapability,
				ChatGPTBaseURL: "http://localhost:48730/backend-api",
			},
		},
	}
	runner := patch.NewRunner(context.Background(), false, io.Discard)
	err := PreLaunchPolicyHook(context.Background(), runner, &target, patch.Options{DryRun: false, Out: io.Discard})
	if err != nil {
		t.Fatalf("PreLaunchPolicyHook: %v", err)
	}
	if _, err := os.Stat(InstalledPath()); err != nil {
		t.Fatalf("stat installed wrapper: %v", err)
	}
	requireEnv(t, target, EnvCLIPath, InstalledPath())
	requireEnv(t, target, EnvRealCLI, filepath.Join(appPath, "Contents", "Resources", "codex"))
	requireEnv(t, target, EnvChatGPTBaseURL, "http://localhost:48730/backend-api")
}

func TestPreLaunchPolicyHookUsesFinalAppPathForRealCLIEnv(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	stagedAppPath := filepath.Join(t.TempDir(), "staged", "Codex.app")
	finalAppPath := filepath.Join(t.TempDir(), "live", "Codex.app")
	target := targets.Target{
		ID:      "codex",
		AppPath: stagedAppPath,
		Extensions: extensions.Target{
			CodexCLIShim: &extensions.CodexCLIShimSpec{
				Capability:     HookCapability,
				ChatGPTBaseURL: "http://localhost:48730/backend-api",
			},
		},
	}
	runner := patch.NewRunner(context.Background(), false, io.Discard)
	err := PreLaunchPolicyHook(context.Background(), runner, &target, patch.Options{
		DryRun:       false,
		Out:          io.Discard,
		FinalAppPath: finalAppPath,
	})
	if err != nil {
		t.Fatalf("PreLaunchPolicyHook: %v", err)
	}
	requireEnv(t, target, EnvRealCLI, filepath.Join(finalAppPath, "Contents", "Resources", "codex"))
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func requireEnv(t *testing.T, target targets.Target, key string, want string) {
	t.Helper()
	for _, action := range target.LaunchPolicy.Environment {
		if action.Key != key {
			continue
		}
		if action.Action != "set" || action.Value != want {
			t.Fatalf("env %s = (%s, %s), want set %s", key, action.Action, action.Value, want)
		}
		return
	}
	t.Fatalf("missing env %s in %#v", key, target.LaunchPolicy.Environment)
}
