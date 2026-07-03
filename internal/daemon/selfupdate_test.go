package daemon

import (
	"context"
	"testing"
	"time"

	"goodkind.io/go-makefile/selfupdate"
)

func TestSelfUpdateSchedulerHooksUseApplyModeAndStopCallback(t *testing.T) {
	stopped := false
	hooks := selfUpdateSchedulerHooks(func() {
		stopped = true
	})

	if !hooks.Enabled() {
		t.Fatal("Enabled() = false, want true")
	}
	if hooks.Mode() != selfupdate.ModeApply {
		t.Fatalf("Mode() = %q, want %q", hooks.Mode(), selfupdate.ModeApply)
	}
	options := hooks.Options()
	if options.Config.Repo != "agoodkind/desktop-via-clyde" {
		t.Fatalf("Repo = %q, want agoodkind/desktop-via-clyde", options.Config.Repo)
	}
	if options.Config.Binary != "desktop-via-clyde" {
		t.Fatalf("Binary = %q, want desktop-via-clyde", options.Config.Binary)
	}
	hooks.StopForRelaunch()
	if !stopped {
		t.Fatal("StopForRelaunch did not call stop callback")
	}
}

func TestStartSelfUpdateSchedulerRunsLibraryScheduler(t *testing.T) {
	originalRunner := selfUpdateSchedulerRunner
	t.Cleanup(func() {
		selfUpdateSchedulerRunner = originalRunner
	})

	called := make(chan selfupdate.SchedulerHooks, 1)
	selfUpdateSchedulerRunner = func(_ context.Context, hooks selfupdate.SchedulerHooks) {
		called <- hooks
	}

	startSelfUpdateScheduler(context.Background(), func() {})

	select {
	case hooks := <-called:
		if hooks.Mode() != selfupdate.ModeApply {
			t.Fatalf("scheduler mode = %q, want %q", hooks.Mode(), selfupdate.ModeApply)
		}
	case <-time.After(time.Second):
		t.Fatal("self-update scheduler runner was not called")
	}
}
