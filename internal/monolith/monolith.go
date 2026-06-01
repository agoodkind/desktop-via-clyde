// Package monolith exposes the running desktop-via-clyde executable as the
// payload for helper installs.
package monolith

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

var monolithLog = slog.With("component", "desktop-via-clyde", "subcomponent", "monolith")

// ExecutablePath returns the current desktop-via-clyde executable path.
func ExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		monolithLog.Error("monolith.executable_failed", "err", err)
		return "", fmt.Errorf("current executable: %w", err)
	}
	return path, nil
}

// Size returns the current executable size when it can be statted.
func Size() int64 {
	path, err := ExecutablePath()
	if err != nil {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// CopyTo copies the current executable to path with executable permissions.
func CopyTo(path string) error {
	sourcePath, err := ExecutablePath()
	if err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		monolithLog.Error("monolith.copy_open_source_failed", "path", sourcePath, "err", err)
		return fmt.Errorf("open current executable %s: %w", sourcePath, err)
	}
	defer source.Close()

	destination, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		monolithLog.Error("monolith.copy_open_destination_failed", "path", path, "err", err)
		return fmt.Errorf("open destination %s: %w", path, err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		monolithLog.Error("monolith.copy_failed", "path", path, "err", err)
		return fmt.Errorf("copy monolith to %s: %w", path, err)
	}
	if err := destination.Close(); err != nil {
		monolithLog.Error("monolith.copy_close_destination_failed", "path", path, "err", err)
		return fmt.Errorf("close destination %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		monolithLog.Error("monolith.copy_chmod_failed", "path", path, "err", err)
		return fmt.Errorf("chmod destination %s: %w", path, err)
	}
	return nil
}
