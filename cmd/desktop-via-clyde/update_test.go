package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/version"
	"goodkind.io/go-makefile/selfupdate"
)

func TestUpdateCheckUsesDesktopViaClydeSelfUpdateOptions(t *testing.T) {
	originalCheck := selfUpdateCheck
	t.Cleanup(func() {
		selfUpdateCheck = originalCheck
	})

	var gotOptions selfupdate.Options
	selfUpdateCheck = func(_ context.Context, options selfupdate.Options) (selfupdate.CheckResult, error) {
		gotOptions = options
		return selfupdate.CheckResult{
			CurrentVersion:  options.Config.CurrentVersion,
			CurrentCommit:   options.Config.CurrentCommit,
			LatestTag:       "v1.2.3",
			AssetName:       "desktop-via-clyde_darwin_arm64.tar.gz",
			UpdateAvailable: true,
		}, nil
	}

	output, err := executeRootNoFixture("update", "check")
	if err != nil {
		t.Fatalf("executeRootNoFixture(update check): %v\noutput:\n%s", err, output)
	}
	assertDesktopViaClydeOptions(t, gotOptions)
	for _, want := range []string{
		"current version: " + version.Version,
		"latest tag:      v1.2.3",
		"asset:           desktop-via-clyde_darwin_arm64.tar.gz",
		"update available: yes",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("update check output missing %q\noutput:\n%s", want, output)
		}
	}
}

func TestUpdateApplyDryRunPassesDryRunOption(t *testing.T) {
	originalApply := selfUpdateApply
	t.Cleanup(func() {
		selfUpdateApply = originalApply
	})

	var gotOptions selfupdate.Options
	selfUpdateApply = func(_ context.Context, options selfupdate.Options) (selfupdate.ApplyResult, error) {
		gotOptions = options
		return selfupdate.ApplyResult{
			CheckResult: selfupdate.CheckResult{
				CurrentVersion:  options.Config.CurrentVersion,
				LatestTag:       "v1.2.3",
				UpdateAvailable: true,
			},
			DryRun: true,
		}, nil
	}

	output, err := executeRootNoFixture("update", "apply", "--dry-run")
	if err != nil {
		t.Fatalf("executeRootNoFixture(update apply --dry-run): %v\noutput:\n%s", err, output)
	}
	assertDesktopViaClydeOptions(t, gotOptions)
	if !gotOptions.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if !strings.Contains(output, "desktop-via-clyde: update apply dry run ok") {
		t.Fatalf("update apply output missing dry-run success\noutput:\n%s", output)
	}
}

func TestUpdateStatusLoadsDefaultStatePath(t *testing.T) {
	originalLoadState := selfUpdateLoadState
	t.Cleanup(func() {
		selfUpdateLoadState = originalLoadState
	})

	nextCheck := time.Date(2026, 7, 2, 12, 30, 0, 0, time.UTC)
	var gotPath string
	selfUpdateLoadState = func(path string) (selfupdate.State, error) {
		gotPath = path
		return selfupdate.State{
			NextCheckAt: nextCheck,
			LatestTag:   "v1.2.3",
			LastResult:  "check",
		}, nil
	}

	output, err := executeRootNoFixture("update", "status")
	if err != nil {
		t.Fatalf("executeRootNoFixture(update status): %v\noutput:\n%s", err, output)
	}
	if gotPath == "" {
		t.Fatal("selfUpdateLoadState path = empty, want default state path")
	}
	if !strings.Contains(output, "next check:        2026-07-02T12:30:00Z") {
		t.Fatalf("update status output missing next check\noutput:\n%s", output)
	}
	if !strings.Contains(output, "latest tag:        v1.2.3") {
		t.Fatalf("update status output missing latest tag\noutput:\n%s", output)
	}
}

func assertDesktopViaClydeOptions(t *testing.T, options selfupdate.Options) {
	t.Helper()
	if options.Config.Repo != "agoodkind/desktop-via-clyde" {
		t.Fatalf("Repo = %q, want agoodkind/desktop-via-clyde", options.Config.Repo)
	}
	if options.Config.Binary != "desktop-via-clyde" {
		t.Fatalf("Binary = %q, want desktop-via-clyde", options.Config.Binary)
	}
	if options.Config.CurrentVersion != version.Version {
		t.Fatalf("CurrentVersion = %q, want %q", options.Config.CurrentVersion, version.Version)
	}
	if options.Config.CurrentCommit != version.Commit {
		t.Fatalf("CurrentCommit = %q, want %q", options.Config.CurrentCommit, version.Commit)
	}
	if options.Config.AllowPrerelease != nil {
		t.Fatalf("AllowPrerelease = %#v, want nil", options.Config.AllowPrerelease)
	}
}
