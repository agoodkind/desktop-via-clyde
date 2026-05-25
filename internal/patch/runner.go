package patch

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/gklog"
)

// Runner abstracts process execution so dry-run and real-run share one path.
type Runner struct {
	DryRun bool
	Out    io.Writer
	Log    *slog.Logger
	ctxFn  func() context.Context
}

// NewRunner constructs a Runner that writes progress to out.
func NewRunner(ctx context.Context, dryRun bool, out io.Writer) *Runner {
	if out == nil {
		out = os.Stdout
	}
	return &Runner{
		DryRun: dryRun,
		Out:    out,
		Log:    gklog.LoggerFromContext(ctx),
		ctxFn: func() context.Context {
			if ctx == nil {
				return context.Background()
			}
			return ctx
		},
	}
}

// Run executes a command, or prints what would run when DryRun is true.
// stdout and stderr are forwarded to the runner's Out.
func (r *Runner) Run(ctx context.Context, name string, args ...string) error {
	ctx = coalesceContext(ctx, r.context())
	r.Log.DebugContext(ctx, "runner.run.boundary", "command", name, "args", args, "dry_run", r.DryRun)
	r.logCommand(nil, name, args...)
	r.logInfo(ctx, "runner.command.start",
		slog.String("command", name),
		slog.Any("args", args),
		slog.Bool("dry_run", r.DryRun))
	if r.DryRun {
		return nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Out
	if err := cmd.Run(); err != nil {
		r.logError(ctx, "runner.command.failed", err,
			slog.String("command", name),
			slog.Any("args", args))
		r.Log.ErrorContext(ctx, "runner.command.returning_error", "err", err, "command", name, "args", args)
		return fmt.Errorf("run %s: %w", name, err)
	}
	r.logInfo(ctx, "runner.command.succeeded",
		slog.String("command", name),
		slog.Any("args", args))
	return nil
}

// RunWithHeartbeat executes a command and logs periodic progress while it is
// still running.
func (r *Runner) RunWithHeartbeat(ctx context.Context, label string, interval time.Duration, name string, args ...string) error {
	ctx = coalesceContext(ctx, r.context())
	r.logCommand(nil, name, args...)
	r.logInfo(ctx, "runner.command.start",
		slog.String("label", label),
		slog.String("command", name),
		slog.Any("args", args),
		slog.Bool("dry_run", r.DryRun))
	if r.DryRun {
		return nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Out
	if err := cmd.Start(); err != nil {
		r.logError(ctx, "runner.command.start_failed", err,
			slog.String("label", label),
			slog.String("command", name),
			slog.Any("args", args))
		return fmt.Errorf("start %s: %w", name, err)
	}
	done := make(chan error, 1)
	start := clock.Now()
	go func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			r.Log.LogAttrs(ctx, slog.LevelError, "runner.goroutine.panic",
				slog.String("label", "runner.wait"),
				slog.String("err", fmt.Sprintf("panic: %v", recovered)))
		}()
		done <- cmd.Wait()
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				r.logError(ctx, "runner.command.failed", err,
					slog.String("label", label),
					slog.String("command", name),
					slog.Any("args", args))
				return fmt.Errorf("wait %s: %w", name, err)
			}
			r.logInfo(ctx, "runner.command.succeeded",
				slog.String("label", label),
				slog.String("command", name),
				slog.Any("args", args))
			return nil
		case <-ticker.C:
			elapsed := clock.Since(start).Round(time.Second)
			notef(r, fmt.Sprintf("%s still running after %s", label, elapsed))
			r.logInfo(ctx, "runner.command.heartbeat",
				slog.String("label", label),
				slog.String("command", name),
				slog.String("elapsed", elapsed.String()))
		}
	}
}

// RunEnvWithHeartbeat executes a command with environment overrides and logs
// periodic progress while it is still running.
func (r *Runner) RunEnvWithHeartbeat(
	ctx context.Context,
	label string,
	interval time.Duration,
	env map[string]string,
	name string,
	args ...string,
) error {
	ctx = coalesceContext(ctx, r.context())
	r.logCommand(env, name, args...)
	r.logInfo(ctx, "runner.command.start",
		slog.String("label", label),
		slog.String("command", name),
		slog.Any("args", args),
		slog.Any("env", env),
		slog.Bool("dry_run", r.DryRun))
	if r.DryRun {
		return nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = mergedEnv(env)
	cmd.Stdout = r.Out
	cmd.Stderr = r.Out
	if err := cmd.Start(); err != nil {
		r.logError(ctx, "runner.command.start_failed", err,
			slog.String("label", label),
			slog.String("command", name),
			slog.Any("args", args))
		return fmt.Errorf("start %s: %w", name, err)
	}
	done := make(chan error, 1)
	start := clock.Now()
	go func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			r.Log.LogAttrs(ctx, slog.LevelError, "runner.goroutine.panic",
				slog.String("label", "runner.wait"),
				slog.String("err", fmt.Sprintf("panic: %v", recovered)))
		}()
		done <- cmd.Wait()
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				r.logError(ctx, "runner.command.failed", err,
					slog.String("label", label),
					slog.String("command", name),
					slog.Any("args", args))
				return fmt.Errorf("wait %s: %w", name, err)
			}
			r.logInfo(ctx, "runner.command.succeeded",
				slog.String("label", label),
				slog.String("command", name),
				slog.Any("args", args))
			return nil
		case <-ticker.C:
			elapsed := clock.Since(start).Round(time.Second)
			notef(r, fmt.Sprintf("%s still running after %s", label, elapsed))
			r.logInfo(ctx, "runner.command.heartbeat",
				slog.String("label", label),
				slog.String("command", name),
				slog.String("elapsed", elapsed.String()))
		}
	}
}

// RunCaptureStdout runs a command and returns only its stdout (stderr goes to Out).
func (r *Runner) RunCaptureStdout(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx = coalesceContext(ctx, r.context())
	r.Log.DebugContext(ctx, "runner.capture_stdout.boundary", "command", name, "args", args, "dry_run", r.DryRun)
	r.logCommand(nil, name, args...)
	r.logInfo(ctx, "runner.command.start",
		slog.String("command", name),
		slog.Any("args", args),
		slog.Bool("capture_stdout", true),
		slog.Bool("dry_run", r.DryRun))
	if r.DryRun {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = r.Out
	output, err := cmd.Output()
	if err != nil {
		r.logError(ctx, "runner.command.failed", err,
			slog.String("command", name),
			slog.Any("args", args),
			slog.Bool("capture_stdout", true))
		r.Log.ErrorContext(ctx, "runner.command.returning_error", "err", err, "command", name, "args", args, "capture_stdout", true)
		return nil, fmt.Errorf("output %s: %w", name, err)
	}
	r.logInfo(ctx, "runner.command.succeeded",
		slog.String("command", name),
		slog.Any("args", args),
		slog.Bool("capture_stdout", true))
	return output, nil
}

func (r *Runner) prefix() string {
	if r.DryRun {
		return "[dry-run]"
	}
	return "[run]"
}

func (r *Runner) logCommand(env map[string]string, name string, args ...string) {
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(r.Out, "%s env %s=%s\n", r.prefix(), key, env[key])
		}
	}
	fmt.Fprintf(r.Out, "%s %s %s\n", r.prefix(), name, joinArgs(args))
}

func (r *Runner) context() context.Context {
	if r.ctxFn == nil {
		return context.Background()
	}
	return r.ctxFn()
}

func coalesceContext(primary context.Context, fallback context.Context) context.Context {
	if primary != nil {
		return primary
	}
	if fallback != nil {
		return fallback
	}
	return context.Background()
}

func (r *Runner) logInfo(ctx context.Context, msg string, attrs ...slog.Attr) {
	r.Log.LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
}

func (r *Runner) logError(ctx context.Context, msg string, err error, attrs ...slog.Attr) {
	r.Log.LogAttrs(ctx, slog.LevelError, msg, append(attrs, slog.Any("err", err))...)
}

func notef(r *Runner, message string) {
	fmt.Fprintf(r.Out, "%s %s\n", r.prefix(), message)
}

func joinArgs(args []string) string {
	var builder strings.Builder
	for i := range args {
		if i > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(args[i])
	}
	return builder.String()
}

func mergedEnv(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(overrides))
	seen := make(map[string]bool, len(base)+len(overrides))
	for _, entry := range base {
		key := entry
		entryKey, _, ok := strings.Cut(entry, "=")
		if ok {
			key = entryKey
		}
		if value, ok := overrides[key]; ok {
			out = append(out, key+"="+value)
			seen[key] = true
			continue
		}
		out = append(out, entry)
		seen[key] = true
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		if seen[key] {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+overrides[key])
	}
	return out
}
