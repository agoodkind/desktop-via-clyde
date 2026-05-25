package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRootHelpListsTargetCommands(t *testing.T) {
	output, err := executeRoot("--help")
	if err != nil {
		t.Fatalf("executeRoot(--help): %v", err)
	}

	required := []string{
		"cursor",
		"codex",
		"claude",
		"codex-cli",
		"status",
	}
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
	output, err := executeRoot("codex", "--help")
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

func TestOperationHelpCommandsSucceed(t *testing.T) {
	for _, args := range [][]string{
		{"codex", "patch", "--help"},
		{"codex", "upgrade", "--help"},
		{"codex", "keychain-migrate", "--help"},
		{"codex", "status", "--help"},
		{"codex-cli", "upgrade", "--help"},
		{"codex-cli", "install", "--help"},
		{"codex-cli", "status", "--help"},
	} {
		if _, err := executeRoot(args...); err != nil {
			t.Fatalf("executeRoot(%v): %v", args, err)
		}
	}
}

func TestCodexCLIHelpListsUpgradeWithInstallAlias(t *testing.T) {
	output, err := executeRoot("codex-cli", "--help")
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
	output, err := executeRoot("codex-cli", "upgrade", "--help")
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
	for _, args := range [][]string{
		{"patch", "codex"},
		{"unpatch", "codex"},
		{"upgrade", "codex"},
		{"keychain-migrate", "codex"},
	} {
		output, err := executeRoot(args...)
		if err == nil {
			t.Fatalf("executeRoot(%v) unexpectedly succeeded\noutput:\n%s", args, output)
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("executeRoot(%v) error = %q, want unknown command", args, err.Error())
		}
	}
}

func executeRoot(args ...string) (string, error) {
	var out bytes.Buffer
	cmd := newRootCmd(context.Background(), &out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
