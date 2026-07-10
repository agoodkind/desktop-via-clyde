package daemon

import (
	"context"
	"fmt"
	"maps"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/appguard"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/cmdflags"
	"goodkind.io/desktop-via-clyde/internal/operations"
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
	// cliLoadMonitorInterval is the cadence used to stop daemon-only CLI
	// upgrades when system load rises after the upgrade has started.
	cliLoadMonitorInterval = 30 * time.Second
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
	exec                   *executor
	state                  *updaterState
	checkUpdate            func(ctx context.Context, target targets.Target) (upgrade.UpdateCheck, error)
	appRunning             func(ctx context.Context, target targets.Target) bool
	runUpgrade             func(ctx context.Context, targetID string) bool
	runCLIUpgrade          func(ctx context.Context, program targets.CLIProgram, op spec.OperationSpec) bool
	checkCLILoad           func(ctx context.Context, program targets.CLIProgram) cliLoadDecision
	cliLoadMonitorInterval time.Duration
}

func newTicker(operationExecutor *executor, state *updaterState) *ticker {
	tick := &ticker{
		exec:                   operationExecutor,
		state:                  state,
		checkUpdate:            defaultCheckUpdate,
		appRunning:             appRunning,
		runUpgrade:             nil,
		runCLIUpgrade:          nil,
		checkCLILoad:           defaultCLILoadDecision,
		cliLoadMonitorInterval: cliLoadMonitorInterval,
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
	if t.sweepCLIs(ctx) {
		deferred = true
	}
	return deferred
}

// sweepCLIs upgrades every configured CLI target that declares an upgrade
// operation. Manual CLI upgrades are not routed through this path, so daemon
// load deferral only affects background maintenance.
func (t *ticker) sweepCLIs(ctx context.Context) bool {
	deferred := false
	for _, program := range targets.AllCLIs() {
		op, ok := program.Operations["upgrade"]
		if !ok {
			continue
		}
		decision := t.checkCLILoad(ctx, program)
		if decision.err != nil {
			daemonLog.WarnContext(ctx, "daemon.tick.cli_load_check_failed", "err", decision.err, "target", program.ID)
		}
		if decision.deferUpgrade {
			logCLIUpgradeDeferred(ctx, program, decision, false)
			t.state.recordCheck(program.ID, cliTargetCheck("deferred-system-load"))
			deferred = true
			continue
		}
		switch t.runCLIUpgradeWithLoadMonitor(ctx, program, op) {
		case cliUpgradeDeferred:
			t.state.recordCheck(program.ID, cliTargetCheck("deferred-system-load"))
			deferred = true
			continue
		case cliUpgradeAborted:
			continue
		case cliUpgradeCompleted:
		}
		t.state.recordCheck(program.ID, cliTargetCheck("upgraded"))
	}
	return deferred
}

func cliTargetCheck(outcome string) targetCheck {
	return targetCheck{
		currentVersion:   "",
		availableVersion: "",
		updateAvailable:  false,
		appRunning:       false,
		outcome:          outcome,
		checkedAtUnix:    clock.Now().Unix(),
	}
}

type cliUpgradeOutcome int

const (
	cliUpgradeCompleted cliUpgradeOutcome = iota
	cliUpgradeDeferred
	cliUpgradeAborted
)

func (t *ticker) runCLIUpgradeWithLoadMonitor(
	ctx context.Context,
	program targets.CLIProgram,
	op spec.OperationSpec,
) cliUpgradeOutcome {
	if !program.DaemonDeferral.Enabled {
		if !t.runCLIUpgrade(ctx, program, op) {
			return cliUpgradeAborted
		}
		if ctx.Err() != nil {
			return cliUpgradeAborted
		}
		return cliUpgradeCompleted
	}
	upgradeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	deferredCh := make(chan cliLoadDecision, 1)
	doneCh := make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				daemonLog.ErrorContext(ctx, "daemon.tick.cli_load_monitor_panic", "err", fmt.Sprintf("panic: %v", recovered), "target", program.ID)
			}
		}()
		t.monitorCLIUpgradeLoad(upgradeCtx, program, cancel, doneCh, deferredCh)
	}()

	attempted := t.runCLIUpgrade(upgradeCtx, program, op)
	close(doneCh)
	select {
	case decision := <-deferredCh:
		logCLIUpgradeDeferred(ctx, program, decision, true)
		return cliUpgradeDeferred
	default:
		if !attempted {
			return cliUpgradeAborted
		}
		if upgradeCtx.Err() != nil {
			return cliUpgradeAborted
		}
		return cliUpgradeCompleted
	}
}

func (t *ticker) monitorCLIUpgradeLoad(
	ctx context.Context,
	program targets.CLIProgram,
	cancel context.CancelFunc,
	doneCh <-chan struct{},
	deferredCh chan<- cliLoadDecision,
) {
	interval := t.cliLoadMonitorInterval
	if interval <= 0 {
		interval = cliLoadMonitorInterval
	}
	loadTicker := time.NewTicker(interval)
	defer loadTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-doneCh:
			return
		case <-loadTicker.C:
			decision := t.checkCLILoad(ctx, program)
			if decision.err != nil {
				daemonLog.WarnContext(ctx, "daemon.tick.cli_load_check_failed", "err", decision.err, "target", program.ID)
				continue
			}
			if !decision.deferUpgrade {
				continue
			}
			select {
			case deferredCh <- decision:
			default:
			}
			cancel()
			return
		}
	}
}

func logCLIUpgradeDeferred(ctx context.Context, program targets.CLIProgram, decision cliLoadDecision, inFlight bool) {
	daemonLog.DebugContext(
		ctx,
		"daemon.tick.cli_upgrade_deferred_system_load",
		"target", program.ID,
		"load_average_1m", decision.loadAverage,
		"load_per_cpu", decision.loadPerCPU,
		"threshold_per_cpu", decision.thresholdPerCPU,
		"work_hours", decision.workHours,
		"in_flight", inFlight,
		"reason", decision.reason,
	)
}

type cliLoadDecision struct {
	deferUpgrade    bool
	reason          string
	loadAverage     float64
	loadPerCPU      float64
	thresholdPerCPU float64
	workHours       bool
	err             error
}

func defaultCLILoadDecision(ctx context.Context, program targets.CLIProgram) cliLoadDecision {
	policy := program.DaemonDeferral
	if !policy.Enabled {
		return cliLoadDecision{
			deferUpgrade:    false,
			reason:          "",
			loadAverage:     0,
			loadPerCPU:      0,
			thresholdPerCPU: 0,
			workHours:       false,
			err:             nil,
		}
	}
	loadAverage, err := readOneMinuteLoadAverage(ctx)
	if err != nil {
		return cliLoadDecision{
			deferUpgrade:    false,
			reason:          "",
			loadAverage:     0,
			loadPerCPU:      0,
			thresholdPerCPU: 0,
			workHours:       false,
			err:             err,
		}
	}
	return buildCLILoadDecision(clock.Now(), policy, loadAverage, runtime.NumCPU())
}

func buildCLILoadDecision(
	now time.Time,
	policy targets.CLIDaemonDeferralPolicy,
	loadAverage float64,
	cpuCount int,
) cliLoadDecision {
	if cpuCount < 1 {
		cpuCount = 1
	}
	loadPerCPU := loadAverage / float64(cpuCount)
	workHours := inCLIWorkHours(now, policy)
	threshold := policy.LoadThresholdPerCPU
	if workHours {
		threshold = policy.WorkHoursLoadThresholdPerCPU
	}
	decision := cliLoadDecision{
		deferUpgrade:    loadPerCPU >= threshold,
		reason:          fmt.Sprintf("load %.2f per cpu >= threshold %.2f", loadPerCPU, threshold),
		loadAverage:     loadAverage,
		loadPerCPU:      loadPerCPU,
		thresholdPerCPU: threshold,
		workHours:       workHours,
		err:             nil,
	}
	if !decision.deferUpgrade {
		decision.reason = fmt.Sprintf("load %.2f per cpu < threshold %.2f", loadPerCPU, threshold)
	}
	return decision
}

func inCLIWorkHours(now time.Time, policy targets.CLIDaemonDeferralPolicy) bool {
	if !weekdayMatches(now.Weekday(), policy.WorkHoursWeekdays) {
		return false
	}
	startMinute, startOK := parseClockMinute(policy.WorkHoursStart)
	endMinute, endOK := parseClockMinute(policy.WorkHoursEnd)
	if !startOK || !endOK || startMinute == endMinute {
		return false
	}
	nowMinute := now.Hour()*60 + now.Minute()
	if startMinute < endMinute {
		return nowMinute >= startMinute && nowMinute < endMinute
	}
	return nowMinute >= startMinute || nowMinute < endMinute
}

func weekdayMatches(day time.Weekday, weekdays []string) bool {
	current := strings.ToLower(day.String())
	return slices.Contains(weekdays, current)
}

func parseClockMinute(value string) (int, bool) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, false
	}
	return parsed.Hour()*60 + parsed.Minute(), true
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
func (t *ticker) runCLIUpgradeThroughExecutor(ctx context.Context, program targets.CLIProgram, op spec.OperationSpec) bool {
	job := newCLIUpgradeJob(program, op.Capability, daemonCLIUpgradeFlags(op))
	run, err := t.exec.startOrAttachCancelable(ctx, program.ID, job)
	if err != nil {
		daemonLog.WarnContext(ctx, "daemon.tick.run_cli_upgrade_conflict", "err", err, "target", program.ID)
		return false
	}
	completed := false
	failed := false
	if err := run.broadcaster.stream(ctx, func(event *desktopviaclydev1.ProgressEvent) error {
		if event.GetType() == string(clioutput.EventTargetDone) && event.GetTarget() == program.ID {
			completed = true
			failed = event.GetStatus() == string(clioutput.OutcomeFailed)
		}
		return nil
	}); err != nil {
		daemonLog.WarnContext(ctx, "daemon.tick.run_cli_upgrade_stream_failed", "err", err, "target", program.ID)
		return false
	}
	return completed && !failed
}

func daemonCLIUpgradeFlags(op spec.OperationSpec) operations.FlagValues {
	flags := cmdflags.Defaults(op.Flags)
	flags.SetBool("no-sccache", true)
	return flags
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
