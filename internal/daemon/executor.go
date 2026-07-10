package daemon

import (
	"context"
	"fmt"
	"sort"
	"sync"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
)

// operationJob runs one operation to completion, emitting its progress events to
// emit.
type operationJob func(ctx context.Context, emit func(event *desktopviaclydev1.ProgressEvent)) error

// activeRun is one in-flight operation and its event broadcaster.
type activeRun struct {
	operation   string
	target      string
	cancelable  bool
	broadcaster *broadcaster
}

type runKey struct {
	operation string
	target    string
}

type sameTargetConflictError struct {
	ActiveOperation    string
	ActiveTarget       string
	RequestedOperation string
	RequestedTarget    string
}

func (e *sameTargetConflictError) Error() string {
	return fmt.Sprintf(
		"same-target conflict: active operation=%s active target=%s requested operation=%s requested target=%s",
		e.ActiveOperation,
		e.ActiveTarget,
		e.RequestedOperation,
		e.RequestedTarget,
	)
}

type runCancellationConflictError struct {
	Operation           string
	Target              string
	ActiveCancelable    bool
	RequestedCancelable bool
}

func (e *runCancellationConflictError) Error() string {
	return fmt.Sprintf(
		"run cancellation conflict: operation=%s target=%s active cancelable=%t requested cancelable=%t",
		e.Operation,
		e.Target,
		e.ActiveCancelable,
		e.RequestedCancelable,
	)
}

// executor tracks independent in-flight runs by operation and target. Exact
// duplicates attach to the same broadcaster, while distinct targets can run at
// the same time. A different mutating operation for the same target is blocked
// because both would touch the same bundle or installed artifact.
type executor struct {
	mu   sync.Mutex
	runs map[runKey]*activeRun
}

func newExecutor() *executor {
	return &executor{
		mu:   sync.Mutex{},
		runs: map[runKey]*activeRun{},
	}
}

// startOrAttach returns the exact in-flight run when one exists, otherwise it
// starts job as a new run. The returned run's broadcaster streams the run's
// events to any number of subscribers. A different operation for the same
// target is rejected with a same-target conflict.
func (e *executor) startOrAttach(ctx context.Context, operation string, target string, job operationJob) (*activeRun, error) {
	return e.startOrAttachWithCancellation(ctx, operation, target, job, false)
}

// startOrAttachCancelable preserves ctx cancellation for daemon upgrade jobs
// that are allowed to stop early and retry later.
func (e *executor) startOrAttachCancelable(ctx context.Context, target string, job operationJob) (*activeRun, error) {
	return e.startOrAttachWithCancellation(ctx, "upgrade", target, job, true)
}

func (e *executor) startOrAttachWithCancellation(
	ctx context.Context,
	operation string,
	target string,
	job operationJob,
	cancelable bool,
) (*activeRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := runKey{operation: operation, target: target}
	if run, ok := e.runs[key]; ok {
		if run.cancelable != cancelable {
			return nil, &runCancellationConflictError{
				Operation:           operation,
				Target:              target,
				ActiveCancelable:    run.cancelable,
				RequestedCancelable: cancelable,
			}
		}
		return run, nil
	}
	for _, run := range e.runs {
		if run.target == target {
			return nil, &sameTargetConflictError{
				ActiveOperation:    run.operation,
				ActiveTarget:       run.target,
				RequestedOperation: operation,
				RequestedTarget:    target,
			}
		}
	}
	run := &activeRun{
		operation:   operation,
		target:      target,
		cancelable:  cancelable,
		broadcaster: newBroadcaster(),
	}
	e.runs[key] = run
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				daemonLog.ErrorContext(ctx, "daemon.executor.job_panic",
					"err", fmt.Sprintf("panic: %v", recovered),
					"operation", run.operation,
					"target", run.target,
				)
			}
		}()
		e.runJob(ctx, run, job, cancelable)
	}()
	return run, nil
}

// activeMatching returns every in-flight run that matches the optional
// operation and target filters, ordered stably by target and operation.
func (e *executor) activeMatching(operation string, target string) []*activeRun {
	e.mu.Lock()
	defer e.mu.Unlock()
	runs := make([]*activeRun, 0, len(e.runs))
	for _, run := range e.runs {
		if operation != "" && run.operation != operation {
			continue
		}
		if target != "" && run.target != target {
			continue
		}
		runs = append(runs, run)
	}
	sortActiveRuns(runs)
	return runs
}

// activeRuns returns every in-flight run in stable order.
func (e *executor) activeRuns() []*activeRun {
	return e.activeMatching("", "")
}

func (e *executor) runJob(ctx context.Context, run *activeRun, job operationJob, cancelable bool) {
	defer func() {
		// Clear the finished run before closing its broadcaster so a subscriber
		// that returns the moment the run finishes never observes stale executor
		// state, and a fresh operation on the same target can start immediately.
		e.mu.Lock()
		delete(e.runs, runKey{operation: run.operation, target: run.target})
		e.mu.Unlock()
		run.broadcaster.finish()
	}()
	jobCtx := ctx
	if !cancelable {
		// Detach cancellation so a bundle swap finishes even if the caller's
		// context is cancelled, while keeping the context chain and its values.
		jobCtx = context.WithoutCancel(ctx)
	}
	if err := job(jobCtx, run.broadcaster.emit); err != nil {
		daemonLog.ErrorContext(jobCtx, "daemon.executor.job_failed", "err", err, "operation", run.operation, "target", run.target)
	}
}

func sortActiveRuns(runs []*activeRun) {
	sort.Slice(runs, func(i int, j int) bool {
		if runs[i].target != runs[j].target {
			return runs[i].target < runs[j].target
		}
		return runs[i].operation < runs[j].operation
	})
}
