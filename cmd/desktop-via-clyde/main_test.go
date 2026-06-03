package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/batchops"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/composition"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/gklog/correlation"
)

func TestRootHelpListsTargetCommands(t *testing.T) {
	output, err := executeRoot(t, "--help")
	if err != nil {
		t.Fatalf("executeRoot(--help): %v", err)
	}

	required := []string{"patch", "upgrade", "cursor", "codex", "claude", "codex-cli", "status"}
	for _, want := range required {
		if !strings.Contains(output, want) {
			t.Fatalf("root help missing %q\noutput:\n%s", want, output)
		}
	}

	forbidden := []string{"unpatch", "keychain-migrate", strings.Join([]string{"mitm", "hook"}, "-")}
	for _, want := range forbidden {
		if strings.Contains(output, "\n  "+want+" ") {
			t.Fatalf("root help unexpectedly lists %q\noutput:\n%s", want, output)
		}
	}
}

func TestTargetHelpListsOperations(t *testing.T) {
	output, err := executeRoot(t, "codex", "--help")
	if err != nil {
		t.Fatalf("executeRoot(codex --help): %v", err)
	}

	required := []string{"patch", "upgrade", "keychain-migrate", "status"}
	for _, want := range required {
		if !strings.Contains(output, want) {
			t.Fatalf("target help missing %q\noutput:\n%s", want, output)
		}
	}
}

func TestCursorUpgradeHelpShowsDevChannelDefault(t *testing.T) {
	output, err := executeRoot(t, "cursor", "upgrade", "--help")
	if err != nil {
		t.Fatalf("executeRoot(cursor upgrade --help): %v", err)
	}
	if !strings.Contains(output, "--channel string") {
		t.Fatalf("cursor upgrade help missing --channel\noutput:\n%s", output)
	}
	if !strings.Contains(output, `default "dev"`) {
		t.Fatalf("cursor upgrade help missing dev default\noutput:\n%s", output)
	}
}

func TestCodexUpgradeHelpShowsBetaChannelDefault(t *testing.T) {
	output, err := executeRoot(t, "codex", "upgrade", "--help")
	if err != nil {
		t.Fatalf("executeRoot(codex upgrade --help): %v", err)
	}
	if !strings.Contains(output, "--channel string") {
		t.Fatalf("codex upgrade help missing --channel\noutput:\n%s", output)
	}
	if !strings.Contains(output, `default "beta"`) {
		t.Fatalf("codex upgrade help missing beta default\noutput:\n%s", output)
	}
}

func TestClaudeUpgradeHelpOmitsChannelFlag(t *testing.T) {
	output, err := executeRoot(t, "claude", "upgrade", "--help")
	if err != nil {
		t.Fatalf("executeRoot(claude upgrade --help): %v", err)
	}
	if strings.Contains(output, "--channel") {
		t.Fatalf("claude upgrade help unexpectedly lists --channel\noutput:\n%s", output)
	}
}

func TestBatchAllHelpListsSharedFlags(t *testing.T) {
	output, err := executeRoot(t, "upgrade", "all", "--help")
	if err != nil {
		t.Fatalf("executeRoot(upgrade all --help): %v", err)
	}
	for _, want := range []string{"--output-format string", "--parallel int", "--target stringArray", "--set stringArray", "--migrate-keychain"} {
		if !strings.Contains(output, want) {
			t.Fatalf("upgrade all help missing %q\noutput:\n%s", want, output)
		}
	}
	if strings.Contains(output, "--no-migrate-keychain") {
		t.Fatalf("upgrade all help unexpectedly lists --no-migrate-keychain\noutput:\n%s", output)
	}
}

func TestRootHelpUsesResponseWrapper(t *testing.T) {
	output, err := executeRoot(t, "--help")
	if err != nil {
		t.Fatalf("executeRoot(--help): %v", err)
	}
	if !strings.HasPrefix(output, "trace_id=") {
		t.Fatalf("root help missing response header\noutput:\n%s", output)
	}
	if !strings.Contains(output, "--output-format string") {
		t.Fatalf("root help missing inherited output format flag\noutput:\n%s", output)
	}
}

func TestClaudeHelpDoesNotListBundledCLITeeEntrypoint(t *testing.T) {
	output, err := executeRoot(t, "claude", "--help")
	if err != nil {
		t.Fatalf("executeRoot(claude --help): %v", err)
	}
	if strings.Contains(output, "bundled-cli-tee") {
		t.Fatalf("claude help unexpectedly lists bundled-cli-tee\noutput:\n%s", output)
	}
}

func TestClaudeBundledCLITeeEntrypointReturnsError(t *testing.T) {
	output, err := executeRoot(t, "claude", "bundled-cli-tee")
	if err == nil {
		t.Fatalf("executeRoot(claude bundled-cli-tee) unexpectedly succeeded\noutput:\n%s", output)
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("executeRoot(claude bundled-cli-tee) error = %q, want unknown command", err.Error())
	}
}

func TestOperationHelpCommandsSucceed(t *testing.T) {
	for _, args := range [][]string{
		{"patch", "--help"},
		{"patch", "all", "--help"},
		{"upgrade", "--help"},
		{"upgrade", "all", "--help"},
		{"cursor", "upgrade", "--help"},
		{"codex", "patch", "--help"},
		{"codex", "upgrade", "--help"},
		{"codex", "keychain-migrate", "--help"},
		{"codex", "status", "--help"},
		{"claude", "upgrade", "--help"},
		{"codex-cli", "upgrade", "--help"},
		{"codex-cli", "install", "--help"},
		{"codex-cli", "status", "--help"},
	} {
		if _, err := executeRoot(t, args...); err != nil {
			t.Fatalf("executeRoot(%v): %v", args, err)
		}
	}
}

func TestCodexCLIHelpListsUpgradeWithInstallAlias(t *testing.T) {
	output, err := executeRoot(t, "codex-cli", "--help")
	if err != nil {
		t.Fatalf("executeRoot(codex-cli --help): %v", err)
	}
	if !strings.Contains(output, "upgrade") {
		t.Fatalf("codex-cli help missing upgrade verb\noutput:\n%s", output)
	}
	if !strings.Contains(output, "status") {
		t.Fatalf("codex-cli help missing status verb\noutput:\n%s", output)
	}
}

func TestCodexCLIUpgradeHelpAdvertisesLocalFastDefault(t *testing.T) {
	output, err := executeRoot(t, "codex-cli", "upgrade", "--help")
	if err != nil {
		t.Fatalf("executeRoot(codex-cli upgrade --help): %v", err)
	}
	if !strings.Contains(output, "--build-mode") {
		t.Fatalf("codex-cli upgrade help missing --build-mode flag\noutput:\n%s", output)
	}
	if !strings.Contains(output, "local-fast") {
		t.Fatalf("codex-cli upgrade help should advertise local-fast as default\noutput:\n%s", output)
	}
}

func TestVerbFirstCommandsReturnError(t *testing.T) {
	for _, args := range [][]string{{"patch", "codex"}, {"upgrade", "codex"}, {"keychain-migrate", "codex"}} {
		output, err := executeRoot(t, args...)
		if err == nil {
			t.Fatalf("executeRoot(%v) unexpectedly succeeded\noutput:\n%s", args, output)
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("executeRoot(%v) error = %q, want unknown command", args, err.Error())
		}
	}
}

func TestRootHelpRendersConfiguredFakeCommands(t *testing.T) {
	config.SetCurrent(&spec.Config{
		Signing: spec.SigningSpec{Identity: "Test Identity", TeamID: "TEST123456"},
		Apps: map[string]spec.AppSpec{
			"alpha": {
				ID:       "alpha",
				AppPath:  "/Applications/Alpha.app",
				BundleID: "example.alpha",
				ExecName: "Alpha",
				Command:  spec.CommandSpec{Use: "alpha-app", Short: "Operate on alpha"},
				Operations: map[string]spec.OperationSpec{
					"status": {
						ID:         "status",
						Use:        "check",
						Short:      "Check alpha",
						Capability: "app.status",
						Flags: []spec.FlagSpec{
							{Name: "scope", Type: "string", Usage: "check scope"},
						},
					},
				},
			},
		},
		CLIs: map[string]spec.CLISpec{
			"beta": {
				ID:      "beta",
				Command: spec.CommandSpec{Use: "beta-tool", Short: "Operate on beta"},
				Operations: map[string]spec.OperationSpec{
					"inspect": {
						ID:         "inspect",
						Use:        "inspect",
						Aliases:    []string{"see"},
						Short:      "Inspect beta",
						Capability: "standalone-cli.status",
					},
				},
			},
		},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })

	output, err := executeRootNoFixture("beta-tool", "see", "--help")
	if err != nil {
		t.Fatalf("executeRootNoFixture(beta-tool see --help): %v", err)
	}
	assertContainsOutput(t, output, "beta-tool")
	assertContainsOutput(t, output, "inspect")

	rootOutput, err := executeRootNoFixture("--help")
	if err != nil {
		t.Fatalf("executeRootNoFixture(--help): %v", err)
	}
	assertContainsOutput(t, rootOutput, "patch")
	assertContainsOutput(t, rootOutput, "upgrade")
	assertContainsOutput(t, rootOutput, "alpha-app")
	assertContainsOutput(t, rootOutput, "beta-tool")
}

func TestFakeOperationHelpShowsConfiguredFlag(t *testing.T) {
	config.SetCurrent(&spec.Config{
		Signing: spec.SigningSpec{Identity: "Test Identity", TeamID: "TEST123456"},
		Apps: map[string]spec.AppSpec{
			"alpha": {
				ID:       "alpha",
				AppPath:  "/Applications/Alpha.app",
				BundleID: "example.alpha",
				ExecName: "Alpha",
				Command:  spec.CommandSpec{Use: "alpha-app", Short: "Operate on alpha"},
				Operations: map[string]spec.OperationSpec{
					"status": {
						ID:         "status",
						Use:        "check",
						Short:      "Check alpha",
						Capability: "app.status",
						Flags: []spec.FlagSpec{
							{Name: "scope", Type: "string", Usage: "check scope"},
						},
					},
				},
			},
		},
		CLIs: map[string]spec.CLISpec{
			"beta": {
				ID:         "beta",
				Command:    spec.CommandSpec{Use: "beta-tool", Short: "Operate on beta"},
				Operations: map[string]spec.OperationSpec{},
			},
		},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })

	output, err := executeRootNoFixture("alpha-app", "check", "--help")
	if err != nil {
		t.Fatalf("executeRootNoFixture(alpha-app check --help): %v", err)
	}
	assertContainsOutput(t, output, "--scope string")
}

func TestFakeAppOperationDispatchUsesConfiguredCapabilityAndFlags(t *testing.T) {
	falseValue := false
	config.SetCurrent(&spec.Config{
		Signing: spec.SigningSpec{Identity: "Test Identity", TeamID: "TEST123456"},
		Apps: map[string]spec.AppSpec{
			"alpha": {
				ID:       "alpha",
				AppPath:  "/Applications/Alpha.app",
				BundleID: "example.alpha",
				ExecName: "Alpha",
				Command: spec.CommandSpec{
					Use:     "alpha-app",
					Aliases: []string{"aa"},
					Short:   "Operate on alpha",
				},
				Operations: map[string]spec.OperationSpec{
					"status": {
						ID:         "status",
						Use:        "check",
						Aliases:    []string{"inspect"},
						Short:      "Check alpha",
						Capability: "app.status",
						Flags: []spec.FlagSpec{
							{Name: "app-path", Type: "string", Usage: "app path"},
							{Name: "scope", Type: "string", Usage: "check scope", DefaultString: "brief"},
							{Name: "dry-run", Type: "bool", Usage: "dry run", DefaultBool: &falseValue},
						},
					},
				},
			},
		},
		CLIs: map[string]spec.CLISpec{},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "aa", "inspect", "--app-path", "/tmp/Alpha.app", "--scope", "full", "--dry-run")
	if err != nil {
		t.Fatalf("executeRootWithRunner(fake app dispatch): %v", err)
	}
	if got.Capability != "app.status" {
		t.Fatalf("capability = %q, want app.status", got.Capability)
	}
	if got.App == nil || got.App.ID != "alpha" || got.App.AppPath != "/tmp/Alpha.app" {
		t.Fatalf("app request = %#v", got.App)
	}
	if got.CLI != nil {
		t.Fatalf("CLI request = %#v, want nil", got.CLI)
	}
	if got.Flags.String("scope") != "full" {
		t.Fatalf("scope flag = %q, want full", got.Flags.String("scope"))
	}
	if !got.Flags.Bool("dry-run") {
		t.Fatal("dry-run flag = false, want true")
	}
}

func TestFakeCLIOperationDispatchUsesConfiguredAliasAndFlags(t *testing.T) {
	config.SetCurrent(&spec.Config{
		Signing: spec.SigningSpec{Identity: "Test Identity", TeamID: "TEST123456"},
		Apps:    map[string]spec.AppSpec{},
		CLIs: map[string]spec.CLISpec{
			"beta": {
				ID:      "beta",
				Command: spec.CommandSpec{Use: "beta-tool", Short: "Operate on beta"},
				Operations: map[string]spec.OperationSpec{
					"status": {
						ID:         "status",
						Use:        "inspect",
						Aliases:    []string{"see"},
						Short:      "Inspect beta",
						Capability: "standalone-cli.status",
						Flags: []spec.FlagSpec{
							{Name: "visible-mode", Binding: "mode", Type: "string", Usage: "inspection mode", DefaultString: "summary"},
						},
					},
				},
			},
		},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "beta-tool", "see", "--visible-mode", "detail")
	if err != nil {
		t.Fatalf("executeRootWithRunner(fake CLI dispatch): %v", err)
	}
	if got.Capability != "standalone-cli.status" {
		t.Fatalf("capability = %q, want standalone-cli.status", got.Capability)
	}
	if got.App != nil {
		t.Fatalf("app request = %#v, want nil", got.App)
	}
	if got.CLI == nil || got.CLI.ID != "beta" {
		t.Fatalf("CLI request = %#v", got.CLI)
	}
	if got.Flags.String("mode") != "detail" {
		t.Fatalf("mode flag = %q, want detail", got.Flags.String("mode"))
	}
	if got.Flags.String("visible-mode") != "detail" {
		t.Fatalf("visible-mode flag = %q, want detail", got.Flags.String("visible-mode"))
	}
}

func TestAppPatchDispatchDefaultsMigrateKeychainFalse(t *testing.T) {
	installFixture(t)

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "codex", "patch", "--dry-run")
	if err != nil {
		t.Fatalf("executeRootWithRunner(codex patch): %v", err)
	}
	if got.Capability != "app.patch" {
		t.Fatalf("capability = %q, want app.patch", got.Capability)
	}
	if got.App == nil || got.App.ID != "codex" {
		t.Fatalf("app request = %#v", got.App)
	}
	if got.Flags.Bool("migrate-keychain") {
		t.Fatal("migrate-keychain = true, want false")
	}
}

func TestAppPatchDispatchReadsMigrateKeychainFlag(t *testing.T) {
	installFixture(t)

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "codex", "patch", "--dry-run", "--migrate-keychain")
	if err != nil {
		t.Fatalf("executeRootWithRunner(codex patch --migrate-keychain): %v", err)
	}
	if !got.Flags.Bool("migrate-keychain") {
		t.Fatal("migrate-keychain = false, want true")
	}
}

func TestAppUpgradeDispatchDefaultsMigrateKeychainFalse(t *testing.T) {
	installFixture(t)

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "codex", "upgrade", "--dry-run")
	if err != nil {
		t.Fatalf("executeRootWithRunner(codex upgrade): %v", err)
	}
	if got.Capability != "app.upgrade" {
		t.Fatalf("capability = %q, want app.upgrade", got.Capability)
	}
	if got.App == nil || got.App.ID != "codex" {
		t.Fatalf("app request = %#v", got.App)
	}
	if got.Flags.Bool("migrate-keychain") {
		t.Fatal("migrate-keychain = true, want false")
	}
}

func TestAppUpgradeDispatchReadsMigrateKeychainFlag(t *testing.T) {
	installFixture(t)

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "codex", "upgrade", "--dry-run", "--migrate-keychain")
	if err != nil {
		t.Fatalf("executeRootWithRunner(codex upgrade --migrate-keychain): %v", err)
	}
	if !got.Flags.Bool("migrate-keychain") {
		t.Fatal("migrate-keychain = false, want true")
	}
}

func TestPatchAllDryRunDispatchesBatchRequest(t *testing.T) {
	var got batchops.Request
	output, err := executeRootWithBatchRunner(t, func(_ context.Context, req batchops.Request) error {
		got = req
		return nil
	}, "patch", "all", "--dry-run", "--migrate-keychain", "--parallel", "2", "--target", "cursor", "--set", "cursor.app-path=/tmp/Cursor.app")
	if err != nil {
		t.Fatalf("executeRootWithBatchRunner(patch all): %v\noutput:\n%s", err, output)
	}
	if got.Operation != batchops.OperationPatch {
		t.Fatalf("operation = %q, want %q", got.Operation, batchops.OperationPatch)
	}
	if !got.DryRun {
		t.Fatal("dry-run = false, want true")
	}
	if !got.MigrateKeychain {
		t.Fatal("migrate-keychain = false, want true")
	}
	if got.Parallel != 2 {
		t.Fatalf("parallel = %d, want 2", got.Parallel)
	}
	if len(got.Targets) != 1 || got.Targets[0] != "cursor" {
		t.Fatalf("targets = %#v", got.Targets)
	}
	if len(got.Sets) != 1 || got.Sets[0] != "cursor.app-path=/tmp/Cursor.app" {
		t.Fatalf("sets = %#v", got.Sets)
	}
	if got.Format != clioutput.FormatText {
		t.Fatalf("format = %q, want %q", got.Format, clioutput.FormatText)
	}
}

func TestUpgradeAllDryRunDispatchesBatchRequest(t *testing.T) {
	var got batchops.Request
	output, err := executeRootWithBatchRunner(t, func(_ context.Context, req batchops.Request) error {
		got = req
		return nil
	}, "upgrade", "all", "--dry-run", "--target", "cursor", "--target", "codex-cli", "--set", "cursor.channel=stable", "--set", "codex-cli.codex-home=/tmp/codex-home")
	if err != nil {
		t.Fatalf("executeRootWithBatchRunner(upgrade all): %v\noutput:\n%s", err, output)
	}
	if got.Operation != batchops.OperationUpgrade {
		t.Fatalf("operation = %q, want %q", got.Operation, batchops.OperationUpgrade)
	}
	if !got.DryRun {
		t.Fatal("dry-run = false, want true")
	}
	if got.MigrateKeychain {
		t.Fatal("migrate-keychain = true, want false")
	}
	if want := []string{"cursor", "codex-cli"}; strings.Join(got.Targets, ",") != strings.Join(want, ",") {
		t.Fatalf("targets = %#v, want %#v", got.Targets, want)
	}
	if want := []string{"cursor.channel=stable", "codex-cli.codex-home=/tmp/codex-home"}; strings.Join(got.Sets, ",") != strings.Join(want, ",") {
		t.Fatalf("sets = %#v, want %#v", got.Sets, want)
	}
}

func TestBatchRunnerErrorReturnsNonZero(t *testing.T) {
	output, err := executeRootWithBatchRunner(t, func(_ context.Context, _ batchops.Request) error {
		return errors.New("boom")
	}, "patch", "all", "--dry-run")
	if err == nil {
		t.Fatalf("executeRootWithBatchRunner(patch all) unexpectedly succeeded\noutput:\n%s", output)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %q, want boom", err.Error())
	}
	if !strings.HasPrefix(output, "trace_id=") {
		t.Fatalf("output missing leading trace header\noutput:\n%s", output)
	}
	if strings.Count(output, "trace_id=") != 1 {
		t.Fatalf("output trace header count = %d, want 1\noutput:\n%s", strings.Count(output, "trace_id="), output)
	}
}

func TestRuntimeTextErrorOmitsTrailingTraceHeader(t *testing.T) {
	ctx, _ := correlation.Ensure(context.Background(), "")
	var out bytes.Buffer
	writeRuntimeMessage(ctx, &out, clioutput.FormatText, "error: boom")
	output := out.String()
	if output != "error: boom\n" {
		t.Fatalf("runtime text output = %q, want plain error", output)
	}
}

func TestRootStatusJSONEmitsTypedPayload(t *testing.T) {
	output, err := executeRoot(t, "status", "--output-format", "json")
	if err != nil {
		t.Fatalf("executeRoot(status json): %v", err)
	}
	var payload struct {
		Meta struct {
			TraceID string `json:"trace_id"`
			SpanID  string `json:"span_id"`
		} `json:"_meta"`
		StateFile string `json:"state_file"`
		Targets   []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"targets"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("unmarshal status json: %v\noutput:\n%s", err, output)
	}
	if payload.Meta.TraceID == "" || payload.Meta.SpanID == "" {
		t.Fatalf("missing metadata in status json: %#v", payload.Meta)
	}
	if payload.StateFile == "" || len(payload.Targets) == 0 {
		t.Fatalf("incomplete status payload: %#v", payload)
	}
}

func TestPatchAllDryRunJSONEmitsProgressAndSummary(t *testing.T) {
	output, err := executeRoot(t, "patch", "all", "--dry-run", "--target", "cursor", "--output-format", "json")
	if err != nil {
		t.Fatalf("executeRoot(patch all json): %v\noutput:\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected progress and summary lines\noutput:\n%s", output)
	}
	var progress map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &progress); err != nil {
		t.Fatalf("unmarshal progress line: %v\nline:\n%s", err, lines[0])
	}
	if progress["type"] != "run_started" {
		t.Fatalf("progress type = %#v", progress["type"])
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		t.Fatalf("unmarshal summary line: %v\nline:\n%s", err, lines[len(lines)-1])
	}
	if summary["type"] != "run_done" {
		t.Fatalf("summary type = %#v", summary["type"])
	}
}

func executeRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	installFixture(t)
	return executeRootNoFixture(args...)
}

func executeRootNoFixture(args ...string) (string, error) {
	return executeRootNoFixtureWithRunners(operations.Run, batchops.Run, args...)
}

func executeRootWithRunner(runner operationRunner, args ...string) (string, error) {
	return executeRootNoFixtureWithRunners(runner, func(_ context.Context, _ batchops.Request) error { return nil }, args...)
}

func executeRootWithBatchRunner(t *testing.T, runner batchOperationRunner, args ...string) (string, error) {
	t.Helper()
	installFixture(t)
	return executeRootNoFixtureWithRunners(func(_ context.Context, _ operations.Request) error { return nil }, runner, args...)
}

func executeRootNoFixtureWithRunners(
	runner operationRunner,
	batchRunner batchOperationRunner,
	args ...string,
) (string, error) {
	var out bytes.Buffer
	cmd := newRootCmdWithRunners(context.Background(), &out, &out, runner, batchRunner)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func assertContainsOutput(t *testing.T, output string, want string) {
	t.Helper()
	if !strings.Contains(output, want) {
		t.Fatalf("output missing %q\noutput:\n%s", want, output)
	}
}

func installFixture(t *testing.T) {
	t.Helper()
	if err := composition.Register(); err != nil {
		t.Fatalf("composition.Register: %v", err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfg, err := config.LoadPath(filepath.Join("..", "..", "internal", "testconfig", "testdata", "current-config.toml"))
	if err != nil {
		t.Fatalf("LoadPath(current-config.toml): %v", err)
	}
	for id, app := range cfg.Apps {
		app.AppPath = filepath.Join(t.TempDir(), filepath.Base(app.AppPath))
		cfg.Apps[id] = app
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() {
		config.SetCurrent(nil)
	})
}
