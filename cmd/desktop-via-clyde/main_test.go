package main

import (
	"bytes"
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
	} {
		if _, err := executeRoot(args...); err != nil {
			t.Fatalf("executeRoot(%v): %v", args, err)
		}
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
	cmd := newRootCmd(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
