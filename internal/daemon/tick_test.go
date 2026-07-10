package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

func TestTickIntervalAdaptsToDeferral(t *testing.T) {
	if got := tickInterval(false); got != 6*time.Hour {
		t.Fatalf("tickInterval(false) = %s, want 6h", got)
	}
	if got := tickInterval(true); got != 30*time.Minute {
		t.Fatalf("tickInterval(true) = %s, want 30m", got)
	}
}

func setupTwoApps(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	config.SetCurrent(&spec.Config{
		Signing: spec.SigningSpec{Identity: "id", TeamID: "TEAM123456"},
		Apps: map[string]spec.AppSpec{
			"running": {
				ID: "running", AppPath: "/Applications/Running.app", BundleID: "x.running",
				ExecName: "Running", Command: spec.CommandSpec{Use: "running"},
			},
			"closed": {
				ID: "closed", AppPath: "/Applications/Closed.app", BundleID: "x.closed",
				ExecName: "Closed", Command: spec.CommandSpec{Use: "closed"},
			},
		},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })
}

func TestSweepDefersRunningTargetAndUpgradesClosed(t *testing.T) {
	setupTwoApps(t)
	upgraded := map[string]bool{}
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{CurrentVersion: "1.0", AvailableVersion: "2.0", UpdateAvailable: true}, nil
		},
		appRunning: func(_ context.Context, target targets.Target) bool {
			return target.ID == "running"
		},
		runUpgrade: func(_ context.Context, targetID string) bool {
			upgraded[targetID] = true
			return false
		},
	}

	deferred := tick.sweep(context.Background())
	if !deferred {
		t.Fatal("sweep did not defer the running target")
	}
	if upgraded["running"] {
		t.Fatal("upgraded a target whose app was running")
	}
	if !upgraded["closed"] {
		t.Fatal("did not upgrade the closed target")
	}
}

func TestSweepUpgradesCLITargetWhenLoadIsIdle(t *testing.T) {
	setupCLITarget(t, enabledCLIDaemonDeferral())
	var upgradedCLIs []string
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(_ context.Context, program targets.CLIProgram, _ spec.OperationSpec) bool {
			upgradedCLIs = append(upgradedCLIs, program.ID)
			return true
		},
		checkCLILoad: func(_ context.Context, program targets.CLIProgram) cliLoadDecision {
			now := time.Date(2026, time.July, 6, 10, 0, 0, 0, time.Local)
			return buildCLILoadDecision(now, program.DaemonDeferral, 1.0, 4)
		},
	}

	if tick.sweep(context.Background()) {
		t.Fatal("sweep deferred while load was below the work-hours threshold")
	}
	if len(upgradedCLIs) != 1 || upgradedCLIs[0] != "codex-cli" {
		t.Fatalf("CLI upgrades = %v, want [codex-cli]", upgradedCLIs)
	}
}

func TestSweepDefersCLITargetWhenLoadIsHighOutsideWorkHours(t *testing.T) {
	setupCLITarget(t, enabledCLIDaemonDeferral())
	upgraded := false
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(_ context.Context, _ targets.CLIProgram, _ spec.OperationSpec) bool {
			upgraded = true
			return true
		},
		checkCLILoad: func(_ context.Context, program targets.CLIProgram) cliLoadDecision {
			now := time.Date(2026, time.July, 5, 10, 0, 0, 0, time.Local)
			return buildCLILoadDecision(now, program.DaemonDeferral, 4.0, 4)
		},
	}

	if !tick.sweep(context.Background()) {
		t.Fatal("sweep did not defer high-load CLI upgrade")
	}
	if upgraded {
		t.Fatal("upgraded CLI while load was at the normal threshold")
	}
	snapshot := tick.state.snapshot()
	if snapshot.checks["codex-cli"].outcome != "deferred-system-load" {
		t.Fatalf("codex-cli outcome = %q, want deferred-system-load", snapshot.checks["codex-cli"].outcome)
	}
}

func TestSweepDefersCLITargetAtWorkHoursThreshold(t *testing.T) {
	setupCLITarget(t, enabledCLIDaemonDeferral())
	upgraded := false
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(_ context.Context, _ targets.CLIProgram, _ spec.OperationSpec) bool {
			upgraded = true
			return true
		},
		checkCLILoad: func(_ context.Context, program targets.CLIProgram) cliLoadDecision {
			now := time.Date(2026, time.July, 6, 10, 0, 0, 0, time.Local)
			return buildCLILoadDecision(now, program.DaemonDeferral, 1.2, 4)
		},
	}

	if !tick.sweep(context.Background()) {
		t.Fatal("sweep did not defer at the work-hours threshold")
	}
	if upgraded {
		t.Fatal("upgraded CLI while work-hours load was at 30 percent")
	}
}

func TestSweepUpgradesCLITargetWhenDeferralDisabled(t *testing.T) {
	setupCLITarget(t, spec.CLIDaemonDeferralSpec{})
	var upgradedCLIs []string
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(_ context.Context, program targets.CLIProgram, _ spec.OperationSpec) bool {
			upgradedCLIs = append(upgradedCLIs, program.ID)
			return true
		},
		checkCLILoad: defaultCLILoadDecision,
	}

	if tick.sweep(context.Background()) {
		t.Fatal("sweep deferred with daemon deferral disabled")
	}
	if len(upgradedCLIs) != 1 || upgradedCLIs[0] != "codex-cli" {
		t.Fatalf("CLI upgrades = %v, want [codex-cli]", upgradedCLIs)
	}
}

func TestSweepUpgradesCLITargetWhenLoadReaderFails(t *testing.T) {
	setupCLITarget(t, enabledCLIDaemonDeferral())
	var upgradedCLIs []string
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(_ context.Context, program targets.CLIProgram, _ spec.OperationSpec) bool {
			upgradedCLIs = append(upgradedCLIs, program.ID)
			return true
		},
		checkCLILoad: func(context.Context, targets.CLIProgram) cliLoadDecision {
			return cliLoadDecision{err: errors.New("load unavailable")}
		},
	}

	if tick.sweep(context.Background()) {
		t.Fatal("sweep deferred after load reader failure")
	}
	if len(upgradedCLIs) != 1 || upgradedCLIs[0] != "codex-cli" {
		t.Fatalf("CLI upgrades = %v, want [codex-cli]", upgradedCLIs)
	}
}

func TestSweepDefersRunningCLIUpgradeWhenLoadRises(t *testing.T) {
	setupCLITarget(t, enabledCLIDaemonDeferral())
	loadChecks := 0
	ctxCancelled := make(chan struct{})
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(ctx context.Context, _ targets.CLIProgram, _ spec.OperationSpec) bool {
			<-ctx.Done()
			close(ctxCancelled)
			return false
		},
		checkCLILoad: func(_ context.Context, program targets.CLIProgram) cliLoadDecision {
			loadChecks++
			if loadChecks == 1 {
				now := time.Date(2026, time.July, 5, 10, 0, 0, 0, time.Local)
				return buildCLILoadDecision(now, program.DaemonDeferral, 1.0, 4)
			}
			now := time.Date(2026, time.July, 5, 10, 0, 0, 0, time.Local)
			return buildCLILoadDecision(now, program.DaemonDeferral, 4.0, 4)
		},
		cliLoadMonitorInterval: time.Millisecond,
	}

	if !tick.sweep(context.Background()) {
		t.Fatal("sweep did not defer after in-flight CLI load rose")
	}
	select {
	case <-ctxCancelled:
	default:
		t.Fatal("CLI upgrade context was not cancelled")
	}
	snapshot := tick.state.snapshot()
	if snapshot.checks["codex-cli"].outcome != "deferred-system-load" {
		t.Fatalf("codex-cli outcome = %q, want deferred-system-load", snapshot.checks["codex-cli"].outcome)
	}
}

func TestSweepDoesNotRecordCLIUpgradeWhenContextCancelled(t *testing.T) {
	setupCLITarget(t, enabledCLIDaemonDeferral())
	ctx, cancel := context.WithCancel(context.Background())
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(_ context.Context, _ targets.CLIProgram, _ spec.OperationSpec) bool {
			cancel()
			return true
		},
		checkCLILoad: func(_ context.Context, program targets.CLIProgram) cliLoadDecision {
			now := time.Date(2026, time.July, 5, 10, 0, 0, 0, time.Local)
			return buildCLILoadDecision(now, program.DaemonDeferral, 1.0, 4)
		},
	}

	if tick.sweep(ctx) {
		t.Fatal("sweep deferred after context cancellation")
	}
	snapshot := tick.state.snapshot()
	if _, ok := snapshot.checks["codex-cli"]; ok {
		t.Fatal("recorded codex-cli check after context cancellation")
	}
}

func TestSweepDoesNotRecordCLIUpgradeWhenUpgradeDoesNotStart(t *testing.T) {
	setupCLITarget(t, enabledCLIDaemonDeferral())
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(_ context.Context, _ targets.CLIProgram, _ spec.OperationSpec) bool {
			return false
		},
		checkCLILoad: func(_ context.Context, program targets.CLIProgram) cliLoadDecision {
			now := time.Date(2026, time.July, 5, 10, 0, 0, 0, time.Local)
			return buildCLILoadDecision(now, program.DaemonDeferral, 1.0, 4)
		},
	}

	if tick.sweep(context.Background()) {
		t.Fatal("sweep deferred after CLI upgrade did not start")
	}
	snapshot := tick.state.snapshot()
	if _, ok := snapshot.checks["codex-cli"]; ok {
		t.Fatal("recorded codex-cli check after CLI upgrade did not start")
	}
}

func TestDaemonCLIUpgradeFlagsDisableSccache(t *testing.T) {
	falseValue := false
	op := spec.OperationSpec{
		Flags: []spec.FlagSpec{
			{Name: "no-sccache", Binding: "no-sccache", Type: spec.FlagTypeBool, DefaultBool: &falseValue},
		},
	}

	flags := daemonCLIUpgradeFlags(op)
	if !flags.Bool("no-sccache") {
		t.Fatal("daemon CLI upgrade flags did not enable no-sccache")
	}
}

func TestRunCLIUpgradeThroughExecutorReturnsFalseOnFailedTerminalEvent(t *testing.T) {
	exec := newExecutor()
	started := make(chan struct{})
	release := make(chan struct{})
	_, err := exec.startOrAttachCancelable(context.Background(), "codex-cli", func(_ context.Context, emit func(*desktopviaclydev1.ProgressEvent)) error {
		close(started)
		<-release
		emit(&desktopviaclydev1.ProgressEvent{
			Type:   string(clioutput.EventTargetDone),
			Target: "codex-cli",
			Status: string(clioutput.OutcomeFailed),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttachCancelable: %v", err)
	}
	<-started

	tick := &ticker{exec: exec}
	program := targets.CLIProgram{ID: "codex-cli"}
	done := make(chan bool)
	go func() {
		done <- tick.runCLIUpgradeThroughExecutor(context.Background(), program, spec.OperationSpec{})
	}()
	close(release)

	if <-done {
		t.Fatal("runCLIUpgradeThroughExecutor returned true after failed terminal event")
	}
}

func setupCLITarget(t *testing.T, deferral spec.CLIDaemonDeferralSpec) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	config.SetCurrent(&spec.Config{
		Signing: spec.SigningSpec{Identity: "id", TeamID: "TEAM123456"},
		Apps:    map[string]spec.AppSpec{},
		CLIs: map[string]spec.CLISpec{
			"codex-cli": {
				ID:      "codex-cli",
				Command: spec.CommandSpec{Use: "codex-cli"},
				Operations: map[string]spec.OperationSpec{
					"upgrade": {ID: "upgrade", Use: "upgrade", Capability: "standalone-cli.install"},
				},
				DaemonDeferral: deferral,
			},
		},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })
}

func enabledCLIDaemonDeferral() spec.CLIDaemonDeferralSpec {
	return spec.CLIDaemonDeferralSpec{
		Enabled:                      true,
		LoadThresholdPerCPU:          1.0,
		WorkHoursLoadThresholdPerCPU: 0.30,
		WorkHoursStart:               "09:00",
		WorkHoursEnd:                 "17:00",
		WorkHoursWeekdays:            []string{"monday", "tuesday", "wednesday", "thursday", "friday"},
	}
}

func TestSweepNoUpdatesDoesNotDeferOrUpgrade(t *testing.T) {
	setupTwoApps(t)
	upgraded := map[string]bool{}
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{CurrentVersion: "1.0", AvailableVersion: "", UpdateAvailable: false}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, targetID string) bool {
			upgraded[targetID] = true
			return false
		},
	}

	if tick.sweep(context.Background()) {
		t.Fatal("sweep deferred even though no update was available")
	}
	if len(upgraded) != 0 {
		t.Fatalf("upgraded targets with no update available: %v", upgraded)
	}
}

func TestSweepTreatsInJobDeferralAsDeferredAppRunning(t *testing.T) {
	setupTwoApps(t)
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{CurrentVersion: "1.0", AvailableVersion: "2.0", UpdateAvailable: true}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return false },
		runUpgrade: func(_ context.Context, targetID string) bool {
			return targetID == "running"
		},
	}

	if !tick.sweep(context.Background()) {
		t.Fatal("sweep did not report in-job deferral")
	}
	snapshot := tick.state.snapshot()
	if snapshot.checks["running"].outcome != "deferred-app-running" {
		t.Fatalf("running outcome = %q, want deferred-app-running", snapshot.checks["running"].outcome)
	}
}
