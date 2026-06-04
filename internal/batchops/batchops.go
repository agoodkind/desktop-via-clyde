// Package batchops runs aggregate patch and upgrade operations across selected
// desktop-via-clyde targets.
package batchops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/cmdflags"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// OperationName identifies one aggregate operation.
type OperationName string

const (
	// OperationPatch identifies the aggregate patch command.
	OperationPatch OperationName = "patch"
	// OperationUpgrade identifies the aggregate upgrade command.
	OperationUpgrade OperationName = "upgrade"
	// OperationHardReset identifies the aggregate hard-reset command.
	OperationHardReset OperationName = "hard-reset"
)

// Request describes one aggregate command execution.
type Request struct {
	Out             io.Writer
	Operation       OperationName
	DryRun          bool
	MigrateKeychain bool
	Parallel        int
	Targets         []string
	Sets            []string
	Format          clioutput.Format
}

// RunnerFunc dispatches one selected target operation.
type RunnerFunc func(context.Context, operations.Request) error

type selectedOperation struct {
	ID        string
	Kind      string
	App       *targets.Target
	CLI       *targets.CLIProgram
	Operation spec.OperationSpec
}

// Result records one aggregate target result.
type Result struct {
	ID       string
	Kind     string
	Err      error
	Duration time.Duration
}

// Run executes one aggregate batch operation with the default operation runner.
func Run(ctx context.Context, req Request) error {
	return RunWithOperationRunner(ctx, req, operations.Run)
}

// RunWithOperationRunner executes one aggregate batch operation.
func RunWithOperationRunner(ctx context.Context, req Request, runner RunnerFunc) error {
	if runner == nil {
		return errors.New("batch operation runner is required")
	}
	if req.Out == nil {
		req.Out = os.Stdout
	}
	selections, knownTargets, err := selectOperations(req.Operation, req.Targets)
	if err != nil {
		return err
	}
	overrides, err := parseOverrides(req.Sets)
	if err != nil {
		return err
	}
	if err := validateOverrideTargets(req.Operation, knownTargets, selections, overrides); err != nil {
		return err
	}
	parallelism := req.Parallel
	if parallelism <= 0 || parallelism > len(selections) {
		parallelism = len(selections)
	}
	session, err := clioutput.NewSession(ctx, clioutput.SessionOptions{
		Out:       req.Out,
		Format:    req.Format,
		Operation: string(req.Operation),
		Scope:     scopeForSelections(selections),
		Parallel:  parallelism,
		DryRun:    req.DryRun,
	})
	if err != nil {
		return fmt.Errorf("create output session: %w", err)
	}
	for _, selection := range selections {
		event := clioutput.NewEvent(clioutput.EventTargetQueued, string(req.Operation))
		event.Target = selection.ID
		event.Status = "queued"
		if emitErr := session.Emit(event); emitErr != nil {
			return fmt.Errorf("emit queued target event: %w", emitErr)
		}
	}

	results := make([]Result, len(selections))
	var waitGroup sync.WaitGroup
	semaphore := make(chan struct{}, parallelism)

	for index, selection := range selections {
		waitGroup.Add(1)
		go func(index int, selection selectedOperation) {
			defer waitGroup.Done()
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				slog.ErrorContext(ctx, "batchops.worker_wrapper.panic", "err", fmt.Errorf("panic: %v", recovered), "target", selection.ID, "operation", req.Operation)
				results[index] = Result{
					ID:       selection.ID,
					Kind:     selection.Kind,
					Err:      fmt.Errorf("panic: %v", recovered),
					Duration: 0,
				}
			}()
			results[index] = runSelection(ctx, req, runner, session, semaphore, selection, overrides[selection.ID])
		}(index, selection)
	}

	waitGroup.Wait()
	targetResults := make([]clioutput.TargetResult, 0, len(results))
	for _, result := range results {
		targetResults = append(targetResults, clioutput.NewTargetResult(result.ID, result.Kind, result.Err, result.Duration))
	}
	if err := session.Close(targetResults); err != nil {
		return fmt.Errorf("close output session: %w", err)
	}

	failedCount := 0
	failedIDs := make([]string, 0, len(results))
	for _, result := range results {
		if result.Err == nil {
			continue
		}
		failedCount++
		failedIDs = append(failedIDs, result.ID)
	}
	if failedCount > 0 {
		return fmt.Errorf("%d %s target(s) failed: %s", failedCount, req.Operation, strings.Join(failedIDs, ", "))
	}
	return nil
}

func runSelection(
	ctx context.Context,
	req Request,
	runner RunnerFunc,
	session *clioutput.Session,
	semaphore chan struct{},
	selection selectedOperation,
	overrides map[string]string,
) (result Result) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		slog.ErrorContext(ctx, "batchops.worker.panic", "err", fmt.Errorf("panic: %v", recovered), "target", selection.ID, "operation", req.Operation)
		result = Result{
			ID:       selection.ID,
			Kind:     selection.Kind,
			Err:      fmt.Errorf("panic: %v", recovered),
			Duration: 0,
		}
	}()

	result = Result{
		ID:       selection.ID,
		Kind:     selection.Kind,
		Err:      nil,
		Duration: 0,
	}
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	case <-ctx.Done():
		result.Err = ctx.Err()
		return result
	}

	started := clock.Now()
	rawLog, _, logErr := session.OpenTargetLog(selection.ID)
	if logErr != nil {
		result.Err = logErr
		return result
	}
	defer func() {
		_ = rawLog.Close()
	}()
	flagValues, valuesErr := buildFlagValues(selection.Operation.Flags, req, overrides)
	if valuesErr != nil {
		result.Err = valuesErr
		result.Duration = clock.Since(started)
		emitTargetFailure(session, req.Operation, selection.ID, valuesErr)
		emitTargetDone(session, req.Operation, selection.ID, valuesErr, result.Duration)
		return result
	}

	appTarget := cloneAppTarget(selection.App, flagValues)
	progressOut := session.ProgressWriter(selection.ID)
	result.Err = runner(ctx, operations.Request{
		Out:        progressOut,
		LogOut:     rawLog,
		App:        appTarget,
		CLI:        selection.CLI,
		Capability: selection.Operation.Capability,
		Flags:      flagValues,
		Format:     req.Format,
	})
	result.Duration = clock.Since(started)
	emitTargetFailure(session, req.Operation, selection.ID, result.Err)
	emitTargetDone(session, req.Operation, selection.ID, result.Err, result.Duration)
	return result
}

func scopeForSelections(selections []selectedOperation) string {
	if len(selections) == 1 {
		return selections[0].ID
	}
	return "all"
}

func emitTargetFailure(session *clioutput.Session, operation OperationName, targetID string, err error) {
	if err == nil {
		return
	}
	if emitErr := session.EmitStepFailed(targetID, err.Error()); emitErr != nil {
		slog.Warn("batchops.emit_target_failure_failed", "err", emitErr, "target", targetID, "operation", operation)
	}
}

func emitTargetDone(session *clioutput.Session, operation OperationName, targetID string, err error, duration time.Duration) {
	status := "ok"
	if err != nil {
		status = "failed"
	}
	durationMS := duration.Milliseconds()
	event := clioutput.NewEvent(clioutput.EventTargetDone, string(operation))
	event.Target = targetID
	event.Status = status
	event.DurationMS = &durationMS
	if err != nil {
		event.Step = "operation_failed"
		event.Detail = err.Error()
	}
	if emitErr := session.Emit(event); emitErr != nil {
		slog.Warn("batchops.emit_target_done_failed", "err", emitErr, "target", targetID, "operation", operation)
	}
}

func selectOperations(operation OperationName, filters []string) ([]selectedOperation, map[string]bool, error) {
	selected := make([]selectedOperation, 0)
	selectedByID := map[string]selectedOperation{}
	knownTargets := map[string]bool{}

	for _, appTarget := range targets.All() {
		knownTargets[appTarget.ID] = true
		operationSpec, ok := appTarget.Operations[string(operation)]
		if !ok {
			continue
		}
		copied := appTarget
		item := selectedOperation{
			ID:        copied.ID,
			Kind:      "app",
			App:       &copied,
			CLI:       nil,
			Operation: operationSpec,
		}
		selected = append(selected, item)
		selectedByID[copied.ID] = item
	}
	for _, cliProgram := range targets.AllCLIs() {
		knownTargets[cliProgram.ID] = true
		operationSpec, ok := cliProgram.Operations[string(operation)]
		if !ok {
			continue
		}
		copied := cliProgram
		item := selectedOperation{
			ID:        copied.ID,
			Kind:      "cli",
			App:       nil,
			CLI:       &copied,
			Operation: operationSpec,
		}
		selected = append(selected, item)
		selectedByID[copied.ID] = item
	}
	if len(selected) == 0 {
		return nil, knownTargets, fmt.Errorf("no targets support %s", operation)
	}

	requestedIDs := normalizeTargetFilters(filters)
	if len(requestedIDs) == 0 {
		return selected, knownTargets, nil
	}

	filtered := make([]selectedOperation, 0, len(requestedIDs))
	for _, targetID := range requestedIDs {
		selection, ok := selectedByID[targetID]
		if ok {
			filtered = append(filtered, selection)
			continue
		}
		if knownTargets[targetID] {
			return nil, knownTargets, fmt.Errorf("target %q does not support %s", targetID, operation)
		}
		return nil, knownTargets, fmt.Errorf("unknown target %q", targetID)
	}
	return filtered, knownTargets, nil
}

func normalizeTargetFilters(filters []string) []string {
	requested := make([]string, 0, len(filters))
	seen := map[string]bool{}
	for _, filter := range filters {
		targetID := strings.TrimSpace(filter)
		if targetID == "" || seen[targetID] {
			continue
		}
		seen[targetID] = true
		requested = append(requested, targetID)
	}
	return requested
}

func parseOverrides(raw []string) (map[string]map[string]string, error) {
	overrides := map[string]map[string]string{}
	for _, item := range raw {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		equalsIndex := strings.Index(trimmed, "=")
		if equalsIndex <= 0 || equalsIndex == len(trimmed)-1 {
			return nil, fmt.Errorf("override %q must use target.flag=value", trimmed)
		}
		key := trimmed[:equalsIndex]
		value := trimmed[equalsIndex+1:]
		dotIndex := strings.Index(key, ".")
		if dotIndex <= 0 || dotIndex == len(key)-1 {
			return nil, fmt.Errorf("override %q must use target.flag=value", trimmed)
		}
		targetID := key[:dotIndex]
		flagName := key[dotIndex+1:]
		if overrides[targetID] == nil {
			overrides[targetID] = map[string]string{}
		}
		overrides[targetID][flagName] = value
	}
	return overrides, nil
}

func validateOverrideTargets(
	operation OperationName,
	knownTargets map[string]bool,
	selected []selectedOperation,
	overrides map[string]map[string]string,
) error {
	selectedIDs := map[string]bool{}
	for _, selection := range selected {
		selectedIDs[selection.ID] = true
	}
	for targetID := range overrides {
		if selectedIDs[targetID] {
			continue
		}
		if knownTargets[targetID] {
			return fmt.Errorf("target %q does not support %s", targetID, operation)
		}
		return fmt.Errorf("unknown target %q", targetID)
	}
	return nil
}

func buildFlagValues(
	flags []spec.FlagSpec,
	req Request,
	overrides map[string]string,
) (operations.FlagValues, error) {
	values := cmdflags.Defaults(flags)
	if req.DryRun {
		if _, ok := cmdflags.Find(flags, "dry-run"); ok {
			if err := cmdflags.ApplyOverride(values, flags, "dry-run", "true"); err != nil {
				slog.Warn("batchops.apply_dry_run_override_failed", "err", err)
				return operations.FlagValues{}, fmt.Errorf("apply dry-run override: %w", err)
			}
		}
	}
	if req.MigrateKeychain {
		if _, ok := cmdflags.Find(flags, "migrate-keychain"); ok {
			if err := cmdflags.ApplyOverride(values, flags, "migrate-keychain", "true"); err != nil {
				slog.Warn("batchops.apply_migrate_keychain_override_failed", "err", err)
				return operations.FlagValues{}, fmt.Errorf("apply migrate-keychain override: %w", err)
			}
		}
	}
	for _, flagName := range sortedOverrideKeys(overrides) {
		if err := cmdflags.ApplyOverride(values, flags, flagName, overrides[flagName]); err != nil {
			slog.Warn("batchops.apply_override_failed", "err", err, "flag", flagName)
			return operations.FlagValues{}, fmt.Errorf("apply override %s: %w", flagName, err)
		}
	}
	return values, nil
}

func sortedOverrideKeys(overrides map[string]string) []string {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneAppTarget(target *targets.Target, flagValues operations.FlagValues) *targets.Target {
	if target == nil {
		return nil
	}
	copied := *target
	if appPath := flagValues.String("app-path"); appPath != "" {
		copied.AppPath = appPath
	}
	return &copied
}
