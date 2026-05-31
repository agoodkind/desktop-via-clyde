// Package clihandlers binds configured CLI operations to linked behavior.
package clihandlers

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/desktop-via-clyde/internal/codexcli"
)

var cliHandlersLog = slog.With("component", "desktop-via-clyde", "subcomponent", "cli-handlers")

// FlagReader exposes parsed operation flags to behavior handlers.
type FlagReader interface {
	String(name string) string
	Bool(name string) bool
}

// Request carries one configured CLI operation into a behavior handler.
type Request struct {
	Flags FlagReader
	Out   io.Writer
}

// InstallStandaloneCLI installs the linked standalone CLI implementation.
func InstallStandaloneCLI(ctx context.Context, req Request) error {
	if err := codexcli.Install(ctx, codexcli.InstallOptions{
		DryRun:            req.Flags.Bool("dry-run"),
		Repo:              req.Flags.String("repo"),
		SourceDir:         req.Flags.String("source-dir"),
		Ref:               req.Flags.String("ref"),
		PackageDir:        req.Flags.String("package-dir"),
		PackageVariant:    req.Flags.String("package-variant"),
		PackageBinaryPath: req.Flags.String("package-binary-path"),
		CommandName:       req.Flags.String("command-name"),
		InstallDir:        req.Flags.String("install-dir"),
		PackageHome:       req.Flags.String("package-home"),
		BuildMode:         req.Flags.String("build-mode"),
		NoSccache:         req.Flags.Bool("no-sccache"),
		ForceRebuild:      req.Flags.Bool("force-rebuild"),
		Out:               req.Out,
		Trace:             nil,
	}); err != nil {
		cliHandlersLog.ErrorContext(ctx, "clihandlers.standalone_cli_install_failed", "err", err)
		return fmt.Errorf("install standalone cli: %w", err)
	}
	return nil
}

// StatusStandaloneCLI prints status for the linked standalone CLI implementation.
func StatusStandaloneCLI(ctx context.Context, req Request) error {
	if err := codexcli.Status(ctx, codexcli.StatusOptions{
		SourceDir:         req.Flags.String("source-dir"),
		InstallDir:        req.Flags.String("install-dir"),
		PackageHome:       req.Flags.String("package-home"),
		CommandName:       req.Flags.String("command-name"),
		PackageBinaryPath: req.Flags.String("package-binary-path"),
		Out:               req.Out,
	}); err != nil {
		cliHandlersLog.ErrorContext(ctx, "clihandlers.standalone_cli_status_failed", "err", err)
		return fmt.Errorf("print standalone cli status: %w", err)
	}
	return nil
}
