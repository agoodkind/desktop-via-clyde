// Package clispec renders the desktop-via-clyde command tree from declarative
// command and operation specs.
package clispec

import (
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
		_ = response.WriteTextHeaderOnce(cmd.Context(), cmd.OutOrStdout())
		_, _ = io.WriteString(cmd.OutOrStdout(), helpText(cmd))
	})
	clioutput.PersistentFlag(root)

	for _, group := range collectVerbGroups() {
		root.AddCommand(newVerbCmd(out, group, runner, batchRunner))
	}
	return root
}

// verbChild is one per-target noun under a verb-first parent command.
type verbChild struct {
	noun      spec.CommandSpec
	operation spec.OperationSpec
	target    *targets.Target
	program   *targets.CLIProgram
}

// verbGroup is one verb-first parent command and the targets that declare it.
type verbGroup struct {
	name     string
	children []verbChild
}

// collectVerbGroups gathers every configured app and CLI operation into
// verb-first parent groups keyed by the operation's command verb, ordered with
// the aggregate operations first and status last.
func collectVerbGroups() []verbGroup {
	groups := map[string]*verbGroup{}
	addChild := func(verb string, child verbChild) {
		group, ok := groups[verb]
		if !ok {
			group = &verbGroup{name: verb, children: nil}
			groups[verb] = group
		}
		group.children = append(group.children, child)
	}
	for _, app := range targets.All() {
		appCopy := app
		for _, operation := range sortedOperations(app.Operations) {
			addChild(operation.Use, verbChild{
				noun:      app.Command,
				operation: operation,
				target:    &appCopy,
				program:   nil,
			})
		}
	}
	for _, program := range targets.AllCLIs() {
		programCopy := program
		for _, operation := range sortedOperations(program.Operations) {
			addChild(operation.Use, verbChild{
				noun:      program.Command,
				operation: operation,
				target:    nil,
				program:   &programCopy,
			})
		}
	}
	return orderVerbGroups(groups)
}

func orderVerbGroups(groups map[string]*verbGroup) []verbGroup {
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return verbLess(names[i], names[j])
	})
	ordered := make([]verbGroup, 0, len(names))
	for _, name := range names {
		ordered = append(ordered, *groups[name])
	}
	return ordered
}

func verbLess(left string, right string) bool {
	leftRank, leftBatch := batchVerbRank(left)
	rightRank, rightBatch := batchVerbRank(right)
	if leftBatch && rightBatch {
		return leftRank < rightRank
	}
	if leftBatch != rightBatch {
		return leftBatch
	}
	leftStatus := left == statusVerb
	rightStatus := right == statusVerb
	if leftStatus != rightStatus {
		return rightStatus
	}
	return left < right
}

const statusVerb = "status"

func batchVerbRank(verb string) (int, bool) {
	switch batchops.OperationName(verb) {
	case batchops.OperationPatch:
		return 0, true
	case batchops.OperationUpgrade:
		return 1, true
	case batchops.OperationHardReset:
		return 2, true
	default:
		return 0, false
	}
}

func isBatchVerb(verb string) bool {
	_, ok := batchVerbRank(verb)
	return ok
}

func newVerbCmd(out io.Writer, group verbGroup, runner OperationRunner, batchRunner BatchRunner) *cobra.Command {
	if isBatchVerb(group.name) {
		return newBatchVerbCmd(out, group, batchRunner, runner)
	}
	if group.name == statusVerb {
		return newStatusVerbCmd(out, group, runner)
	}
	return newPlainVerbCmd(out, group, runner)
}

func newBatchVerbCmd(
	out io.Writer,
	group verbGroup,
	batchRunner BatchRunner,
	operationRunner OperationRunner,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   group.name,
		Short: fmt.Sprintf("Run %s across configured targets", group.name),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newBatchAllCmd(out, batchops.OperationName(group.name), batchRunner))
	for _, child := range group.children {
		cmd.AddCommand(newVerbChildCmd(out, child, operationRunner))
	}
	return cmd
}

func newPlainVerbCmd(out io.Writer, group verbGroup, runner OperationRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:   group.name,
		Short: fmt.Sprintf("Run %s for one configured target", group.name),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	for _, child := range group.children {
		cmd.AddCommand(newVerbChildCmd(out, child, runner))
	}
	return cmd
}

func newStatusVerbCmd(out io.Writer, group verbGroup, runner OperationRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [target]",
		Short: "Print per-target state (clean, patched, drifted) and bundle metadata",
		Args:  cobra.NoArgs,
		RunE:  statusHandler{out: out}.run,
	}
	allCmd := &cobra.Command{
		Use:   "all",
		Short: "Print state for every configured target",
		Args:  cobra.NoArgs,
		RunE:  statusHandler{out: out}.run,
	}
	cmd.AddCommand(allCmd)
	for _, child := range group.children {
		cmd.AddCommand(newVerbChildCmd(out, child, runner))
	}
	return cmd
}

func newVerbChildCmd(out io.Writer, child verbChild, runner OperationRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:     child.noun.Use,
		Aliases: append([]string(nil), child.noun.Aliases...),
		Short:   fmt.Sprintf("Run %s for %s", child.operation.Use, child.noun.Use),
		Hidden:  child.noun.Hidden,
		Args:    cobra.NoArgs,
	}
	handler := operationHandler{
		out:       out,
		operation: child.operation,
		target:    child.target,
		program:   child.program,
		runner:    runner,
	}
	cmdflags.Register(cmd, child.operation.Flags)
	cmd.RunE = handler.run
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

func (h batchAllHandler) run(cmd *cobra.Command, _ []string) error {
	return runBatchAll(cmd.Context(), cmd, h.out, h.operation, h.runner)
}

func (h operationHandler) run(cmd *cobra.Command, _ []string) error {
	return runOperation(cmd.Context(), cmd, h.out, h.operation, h.target, h.program, h.runner)
}

func (h statusHandler) run(cmd *cobra.Command, _ []string) error {
	return runStatus(cmd.Context(), cmd, h.out)
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

	// Report capabilities (per-target status) write their payload directly to
	// out. They are not progress operations, so they bypass the live session
	// entirely rather than routing a report through a progress renderer.
	if isReportCapability(operation.Capability) {
		return runner(ctx, operations.Request{
			Out:        out,
			LogOut:     nil,
			Progress:   nil,
			App:        appTarget,
			CLI:        program,
			Capability: operation.Capability,
			Flags:      flagValues,
			Format:     format,
		})
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
	progress := session.TargetProgress(targetName)
	runErr := runner(ctx, operations.Request{
		Out:        rawLog,
		LogOut:     rawLog,
		Progress:   progress,
		App:        appTarget,
		CLI:        program,
		Capability: operation.Capability,
		Flags:      flagValues,
		Format:     format,
	})
	_ = rawLog.Close()
	failureEmitErr := error(nil)
	if runErr != nil {
		failureEmitErr = session.EmitStepFailed(targetName, runErr.Error())
	}
	duration := clock.Since(started)
	emitErr := session.EmitTargetDone(targetName, runErr, duration)
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

// isReportCapability reports whether a capability writes a status report to its
// output writer instead of running a multi-step progress operation.
func isReportCapability(capability string) bool {
	return strings.HasSuffix(capability, ".status")
}

func runStatus(ctx context.Context, cmd *cobra.Command, out io.Writer) error {
	format, err := readOutputFormat(ctx, cmd)
	if err != nil {
		return err
	}
	report, err := statusreport.BuildAll(ctx)
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
	if err := response.WriteTextHeaderOnce(ctx, out); err != nil {
		slog.WarnContext(ctx, "clispec.status.write_header_failed", "err", err)
		return fmt.Errorf("write status header: %w", err)
	}
	if err := statusreport.WriteText(out, report); err != nil {
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
