package daemon

import (
	"context"

	"goodkind.io/desktop-via-clyde/internal/updateopts"
	"goodkind.io/go-makefile/selfupdate"
)

var selfUpdateSchedulerRunner = selfupdate.RunScheduler

func startSelfUpdateScheduler(ctx context.Context, stop func()) {
	go selfUpdateSchedulerRunner(ctx, selfUpdateSchedulerHooks(stop))
}

func selfUpdateSchedulerHooks(stop func()) selfupdate.SchedulerHooks {
	return selfupdate.SchedulerHooks{
		Enabled: func() bool {
			return true
		},
		Mode: func() string {
			return selfupdate.ModeApply
		},
		Options: func() selfupdate.Options {
			return updateopts.Options(updateopts.Overrides{})
		},
		StopForRelaunch: stop,
		Log:             daemonLog,
	}
}
