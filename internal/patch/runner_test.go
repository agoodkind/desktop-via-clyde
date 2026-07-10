package patch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunEnvInDirWithHeartbeatCancelsProcessGroup(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "spawn-child.sh")
	pidPath := filepath.Join(tempDir, "child.pid")
	script := strings.Join([]string{
		"#!/bin/sh",
		"sleep 30 &",
		"echo $! > " + strconv.Quote(pidPath),
		"wait",
		"",
	}, "\n")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := NewRunner(ctx, false, &bytes.Buffer{})
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- runner.RunEnvInDirWithHeartbeat(
			ctx,
			"test command",
			time.Hour,
			nil,
			tempDir,
			"/bin/sh",
			scriptPath,
		)
	}()

	childPID := waitForChildPID(t, pidPath)
	cancel()

	select {
	case err := <-resultCh:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not return after cancellation")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(childPID) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child process %d was still running after cancellation", childPID)
}

func waitForChildPID(t *testing.T, pidPath string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pidBytes, err := os.ReadFile(pidPath)
		if err == nil {
			pidText := strings.TrimSpace(string(pidBytes))
			pid, parseErr := strconv.Atoi(pidText)
			if parseErr != nil {
				t.Fatalf("parse child pid %q: %v", pidText, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read child pid: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("child pid file was not written")
	return 0
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
