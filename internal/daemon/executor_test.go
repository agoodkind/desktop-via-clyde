package daemon

import (
	"context"
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

	run1 := exec.startOrAttach("upgrade", "demo", first)
	<-started
	run2 := exec.startOrAttach("upgrade", "demo", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		t.Error("second job must not run while one is in flight")
		return nil
	})
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
	run := exec.startOrAttach("patch", "demo", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		return nil
	})
	collectStream(t, run)
	if exec.startOrAttachIsBusy() {
		t.Fatal("executor still reports an in-flight run after completion")
	}
}

// startOrAttachIsBusy reports whether the executor currently holds a run. It is
// a test-only probe over the unexported state.
func (e *executor) startOrAttachIsBusy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.current != nil
}
