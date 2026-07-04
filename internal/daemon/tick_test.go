package daemon

import (
	"context"
	"testing"
	"time"

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

func TestSweepUpgradesCLITargetWithoutRunningGate(t *testing.T) {
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
			},
		},
	})
	t.Cleanup(func() { config.SetCurrent(nil) })

	var upgradedCLIs []string
	tick := &ticker{
		exec:  newExecutor(),
		state: newUpdaterState(),
		checkUpdate: func(_ context.Context, _ targets.Target) (upgrade.UpdateCheck, error) {
			return upgrade.UpdateCheck{}, nil
		},
		appRunning: func(_ context.Context, _ targets.Target) bool { return true },
		runUpgrade: func(_ context.Context, _ string) bool { return false },
		runCLIUpgrade: func(_ context.Context, program targets.CLIProgram, _ spec.OperationSpec) {
			upgradedCLIs = append(upgradedCLIs, program.ID)
		},
	}

	tick.sweep(context.Background())
	if len(upgradedCLIs) != 1 || upgradedCLIs[0] != "codex-cli" {
		t.Fatalf("CLI upgrades = %v, want [codex-cli] regardless of any running gate", upgradedCLIs)
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
