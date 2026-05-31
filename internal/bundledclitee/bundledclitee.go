// Package bundledclitee binds declared bundled CLI tee hooks to linked behavior.
package bundledclitee

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/desktop-via-clyde/internal/claudetee"
)

var bundledCLITeeLog = slog.With("component", "desktop-via-clyde", "subcomponent", "bundled-cli-tee")

// Options carries one bundled CLI tee hook invocation.
type Options struct {
	DryRun                   bool
	AppSupportDir            string
	VersionDir               string
	BundledCLIRel            string
	BundledCLIPath           string
	TerminateProcessNames    []string
	TerminateProcessPatterns []string
	CompletionSteps          []string
	Out                      io.Writer
	Trace                    *claudetee.Trace
}

// ResolvePath returns the bundled CLI path selected by the linked tee handler.
func ResolvePath(opts Options) (string, error) {
	path, err := claudetee.ResolveBundledCLIPath(toClaudeOptions(opts))
	if err != nil {
		bundledCLITeeLog.Error("bundledclitee.resolve_path_failed", "err", err)
		return "", fmt.Errorf("resolve bundled cli tee path: %w", err)
	}
	return path, nil
}

// Install wraps the selected bundled CLI with the linked tee handler.
func Install(ctx context.Context, opts Options) error {
	if err := claudetee.Install(ctx, toClaudeOptions(opts)); err != nil {
		bundledCLITeeLog.ErrorContext(ctx, "bundledclitee.install_failed", "err", err)
		return fmt.Errorf("install bundled cli tee: %w", err)
	}
	return nil
}

// Uninstall removes the linked tee wrapper from the selected bundled CLI.
func Uninstall(ctx context.Context, opts Options) error {
	if err := claudetee.Uninstall(ctx, toClaudeOptions(opts)); err != nil {
		bundledCLITeeLog.ErrorContext(ctx, "bundledclitee.uninstall_failed", "err", err)
		return fmt.Errorf("uninstall bundled cli tee: %w", err)
	}
	return nil
}

func toClaudeOptions(opts Options) claudetee.Options {
	return claudetee.Options{
		DryRun:                   opts.DryRun,
		AppSupportDir:            opts.AppSupportDir,
		VersionDir:               opts.VersionDir,
		BundledCLIRel:            opts.BundledCLIRel,
		BundledCLIPath:           opts.BundledCLIPath,
		TerminateProcessNames:    append([]string(nil), opts.TerminateProcessNames...),
		TerminateProcessPatterns: append([]string(nil), opts.TerminateProcessPatterns...),
		CompletionSteps:          append([]string(nil), opts.CompletionSteps...),
		LogDir:                   "",
		Out:                      opts.Out,
		Trace:                    opts.Trace,
	}
}
