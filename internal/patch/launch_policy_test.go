package patch

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

const testPreLaunchPolicyHook = "test-codex-cli-shim"

var (
	testPreLaunchPolicyOnce sync.Once
	testPreLaunchPolicyErr  error
)

func TestPreLaunchPolicyHookValuesAreSerialized(t *testing.T) {
	registerTestPreLaunchPolicyHook(t)
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	if err := os.MkdirAll(filepath.Join(appPath, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatalf("mkdir MacOS: %v", err)
	}
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		ExecName: "Codex",
		Extensions: extensions.Target{
			CodexCLIShim: &extensions.CodexCLIShimSpec{Capability: testPreLaunchPolicyHook},
		},
	}
	runner := NewRunner(context.Background(), false, io.Discard)
	if err := stepPreLaunchPolicy(context.Background(), runner, &target, Options{Out: io.Discard}); err != nil {
		t.Fatalf("stepPreLaunchPolicy: %v", err)
	}
	if err := stepInstallShim(context.Background(), runner, target); err != nil {
		t.Fatalf("stepInstallShim: %v", err)
	}

	data, err := os.ReadFile(paths.LaunchPolicyPath(target))
	if err != nil {
		t.Fatalf("read launch policy: %v", err)
	}
	var policy spec.LaunchPolicySpec
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatalf("unmarshal launch policy: %v", err)
	}
	requireLaunchEnv(t, policy, "CODEX_CLI_PATH", "/tmp/dvc-codex-cli-shim")
	requireLaunchEnv(t, policy, "DVC_CODEX_REAL_CLI", filepath.Join(appPath, "Contents", "Resources", "codex"))
	requireLaunchEnv(t, policy, "DVC_CODEX_CHATGPT_BASE_URL", "http://localhost:48730/backend-api")
}

func registerTestPreLaunchPolicyHook(t *testing.T) {
	t.Helper()
	testPreLaunchPolicyOnce.Do(func() {
		if err := catalog.RegisterPreLaunchPolicyHookCapability(testPreLaunchPolicyHook); err != nil {
			testPreLaunchPolicyErr = err
			return
		}
		testPreLaunchPolicyErr = RegisterPreLaunchPolicyHook(
			testPreLaunchPolicyHook,
			func(_ context.Context, _ *Runner, target *targets.Target, _ Options) error {
				target.LaunchPolicy.Environment = append(target.LaunchPolicy.Environment,
					spec.EnvActionSpec{Action: "set", Key: "CODEX_CLI_PATH", Value: "/tmp/dvc-codex-cli-shim"},
					spec.EnvActionSpec{
						Action: "set",
						Key:    "DVC_CODEX_REAL_CLI",
						Value:  filepath.Join(target.AppPath, "Contents", "Resources", "codex"),
					},
					spec.EnvActionSpec{
						Action: "set",
						Key:    "DVC_CODEX_CHATGPT_BASE_URL",
						Value:  "http://localhost:48730/backend-api",
					},
				)
				return nil
			},
		)
	})
	if testPreLaunchPolicyErr != nil {
		t.Fatalf("register test pre-launch-policy hook: %v", testPreLaunchPolicyErr)
	}
}

func requireLaunchEnv(t *testing.T, policy spec.LaunchPolicySpec, key string, want string) {
	t.Helper()
	for _, action := range policy.Environment {
		if action.Key != key {
			continue
		}
		if action.Action != "set" || action.Value != want {
			t.Fatalf("env %s = (%s, %s), want set %s", key, action.Action, action.Value, want)
		}
		return
	}
	t.Fatalf("missing env %s in %#v", key, policy.Environment)
}
