// Package clispec renders the desktop-via-clyde command tree from declarative
// command and operation specs.
package clispec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/desktop-via-clyde/internal/batchops"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/cmdflags"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/response"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/statusreport"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// OperationRunner executes one operation request.
type OperationRunner func(context.Context, operations.Request) error

// BatchRunner executes one batch request.
type BatchRunner func(context.Context, batchops.Request) error

type batchAllHandler struct {
	out       io.Writer
	operation batchops.OperationName
	runner    BatchRunner
}

type operationHandler struct {
	out       io.Writer
	operation spec.OperationSpec
	target    *targets.Target
	program   *targets.CLIProgram
	runner    OperationRunner
}

type statusHandler struct {
	out io.Writer
}

// BuildRoot constructs the full Cobra tree from configured targets and specs.
func BuildRoot(
	ctx context.Context,
	out io.Writer,
	errOut io.Writer,
	runner OperationRunner,
	batchRunner BatchRunner,
) *cobra.Command {
	root := &cobra.Command{
		Use:           "desktop-via-clyde",
		Short:         "Operate on configured desktop-via-clyde apps and CLIs",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetContext(ctx)
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) {
		_ = response.WriteText(cmd.Context(), cmd.OutOrStdout(), helpText(cmd))
	})
	clioutput.PersistentFlag(root)

	root.AddCommand(newBatchParentCmd(out, batchops.OperationPatch, batchRunner, runner))
	root.AddCommand(newBatchParentCmd(out, batchops.OperationUpgrade, batchRunner, runner))
	root.AddCommand(newBatchParentCmd(out, batchops.OperationHardReset, batchRunner, runner))
	for _, configuredTarget := range targets.All() {
		target, lookupErr := targets.Lookup(configuredTarget.ID)
		if lookupErr != nil {
			continue
		}
		root.AddCommand(newAppCmd(out, target, runner))
	}
	for _, program := range targets.AllCLIs() {
		root.AddCommand(newCLICmd(out, program, runner))
	}
	root.AddCommand(newStatusCmd(out))
	return root
}

func newBatchParentCmd(
	out io.Writer,
	operation batchops.OperationName,
	batchRunner BatchRunner,
	operationRunner OperationRunner,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   string(operation),
		Short: fmt.Sprintf("Run %s across configured targets", operation),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newBatchAllCmd(out, operation, batchRunner))
	if operation == batchops.OperationHardReset {
		for _, target := range targets.All() {
			operationSpec, ok := target.Operations[string(operation)]
			if !ok {
				continue
			}
			targetCopy := target
			cmd.AddCommand(newVerbFirstTargetOperationCmd(out, operationSpec, &targetCopy, operationRunner))
		}
	}
	return cmd
}

func newBatchAllCmd(out io.Writer, operation batchops.OperationName, runner BatchRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all",
		Short: fmt.Sprintf("Run %s for every configured target that supports it", operation),
		Args:  cobra.NoArgs,
	}
	handler := batchAllHandler{out: out, operation: operation, runner: runner}
	cmd.Flags().Bool("dry-run", false, "print every step without modifying bundles, installs, or the filesystem")
	cmd.Flags().Bool("migrate-keychain", false, "restore keychain access where that flag is supported")
	cmd.Flags().Int("parallel", 0, "maximum concurrent targets; 0 runs all selected targets in parallel")
	cmd.Flags().StringArray("target", nil, "restrict the batch to one target id; repeat the flag to select multiple ids")
	cmd.Flags().StringArray("set", nil, "override one target flag as target.flag=value; repeat the flag for multiple overrides")
	cmd.RunE = handler.run
	return cmd
}

func newAppCmd(out io.Writer, target targets.Target, runner OperationRunner) *cobra.Command {
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
	for _, operation := range sortedOperations(target.Operations) {
		cmd.AddCommand(newOperationCmd(out, operation, &target, nil, runner))
	}
	return cmd
}

func newCLICmd(out io.Writer, program targets.CLIProgram, runner OperationRunner) *cobra.Command {
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
	for _, operation := range sortedOperations(program.Operations) {
		cmd.AddCommand(newOperationCmd(out, operation, nil, &program, runner))
	}
	return cmd
}

func newOperationCmd(out io.Writer, operation spec.OperationSpec, target *targets.Target, program *targets.CLIProgram, runner OperationRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:     operation.Use,
		Aliases: append([]string(nil), operation.Aliases...),
		Short:   operation.Short,
		Long:    operation.Long,
		Hidden:  operation.Hidden,
		Args:    cobra.NoArgs,
	}
	handler := operationHandler{
		out:       out,
		operation: operation,
		target:    target,
		program:   program,
		runner:    runner,
	}
	cmdflags.Register(cmd, operation.Flags)
	cmd.RunE = handler.run
	return cmd
}

func newVerbFirstTargetOperationCmd(
	out io.Writer,
	operation spec.OperationSpec,
	target *targets.Target,
	runner OperationRunner,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:     target.Command.Use,
		Aliases: append([]string(nil), target.Command.Aliases...),
		Short:   fmt.Sprintf("Run %s for %s", operation.Use, target.ID),
		Args:    cobra.NoArgs,
	}
	handler := operationHandler{
		out:       out,
		operation: operation,
		target:    target,
		program:   nil,
		runner:    runner,
	}
	cmdflags.Register(cmd, operation.Flags)
	cmd.RunE = handler.run
	return cmd
}

func newStatusCmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [target...]",
		Short: "Print per-target state (clean, patched, drifted) and bundle metadata",
		Args:  cobra.ArbitraryArgs,
	}
	cmd.RunE = statusHandler{out: out}.run
	return cmd
}

func (h batchAllHandler) run(cmd *cobra.Command, _ []string) error {
	return runBatchAll(cmd.Context(), cmd, h.out, h.operation, h.runner)
}

func (h operationHandler) run(cmd *cobra.Command, _ []string) error {
	return runOperation(cmd.Context(), cmd, h.out, h.operation, h.target, h.program, h.runner)
}

func (h statusHandler) run(cmd *cobra.Command, args []string) error {
	return runStatus(cmd.Context(), cmd, h.out, args)
}

func runBatchAll(
	ctx context.Context,
	cmd *cobra.Command,
	out io.Writer,
	operation batchops.OperationName,
	runner BatchRunner,
) error {
	format, err := readOutputFormat(ctx, cmd)
	if err != nil {
		return err
	}
	dryRun, err := cmd.Flags().GetBool("dry-run")
	if err != nil {
		slog.WarnContext(ctx, "clispec.batch.read_dry_run_failed", "err", err)
		return fmt.Errorf("read bool flag dry-run: %w", err)
	}
	migrateKeychain, err := cmd.Flags().GetBool("migrate-keychain")
	if err != nil {
		slog.WarnContext(ctx, "clispec.batch.read_migrate_keychain_failed", "err", err)
		return fmt.Errorf("read bool flag migrate-keychain: %w", err)
	}
	parallel, err := cmd.Flags().GetInt("parallel")
	if err != nil {
		slog.WarnContext(ctx, "clispec.batch.read_parallel_failed", "err", err)
		return fmt.Errorf("read int flag parallel: %w", err)
	}
	targetIDs, err := cmd.Flags().GetStringArray("target")
	if err != nil {
		slog.WarnContext(ctx, "clispec.batch.read_target_failed", "err", err)
		return fmt.Errorf("read string array flag target: %w", err)
	}
	sets, err := cmd.Flags().GetStringArray("set")
	if err != nil {
		slog.WarnContext(ctx, "clispec.batch.read_set_failed", "err", err)
		return fmt.Errorf("read string array flag set: %w", err)
	}
	return runner(ctx, batchops.Request{
		Out:             out,
		Operation:       operation,
		DryRun:          dryRun,
		MigrateKeychain: migrateKeychain,
		Parallel:        parallel,
		Targets:         targetIDs,
		Sets:            sets,
		Format:          format,
	})
}

func runOperation(
	ctx context.Context,
	cmd *cobra.Command,
	out io.Writer,
	operation spec.OperationSpec,
	target *targets.Target,
	program *targets.CLIProgram,
	runner OperationRunner,
) error {
	format, err := readOutputFormat(ctx, cmd)
	if err != nil {
		return err
	}
	flagValues, err := cmdflags.Read(cmd, operation.Flags)
	if err != nil {
		slog.WarnContext(ctx, "clispec.operation.read_flags_failed", "err", err, "operation", operation.Use)
		return fmt.Errorf("read operation flags for %s: %w", operation.Use, err)
	}

	var appTarget *targets.Target
	if target != nil {
		copied := *target
		if appPath := flagValues.String("app-path"); appPath != "" {
			copied.AppPath = appPath
		}
		appTarget = &copied
	}

	targetName := targetID(appTarget, program)
	session, err := clioutput.NewSession(ctx, clioutput.SessionOptions{
		Out:       out,
		Format:    format,
		Operation: operation.Use,
		Scope:     targetName,
		Parallel:  1,
		DryRun:    flagValues.Bool("dry-run"),
	})
	if err != nil {
		return fmt.Errorf("create output session: %w", err)
	}
	rawLog, _, err := session.OpenTargetLog(targetName)
	if err != nil {
		return fmt.Errorf("open target output log: %w", err)
	}
	started := clock.Now()
	commandOut := session.ProgressWriter(targetName)
	runErr := runner(ctx, operations.Request{
		Out:        commandOut,
		LogOut:     rawLog,
		App:        appTarget,
		CLI:        program,
		Capability: operation.Capability,
		Flags:      flagValues,
		Format:     format,
	})
	_ = rawLog.Close()
	status := "ok"
	if runErr != nil {
		status = "failed"
	}
	failureEmitErr := error(nil)
	if runErr != nil {
		failureEmitErr = session.EmitStepFailed(targetName, runErr.Error())
	}
	duration := clock.Since(started)
	durationMS := duration.Milliseconds()
	event := clioutput.NewEvent(clioutput.EventTargetDone, operation.Use)
	event.Target = targetName
	event.Status = status
	event.DurationMS = &durationMS
	emitErr := session.Emit(event)
	closeErr := session.Close([]clioutput.TargetResult{
		clioutput.NewTargetResult(targetName, targetKind(appTarget, program), runErr, duration),
	})
	if runErr != nil {
		return runErr
	}
	if failureEmitErr != nil {
		return fmt.Errorf("emit target failure event: %w", failureEmitErr)
	}
	if emitErr != nil {
		return fmt.Errorf("emit target completion event: %w", emitErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close output session: %w", closeErr)
	}
	return nil
}

func runStatus(ctx context.Context, cmd *cobra.Command, out io.Writer, targetIDs []string) error {
	format, err := readOutputFormat(ctx, cmd)
	if err != nil {
		return err
	}
	var report statusreport.Report
	if len(targetIDs) == 0 {
		report, err = statusreport.BuildAll(ctx)
	} else {
		report, err = statusreport.BuildTargets(ctx, targetIDs)
	}
	if err != nil {
		slog.WarnContext(ctx, "clispec.status.build_failed", "err", err)
		return fmt.Errorf("build status report: %w", err)
	}
	return writeStatus(ctx, out, format, report)
}

func writeStatus(ctx context.Context, out io.Writer, format clioutput.Format, report statusreport.Report) error {
	if format == clioutput.FormatJSON {
		body, err := json.Marshal(report)
		if err != nil {
			slog.WarnContext(ctx, "clispec.status.marshal_failed", "err", err)
			return fmt.Errorf("marshal status report: %w", err)
		}
		if err := response.WriteJSON(ctx, out, body, response.JSONIndented); err != nil {
			slog.WarnContext(ctx, "clispec.status.write_json_failed", "err", err)
			return fmt.Errorf("write status json: %w", err)
		}
		return nil
	}
	var body bytes.Buffer
	if err := statusreport.WriteText(&body, report); err != nil {
		slog.WarnContext(ctx, "clispec.status.write_text_buffer_failed", "err", err)
		return fmt.Errorf("write status report: %w", err)
	}
	if err := response.WriteText(ctx, out, body.String()); err != nil {
		slog.WarnContext(ctx, "clispec.status.write_text_failed", "err", err)
		return fmt.Errorf("write status text: %w", err)
	}
	return nil
}

func readOutputFormat(ctx context.Context, cmd *cobra.Command) (clioutput.Format, error) {
	rawFormat, err := cmd.Root().PersistentFlags().GetString(clioutput.FlagName)
	if err != nil {
		slog.WarnContext(ctx, "clispec.read_output_format_failed", "err", err)
		return "", fmt.Errorf("read output format: %w", err)
	}
	format, err := clioutput.ParseFormat(rawFormat)
	if err != nil {
		slog.WarnContext(ctx, "clispec.parse_output_format_failed", "err", err, "value", rawFormat)
		return "", fmt.Errorf("parse output format: %w", err)
	}
	return format, nil
}

func helpText(cmd *cobra.Command) string {
	description := strings.TrimSpace(cmd.Long)
	if description == "" {
		description = strings.TrimSpace(cmd.Short)
	}
	usage := cmd.UsageString()
	if description == "" {
		return usage
	}
	return description + "\n\n" + usage
}

func sortedOperations(operationsMap map[string]spec.OperationSpec) []spec.OperationSpec {
	ids := make([]string, 0, len(operationsMap))
	for id := range operationsMap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	operationsList := make([]spec.OperationSpec, 0, len(ids))
	for _, id := range ids {
		operationsList = append(operationsList, operationsMap[id])
	}
	return operationsList
}

func targetID(appTarget *targets.Target, program *targets.CLIProgram) string {
	if appTarget != nil {
		return appTarget.ID
	}
	if program != nil {
		return program.ID
	}
	return ""
}

func targetKind(appTarget *targets.Target, program *targets.CLIProgram) string {
	if appTarget != nil {
		return "app"
	}
	if program != nil {
		return "cli"
	}
	return "target"
}
