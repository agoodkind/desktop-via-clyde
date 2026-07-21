package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/composition"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/spec"
)

func TestMain(m *testing.M) {
	if err := composition.Register(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestLoadRequiredMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, err := config.LoadRequired()
	if err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestLoadPathLoadsDeclaredConfig(t *testing.T) {
	path := fixturePath(t)
	cfg, err := config.LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath(%s): %v", path, err)
	}

	cursor, ok := cfg.Apps["cursor"]
	if !ok {
		t.Fatal("fixture missing cursor app")
	}
	if cursor.Command.Use != "cursor" {
		t.Fatalf("cursor command use = %q", cursor.Command.Use)
	}

	claude := cfg.Apps["claude"]
	if claude.Extensions.BundledCLITee == nil {
		t.Fatal("fixture missing bundled CLI tee config")
	}
	if len(claude.Extensions.BundledCLITee.TerminateProcessNames) != 1 || claude.Extensions.BundledCLITee.TerminateProcessNames[0] != "Claude" {
		t.Fatalf("bundled CLI tee process names = %#v", claude.Extensions.BundledCLITee.TerminateProcessNames)
	}
	if len(claude.Extensions.BundledCLITee.CompletionSteps) == 0 {
		t.Fatal("fixture bundled CLI tee completion steps are empty")
	}

	codexCLI, ok := cfg.CLIs["codex-cli"]
	if !ok {
		t.Fatal("fixture missing codex-cli")
	}
	if codexCLI.Command.Use != "codex-cli" {
		t.Fatalf("codex-cli command use = %q", codexCLI.Command.Use)
	}
	if !codexCLI.DaemonDeferral.Enabled {
		t.Fatal("fixture codex-cli daemon deferral is disabled")
	}
	if codexCLI.DaemonDeferral.WorkHoursLoadThresholdPerCPU != 0.30 {
		t.Fatalf("codex-cli work-hours threshold = %v, want 0.30", codexCLI.DaemonDeferral.WorkHoursLoadThresholdPerCPU)
	}
	requireFlagBinding(t, codexCLI.Operations["upgrade"].Flags, "codex-home", "package-home")
	requireFlagBinding(t, codexCLI.Operations["status"].Flags, "codex-home", "package-home")
}

func TestLoadPathRejectsNoApps(t *testing.T) {
	path := writeConfigForTest(t, `
[signing]
identity = "Developer ID Application: Test (TEST123456)"
team_id = "TEST123456"

[clis.fake.command]
use = "fake"
short = "fake"

[clis.fake.operations.status]
use = "status"
short = "status"
capability = "standalone-cli.status"
`)

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), "at least one app must be declared") {
		t.Fatalf("LoadPath should reject missing apps, err=%v", err)
	}
}

func TestLoadPathAcceptsAppsWithoutCLIs(t *testing.T) {
	path := writeConfigForTest(t, `
[signing]
identity = "Developer ID Application: Test (TEST123456)"
team_id = "TEST123456"

[apps.fake]
app_path = "/Applications/Fake.app"
bundle_id = "example.fake"
exec_name = "Fake"

[apps.fake.command]
use = "fake"
short = "fake"

[apps.fake.entitlements]

[apps.fake.updater]
kind = "sparkle_appcast"
url = "https://example.com/appcast.xml"
user_agent = "desktop-via-clyde/upgrade"
sparkle_public_key = "abc"

[apps.fake.launch_policy]
proxy_host = "::1"
proxy_port = 48723
ca_certificate = "/tmp/ca.crt"
no_proxy = "localhost,127.0.0.1,::1,[::1]"
launch_working_directory = "/tmp"

[apps.fake.operations.status]
use = "status"
short = "status"
capability = "app.status"
`)

	cfg, err := config.LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath should accept apps without clis, err=%v", err)
	}
	if _, ok := cfg.Apps["fake"]; !ok {
		t.Fatal("expected fake app in loaded config")
	}
	if len(cfg.CLIs) != 0 {
		t.Fatalf("expected no clis, got %#v", cfg.CLIs)
	}
}

func TestLoadPathRejectsUnknownCapability(t *testing.T) {
	path := writeConfigForTest(t, `
[signing]
identity = "Developer ID Application: Test (TEST123456)"
team_id = "TEST123456"

[apps.fake]
app_path = "/Applications/Fake.app"
bundle_id = "example.fake"
exec_name = "Fake"

[apps.fake.command]
use = "fake"
short = "fake"

[apps.fake.entitlements]

[apps.fake.updater]
kind = "sparkle_appcast"
url = "https://example.com/appcast.xml"
user_agent = "desktop-via-clyde/upgrade"
sparkle_public_key = "abc"

[apps.fake.launch_policy]
proxy_host = "::1"
proxy_port = 48723
ca_certificate = "~/.local/state/clyde/mitm/ca/clyde-mitm-ca.crt"
no_proxy = "localhost,127.0.0.1,::1,[::1]"
launch_working_directory = "~"

[apps.fake.operations.patch]
use = "patch"
short = "patch"
capability = "unknown.capability"

[clis.fake.command]
use = "fake-cli"
short = "fake-cli"

[clis.fake.operations.status]
use = "status"
short = "status"
capability = "standalone-cli.status"
`)

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), `unknown.capability`) {
		t.Fatalf("LoadPath should reject unknown capability, err=%v", err)
	}
}

func TestLoadPathRejectsUnknownBundledCLITeeCapability(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[apps.fake.bundled_cli_tee]
capability = "unknown.hook"
bundled_cli_path = "/tmp/fake-cli"
`))

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), `unknown.hook`) {
		t.Fatalf("LoadPath should reject unknown bundled CLI tee capability, err=%v", err)
	}
}

func TestLoadPathRejectsLaunchPolicyTemplateValues(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[[apps.fake.launch_policy.environment]]
action = "set"
key = "HTTPS_PROXY"
value = "{proxy_url}"
`))

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), `must be fully resolved`) {
		t.Fatalf("LoadPath should reject launch policy template value, err=%v", err)
	}
}

func TestLoadPathAcceptsLocalhostLaunchPolicyHost(t *testing.T) {
	body := strings.Replace(validFakeConfig(""), `proxy_host = "::1"`, `proxy_host = "localhost"`, 1)
	path := writeConfigForTest(t, body)

	cfg, err := config.LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath(%s): %v", path, err)
	}
	if got := cfg.Apps["fake"].LaunchPolicy.ProxyHost; got != "localhost" {
		t.Fatalf("launch policy proxy_host = %q, want localhost", got)
	}
}

func TestLoadPathExpandsConfiguredFlagPathTokens(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	path := writeConfigForTest(t, validFakeConfig(`
[[clis.fake.operations.status.flags]]
name = "source-dir"
type = "string"
usage = "source directory"
default_string = "{cache_root}/declared/source"
expand_path = true
`))

	cfg, err := config.LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath(%s): %v", path, err)
	}
	flags := cfg.CLIs["fake"].Operations["status"].Flags
	if len(flags) != 1 {
		t.Fatalf("status flags = %#v", flags)
	}
	want := filepath.Join(cacheRoot, "clyde", "declared", "source")
	if flags[0].DefaultString != want {
		t.Fatalf("expanded flag default = %q, want %q", flags[0].DefaultString, want)
	}
}

func TestLoadPathDefaultsFlagBindingToName(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[[clis.fake.operations.status.flags]]
name = "mode"
type = "string"
usage = "mode"
default_string = "summary"
`))

	cfg, err := config.LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath(%s): %v", path, err)
	}
	flags := cfg.CLIs["fake"].Operations["status"].Flags
	requireFlagBinding(t, flags, "mode", "mode")
}

func TestLoadPathAcceptsCLIDaemonDeferral(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[clis.fake.daemon_deferral]
enabled = true
load_threshold_per_cpu = 1.0
work_hours_load_threshold_per_cpu = 0.30
work_hours_start = "09:00"
work_hours_end = "17:00"
work_hours_weekdays = ["Monday", " tuesday "]
`))

	cfg, err := config.LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath(%s): %v", path, err)
	}
	deferral := cfg.CLIs["fake"].DaemonDeferral
	if !deferral.Enabled {
		t.Fatal("daemon deferral is disabled")
	}
	if deferral.LoadThresholdPerCPU != 1.0 {
		t.Fatalf("load threshold = %v, want 1.0", deferral.LoadThresholdPerCPU)
	}
	if deferral.WorkHoursLoadThresholdPerCPU != 0.30 {
		t.Fatalf("work-hours load threshold = %v, want 0.30", deferral.WorkHoursLoadThresholdPerCPU)
	}
	if got := strings.Join(deferral.WorkHoursWeekdays, ","); got != "monday,tuesday" {
		t.Fatalf("work-hours weekdays = %q, want monday,tuesday", got)
	}
}

func TestLoadPathRejectsCLIDaemonDeferralThreshold(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[clis.fake.daemon_deferral]
enabled = true
load_threshold_per_cpu = 0
work_hours_load_threshold_per_cpu = 0.30
work_hours_start = "09:00"
work_hours_end = "17:00"
work_hours_weekdays = ["monday"]
`))

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), "load_threshold_per_cpu must be positive") {
		t.Fatalf("LoadPath should reject invalid load threshold, err=%v", err)
	}
}

func TestLoadPathRejectsCLIDaemonDeferralInvertedWorkHoursThreshold(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[clis.fake.daemon_deferral]
enabled = true
load_threshold_per_cpu = 1.0
work_hours_load_threshold_per_cpu = 1.5
work_hours_start = "09:00"
work_hours_end = "17:00"
work_hours_weekdays = ["monday"]
`))

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), "work_hours_load_threshold_per_cpu must be less than or equal to") {
		t.Fatalf("LoadPath should reject inverted work-hours threshold, err=%v", err)
	}
}

func TestLoadPathRejectsCLIDaemonDeferralWeekday(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[clis.fake.daemon_deferral]
enabled = true
load_threshold_per_cpu = 1.0
work_hours_load_threshold_per_cpu = 0.30
work_hours_start = "09:00"
work_hours_end = "17:00"
work_hours_weekdays = ["funday"]
`))

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), `unknown weekday "funday"`) {
		t.Fatalf("LoadPath should reject invalid weekday, err=%v", err)
	}
}

func TestLoadPathRejectsCLIDaemonDeferralTime(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[clis.fake.daemon_deferral]
enabled = true
load_threshold_per_cpu = 1.0
work_hours_load_threshold_per_cpu = 0.30
work_hours_start = "9am"
work_hours_end = "17:00"
work_hours_weekdays = ["monday"]
`))

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), "work_hours_start must use HH:MM") {
		t.Fatalf("LoadPath should reject invalid time, err=%v", err)
	}
}

func TestLoadPathRejectsCLIDaemonDeferralMatchingWorkHoursTimes(t *testing.T) {
	path := writeConfigForTest(t, validFakeConfig(`
[clis.fake.daemon_deferral]
enabled = true
load_threshold_per_cpu = 1.0
work_hours_load_threshold_per_cpu = 0.30
work_hours_start = "09:00"
work_hours_end = "09:00"
work_hours_weekdays = ["monday"]
`))

	_, err := config.LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), "work_hours_start must differ from") {
		t.Fatalf("LoadPath should reject matching work-hours times, err=%v", err)
	}
}

func requireFlagBinding(t *testing.T, flags []spec.FlagSpec, name string, binding string) {
	t.Helper()
	for _, flag := range flags {
		if flag.Name == name {
			if flag.Binding != binding {
				t.Fatalf("flag %q binding = %q, want %q", name, flag.Binding, binding)
			}
			return
		}
	}
	t.Fatalf("missing flag %q in %#v", name, flags)
}

func fixturePath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "testconfig", "testdata", "current-config.toml")
}

func validFakeConfig(extra string) string {
	return `
[signing]
identity = "Developer ID Application: Test (TEST123456)"
team_id = "TEST123456"

[apps.fake]
app_path = "/Applications/Fake.app"
bundle_id = "example.fake"
exec_name = "Fake"

[apps.fake.command]
use = "fake"
short = "fake"

[apps.fake.entitlements]

[apps.fake.updater]
kind = "sparkle_appcast"
url = "https://example.com/appcast.xml"
user_agent = "desktop-via-clyde/upgrade"
sparkle_public_key = "abc"

[apps.fake.launch_policy]
proxy_host = "::1"
proxy_port = 48723
ca_certificate = "/tmp/ca.crt"
no_proxy = "localhost,127.0.0.1,::1,[::1]"
launch_working_directory = "/tmp"

[apps.fake.operations.status]
use = "status"
short = "status"
capability = "app.status"

[clis.fake.command]
use = "fake-cli"
short = "fake-cli"

[clis.fake.operations.status]
use = "status"
short = "status"
capability = "standalone-cli.status"
` + extra
}

func writeConfigForTest(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}
