package statusreport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/devsign"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestBuildTargetStatusDevelopmentSigningSkipsRealBinaryRequirement(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	if err := os.MkdirAll(filepath.Join(appPath, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatalf("MkdirAll app: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appPath, "Contents", "embedded.provisionprofile"), []byte("profile"), 0o600); err != nil {
		t.Fatalf("WriteFile embedded profile: %v", err)
	}

	originalRuntimeBundleStatusesFn := runtimeBundleStatusesFn
	originalReadBundleVersionFn := readBundleVersionFn
	runtimeBundleStatusesFn = func(_ context.Context, _ targets.Target, expectLocalTeam bool) []RuntimeBundleStatus {
		if expectLocalTeam {
			t.Fatal("development-signed bundle unexpectedly required local runtime teams")
		}
		return []RuntimeBundleStatus{
			{BundleID: "com.openai.codex.beta", Path: ".", TeamID: "H3BMXM4W7H", State: string(runtimeBundleStateLocal)},
			{BundleID: "com.openai.codex.framework", Path: "Contents/Frameworks/Codex Framework.framework", TeamID: "2DC432GLL2", State: string(runtimeBundleStateVendor)},
		}
	}
	readBundleVersionFn = func(target targets.Target) string {
		if target.AppPath != appPath {
			t.Fatalf("readBundleVersion target = %q, want %q", target.AppPath, appPath)
		}
		return "4009"
	}
	t.Cleanup(func() {
		runtimeBundleStatusesFn = originalRuntimeBundleStatusesFn
		readBundleVersionFn = originalReadBundleVersionFn
	})

	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		ExecName: "Codex (Beta)",
		DevelopmentSigning: &targets.DevelopmentSigningPolicy{
			Enabled: true,
		},
	}
	multiState := state.MultiState{
		Targets: map[string]state.TargetState{
			"codex": {
				PatchedVersion: "4009",
				PatchedAt:      time.Unix(0, 0).UTC(),
				SignIdentity:   "Developer ID Application: Alex Goodkind (H3BMXM4W7H)",
			},
		},
	}

	status, err := buildTargetStatus(context.Background(), target, multiState)
	if err != nil {
		t.Fatalf("buildTargetStatus: %v", err)
	}
	if status.State != "patched" {
		t.Fatalf("state = %q, want patched", status.State)
	}
	if strings.Contains(status.Notes, ".real missing") {
		t.Fatalf("notes = %q, want no .real drift", status.Notes)
	}
	if !strings.Contains(status.Notes, "development-signing active") {
		t.Fatalf("notes = %q, want development-signing marker", status.Notes)
	}
}

func TestBuildTargetStatusShimmedBundleStillRequiresRealBinary(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Cursor.app")
	if err := os.MkdirAll(appPath, 0o755); err != nil {
		t.Fatalf("MkdirAll app: %v", err)
	}

	originalRuntimeBundleStatusesFn := runtimeBundleStatusesFn
	originalReadBundleVersionFn := readBundleVersionFn
	runtimeBundleStatusesFn = func(_ context.Context, _ targets.Target, expectLocalTeam bool) []RuntimeBundleStatus {
		if !expectLocalTeam {
			t.Fatal("shimmed bundle unexpectedly skipped local runtime team check")
		}
		return []RuntimeBundleStatus{
			{BundleID: "com.cursor.app", Path: ".", TeamID: "H3BMXM4W7H", State: string(runtimeBundleStatePatched)},
		}
	}
	readBundleVersionFn = func(target targets.Target) string {
		if target.AppPath != appPath {
			t.Fatalf("readBundleVersion target = %q, want %q", target.AppPath, appPath)
		}
		return "3.8.6"
	}
	t.Cleanup(func() {
		runtimeBundleStatusesFn = originalRuntimeBundleStatusesFn
		readBundleVersionFn = originalReadBundleVersionFn
	})

	target := targets.Target{
		ID:       "cursor",
		AppPath:  appPath,
		ExecName: "Cursor",
	}
	multiState := state.MultiState{
		Targets: map[string]state.TargetState{
			"cursor": {
				PatchedVersion: "3.8.6",
				PatchedAt:      time.Unix(0, 0).UTC(),
				SignIdentity:   "Developer ID Application: Alex Goodkind (H3BMXM4W7H)",
			},
		},
	}

	status, err := buildTargetStatus(context.Background(), target, multiState)
	if err != nil {
		t.Fatalf("buildTargetStatus: %v", err)
	}
	if status.State != "drifted" {
		t.Fatalf("state = %q, want drifted", status.State)
	}
	if !strings.Contains(status.Notes, "Cursor.real missing") {
		t.Fatalf("notes = %q, want .real drift", status.Notes)
	}
}

func TestDevelopmentSigningInjectorDriftDetectsAppLocalInjector(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		ExecName: "Codex (Beta)",
		DevelopmentSigning: &targets.DevelopmentSigningPolicy{
			Enabled:        true,
			ProxyInjection: true,
		},
	}
	if err := os.MkdirAll(filepath.Dir(devsign.AppLocalInjectorPath(target)), 0o755); err != nil {
		t.Fatalf("MkdirAll app local injector dir: %v", err)
	}
	if err := os.WriteFile(devsign.AppLocalInjectorPath(target), []byte("old"), 0o755); err != nil {
		t.Fatalf("WriteFile app local injector: %v", err)
	}

	drift := developmentSigningInjectorDrift(target)
	if !strings.Contains(drift, "stale app-local injector") {
		t.Fatalf("drift = %q, want stale app-local injector", drift)
	}
}

func TestDevelopmentSigningInjectorDriftAcceptsExternalInjector(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		ExecName: "Codex (Beta)",
		DevelopmentSigning: &targets.DevelopmentSigningPolicy{
			Enabled:        true,
			ProxyInjection: true,
		},
	}
	if err := os.MkdirAll(filepath.Join(appPath, "Contents"), 0o755); err != nil {
		t.Fatalf("MkdirAll app contents: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(devsign.InjectorPath(target)), 0o755); err != nil {
		t.Fatalf("MkdirAll injector dir: %v", err)
	}
	if err := os.WriteFile(devsign.InjectorPath(target), []byte("dylib"), 0o755); err != nil {
		t.Fatalf("WriteFile injector: %v", err)
	}
	if err := os.WriteFile(devsign.InjectorPolicyPath(target), []byte("policy"), 0o600); err != nil {
		t.Fatalf("WriteFile policy: %v", err)
	}
	writeInjectorInfoPlist(t, target)

	if drift := developmentSigningInjectorDrift(target); drift != "" {
		t.Fatalf("drift = %q, want none", drift)
	}
}

func writeInjectorInfoPlist(t *testing.T, target targets.Target) {
	t.Helper()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>LSEnvironment</key><dict>
<key>` + devsign.DyldInsertLibrariesKey + `</key><string>` + devsign.InjectorPath(target) + `</string>
<key>` + devsign.InjectorPolicyEnvKey + `</key><string>` + devsign.InjectorPolicyPath(target) + `</string>
</dict>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(target.AppPath, "Contents", "Info.plist"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile Info.plist: %v", err)
	}
}
