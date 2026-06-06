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

func TestRootHelpListsVerbCommands(t *testing.T) {
	output, err := executeRoot(t, "--help")
	if err != nil {
		t.Fatalf("executeRoot(--help): %v", err)
	}

	required := []string{"patch", "upgrade", "hard-reset", "keychain-migrate", "status", "provision"}
	for _, want := range required {
		if !strings.Contains(output, "\n  "+want+" ") {
			t.Fatalf("root help missing verb %q\noutput:\n%s", want, output)
		}
	}

	forbidden := []string{"cursor", "codex", "claude", "codex-cli"}
	for _, want := range forbidden {
		if strings.Contains(output, "\n  "+want+" ") {
			t.Fatalf("root help unexpectedly lists noun %q at top level\noutput:\n%s", want, output)
		}
	}
}

func TestVerbHelpListsTargetNouns(t *testing.T) {
	patchOutput, err := executeRoot(t, "patch", "--help")
	if err != nil {
		t.Fatalf("executeRoot(patch --help): %v", err)
	}
	for _, want := range []string{"all", "cursor", "codex", "claude"} {
		if !strings.Contains(patchOutput, "\n  "+want+" ") {
			t.Fatalf("patch help missing noun %q\noutput:\n%s", want, patchOutput)
		}
	}

	upgradeOutput, err := executeRoot(t, "upgrade", "--help")
	if err != nil {
		t.Fatalf("executeRoot(upgrade --help): %v", err)
	}
	if !strings.Contains(upgradeOutput, "\n  codex-cli ") {
		t.Fatalf("upgrade help missing codex-cli noun\noutput:\n%s", upgradeOutput)
	}

	statusOutput, err := executeRoot(t, "status", "--help")
	if err != nil {
		t.Fatalf("executeRoot(status --help): %v", err)
	}
	for _, want := range []string{"all", "codex", "codex-cli"} {
		if !strings.Contains(statusOutput, "\n  "+want+" ") {
			t.Fatalf("status help missing noun %q\noutput:\n%s", want, statusOutput)
		}
	}
}

func TestCursorUpgradeHelpShowsDevChannelDefault(t *testing.T) {
	output, err := executeRoot(t, "upgrade", "cursor", "--help")
	if err != nil {
		t.Fatalf("executeRoot(upgrade cursor --help): %v", err)
	}
	if !strings.Contains(output, "--channel string") {
		t.Fatalf("cursor upgrade help missing --channel\noutput:\n%s", output)
	}
	if !strings.Contains(output, `default "dev"`) {
		t.Fatalf("cursor upgrade help missing dev default\noutput:\n%s", output)
	}
}

func TestCodexUpgradeHelpShowsBetaChannelDefault(t *testing.T) {
	output, err := executeRoot(t, "upgrade", "codex", "--help")
	if err != nil {
		t.Fatalf("executeRoot(upgrade codex --help): %v", err)
	}
	if !strings.Contains(output, "--channel string") {
		t.Fatalf("codex upgrade help missing --channel\noutput:\n%s", output)
	}
	if !strings.Contains(output, `default "beta"`) {
		t.Fatalf("codex upgrade help missing beta default\noutput:\n%s", output)
	}
}

func TestClaudeUpgradeHelpOmitsChannelFlag(t *testing.T) {
	output, err := executeRoot(t, "upgrade", "claude", "--help")
	if err != nil {
		t.Fatalf("executeRoot(upgrade claude --help): %v", err)
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
	if count := strings.Count(output, "trace_id="); count != 1 {
		t.Fatalf("root help trace header count = %d, want 1\noutput:\n%s", count, output)
	}
	if !strings.Contains(output, "--output-format string") {
		t.Fatalf("root help missing inherited output format flag\noutput:\n%s", output)
	}
}

func TestVerbHelpEmitsSingleTraceHeader(t *testing.T) {
	for _, args := range [][]string{{"patch", "--help"}, {"status", "--help"}, {"upgrade", "codex", "--help"}} {
		output, err := executeRoot(t, args...)
		if err != nil {
			t.Fatalf("executeRoot(%v): %v", args, err)
		}
		if count := strings.Count(output, "trace_id="); count != 1 {
			t.Fatalf("executeRoot(%v) trace header count = %d, want 1\noutput:\n%s", args, count, output)
		}
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
		{"hard-reset", "--help"},
		{"hard-reset", "all", "--help"},
		{"keychain-migrate", "--help"},
		{"status", "--help"},
		{"status", "all", "--help"},
		{"upgrade", "cursor", "--help"},
		{"patch", "codex", "--help"},
		{"upgrade", "codex", "--help"},
		{"keychain-migrate", "codex", "--help"},
		{"hard-reset", "codex", "--help"},
		{"status", "codex", "--help"},
		{"upgrade", "claude", "--help"},
		{"upgrade", "codex-cli", "--help"},
		{"status", "codex-cli", "--help"},
		{"provision", "--help"},
		{"provision", "profile", "--help"},
	} {
		if _, err := executeRoot(t, args...); err != nil {
			t.Fatalf("executeRoot(%v): %v", args, err)
		}
	}
}

func TestUpgradeAndStatusVerbHelpListCodexCLINoun(t *testing.T) {
	upgradeOutput, err := executeRoot(t, "upgrade", "--help")
	if err != nil {
		t.Fatalf("executeRoot(upgrade --help): %v", err)
	}
	if !strings.Contains(upgradeOutput, "\n  codex-cli ") {
		t.Fatalf("upgrade help missing codex-cli noun\noutput:\n%s", upgradeOutput)
	}
	statusOutput, err := executeRoot(t, "status", "--help")
	if err != nil {
		t.Fatalf("executeRoot(status --help): %v", err)
	}
	if !strings.Contains(statusOutput, "\n  codex-cli ") {
		t.Fatalf("status help missing codex-cli noun\noutput:\n%s", statusOutput)
	}
}

func TestCodexCLIUpgradeHelpAdvertisesLocalFastDefault(t *testing.T) {
	output, err := executeRoot(t, "upgrade", "codex-cli", "--help")
	if err != nil {
		t.Fatalf("executeRoot(upgrade codex-cli --help): %v", err)
	}
	if !strings.Contains(output, "--build-mode") {
		t.Fatalf("upgrade codex-cli help missing --build-mode flag\noutput:\n%s", output)
	}
	if !strings.Contains(output, "local-fast") {
		t.Fatalf("upgrade codex-cli help should advertise local-fast as default\noutput:\n%s", output)
	}
}

func TestNounFirstCommandsReturnError(t *testing.T) {
	for _, args := range [][]string{
		{"codex", "patch"},
		{"codex", "upgrade"},
		{"codex", "keychain-migrate"},
		{"codex", "status"},
		{"cursor", "patch"},
		{"codex-cli", "upgrade"},
		{"codex-cli", "install"},
		{"codex-cli", "status"},
	} {
		output, err := executeRoot(t, args...)
		if err == nil {
			t.Fatalf("executeRoot(%v) unexpectedly succeeded\noutput:\n%s", args, output)
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("executeRoot(%v) error = %q, want unknown command", args, err.Error())
		}
	}
}

func TestVerbFirstHardResetDispatchReadsDryRunFlag(t *testing.T) {
	installFixture(t)

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "hard-reset", "codex", "--dry-run")
	if err != nil {
		t.Fatalf("executeRootWithRunner(hard-reset codex): %v", err)
	}
	if got.Capability != "app.hard-reset" {
		t.Fatalf("capability = %q, want app.hard-reset", got.Capability)
	}
	if got.App == nil || got.App.ID != "codex" {
		t.Fatalf("app request = %#v", got.App)
	}
	if !got.Flags.Bool("dry-run") {
		t.Fatal("dry-run = false, want true")
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

	output, err := executeRootNoFixture("inspect", "beta-tool", "--help")
	if err != nil {
		t.Fatalf("executeRootNoFixture(inspect beta-tool --help): %v", err)
	}
	assertContainsOutput(t, output, "beta-tool")
	assertContainsOutput(t, output, "inspect")

	rootOutput, err := executeRootNoFixture("--help")
	if err != nil {
		t.Fatalf("executeRootNoFixture(--help): %v", err)
	}
	assertContainsOutput(t, rootOutput, "\n  check ")
	assertContainsOutput(t, rootOutput, "\n  inspect ")
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

	output, err := executeRootNoFixture("check", "alpha-app", "--help")
	if err != nil {
		t.Fatalf("executeRootNoFixture(check alpha-app --help): %v", err)
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
	}, "check", "aa", "--app-path", "/tmp/Alpha.app", "--scope", "full", "--dry-run")
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
	}, "inspect", "beta-tool", "--visible-mode", "detail")
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
	}, "patch", "codex", "--dry-run")
	if err != nil {
		t.Fatalf("executeRootWithRunner(patch codex): %v", err)
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

func TestAppHardResetDispatchReadsDryRunFlag(t *testing.T) {
	installFixture(t)

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "hard-reset", "codex", "--dry-run")
	if err != nil {
		t.Fatalf("executeRootWithRunner(hard-reset codex): %v", err)
	}
	if got.Capability != "app.hard-reset" {
		t.Fatalf("capability = %q, want app.hard-reset", got.Capability)
	}
	if got.App == nil || got.App.ID != "codex" {
		t.Fatalf("app request = %#v", got.App)
	}
	if !got.Flags.Bool("dry-run") {
		t.Fatal("dry-run = false, want true")
	}
}

func TestAppPatchDispatchReadsMigrateKeychainFlag(t *testing.T) {
	installFixture(t)

	var got operations.Request
	_, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "patch", "codex", "--dry-run", "--migrate-keychain")
	if err != nil {
		t.Fatalf("executeRootWithRunner(patch codex --migrate-keychain): %v", err)
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
	}, "upgrade", "codex", "--dry-run")
	if err != nil {
		t.Fatalf("executeRootWithRunner(upgrade codex): %v", err)
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
	}, "upgrade", "codex", "--dry-run", "--migrate-keychain")
	if err != nil {
		t.Fatalf("executeRootWithRunner(upgrade codex --migrate-keychain): %v", err)
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

func TestHardResetAllDryRunDispatchesBatchRequest(t *testing.T) {
	var got batchops.Request
	output, err := executeRootWithBatchRunner(t, func(_ context.Context, req batchops.Request) error {
		got = req
		return nil
	}, "hard-reset", "all", "--dry-run", "--target", "codex")
	if err != nil {
		t.Fatalf("executeRootWithBatchRunner(hard-reset all): %v\noutput:\n%s", err, output)
	}
	if got.Operation != batchops.OperationHardReset {
		t.Fatalf("operation = %q, want %q", got.Operation, batchops.OperationHardReset)
	}
	if !got.DryRun {
		t.Fatal("dry-run = false, want true")
	}
	if len(got.Targets) != 1 || got.Targets[0] != "codex" {
		t.Fatalf("targets = %#v", got.Targets)
	}
}

func TestStatusAllRendersAggregateReport(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"status", "all"}} {
		output, err := executeRoot(t, args...)
		if err != nil {
			t.Fatalf("executeRoot(%v): %v\noutput:\n%s", args, err, output)
		}
		if !strings.Contains(output, "state file:") {
			t.Fatalf("executeRoot(%v) missing aggregate state file line\noutput:\n%s", args, output)
		}
		for _, want := range []string{"cursor", "codex", "claude"} {
			if !strings.Contains(output, want) {
				t.Fatalf("executeRoot(%v) aggregate report missing %q\noutput:\n%s", args, want, output)
			}
		}
		if count := strings.Count(output, "trace_id="); count != 1 {
			t.Fatalf("executeRoot(%v) trace header count = %d, want 1\noutput:\n%s", args, count, output)
		}
	}
}

func TestStatusAggregateEmitsSingleTraceHeaderViaExecuteContext(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"status", "all"}} {
		installFixture(t)
		var out bytes.Buffer
		base, _ := correlation.Ensure(context.Background(), "")
		root := newRootCmdWithRunners(base, &out, &out, operations.Run, func(_ context.Context, _ batchops.Request) error { return nil })
		root.SetArgs(args)
		if err := root.ExecuteContext(root.Context()); err != nil {
			t.Fatalf("ExecuteContext(%v): %v\noutput:\n%s", args, err, out.String())
		}
		output := out.String()
		if count := strings.Count(output, "trace_id="); count != 1 {
			t.Fatalf("ExecuteContext(%v) trace header count = %d, want 1\noutput:\n%s", args, count, output)
		}
	}
}

func TestStatusTargetDispatchesPerTargetOperation(t *testing.T) {
	installFixture(t)

	var got operations.Request
	output, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "status", "codex")
	if err != nil {
		t.Fatalf("executeRootWithRunner(status codex): %v\noutput:\n%s", err, output)
	}
	if got.Capability != "app.status" {
		t.Fatalf("capability = %q, want app.status", got.Capability)
	}
	if got.App == nil || got.App.ID != "codex" {
		t.Fatalf("app request = %#v", got.App)
	}
	if got.CLI != nil {
		t.Fatalf("CLI request = %#v, want nil", got.CLI)
	}
	if count := strings.Count(output, "trace_id="); count > 1 {
		t.Fatalf("status codex trace header count = %d, want at most 1\noutput:\n%s", count, output)
	}
}

func TestStatusCodexCLIDispatchesStandaloneStatus(t *testing.T) {
	installFixture(t)

	var got operations.Request
	output, err := executeRootWithRunner(func(_ context.Context, req operations.Request) error {
		got = req
		return nil
	}, "status", "codex-cli")
	if err != nil {
		t.Fatalf("executeRootWithRunner(status codex-cli): %v\noutput:\n%s", err, output)
	}
	if got.Capability != "standalone-cli.status" {
		t.Fatalf("capability = %q, want standalone-cli.status", got.Capability)
	}
	if got.CLI == nil || got.CLI.ID != "codex-cli" {
		t.Fatalf("CLI request = %#v", got.CLI)
	}
	if got.App != nil {
		t.Fatalf("app request = %#v, want nil", got.App)
	}
}

func TestOperationErrorEmitsSingleTraceHeader(t *testing.T) {
	installFixture(t)

	output, err := executeRootWithRunner(func(_ context.Context, _ operations.Request) error {
		return errors.New("boom")
	}, "patch", "codex", "--dry-run")
	if err == nil {
		t.Fatalf("executeRootWithRunner(patch codex) unexpectedly succeeded\noutput:\n%s", output)
	}
	if !strings.HasPrefix(output, "trace_id=") {
		t.Fatalf("operation error output missing leading trace header\noutput:\n%s", output)
	}
	if count := strings.Count(output, "trace_id="); count != 1 {
		t.Fatalf("operation error trace header count = %d, want 1\noutput:\n%s", count, output)
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
