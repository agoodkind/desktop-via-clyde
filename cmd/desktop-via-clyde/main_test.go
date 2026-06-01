package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/composition"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/spec"
)

func TestRootHelpListsTargetCommands(t *testing.T) {
	output, err := executeRoot(t, "--help")
	if err != nil {
		t.Fatalf("executeRoot(--help): %v", err)
	}

	required := []string{"cursor", "codex", "claude", "codex-cli", "status"}
	for _, want := range required {
		if !strings.Contains(output, want) {
			t.Fatalf("root help missing %q\noutput:\n%s", want, output)
		}
	}

	forbidden := []string{"patch", "unpatch", "upgrade", "keychain-migrate", strings.Join([]string{"mitm", "hook"}, "-")}
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

	required := []string{"patch", "unpatch", "upgrade", "keychain-migrate", "status"}
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
	for _, args := range [][]string{{"patch", "codex"}, {"unpatch", "codex"}, {"upgrade", "codex"}, {"keychain-migrate", "codex"}} {
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

func executeRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	installFixture(t)
	return executeRootNoFixture(args...)
}

func executeRootNoFixture(args ...string) (string, error) {
	var out bytes.Buffer
	cmd := newRootCmd(context.Background(), &out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func executeRootWithRunner(runner operationRunner, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := newRootCmdWithRunner(context.Background(), &out, runner)
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
	cfg, err := config.LoadPath(filepath.Join("..", "..", "internal", "testconfig", "testdata", "current-config.toml"))
	if err != nil {
		t.Fatalf("LoadPath(current-config.toml): %v", err)
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() {
		config.SetCurrent(nil)
	})
}
