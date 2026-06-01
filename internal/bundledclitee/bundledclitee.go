// Package bundledclitee binds declared bundled CLI tee hooks to linked behavior.
package bundledclitee

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/claudetee"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

var bundledCLITeeLog = slog.With("component", "desktop-via-clyde", "subcomponent", "bundled-cli-tee")

// HookCapability is the config capability name for bundled CLI tee hooks.
const HookCapability = "bundled-cli-tee"

// RegisterPatchHooks links bundled CLI tee lifecycle hooks.
func RegisterPatchHooks() error {
	if !catalog.HasPatchHookCapability(HookCapability) {
		if err := catalog.RegisterPatchHookCapability(HookCapability); err != nil {
			return logBundledCLITeeRegistrationError("register bundled CLI tee capability", err)
		}
	}
	if err := patch.RegisterPostPatchHook(HookCapability, PostPatchHook); err != nil {
		return logBundledCLITeeRegistrationError("register bundled CLI tee post-patch hook", err)
	}
	if err := patch.RegisterPreUnpatchHook(HookCapability, PreUnpatchHook); err != nil {
		return logBundledCLITeeRegistrationError("register bundled CLI tee pre-unpatch hook", err)
	}
	return nil
}

// RegisterValidators links bundled CLI tee config validation.
func RegisterValidators() error {
	if err := extensions.RegisterAppValidator("bundled_cli_tee", extensions.ValidateBundledCLITee); err != nil {
		return logBundledCLITeeRegistrationError("register bundled CLI tee validator", err)
	}
	return nil
}

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

// PostPatchHook installs the tee wrapper after a successful shared patch flow.
func PostPatchHook(ctx context.Context, runner *patch.Runner, target targets.Target, opts patch.Options) error {
	if target.Extensions.BundledCLITee == nil {
		return nil
	}
	teeOpts := targetOptions(target, opts)
	bundled, resolveErr := ResolvePath(teeOpts)
	if resolveErr != nil {
		fmt.Fprintf(runner.Out, "bundled CLI not present, skipping tee install (%v)\n", resolveErr)
		return nil
	}
	if _, statErr := os.Stat(bundled); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			bundledCLITeeLog.ErrorContext(ctx, "bundledclitee.post_patch_stat_failed", "path", bundled, "err", statErr)
			return fmt.Errorf("stat bundled cli %s: %w", bundled, statErr)
		}
		fmt.Fprintf(runner.Out, "bundled CLI missing at %s, skipping tee install\n", bundled)
		return nil
	}
	if _, realErr := os.Stat(bundled + ".real"); realErr == nil {
		fmt.Fprintf(runner.Out, "bundled CLI already wrapped (.real present), skipping\n")
		return nil
	}
	fmt.Fprintf(runner.Out, "install bundled CLI stdio tee at %s\n", bundled)
	if opts.DryRun {
		return nil
	}
	if err := Install(ctx, teeOpts); err != nil {
		return err
	}
	return nil
}

// PreUnpatchHook removes the tee wrapper before the shared unpatch flow runs.
func PreUnpatchHook(ctx context.Context, runner *patch.Runner, target targets.Target, opts patch.Options) error {
	if target.Extensions.BundledCLITee == nil {
		return nil
	}
	teeOpts := targetOptions(target, opts)
	bundled, resolveErr := ResolvePath(teeOpts)
	if resolveErr != nil {
		fmt.Fprintf(runner.Out, "bundled CLI not present, skipping tee uninstall (%v)\n", resolveErr)
		return nil
	}
	if _, statErr := os.Stat(bundled + ".real"); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			bundledCLITeeLog.ErrorContext(ctx, "bundledclitee.pre_unpatch_stat_real_failed", "path", bundled+".real", "err", statErr)
			return fmt.Errorf("stat bundled cli real sibling %s.real: %w", bundled, statErr)
		}
		fmt.Fprintf(runner.Out, "no .real sibling at %s.real, nothing to uninstall\n", bundled)
		return nil
	}
	fmt.Fprintf(runner.Out, "uninstall bundled CLI stdio tee at %s\n", bundled)
	if opts.DryRun {
		return nil
	}
	if err := Uninstall(ctx, teeOpts); err != nil {
		return err
	}
	return nil
}

func logBundledCLITeeRegistrationError(message string, err error) error {
	bundledCLITeeLog.Error("bundledclitee.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
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

func targetOptions(target targets.Target, opts patch.Options) Options {
	tee := target.Extensions.BundledCLITee
	return Options{
		DryRun:                   opts.DryRun,
		AppSupportDir:            tee.AppSupportDir,
		VersionDir:               tee.VersionDir,
		BundledCLIRel:            tee.BundledCLIRel,
		BundledCLIPath:           tee.BundledCLIPath,
		TerminateProcessNames:    append([]string(nil), tee.TerminateProcessNames...),
		TerminateProcessPatterns: append([]string(nil), tee.TerminateProcessPatterns...),
		CompletionSteps:          append([]string(nil), tee.CompletionSteps...),
		Out:                      opts.Out,
		Trace:                    nil,
	}
}
