package statusreport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
