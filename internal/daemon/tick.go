package daemon

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/appguard"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/cmdflags"
	"goodkind.io/desktop-via-clyde/internal/spec"
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
	exec          *executor
	state         *updaterState
	checkUpdate   func(ctx context.Context, target targets.Target) (upgrade.UpdateCheck, error)
	appRunning    func(ctx context.Context, target targets.Target) bool
	runUpgrade    func(ctx context.Context, targetID string) bool
	runCLIUpgrade func(ctx context.Context, program targets.CLIProgram, op spec.OperationSpec)
}

func newTicker(operationExecutor *executor, state *updaterState) *ticker {
	tick := &ticker{
		exec:          operationExecutor,
		state:         state,
		checkUpdate:   defaultCheckUpdate,
		appRunning:    appRunning,
		runUpgrade:    nil,
		runCLIUpgrade: nil,
	}
	tick.runUpgrade = tick.runUpgradeThroughExecutor
	tick.runCLIUpgrade = tick.runCLIUpgradeThroughExecutor
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
			if t.runUpgrade(ctx, target.ID) {
				entry.outcome = "deferred-app-running"
				entry.appRunning = true
				deferred = true
			} else {
				entry.outcome = "upgrading"
			}
		}
		t.state.recordCheck(target.ID, entry)
	}
	t.sweepCLIs(ctx)
	return deferred
}

// sweepCLIs upgrades every configured CLI target that declares an upgrade
// operation. CLI targets (codex-cli) have no long-lived process, so there is no
// running-process gate: a new binary is picked up on the next invocation, and
// the upgrade runs every sweep at the base cadence. It never contributes to the
// fast-cadence deferral.
func (t *ticker) sweepCLIs(ctx context.Context) {
	for _, program := range targets.AllCLIs() {
		op, ok := program.Operations["upgrade"]
		if !ok {
			continue
		}
		t.runCLIUpgrade(ctx, program, op)
		t.state.recordCheck(program.ID, targetCheck{
			currentVersion:   "",
			availableVersion: "",
			updateAvailable:  false,
			appRunning:       false,
			outcome:          "upgraded",
			checkedAtUnix:    clock.Now().Unix(),
		})
	}
}

// runUpgradeThroughExecutor runs an upgrade for one target through the shared
// executor and waits for it to finish, so a duplicate upgrade request for the
// same target attaches instead of starting a second run.
func (t *ticker) runUpgradeThroughExecutor(ctx context.Context, targetID string) bool {
	flags := &desktopviaclydev1.OperationFlags{
		Strings: map[string]string{},
		Bools:   map[string]bool{"background": true},
	}
	job := newOperationJob(upgrade.AppUpgradeCapability, "upgrade", targetID, "", flags)
	run, err := t.exec.startOrAttach(ctx, "upgrade", targetID, job)
	if err != nil {
		daemonLog.WarnContext(ctx, "daemon.tick.run_upgrade_conflict", "err", err, "target", targetID)
		return false
	}
	deferred := false
	_ = run.broadcaster.stream(ctx, func(event *desktopviaclydev1.ProgressEvent) error {
		if event.GetType() == string(clioutput.EventTargetDone) &&
			event.GetTarget() == targetID &&
			event.GetStatus() == string(clioutput.OutcomeSkipped) &&
			strings.HasPrefix(strings.TrimSpace(event.GetDetail()), "deferred:") {
			deferred = true
		}
		return nil
	})
	return deferred
}

// runCLIUpgradeThroughExecutor runs a CLI target's upgrade through the shared
// executor using the operation's default flags, which match what
// `upgrade <cli>` runs today including the fast compile build mode.
func (t *ticker) runCLIUpgradeThroughExecutor(ctx context.Context, program targets.CLIProgram, op spec.OperationSpec) {
	job := newCLIUpgradeJob(program, op.Capability, cmdflags.Defaults(op.Flags))
	run, err := t.exec.startOrAttach(ctx, "upgrade", program.ID, job)
	if err != nil {
		daemonLog.WarnContext(ctx, "daemon.tick.run_cli_upgrade_conflict", "err", err, "target", program.ID)
		return
	}
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

// appRunning reports whether the target app or helper processes are live.
func appRunning(ctx context.Context, target targets.Target) bool {
	daemonLog.DebugContext(ctx, "daemon.tick.app_running.boundary", "target", target.ID, "app_path", target.AppPath)
	return appguard.Running(ctx, target)
}

// tickInterval is the adaptive cadence: relaxed by default, fast after a sweep
// deferred a target waiting on an open app.
func tickInterval(deferred bool) time.Duration {
	if deferred {
		return fastTickInterval
	}
	return baseTickInterval
}
