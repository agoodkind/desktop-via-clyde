package daemon

import (
	"context"
	"fmt"
	"maps"
	"os/exec"
	"strings"
	"sync"
	"time"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

const (
	// baseTickInterval is the relaxed cadence when nothing is waiting on an open
	// app.
	baseTickInterval = 6 * time.Hour
	// fastTickInterval is the cadence used after a sweep deferred a target whose
	// app was open, so the updater catches the app shortly after it closes.
	fastTickInterval = 30 * time.Minute
)

// targetCheck is the last upgrade-check result for one target.
type targetCheck struct {
	currentVersion   string
	availableVersion string
	updateAvailable  bool
	appRunning       bool
	outcome          string
	checkedAtUnix    int64
}

// updaterState holds the tick loop's observable state for GetUpdaterStatus.
type updaterState struct {
	mu           sync.Mutex
	lastTickUnix int64
	nextTickUnix int64
	intervalSec  int64
	checks       map[string]targetCheck
}

func newUpdaterState() *updaterState {
	return &updaterState{
		mu:           sync.Mutex{},
		lastTickUnix: 0,
		nextTickUnix: 0,
		intervalSec:  0,
		checks:       map[string]targetCheck{},
	}
}

func (s *updaterState) recordCheck(target string, check targetCheck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[target] = check
}

func (s *updaterState) setTiming(lastUnix int64, nextUnix int64, intervalSec int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastTickUnix = lastUnix
	s.nextTickUnix = nextUnix
	s.intervalSec = intervalSec
}

// updaterSnapshot is an immutable copy of the updater state for one read.
type updaterSnapshot struct {
	lastTickUnix int64
	nextTickUnix int64
	intervalSec  int64
	checks       map[string]targetCheck
}

func (s *updaterState) snapshot() updaterSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	checks := make(map[string]targetCheck, len(s.checks))
	maps.Copy(checks, s.checks)
	return updaterSnapshot{
		lastTickUnix: s.lastTickUnix,
		nextTickUnix: s.nextTickUnix,
		intervalSec:  s.intervalSec,
		checks:       checks,
	}
}

// ticker runs the adaptive upgrade sweep. Its checkUpdate, appRunning, and
// runUpgrade seams are fields so tests can drive the decision logic without
// network, processes, or real upgrades.
type ticker struct {
	exec        *executor
	state       *updaterState
	checkUpdate func(ctx context.Context, target targets.Target) (upgrade.UpdateCheck, error)
	appRunning  func(ctx context.Context, target targets.Target) bool
	runUpgrade  func(ctx context.Context, targetID string)
}

func newTicker(operationExecutor *executor, state *updaterState) *ticker {
	tick := &ticker{
		exec:        operationExecutor,
		state:       state,
		checkUpdate: defaultCheckUpdate,
		appRunning:  appRunning,
		runUpgrade:  nil,
	}
	tick.runUpgrade = tick.runUpgradeThroughExecutor
	return tick
}

// loop runs the sweep on load and then on the adaptive interval until ctx is
// cancelled.
func (t *ticker) loop(ctx context.Context) {
	for {
		deferred := t.sweep(ctx)
		if ctx.Err() != nil {
			return
		}
		interval := tickInterval(deferred)
		now := clock.Now()
		t.state.setTiming(now.Unix(), now.Add(interval).Unix(), int64(interval.Seconds()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// sweep checks every app target once and returns whether any target was deferred
// because its app was open with an update waiting.
func (t *ticker) sweep(ctx context.Context) bool {
	deferred := false
	for _, target := range targets.All() {
		check, err := t.checkUpdate(ctx, target)
		if err != nil {
			t.state.recordCheck(target.ID, targetCheck{
				currentVersion:   "",
				availableVersion: "",
				updateAvailable:  false,
				appRunning:       false,
				outcome:          "check-failed",
				checkedAtUnix:    clock.Now().Unix(),
			})
			continue
		}
		running := t.appRunning(ctx, target)
		entry := targetCheck{
			currentVersion:   check.CurrentVersion,
			availableVersion: check.AvailableVersion,
			updateAvailable:  check.UpdateAvailable,
			appRunning:       running,
			outcome:          "",
			checkedAtUnix:    clock.Now().Unix(),
		}
		switch {
		case !check.UpdateAvailable:
			entry.outcome = "up-to-date"
		case running:
			entry.outcome = "deferred-app-running"
			deferred = true
		default:
			entry.outcome = "upgrading"
			t.runUpgrade(ctx, target.ID)
		}
		t.state.recordCheck(target.ID, entry)
	}
	return deferred
}

// runUpgradeThroughExecutor runs an upgrade for one target through the shared
// executor and waits for it to finish, so a concurrent CLI request attaches to
// the same run rather than starting a second.
func (t *ticker) runUpgradeThroughExecutor(ctx context.Context, targetID string) {
	flags := &desktopviaclydev1.OperationFlags{
		Strings: map[string]string{},
		Bools:   map[string]bool{},
	}
	job := newOperationJob(upgrade.AppUpgradeCapability, "upgrade", targetID, "", flags)
	run := t.exec.startOrAttach(ctx, "upgrade", targetID, job)
	_ = run.broadcaster.stream(ctx, func(*desktopviaclydev1.ProgressEvent) error { return nil })
}

func defaultCheckUpdate(ctx context.Context, target targets.Target) (upgrade.UpdateCheck, error) {
	check, err := upgrade.CheckAvailable(ctx, target, "")
	if err != nil {
		daemonLog.WarnContext(ctx, "daemon.tick.check_update_failed", "err", err, "target", target.ID)
		return upgrade.UpdateCheck{}, fmt.Errorf("check update for %s: %w", target.ID, err)
	}
	return check, nil
}

// appRunning reports whether the target app's main executable has a live
// process, using a read-only pgrep exact-name match.
func appRunning(ctx context.Context, target targets.Target) bool {
	name := strings.TrimSpace(target.ExecName)
	if name == "" {
		return false
	}
	daemonLog.DebugContext(ctx, "daemon.tick.app_running.boundary", "exec", name)
	return exec.CommandContext(ctx, "/usr/bin/pgrep", "-x", name).Run() == nil
}

// tickInterval is the adaptive cadence: relaxed by default, fast after a sweep
// deferred a target waiting on an open app.
func tickInterval(deferred bool) time.Duration {
	if deferred {
		return fastTickInterval
	}
	return baseTickInterval
}
