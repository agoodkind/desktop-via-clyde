package codexcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/desktop-via-clyde/internal/patch"
)

const codexInstallLockPollInterval = time.Second

type codexInstallLock struct {
	file *os.File
	path string
}

func acquireCodexInstallLock(ctx context.Context, r *patch.Runner, sourceDir string) (*codexInstallLock, error) {
	log := codexcliLog.With("function", "acquireCodexInstallLock")
	lockPath := codexInstallLockPath(sourceDir)
	notef(r, "codex-cli: acquire install lock "+lockPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		wrappedErr := fmt.Errorf("create codex-cli install lock dir %s: %w", filepath.Dir(lockPath), err)
		log.ErrorContext(ctx, "codexcli.install_lock.mkdir_failed", "err", wrappedErr)
		return nil, wrappedErr
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		wrappedErr := fmt.Errorf("open codex-cli install lock %s: %w", lockPath, err)
		log.ErrorContext(ctx, "codexcli.install_lock.open_failed", "err", wrappedErr)
		return nil, wrappedErr
	}
	lock := &codexInstallLock{file: file, path: lockPath}
	waited := false
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			notef(r, "codex-cli: install lock acquired "+lockPath)
			return lock, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			wrappedErr := fmt.Errorf("lock codex-cli install %s: %w", lockPath, err)
			log.ErrorContext(ctx, "codexcli.install_lock.lock_failed", "err", wrappedErr)
			return nil, wrappedErr
		}
		if !waited {
			notef(r, "codex-cli: waiting for install lock "+lockPath)
			waited = true
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			wrappedErr := fmt.Errorf("wait for codex-cli install lock %s: %w", lockPath, ctx.Err())
			log.ErrorContext(ctx, "codexcli.install_lock.context_done", "err", wrappedErr)
			return nil, wrappedErr
		case <-time.After(codexInstallLockPollInterval):
		}
	}
}

func (lock *codexInstallLock) release(ctx context.Context) error {
	log := codexcliLog.With("function", "codexInstallLock.release")
	if lock == nil || lock.file == nil {
		return nil
	}
	if err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN); err != nil {
		wrappedErr := fmt.Errorf("unlock codex-cli install %s: %w", lock.path, err)
		log.ErrorContext(ctx, "codexcli.install_lock.unlock_failed", "err", wrappedErr)
		_ = lock.file.Close()
		return wrappedErr
	}
	if err := lock.file.Close(); err != nil {
		wrappedErr := fmt.Errorf("close codex-cli install lock %s: %w", lock.path, err)
		log.ErrorContext(ctx, "codexcli.install_lock.close_failed", "err", wrappedErr)
		return wrappedErr
	}
	lock.file = nil
	return nil
}

func codexInstallLockPath(sourceDir string) string {
	return filepath.Join(codexBuildRoot(sourceDir), ".install.lock")
}

func codexBuildRoot(sourceDir string) string {
	return filepath.Join(filepath.Dir(sourceDir), "build")
}
