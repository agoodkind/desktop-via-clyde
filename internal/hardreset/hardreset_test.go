package hardreset

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"goodkind.io/desktop-via-clyde/internal/appguard"
	"goodkind.io/desktop-via-clyde/internal/bundlemutate"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestBuildPlanSeparatesFullIdentitySetFromTCCUtilBundleIDs(t *testing.T) {
	appPath := writeTestCodexBundle(t)

	target := targets.Target{
		ID:              "codex",
		AppPath:         appPath,
		BundleID:        "com.openai.codex.beta",
		BundleIDAliases: []string{"com.openai.codex"},
		HelperBundleIDs: []string{"com.openai.sky.CUAService", "com.openai.codex.helper"},
	}

	plan, err := BuildPlan(context.Background(), target)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, want := range []string{
		"com.openai.codex",
		"com.openai.codex.beta",
		"com.openai.codex.helper",
		"com.openai.sky.CUAService",
	} {
		if !containsString(plan.BundleIDs, want) {
			t.Fatalf("bundle IDs missing %q: %v", want, plan.BundleIDs)
		}
	}
	for _, want := range []string{
		"com.openai.codex",
		"com.openai.codex.beta",
		"com.openai.codex.helper",
		"com.openai.sky.CUAService",
	} {
		if !containsString(plan.TCCUtilBundleIDs, want) {
			t.Fatalf("tccutil bundle IDs missing %q: %v", want, plan.TCCUtilBundleIDs)
		}
	}
	for _, forbidden := range []string{
		"com.openai.codex.framework",
		"com.openai.sky.CUAService.AuthorizationPlugin",
	} {
		if containsString(plan.TCCUtilBundleIDs, forbidden) {
			t.Fatalf("tccutil bundle IDs includes non-app bundle %q: %v", forbidden, plan.TCCUtilBundleIDs)
		}
	}
}

func TestRunDryRunPrintsTCCResetCommandsAndReportOnlyAftercare(t *testing.T) {
	appPath := writeTestCodexBundle(t)
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		BundleID: "com.openai.codex.beta",
	}

	var out bytes.Buffer
	if err := Run(context.Background(), target, Options{DryRun: true, Out: &out}); err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"target=codex hard-reset",
		"dry-run: /usr/bin/tccutil reset All com.openai.codex.beta",
		"dry-run: sqlite delete user_tcc_rows",
		"dry-run: sudo sqlite delete system_tcc_rows",
		"dry-run: sqlite count tcc_rows db=user",
		"dry-run: sqlite count tcc_rows db=system",
		"dry-run: /usr/bin/killall tccd reason=after_tcc_db_cleanup",
		"dry-run: remove patch_state target=codex",
		"dry-run: artifact action=remove kind=app_bundle",
		"aftercare=report-only",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dry-run output missing %q\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"System Settings", "open ", "launchctl"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("dry-run output contains forbidden aftercare %q\n%s", forbidden, text)
		}
	}
}

func TestBuildPlanIncludesTargetDerivedArtifacts(t *testing.T) {
	home := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)
	appPath := writeTestCodexBundle(t)
	appSupportDir := filepath.Join(home, "Library", "Application Support", "Claude", "claude-code")
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		BundleID: "com.openai.codex.beta",
		Extensions: extensions.Target{
			ComputerUse: &targets.ComputerUsePolicy{
				AppPathFromHome:       ".codex/computer-use/Codex Computer Use.app",
				CacheAppGlobsFromHome: []string{".codex/plugins/cache/openai-bundled/computer-use/*/Codex Computer Use.app"},
				AuthPluginPath:        "/Library/Security/SecurityAgentPlugins/CodexComputerUseAuthorizationPlugin.bundle",
			},
			BundledCLITee: &extensions.BundledCLITeeSpec{
				AppSupportDir: appSupportDir,
				BundledCLIRel: "claude.app/Contents/MacOS/claude",
			},
		},
	}

	plan, err := BuildPlan(context.Background(), target)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	wantArtifacts := []Artifact{
		{Kind: "app_bundle", Path: appPath, Action: artifactActionRemove, Glob: false},
		{
			Kind:   "upgrade_staging",
			Path:   filepath.Join(stateHome, "clyde", "upgrade-staging", "codex-*"),
			Action: artifactActionRemove,
			Glob:   true,
		},
		{
			Kind:   "computer_use_helper",
			Path:   filepath.Join(home, ".codex", "computer-use", "Codex Computer Use.app"),
			Action: artifactActionRemove,
			Glob:   false,
		},
		{
			Kind:   "computer_use_cache_helper",
			Path:   filepath.Join(home, ".codex", "plugins", "cache", "openai-bundled", "computer-use", "*", "Codex Computer Use.app"),
			Action: artifactActionRemove,
			Glob:   true,
		},
		{
			Kind:   "computer_use_auth_plugin",
			Path:   "/Library/Security/SecurityAgentPlugins/CodexComputerUseAuthorizationPlugin.bundle",
			Action: artifactActionRemoveSudo,
			Glob:   false,
		},
		{
			Kind:   "bundled_cli_tee",
			Path:   filepath.Join(appSupportDir, "*", "claude.app", "Contents", "MacOS", "claude"),
			Action: artifactActionRestoreReal,
			Glob:   true,
		},
	}
	for _, want := range wantArtifacts {
		if !containsArtifact(plan.Artifacts, want) {
			t.Fatalf("artifact missing %#v from %#v", want, plan.Artifacts)
		}
	}
}

func TestRemovePatchStateAndResetArtifacts(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	helperPath := filepath.Join(t.TempDir(), "Codex Computer Use.app")
	stagingPath := filepath.Join(stateHome, "clyde", "upgrade-staging", "codex-1")
	teePath := filepath.Join(t.TempDir(), "claude")
	for _, path := range []string{appPath, helperPath, stagingPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(teePath), 0o755); err != nil {
		t.Fatalf("MkdirAll tee parent: %v", err)
	}
	if err := os.WriteFile(teePath, []byte("shim"), 0o755); err != nil {
		t.Fatalf("WriteFile tee shim: %v", err)
	}
	if err := os.WriteFile(teePath+".real", []byte("real"), 0o755); err != nil {
		t.Fatalf("WriteFile tee real: %v", err)
	}
	if err := state.Update(paths.StateFile(), func(ms state.MultiState) (state.MultiState, error) {
		ms.Targets["codex"] = state.TargetState{PatchedVersion: "1", PatchedAt: time.Unix(0, 0).UTC()}
		ms.Targets["claude"] = state.TargetState{PatchedVersion: "2", PatchedAt: time.Unix(0, 0).UTC()}
		return ms, nil
	}); err != nil {
		t.Fatalf("state.Update: %v", err)
	}

	var out bytes.Buffer
	opts := Options{Out: &out}
	if err := removePatchState(context.Background(), opts, "codex"); err != nil {
		t.Fatalf("removePatchState: %v", err)
	}
	if err := resetArtifacts(context.Background(), opts, []Artifact{
		{Kind: "app_bundle", Path: appPath, Action: artifactActionRemove},
		{Kind: "computer_use_helper", Path: helperPath, Action: artifactActionRemove},
		{Kind: "upgrade_staging", Path: filepath.Join(stateHome, "clyde", "upgrade-staging", "codex-*"), Action: artifactActionRemove, Glob: true},
		{Kind: "bundled_cli_tee", Path: teePath, Action: artifactActionRestoreReal},
	}); err != nil {
		t.Fatalf("resetArtifacts: %v", err)
	}
	loaded, err := state.Load(paths.StateFile())
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if _, ok := loaded.Targets["codex"]; ok {
		t.Fatalf("codex state still present: %#v", loaded.Targets)
	}
	if _, ok := loaded.Targets["claude"]; !ok {
		t.Fatalf("claude state missing after codex reset: %#v", loaded.Targets)
	}
	for _, path := range []string{appPath, helperPath, stagingPath, teePath + ".real"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("path %s stat err = %v, want not exists", path, err)
		}
	}
	data, err := os.ReadFile(teePath)
	if err != nil {
		t.Fatalf("ReadFile restored tee: %v", err)
	}
	if string(data) != "real" {
		t.Fatalf("restored tee = %q, want real", string(data))
	}
	text := out.String()
	for _, want := range []string{"patch_state_removed target=codex", "kind=app_bundle", "kind=bundled_cli_tee"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q\n%s", want, text)
		}
	}
}

type recordedCommand struct {
	name  string
	args  []string
	stdin string
}

func stubRunCommand(t *testing.T, handler func(name string, args []string, stdin string) ([]byte, error)) *[]recordedCommand {
	t.Helper()
	original := runCommand
	calls := make([]recordedCommand, 0)
	runCommand = func(_ context.Context, name string, args []string, stdin string) ([]byte, error) {
		calls = append(calls, recordedCommand{name: name, args: append([]string(nil), args...), stdin: stdin})
		return handler(name, args, stdin)
	}
	t.Cleanup(func() { runCommand = original })
	return &calls
}

func stubMutateBundle(t *testing.T, handler func(targets.Target, Options, func(context.Context) error) error) {
	t.Helper()
	original := mutateBundle
	mutateBundle = func(_ context.Context, target targets.Target, opts Options, fn func(context.Context) error) error {
		return handler(target, opts, fn)
	}
	t.Cleanup(func() { mutateBundle = original })
}

func setSystemTCCDatabasePath(t *testing.T, path string) {
	t.Helper()
	original := systemTCCDatabasePath
	systemTCCDatabasePath = path
	t.Cleanup(func() { systemTCCDatabasePath = original })
}

func TestDeleteSystemTCCRowsIssuesPrivilegedDeleteForBundleIDs(t *testing.T) {
	systemDB := filepath.Join(t.TempDir(), "system-TCC.db")
	if err := os.WriteFile(systemDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write system db: %v", err)
	}
	setSystemTCCDatabasePath(t, systemDB)
	calls := stubRunCommand(t, func(_ string, _ []string, _ string) ([]byte, error) {
		return []byte("2\n"), nil
	})

	bundleIDs := []string{"com.openai.codex", "com.openai.sky.CUAService", "org.sparkle-project.Sparkle"}
	var out bytes.Buffer
	if err := deleteSystemTCCRows(context.Background(), Options{Out: &out}, bundleIDs); err != nil {
		t.Fatalf("deleteSystemTCCRows: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 privileged command, got %d: %#v", len(*calls), *calls)
	}
	call := (*calls)[0]
	if call.name != "/usr/bin/sudo" {
		t.Fatalf("privileged delete not run via sudo: %#v", call)
	}
	if len(call.args) == 0 || call.args[0] != "/usr/bin/sqlite3" {
		t.Fatalf("sudo did not invoke sqlite3: %#v", call.args)
	}
	if !containsString(call.args, systemDB) {
		t.Fatalf("delete did not target system db path %q: %#v", systemDB, call.args)
	}
	if !strings.HasPrefix(strings.TrimSpace(call.stdin), "DELETE FROM access WHERE client IN (") {
		t.Fatalf("stdin is not a DELETE statement: %q", call.stdin)
	}
	for _, want := range bundleIDs {
		if !strings.Contains(call.stdin, "'"+want+"'") {
			t.Fatalf("delete sql missing bundle id %q: %q", want, call.stdin)
		}
	}
	if !strings.Contains(out.String(), "system_tcc_rows_deleted db="+systemDB+" deleted=2") {
		t.Fatalf("output missing system delete summary: %s", out.String())
	}
}

func TestDeleteSystemTCCRowsBestEffortOnPrivilegedFailure(t *testing.T) {
	systemDB := filepath.Join(t.TempDir(), "system-TCC.db")
	if err := os.WriteFile(systemDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write system db: %v", err)
	}
	setSystemTCCDatabasePath(t, systemDB)
	calls := stubRunCommand(t, func(_ string, _ []string, _ string) ([]byte, error) {
		return []byte("authorization denied"), errors.New("exit status 1")
	})

	var out bytes.Buffer
	if err := deleteSystemTCCRows(context.Background(), Options{Out: &out}, []string{"com.openai.codex"}); err != nil {
		t.Fatalf("deleteSystemTCCRows must be best-effort, got err: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 attempted command, got %d", len(*calls))
	}
	if !strings.Contains(out.String(), "system_tcc_rows_deleted db="+systemDB+" deleted=error") {
		t.Fatalf("output missing best-effort error status: %s", out.String())
	}
}

func TestDeleteUserTCCRowsBestEffortOnPrivacyFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userDB := filepath.Join(home, "Library", "Application Support", "com.apple.TCC", "TCC.db")
	if err := os.MkdirAll(filepath.Dir(userDB), 0o755); err != nil {
		t.Fatalf("mkdir user db parent: %v", err)
	}
	if err := os.WriteFile(userDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write user db: %v", err)
	}
	calls := stubRunCommand(t, func(_ string, _ []string, _ string) ([]byte, error) {
		return []byte("authorization denied"), errors.New("exit status 1")
	})

	var out bytes.Buffer
	if err := deleteUserTCCRows(context.Background(), Options{Out: &out}, []string{"com.openai.codex"}); err != nil {
		t.Fatalf("deleteUserTCCRows must be best-effort, got err: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 attempted command, got %d", len(*calls))
	}
	if !strings.Contains(out.String(), "user_tcc_rows_deleted db="+userDB+" deleted=error") {
		t.Fatalf("output missing best-effort error status: %s", out.String())
	}
}

func TestVerifyNoTCCRowsReportsUnknownOnCountFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userDB := filepath.Join(home, "Library", "Application Support", "com.apple.TCC", "TCC.db")
	if err := os.MkdirAll(filepath.Dir(userDB), 0o755); err != nil {
		t.Fatalf("mkdir user db parent: %v", err)
	}
	if err := os.WriteFile(userDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write user db: %v", err)
	}
	systemDB := filepath.Join(t.TempDir(), "system-TCC.db")
	if err := os.WriteFile(systemDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write system db: %v", err)
	}
	setSystemTCCDatabasePath(t, systemDB)
	stubRunCommand(t, func(_ string, _ []string, _ string) ([]byte, error) {
		return []byte("authorization denied"), errors.New("exit status 1")
	})

	var out bytes.Buffer
	if err := verifyNoTCCRows(context.Background(), Options{Out: &out}, []string{"com.openai.codex"}); err != nil {
		t.Fatalf("verifyNoTCCRows must report unknown counts, got err: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"tcc_rows_remaining db=user count=unknown reason=count_error",
		"tcc_rows_remaining db=system count=unknown reason=count_error",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q\n%s", want, text)
		}
	}
}

func TestRunDeletesUserAndSystemTCCRowsAndVerifiesClean(t *testing.T) {
	home := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)

	userDB := filepath.Join(home, "Library", "Application Support", "com.apple.TCC", "TCC.db")
	if err := os.MkdirAll(filepath.Dir(userDB), 0o755); err != nil {
		t.Fatalf("mkdir user db parent: %v", err)
	}
	if err := os.WriteFile(userDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write user db: %v", err)
	}
	systemDB := filepath.Join(t.TempDir(), "system-TCC.db")
	if err := os.WriteFile(systemDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write system db: %v", err)
	}
	setSystemTCCDatabasePath(t, systemDB)

	calls := stubRunCommand(t, func(_ string, args []string, stdin string) ([]byte, error) {
		if strings.Contains(stdin, "DELETE FROM access") {
			return []byte("1\n"), nil
		}
		if len(args) > 0 && strings.Contains(args[len(args)-1], "count(*)") {
			return []byte("0\n"), nil
		}
		return []byte(""), nil
	})

	if err := state.Update(paths.StateFile(), func(ms state.MultiState) (state.MultiState, error) {
		ms.Targets["codex"] = state.TargetState{PatchedVersion: "1", PatchedAt: time.Unix(0, 0).UTC()}
		return ms, nil
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	appPath := writeTestCodexBundle(t)
	target := targets.Target{
		ID:              "codex",
		AppPath:         appPath,
		BundleID:        "com.openai.codex.beta",
		BundleIDAliases: []string{"com.openai.codex"},
		HelperBundleIDs: []string{"com.openai.sky.CUAService", "org.sparkle-project.Sparkle", "com.openai.codex.helper"},
	}

	var out bytes.Buffer
	if err := Run(context.Background(), target, Options{Out: &out}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	userDeletes := 0
	systemDeletes := 0
	for _, call := range *calls {
		if !strings.Contains(call.stdin, "DELETE FROM access") {
			continue
		}
		switch {
		case call.name == "/usr/bin/sudo" && containsString(call.args, systemDB):
			systemDeletes++
			assertHelpersCovered(t, "system", call.stdin)
		case call.name == "/usr/bin/sqlite3" && containsString(call.args, userDB):
			userDeletes++
			assertHelpersCovered(t, "user", call.stdin)
		}
	}
	if userDeletes != 1 {
		t.Fatalf("want exactly 1 user-db delete, got %d", userDeletes)
	}
	if systemDeletes != 1 {
		t.Fatalf("want exactly 1 system-db delete, got %d", systemDeletes)
	}

	text := out.String()
	for _, want := range []string{
		"user_tcc_rows_deleted db=" + userDB + " deleted=1",
		"system_tcc_rows_deleted db=" + systemDB + " deleted=1",
		"tcc_rows_remaining db=user count=0",
		"tcc_rows_remaining db=system count=0",
		"aftercare=report-only",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Run output missing %q\n%s", want, text)
		}
	}
}

func TestRunClosesAppBeforeTCCAndArtifactRemoval(t *testing.T) {
	home := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)

	userDB := filepath.Join(home, "Library", "Application Support", "com.apple.TCC", "TCC.db")
	if err := os.MkdirAll(filepath.Dir(userDB), 0o755); err != nil {
		t.Fatalf("mkdir user db parent: %v", err)
	}
	if err := os.WriteFile(userDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write user db: %v", err)
	}
	systemDB := filepath.Join(t.TempDir(), "system-TCC.db")
	if err := os.WriteFile(systemDB, []byte("db"), 0o644); err != nil {
		t.Fatalf("write system db: %v", err)
	}
	setSystemTCCDatabasePath(t, systemDB)
	stubRunCommand(t, func(_ string, args []string, stdin string) ([]byte, error) {
		if strings.Contains(stdin, "DELETE FROM access") {
			return []byte("1\n"), nil
		}
		if len(args) > 0 && strings.Contains(args[len(args)-1], "count(*)") {
			return []byte("0\n"), nil
		}
		return []byte(""), nil
	})

	closeCalls := 0
	stubMutateBundle(t, func(target targets.Target, opts Options, fn func(context.Context) error) error {
		closeCalls++
		if !opts.CloseBeforeMutate {
			t.Fatal("CloseBeforeMutate = false, want true")
		}
		_, writeErr := opts.Out.Write([]byte("hardreset_app_closed target=" + target.ID + "\n"))
		if writeErr != nil {
			return writeErr
		}
		return fn(context.Background())
	})

	appPath := writeTestCodexBundle(t)
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		BundleID: "com.openai.codex.beta",
	}
	var out bytes.Buffer
	if err := Run(context.Background(), target, Options{Out: &out, CloseBeforeMutate: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
	text := out.String()
	closeIndex := strings.Index(text, "hardreset_app_closed target=codex")
	tccIndex := strings.Index(text, "tccutil_reset_all")
	artifactIndex := strings.Index(text, "artifact action=remove kind=app_bundle path="+appPath+" status=removed")
	if closeIndex == -1 {
		t.Fatalf("output missing close proof\n%s", text)
	}
	if tccIndex == -1 {
		t.Fatalf("output missing tccutil summary\n%s", text)
	}
	if artifactIndex == -1 {
		t.Fatalf("output missing app bundle removal\n%s", text)
	}
	if closeIndex > tccIndex {
		t.Fatalf("close proof occurred after TCC reset\n%s", text)
	}
	if closeIndex > artifactIndex {
		t.Fatalf("close proof occurred after app removal\n%s", text)
	}
}

func TestOperationClosesBeforeMutateByDefault(t *testing.T) {
	appPath := writeTestCodexBundle(t)
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		BundleID: "com.openai.codex.beta",
	}
	flags := operations.NewFlagValues()
	flags.SetBool("dry-run", true)

	closeCalls := 0
	var closeDryRun bool
	stubMutateBundle(t, func(_ targets.Target, opts Options, fn func(context.Context) error) error {
		closeCalls++
		closeDryRun = opts.DryRun
		if !opts.CloseBeforeMutate {
			t.Fatal("CloseBeforeMutate = false, want true")
		}
		return fn(context.Background())
	})

	var out bytes.Buffer
	err := Operation(context.Background(), operations.Request{
		App:        &target,
		Capability: AppHardResetCapability,
		Flags:      flags,
		Out:        &out,
	})
	if err != nil {
		t.Fatalf("Operation: %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", closeCalls)
	}
	if !closeDryRun {
		t.Fatal("close gate did not inherit dry-run flag")
	}
}

func TestRunDefersWhenMutationGateReportsAppRunning(t *testing.T) {
	appPath := writeTestCodexBundle(t)
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		BundleID: "com.openai.codex.beta",
	}

	originalRunBundleMutate := runBundleMutate
	runBundleMutate = func(
		context.Context,
		targets.Target,
		bundlemutate.Policy,
		bundlemutate.Options,
		func(context.Context) error,
	) error {
		return appguard.ErrAppRunning
	}
	t.Cleanup(func() {
		runBundleMutate = originalRunBundleMutate
	})

	var out bytes.Buffer
	err := Run(context.Background(), target, Options{Out: &out})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "deferred: target=codex app running at mutation time; retry when closed") {
		t.Fatalf("output missing deferred note\n%s", out.String())
	}
}

func TestRunReturnsErrAppRunningWhenCloseModeCannotCloseTarget(t *testing.T) {
	appPath := writeTestCodexBundle(t)
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		BundleID: "com.openai.codex.beta",
	}

	originalRunBundleMutate := runBundleMutate
	runBundleMutate = func(
		context.Context,
		targets.Target,
		bundlemutate.Policy,
		bundlemutate.Options,
		func(context.Context) error,
	) error {
		return appguard.ErrAppRunning
	}
	t.Cleanup(func() {
		runBundleMutate = originalRunBundleMutate
	})

	var out bytes.Buffer
	err := Run(context.Background(), target, Options{
		Out:               &out,
		CloseBeforeMutate: true,
	})
	if !errors.Is(err, appguard.ErrAppRunning) {
		t.Fatalf("Run error = %v, want ErrAppRunning", err)
	}
	if strings.Contains(out.String(), "deferred:") {
		t.Fatalf("output unexpectedly contains deferred note\n%s", out.String())
	}
}

func assertHelpersCovered(t *testing.T, label string, sql string) {
	t.Helper()
	for _, want := range []string{
		"com.openai.codex",
		"com.openai.codex.beta",
		"com.openai.sky.CUAService",
		"org.sparkle-project.Sparkle",
		"com.openai.codex.helper",
	} {
		if !strings.Contains(sql, "'"+want+"'") {
			t.Fatalf("%s delete sql missing helper bundle %q: %s", label, want, sql)
		}
	}
}

func writeTestCodexBundle(t *testing.T) string {
	t.Helper()
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	writeTestBundle(t, appPath, "com.openai.codex.beta", "APPL", "Codex", "Contents/MacOS/Codex")
	writeTestBundle(
		t,
		filepath.Join(appPath, "Contents/Frameworks/Codex Framework.framework"),
		"com.openai.codex.framework",
		"FMWK",
		"Codex Framework",
		"Versions/Current/Codex Framework",
	)
	writeTestBundle(
		t,
		filepath.Join(appPath, "Contents/Frameworks/Codex Framework.framework/Helpers/Codex Helper.app"),
		"com.openai.codex.helper",
		"APPL",
		"Codex Helper",
		"Contents/MacOS/Codex Helper",
	)
	writeTestBundle(
		t,
		filepath.Join(appPath, "Contents/Resources/CodexComputerUseAuthorizationPlugin.bundle"),
		"com.openai.sky.CUAService.AuthorizationPlugin",
		"BNDL",
		"CodexComputerUseAuthorizationPlugin",
		"Contents/MacOS/CodexComputerUseAuthorizationPlugin",
	)
	writeTestBundle(
		t,
		filepath.Join(appPath, "Contents/Resources/Codex Computer Use.app"),
		"com.openai.sky.CUAService",
		"APPL",
		"SkyComputerUseService",
		"Contents/MacOS/SkyComputerUseService",
	)
	return appPath
}

// renderBundleInfoPlist loads the bundle Info.plist template from testdata and
// substitutes the supplied values, keeping the plist XML out of the Go source.
func renderBundleInfoPlist(t *testing.T, data map[string]string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "bundle-info.plist.tmpl"))
	if err != nil {
		t.Fatalf("read bundle-info plist template: %v", err)
	}
	tmpl, err := template.New("bundle-info").Parse(string(raw))
	if err != nil {
		t.Fatalf("parse bundle-info plist template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute bundle-info plist template: %v", err)
	}
	return buf.String()
}

func writeTestBundle(t *testing.T, root string, bundleID string, packageType string, executable string, executableRelPath string) {
	t.Helper()
	infoPath := filepath.Join(root, "Contents", "Info.plist")
	if packageType == "FMWK" {
		infoPath = filepath.Join(root, "Resources", "Info.plist")
	}
	if err := os.MkdirAll(filepath.Dir(infoPath), 0o755); err != nil {
		t.Fatalf("mkdir plist parent: %v", err)
	}
	body := renderBundleInfoPlist(t, map[string]string{
		"BundleID":    bundleID,
		"PackageType": packageType,
		"Executable":  executable,
	})
	if err := os.WriteFile(infoPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	executablePath := filepath.Join(root, filepath.FromSlash(executableRelPath))
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatalf("mkdir executable parent: %v", err)
	}
	if err := os.WriteFile(executablePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsArtifact(values []Artifact, want Artifact) bool {
	for _, value := range values {
		if value.Kind == want.Kind && value.Path == want.Path && value.Action == want.Action && value.Glob == want.Glob {
			return true
		}
	}
	return false
}
