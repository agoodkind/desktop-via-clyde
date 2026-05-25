// Package logging wires desktop-via-clyde onto gklog-backed structured logs.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/version"
	"goodkind.io/gklog"
	gklogversion "goodkind.io/gklog/version"
)

type noopCloser struct{}

func (noopCloser) Close() error {
	return nil
}

// GklogBuild reports the linked gklog build metadata string.
func GklogBuild() string {
	return gklogversion.String()
}

// Setup returns the process logger, its closer, and any file-setup error.
// Logging failures do not block command execution; callers can continue with
// the returned discard-backed logger after surfacing the setup error once.
func Setup() (*slog.Logger, io.Closer, error) {
	logPath := paths.ProcessLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		setupErr := fmt.Errorf("create log dir for %s: %w", logPath, err)
		slog.Error("logging.setup.create_log_dir_failed", "path", logPath, "err", setupErr)
		return discardLogger(), noopCloser{}, setupErr
	}

	logger, closer := gklog.New(gklog.Config{
		BuildVersion: version.String(),
		Handlers: []slog.Handler{
			gklog.FileJSON(logPath, slog.LevelDebug, gklog.RotationConfig{}),
		},
	})
	return logger.With("component", "desktop-via-clyde"), closer, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler).With("component", "desktop-via-clyde")
}
