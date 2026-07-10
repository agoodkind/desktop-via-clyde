package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
)

func collectStream(t *testing.T, run *activeRun) []string {
	t.Helper()
	var types []string
	if err := run.broadcaster.stream(context.Background(), func(event *desktopviaclydev1.ProgressEvent) error {
		types = append(types, event.GetType())
		return nil
	}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	return types
}

func TestBroadcasterReplaysHistoryToLateSubscriber(t *testing.T) {
	broadcast := newBroadcaster()
	broadcast.emit(&desktopviaclydev1.ProgressEvent{Type: "a"})
	broadcast.emit(&desktopviaclydev1.ProgressEvent{Type: "b"})
	broadcast.finish()

	var types []string
	err := broadcast.stream(context.Background(), func(event *desktopviaclydev1.ProgressEvent) error {
		types = append(types, event.GetType())
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if got := strings.Join(types, ","); got != "a,b" {
		t.Fatalf("replayed types = %q, want a,b", got)
	}
}

func TestBroadcasterStreamStopsOnCancelledContext(t *testing.T) {
	broadcast := newBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sent := false
	err := broadcast.stream(ctx, func(*desktopviaclydev1.ProgressEvent) error {
		sent = true
		return nil
	})
	if err != nil {
		t.Fatalf("stream(cancelled) err = %v, want nil", err)
	}
	if sent {
		t.Fatal("send called for an empty cancelled stream")
	}
}

func TestExecutorSecondRequestAttachesToInflightRun(t *testing.T) {
	exec := newExecutor()
	started := make(chan struct{})
	release := make(chan struct{})
	first := func(_ context.Context, emit func(*desktopviaclydev1.ProgressEvent)) error {
		close(started)
		<-release
		emit(&desktopviaclydev1.ProgressEvent{Type: "step_done", Detail: "hi"})
		return nil
	}

	run1, err := exec.startOrAttach(context.Background(), "upgrade", "demo", first)
	if err != nil {
		t.Fatalf("startOrAttach(first): %v", err)
	}
	<-started
	run2, err := exec.startOrAttach(context.Background(), "upgrade", "demo", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		t.Error("second job must not run while one is in flight")
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttach(second): %v", err)
	}
	if run1 != run2 {
		t.Fatal("second startOrAttach did not attach to the in-flight run")
	}

	close(release)
	types := collectStream(t, run1)
	if got := strings.Join(types, ","); got != "step_done" {
		t.Fatalf("streamed types = %q, want step_done", got)
	}
}

func TestExecutorIdleAfterRunCompletes(t *testing.T) {
	exec := newExecutor()
	run, err := exec.startOrAttach(context.Background(), "patch", "demo", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttach: %v", err)
	}
	collectStream(t, run)
	if exec.startOrAttachIsBusy() {
		t.Fatal("executor still reports an in-flight run after completion")
	}
}

func TestExecutorCancelableRunReceivesContextCancellation(t *testing.T) {
	exec := newExecutor()
	started := make(chan struct{})
	cancelled := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	run, err := exec.startOrAttachCancelable(ctx, "codex-cli", func(ctx context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttachCancelable: %v", err)
	}
	<-started
	cancel()
	collectStream(t, run)
	select {
	case <-cancelled:
	default:
		t.Fatal("cancelable job did not receive context cancellation")
	}
}

func TestExecutorCancelableAttachRejectsNonCancelableRun(t *testing.T) {
	exec := newExecutor()
	started := make(chan struct{})
	release := make(chan struct{})
	_, err := exec.startOrAttach(context.Background(), "upgrade", "codex-cli", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttach: %v", err)
	}
	<-started

	_, err = exec.startOrAttachCancelable(context.Background(), "codex-cli", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		t.Fatal("cancelable attach must not start a second job")
		return nil
	})
	var conflictErr *runCancellationConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("startOrAttachCancelable err = %v, want runCancellationConflictError", err)
	}
	if conflictErr.ActiveCancelable {
		t.Fatalf("conflict active cancelable = %t, want false", conflictErr.ActiveCancelable)
	}
	if !conflictErr.RequestedCancelable {
		t.Fatalf("conflict requested cancelable = %t, want true", conflictErr.RequestedCancelable)
	}
	close(release)
}

func TestExecutorNonCancelableAttachRejectsCancelableRun(t *testing.T) {
	exec := newExecutor()
	started := make(chan struct{})
	release := make(chan struct{})
	_, err := exec.startOrAttachCancelable(context.Background(), "codex-cli", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttachCancelable: %v", err)
	}
	<-started

	_, err = exec.startOrAttach(context.Background(), "upgrade", "codex-cli", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		t.Fatal("non-cancelable attach must not start a second job")
		return nil
	})
	var conflictErr *runCancellationConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("startOrAttach err = %v, want runCancellationConflictError", err)
	}
	if !conflictErr.ActiveCancelable {
		t.Fatalf("conflict active cancelable = %t, want true", conflictErr.ActiveCancelable)
	}
	if conflictErr.RequestedCancelable {
		t.Fatalf("conflict requested cancelable = %t, want false", conflictErr.RequestedCancelable)
	}
	close(release)
}

func TestExecutorDistinctTargetsRunConcurrently(t *testing.T) {
	exec := newExecutor()
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	release := make(chan struct{})

	firstRun, err := exec.startOrAttach(context.Background(), "upgrade", "codex", func(_ context.Context, emit func(*desktopviaclydev1.ProgressEvent)) error {
		close(firstStarted)
		<-release
		emit(&desktopviaclydev1.ProgressEvent{Type: "step_done", Target: "codex"})
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttach(first): %v", err)
	}
	<-firstStarted
	secondRun, err := exec.startOrAttach(context.Background(), "upgrade", "codex-cli", func(_ context.Context, emit func(*desktopviaclydev1.ProgressEvent)) error {
		close(secondStarted)
		<-release
		emit(&desktopviaclydev1.ProgressEvent{Type: "step_done", Target: "codex-cli"})
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttach(second): %v", err)
	}
	if firstRun == secondRun {
		t.Fatal("distinct targets attached to the same run")
	}
	<-secondStarted
	close(release)

	firstTypes := collectStream(t, firstRun)
	secondTypes := collectStream(t, secondRun)
	if got := strings.Join(firstTypes, ","); got != "step_done" {
		t.Fatalf("first stream = %q, want step_done", got)
	}
	if got := strings.Join(secondTypes, ","); got != "step_done" {
		t.Fatalf("second stream = %q, want step_done", got)
	}
}

func TestExecutorRejectsDifferentMutationForSameTarget(t *testing.T) {
	exec := newExecutor()
	started := make(chan struct{})
	release := make(chan struct{})

	_, err := exec.startOrAttach(context.Background(), "upgrade", "codex", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttach(first): %v", err)
	}
	<-started
	_, err = exec.startOrAttach(context.Background(), "patch", "codex", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		t.Fatal("conflicting same-target job must not start")
		return nil
	})
	var conflictErr *sameTargetConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("startOrAttach(conflict) err = %v, want sameTargetConflictError", err)
	}
	if conflictErr.ActiveOperation != "upgrade" || conflictErr.ActiveTarget != "codex" {
		t.Fatalf("conflict active = %#v", conflictErr)
	}
	if conflictErr.RequestedOperation != "patch" || conflictErr.RequestedTarget != "codex" {
		t.Fatalf("conflict requested = %#v", conflictErr)
	}
	close(release)
}

// startOrAttachIsBusy reports whether the executor currently holds a run. It is
// a test-only probe over the unexported state.
func (e *executor) startOrAttachIsBusy() bool {
	return len(e.activeRuns()) != 0
}
