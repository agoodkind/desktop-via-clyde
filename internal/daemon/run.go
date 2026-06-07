package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/paths"
)

// Run serves the updater daemon control plane on the unix socket until the
// context is cancelled or an interrupt arrives. launchd owns the daemon's
// lifecycle in production, so this is normally invoked by the launch agent
// rather than directly.
func Run(ctx context.Context) error {
	socketPath := paths.DaemonSocketPath()
	listener, err := listenUnix(ctx, socketPath)
	if err != nil {
		return err
	}

	operationExecutor := newExecutor()
	state := newUpdaterState()
	grpcServer := grpc.NewServer()
	desktopviaclydev1.RegisterDesktopServiceServer(grpcServer, newServer(operationExecutor, state))

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				daemonLog.ErrorContext(signalCtx, "daemon.tick.loop_panic", "err", fmt.Sprintf("panic: %v", recovered))
			}
		}()
		newTicker(operationExecutor, state).loop(signalCtx)
	}()

	serveErr := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				daemonLog.ErrorContext(signalCtx, "daemon.serve_panic", "err", fmt.Sprintf("panic: %v", recovered))
				serveErr <- fmt.Errorf("daemon serve panic: %v", recovered)
			}
		}()
		serveErr <- grpcServer.Serve(listener)
	}()
	daemonLog.InfoContext(signalCtx, "daemon.ready", "socket", socketPath)

	select {
	case <-signalCtx.Done():
		daemonLog.InfoContext(signalCtx, "daemon.shutdown.signal")
		grpcServer.GracefulStop()
		return nil
	case err := <-serveErr:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
			daemonLog.ErrorContext(signalCtx, "daemon.serve_failed", "err", err)
			return fmt.Errorf("serve daemon: %w", err)
		}
		return nil
	}
}

// listenUnix binds the daemon control socket, creating the parent directory and
// clearing any stale socket file left by a previous run that did not exit
// cleanly. A leftover socket file is inert because no process holds the kernel
// listener, so removing it before bind is safe.
func listenUnix(ctx context.Context, socketPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		daemonLog.ErrorContext(ctx, "daemon.socket_dir_failed", "err", err, "path", filepath.Dir(socketPath))
		return nil, fmt.Errorf("create daemon socket dir: %w", err)
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		daemonLog.ErrorContext(ctx, "daemon.socket_remove_stale_failed", "err", err, "path", socketPath)
		return nil, fmt.Errorf("remove stale daemon socket %s: %w", socketPath, err)
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "unix", socketPath)
	if err != nil {
		daemonLog.ErrorContext(ctx, "daemon.listen_failed", "err", err, "path", socketPath)
		return nil, fmt.Errorf("listen daemon socket %s: %w", socketPath, err)
	}
	return listener, nil
}
