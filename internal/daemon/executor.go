package daemon

import (
	"context"
	"fmt"
	"sync"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
)

// operationJob runs one operation to completion, emitting its progress events to
// emit. It runs on a background context so the operation finishes even if the
// initiating client disconnects mid-run.
type operationJob func(ctx context.Context, emit func(event *desktopviaclydev1.ProgressEvent)) error

// activeRun is one in-flight operation and its event broadcaster.
type activeRun struct {
	operation   string
	target      string
	broadcaster *broadcaster
}

// executor serializes operations so at most one runs at a time. A request that
// arrives while an operation is in flight attaches to that run's broadcaster
// instead of starting a second, which is what makes a hand-run command and the
// daemon's own upgrade tick indistinguishable and unable to overlap during a
// bundle swap.
type executor struct {
	mu      sync.Mutex
	current *activeRun
}

func newExecutor() *executor {
	return &executor{
		mu:      sync.Mutex{},
		current: nil,
	}
}

// startOrAttach returns the in-flight run when one exists, otherwise it starts
// job as the new current run. Either way the returned run's broadcaster streams
// the run's events to any number of subscribers.
func (e *executor) startOrAttach(operation string, target string, job operationJob) *activeRun {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current != nil {
		return e.current
	}
	run := &activeRun{
		operation:   operation,
		target:      target,
		broadcaster: newBroadcaster(),
	}
	e.current = run
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				daemonLog.Error("daemon.executor.job_panic",
					"err", fmt.Sprintf("panic: %v", recovered),
					"operation", run.operation,
					"target", run.target,
				)
			}
		}()
		e.runJob(run, job)
	}()
	return run
}

func (e *executor) runJob(run *activeRun, job operationJob) {
	defer func() {
		// Clear the current run before finishing the broadcaster so a subscriber
		// that returns the moment the run finishes never observes a stale
		// in-flight run, and a fresh operation can start immediately. These
		// deferred steps run during a panic unwind too, before the launching
		// goroutine's recover, so cleanup always happens.
		e.mu.Lock()
		if e.current == run {
			e.current = nil
		}
		e.mu.Unlock()
		run.broadcaster.finish()
	}()
	if err := job(context.Background(), run.broadcaster.emit); err != nil {
		daemonLog.Error("daemon.executor.job_failed", "err", err, "operation", run.operation, "target", run.target)
	}
}
