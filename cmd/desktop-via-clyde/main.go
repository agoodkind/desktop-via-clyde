// Command desktop-via-clyde patches configured macOS desktop applications so
// they launch through the Clyde MITM harness.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/composition"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/helperdispatch"
	"goodkind.io/desktop-via-clyde/internal/logging"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/desktop-via-clyde/internal/version"
	"goodkind.io/gklog"
)

var bootstrapLog = slog.New(slog.DiscardHandler)

type operationRunner func(context.Context, operations.Request) error

func main() {
	bootstrapLog.Info("desktop-via-clyde.main")
	exitCode := run()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func run() int {
	logger, closer, loggingErr := logging.Setup()
	slog.SetDefault(logger)

	baseCtx := context.Background()
	ctx := gklog.WithLogger(baseCtx, logger)
	if loggingErr != nil {
		fmt.Fprintf(os.Stderr, "warning: structured logging disabled: %v\n", loggingErr)
	}

	logger.InfoContext(ctx, "cli.start",
		"version", version.String(),
		"gklog_build", logging.GklogBuild(),
		"log_path", paths.ProcessLogPath(),
		"args", os.Args[1:])

	if err := composition.Register(); err != nil {
		logger.ErrorContext(ctx, "cli.composition_register_failed", "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = closer.Close()
		return 1
	}
	if code, ok := helperdispatch.RunIfMatched(); ok {
		_ = closer.Close()
		return code
	}

	loadedConfig, err := config.LoadRequired()
	if err != nil {
		logger.ErrorContext(ctx, "cli.config_load_failed", "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = closer.Close()
		return 1
	}
	config.SetCurrent(loadedConfig)

	root := newRootCmd(ctx, os.Stdout)
	if err := root.ExecuteContext(ctx); err != nil {
		logger.ErrorContext(ctx, "cli.execute.failed", "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		_ = closer.Close()
		return 1
	}
	logger.InfoContext(ctx, "cli.exit")
	_ = closer.Close()
	return 0
}

func newRootCmd(ctx context.Context, out io.Writer) *cobra.Command {
	return newRootCmdWithRunner(ctx, out, operations.Run)
}

func newRootCmdWithRunner(ctx context.Context, out io.Writer, runner operationRunner) *cobra.Command {
	root := &cobra.Command{
		Use:           "desktop-via-clyde",
		Short:         "Operate on configured desktop-via-clyde apps and CLIs",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetOut(out)
	root.SetErr(out)
	root.SetContext(ctx)

	for _, configuredTarget := range targets.All() {
		target, err := targets.Lookup(configuredTarget.ID)
		if err != nil {
			continue
		}
		root.AddCommand(newAppCmd(ctx, out, target, runner))
	}
	for _, program := range targets.AllCLIs() {
		root.AddCommand(newCLICmd(ctx, out, program, runner))
	}
	root.AddCommand(newStatusCmd(ctx, out))
	return root
}

func newAppCmd(ctx context.Context, out io.Writer, target targets.Target, runner operationRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:     target.Command.Use,
		Aliases: append([]string(nil), target.Command.Aliases...),
		Short:   target.Command.Short,
		Long:    target.Command.Long,
		Hidden:  target.Command.Hidden,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.SetContext(ctx)
	for _, operation := range sortedOperations(target.Operations) {
		cmd.AddCommand(newOperationCmd(ctx, out, operation, &target, nil, runner))
	}
	return cmd
}

func newCLICmd(ctx context.Context, out io.Writer, program targets.CLIProgram, runner operationRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:     program.Command.Use,
		Aliases: append([]string(nil), program.Command.Aliases...),
		Short:   program.Command.Short,
		Long:    program.Command.Long,
		Hidden:  program.Command.Hidden,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.SetContext(ctx)
	for _, operation := range sortedOperations(program.Operations) {
		cmd.AddCommand(newOperationCmd(ctx, out, operation, nil, &program, runner))
	}
	return cmd
}

func newOperationCmd(
	ctx context.Context,
	out io.Writer,
	operation spec.OperationSpec,
	target *targets.Target,
	program *targets.CLIProgram,
	runner operationRunner,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:     operation.Use,
		Aliases: append([]string(nil), operation.Aliases...),
		Short:   operation.Short,
		Long:    operation.Long,
		Hidden:  operation.Hidden,
		Args:    cobra.NoArgs,
	}
	cmd.SetContext(ctx)

	flagValues := operations.NewFlagValues()
	for _, flag := range operation.Flags {
		switch flag.Type {
		case spec.FlagTypeBool:
			value := false
			if flag.DefaultBool != nil {
				value = *flag.DefaultBool
			}
			cmd.Flags().Bool(flag.Name, value, flag.Usage)
		case spec.FlagTypeString:
			cmd.Flags().String(flag.Name, flag.DefaultString, flag.Usage)
		}
		if flag.Hidden {
			_ = cmd.Flags().MarkHidden(flag.Name)
		}
	}

	cmd.RunE = func(_ *cobra.Command, _ []string) error {
		for _, flag := range operation.Flags {
			switch flag.Type {
			case spec.FlagTypeBool:
				value, err := cmd.Flags().GetBool(flag.Name)
				if err != nil {
					gklog.LoggerFromContext(ctx).ErrorContext(ctx, "cli.read_bool_flag_failed", "flag", flag.Name, "err", err)
					slog.Default().ErrorContext(ctx, "cli.read_bool_flag_failed", "flag", flag.Name, "err", err)
					return errors.New("read bool flag " + flag.Name + ": " + err.Error())
				}
				flagValues.SetBool(flag.Name, value)
				flagValues.SetBool(flag.Binding, value)
			case spec.FlagTypeString:
				value, err := cmd.Flags().GetString(flag.Name)
				if err != nil {
					gklog.LoggerFromContext(ctx).ErrorContext(ctx, "cli.read_string_flag_failed", "flag", flag.Name, "err", err)
					slog.Default().ErrorContext(ctx, "cli.read_string_flag_failed", "flag", flag.Name, "err", err)
					return errors.New("read string flag " + flag.Name + ": " + err.Error())
				}
				flagValues.SetString(flag.Name, value)
				flagValues.SetString(flag.Binding, value)
			}
		}

		var appTarget *targets.Target
		if target != nil {
			copied := *target
			if appPath := flagValues.String("app-path"); appPath != "" {
				copied.AppPath = appPath
			}
			appTarget = &copied
		}
		return runner(ctx, operations.Request{
			Out:        out,
			App:        appTarget,
			CLI:        program,
			Capability: operation.Capability,
			Flags:      flagValues,
		})
	}

	return cmd
}

func newStatusCmd(ctx context.Context, out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Print per-target state (clean, patched, drifted) and bundle metadata",
		RunE: func(_ *cobra.Command, _ []string) error {
			log := gklog.LoggerFromContext(ctx)
			ms, err := state.Load(paths.StateFile())
			if err != nil {
				log.ErrorContext(ctx, "status.load_state_failed", "err", err)
				return fmt.Errorf("load state file %s: %w", paths.StateFile(), err)
			}
			fmt.Fprintf(out, "state file: %s\n", paths.StateFile())
			fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", "TARGET", "STATE", "VERSION", "NOTES")
			for _, target := range targets.All() {
				printTargetStatus(out, target, ms)
			}
			return nil
		},
	}
	cmd.SetContext(ctx)
	return cmd
}

func printTargetStatus(out io.Writer, target targets.Target, ms state.MultiState) {
	entry, patched := ms.Targets[target.ID]
	if _, err := os.Stat(target.AppPath); err != nil {
		fmt.Fprintf(out, "%-8s  %-9s  %-20s  bundle missing at %s\n", target.ID, "absent", "-", target.AppPath)
		return
	}
	if !patched {
		fmt.Fprintf(out, "%-8s  %-9s  %-20s  bundle present, no state entry\n", target.ID, "clean", "-")
		return
	}
	curVer := readBundleVersion(target)
	stateLabel := "patched"
	notes := fmt.Sprintf("signed-as=%q", entry.SignIdentity)
	realPath := paths.RealBinaryPath(target)
	if _, err := os.Stat(realPath); err != nil {
		stateLabel = "drifted"
		notes = notes + "; " + target.ExecName + ".real missing"
	} else if curVer != "" && curVer != entry.PatchedVersion {
		stateLabel = "drifted"
		notes += fmt.Sprintf("; current version %s != patched %s", curVer, entry.PatchedVersion)
	}
	fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", target.ID, stateLabel, entry.PatchedVersion, notes)
}

func readBundleVersion(target targets.Target) string {
	info, err := patch.ReadInfoPlist(paths.InfoPlistPath(target))
	if err != nil {
		return ""
	}
	return info.CFBundleVersion
}

func sortedOperations(operationsMap map[string]spec.OperationSpec) []spec.OperationSpec {
	ids := make([]string, 0, len(operationsMap))
	for id := range operationsMap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	operations := make([]spec.OperationSpec, 0, len(ids))
	for _, id := range ids {
		operations = append(operations, operationsMap[id])
	}
	return operations
}
