package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunVersionDoesNotRequireConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	output, code := captureRun(t, "desktop-via-clyde", "version")
	if code != 0 {
		t.Fatalf("run(version) code = %d, want 0\noutput:\n%s", code, output)
	}
	if !strings.Contains(output, "version:") {
		t.Fatalf("run(version) output missing version line\noutput:\n%s", output)
	}
}

func TestRunUpdateStatusDoesNotRequireConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	output, code := captureRun(t, "desktop-via-clyde", "update", "status")
	if code != 0 {
		t.Fatalf("run(update status) code = %d, want 0\noutput:\n%s", code, output)
	}
	if !strings.Contains(output, "current version:") {
		t.Fatalf("run(update status) output missing current version\noutput:\n%s", output)
	}
}

func captureRun(t *testing.T, args ...string) (string, int) {
	t.Helper()

	previousArgs := os.Args
	previousStdout := os.Stdout
	previousStderr := os.Stderr
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}

	os.Args = args
	os.Stdout = stdoutWriter
	os.Stderr = stderrWriter
	code := run()

	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	os.Args = previousArgs
	os.Stdout = previousStdout
	os.Stderr = previousStderr

	stdout, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	stderr, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	return string(stdout) + string(stderr), code
}
