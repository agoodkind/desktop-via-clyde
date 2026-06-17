package batchops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/composition"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/operations"
)

func TestRunWithOperationRunnerPatchSelectsConfiguredApps(t *testing.T) {
	installFixture(t)

	gotIDs := make([]string, 0)
	err := RunWithOperationRunner(context.Background(), Request{
		Operation: OperationPatch,
		DryRun:    true,
		Parallel:  1,
	}, func(_ context.Context, req operations.Request) error {
		if req.App == nil {
			return fmt.Errorf("expected app target")
		}
		if req.CLI != nil {
			return fmt.Errorf("expected nil CLI target")
		}
		if !req.Flags.Bool("dry-run") {
			return fmt.Errorf("expected dry-run flag")
		}
		gotIDs = append(gotIDs, req.App.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithOperationRunner(patch): %v", err)
	}
	sort.Strings(gotIDs)
	wantIDs := []string{"claude", "codex", "cursor"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("patch ids = %#v, want %#v", gotIDs, wantIDs)
	}
}

func TestRunWithOperationRunnerUpgradeIncludesCLIAndOverrides(t *testing.T) {
	installFixture(t)

	type observed struct {
		kind            string
		channel         string
		packageHome     string
		migrateKeychain bool
		dryRun          bool
	}
	observedByID := map[string]observed{}
	err := RunWithOperationRunner(context.Background(), Request{
		Operation:       OperationUpgrade,
		DryRun:          true,
		MigrateKeychain: true,
		Parallel:        1,
		Sets: []string{
			"cursor.channel=stable",
			"codex-cli.codex-home=/tmp/codex-home",
		},
	}, func(_ context.Context, req operations.Request) error {
		item := observed{
			channel:         req.Flags.String("channel"),
			packageHome:     req.Flags.String("package-home"),
			migrateKeychain: req.Flags.Bool("migrate-keychain"),
			dryRun:          req.Flags.Bool("dry-run"),
		}
		if req.App != nil {
			item.kind = "app"
			observedByID[req.App.ID] = item
			return nil
		}
		if req.CLI != nil {
			item.kind = "cli"
			observedByID[req.CLI.ID] = item
			return nil
		}
		return fmt.Errorf("expected app or CLI target")
	})
	if err != nil {
		t.Fatalf("RunWithOperationRunner(upgrade): %v", err)
	}
	if len(observedByID) != 4 {
		t.Fatalf("upgrade target count = %d, want 4", len(observedByID))
	}
	if observedByID["cursor"].channel != "stable" {
		t.Fatalf("cursor channel = %q, want stable", observedByID["cursor"].channel)
	}
	if observedByID["codex"].channel != "beta" {
		t.Fatalf("codex channel = %q, want beta", observedByID["codex"].channel)
	}
	if observedByID["codex-cli"].packageHome != "/tmp/codex-home" {
		t.Fatalf("codex-cli package-home = %q, want /tmp/codex-home", observedByID["codex-cli"].packageHome)
	}
	if !observedByID["claude"].migrateKeychain {
		t.Fatal("claude migrate-keychain = false, want true")
	}
	if observedByID["codex-cli"].migrateKeychain {
		t.Fatal("codex-cli migrate-keychain = true, want false")
	}
	if !observedByID["claude"].dryRun || !observedByID["codex-cli"].dryRun {
		t.Fatal("expected dry-run to propagate to every upgrade request")
	}
}

func TestRunWithOperationRunnerAggregatesFailures(t *testing.T) {
	installFixture(t)

	visited := make([]string, 0)
	var visitedMu sync.Mutex
	err := RunWithOperationRunner(context.Background(), Request{
		Operation: OperationUpgrade,
		DryRun:    true,
		Parallel:  4,
	}, func(_ context.Context, req operations.Request) error {
		id := ""
		if req.App != nil {
			id = req.App.ID
		} else if req.CLI != nil {
			id = req.CLI.ID
		}
		visitedMu.Lock()
		visited = append(visited, id)
		visitedMu.Unlock()
		if id == "codex" {
			return fmt.Errorf("boom for %s", id)
		}
		return nil
	})
	if err == nil {
		t.Fatal("RunWithOperationRunner(upgrade) unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Fatalf("error = %q, want codex", err.Error())
	}
	if len(visited) != 4 {
		t.Fatalf("visited targets = %#v, want 4 attempts", visited)
	}
}

func TestRunWithOperationRunnerHonorsParallelLimit(t *testing.T) {
	installFixture(t)

	var mu sync.Mutex
	current := 0
	maxCurrent := 0
	err := RunWithOperationRunner(context.Background(), Request{
		Operation: OperationUpgrade,
		DryRun:    true,
		Parallel:  2,
	}, func(_ context.Context, _ operations.Request) error {
		mu.Lock()
		current++
		if current > maxCurrent {
			maxCurrent = current
		}
		mu.Unlock()
		time.Sleep(25 * time.Millisecond)
		mu.Lock()
		current--
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithOperationRunner(upgrade): %v", err)
	}
	if maxCurrent < 2 {
		t.Fatalf("max parallelism = %d, want at least 2", maxCurrent)
	}
	if maxCurrent > 2 {
		t.Fatalf("max parallelism = %d, want at most 2", maxCurrent)
	}
}

func TestRunWithOperationRunnerPrefixesOutputAndPrintsSummary(t *testing.T) {
	installFixture(t)

	var out bytes.Buffer
	err := RunWithOperationRunner(context.Background(), Request{
		Out:       &out,
		Operation: OperationPatch,
		DryRun:    true,
		Parallel:  1,
		Targets:   []string{"cursor"},
	}, func(_ context.Context, req operations.Request) error {
		_, _ = req.Out.Write([]byte("hello\nworld\n"))
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithOperationRunner(patch output): %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"Patch cursor",
		"cursor queued",
		"cursor started",
		"cursor completed status=ok",
		"Result completed=1 failed=0",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, output)
		}
	}
}

func TestRunWithOperationRunnerJSONSummary(t *testing.T) {
	installFixture(t)

	var out bytes.Buffer
	err := RunWithOperationRunner(context.Background(), Request{
		Out:       &out,
		Operation: OperationPatch,
		DryRun:    true,
		Parallel:  1,
		Targets:   []string{"cursor"},
		Format:    "json",
	}, func(_ context.Context, req operations.Request) error {
		_, _ = req.Out.Write([]byte("[dry-run] hello\n"))
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithOperationRunner(patch json): %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("json output line count = %d, want 5\noutput:\n%s", len(lines), out.String())
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(lines[4]), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\nline:\n%s", err, lines[3])
	}
	if summary["type"] != "run_done" {
		t.Fatalf("summary type = %#v", summary["type"])
	}
}

func TestRunWithOperationRunnerRoutesRawOutputToTargetLog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	installFixture(t)

	var out bytes.Buffer
	err := RunWithOperationRunner(context.Background(), Request{
		Out:       &out,
		Operation: OperationPatch,
		DryRun:    true,
		Parallel:  1,
		Targets:   []string{"cursor"},
		Format:    "json",
	}, func(_ context.Context, req operations.Request) error {
		_, _ = req.Out.Write([]byte("[dry-run] friendly status\n"))
		_, _ = req.LogOut.Write([]byte("raw child output\n"))
		return nil
	})
	if err != nil {
		t.Fatalf("RunWithOperationRunner(patch json): %v", err)
	}
	if strings.Contains(out.String(), "raw child output") {
		t.Fatalf("stdout included raw child output:\n%s", out.String())
	}

	var targetStarted struct {
		Type    string `json:"type"`
		LogFile string `json:"log_file"`
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var event struct {
			Type    string `json:"type"`
			LogFile string `json:"log_file"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("unmarshal event: %v\nline:\n%s", err, line)
		}
		if event.Type == "target_started" {
			targetStarted = event
			break
		}
	}
	if targetStarted.LogFile == "" {
		t.Fatalf("target_started event missing log_file\noutput:\n%s", out.String())
	}
	body, err := os.ReadFile(targetStarted.LogFile)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", targetStarted.LogFile, err)
	}
	if string(body) != "[dry-run] friendly status\nraw child output\n" {
		t.Fatalf("target log = %q, want friendly status plus raw child output", string(body))
	}
}

func installFixture(t *testing.T) {
	t.Helper()
	if err := composition.Register(); err != nil {
		t.Fatalf("composition.Register: %v", err)
	}
	cfg, err := config.LoadPath(filepath.Join("..", "testconfig", "testdata", "current-config.toml"))
	if err != nil {
		t.Fatalf("config.LoadPath(current-config.toml): %v", err)
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() {
		config.SetCurrent(nil)
	})
}
