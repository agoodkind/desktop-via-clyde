package patch

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestPatchExtractedBundleRepairsBundledComputerUseBeforeResign(t *testing.T) {
	tg, err := targets.Lookup("codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	tg.AppPath = filepath.Join(t.TempDir(), "Codex.app")

	var out bytes.Buffer
	if err := PatchExtractedBundle(tg, BundleOptions{DryRun: true, Out: &out}); err != nil {
		t.Fatalf("PatchExtractedBundle dry-run: %v", err)
	}
	log := out.String()
	bundledHelperPath := filepath.Join(tg.AppPath, filepath.FromSlash(tg.ComputerUse.BundledAppPath))

	required := []string{
		"target=codex step 5b: repair bundled Codex Computer Use helper at " + bundledHelperPath,
		"computer-use: repair trusted sender team in " + filepath.Join(bundledHelperPath, "Contents/MacOS/SkyComputerUseService"),
		"computer-use: repair trusted parent team in " + filepath.Join(bundledHelperPath, "Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement"),
		"computer-use: sign " + bundledHelperPath + " with repaired entitlements",
	}
	for _, want := range required {
		if !strings.Contains(log, want) {
			t.Fatalf("PatchExtractedBundle log missing %q\nlog:\n%s", want, log)
		}
	}

	helperRepairIdx := strings.Index(log, "target=codex step 5b: repair bundled Codex Computer Use helper")
	resignIdx := strings.Index(log, "target=codex step 6: re-sign")
	if helperRepairIdx < 0 || resignIdx < 0 {
		t.Fatalf("expected helper repair and resign steps in log:\n%s", log)
	}
	if helperRepairIdx > resignIdx {
		t.Fatalf("helper repair ran after re-sign; log:\n%s", log)
	}
}

func TestCodexNestedSignPathsIncludeTCCActiveResourceExecutables(t *testing.T) {
	tg, err := targets.Lookup("codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}

	required := []string{
		"Contents/Resources/codex",
		"Contents/Resources/codex_chronicle",
		"Contents/Resources/node",
		"Contents/Resources/node_repl",
		"Contents/Resources/native/bare-modifier-monitor",
	}
	for _, want := range required {
		if !containsString(tg.NestedSignPaths, want) {
			t.Fatalf("codex NestedSignPaths missing %q", want)
		}
	}
}

func TestPatchDryRunScansComputerUseCacheHelpers(t *testing.T) {
	tg, err := targets.Lookup("codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}

	var out bytes.Buffer
	if err := Patch(tg, Options{
		DryRun:            true,
		NoMigrateKeychain: true,
		Out:               &out,
	}); err != nil {
		t.Fatalf("Patch dry-run: %v", err)
	}

	pattern := filepath.Join(paths.Home(), filepath.FromSlash(tg.ComputerUse.CacheAppGlobsFromHome[0]))
	want := "target=codex step 7c: scan Codex Computer Use cache helpers at " + pattern
	if !strings.Contains(out.String(), want) {
		t.Fatalf("Patch dry-run log missing %q\nlog:\n%s", want, out.String())
	}
}

func TestClaudePatchRestoresSquirrelInsteadOfResigningIt(t *testing.T) {
	tg, err := targets.Lookup("claude")
	if err != nil {
		t.Fatalf("Lookup(claude): %v", err)
	}

	var out bytes.Buffer
	if err := Patch(tg, Options{
		DryRun:            true,
		NoMigrateKeychain: true,
		Out:               &out,
	}); err != nil {
		t.Fatalf("Patch dry-run: %v", err)
	}

	log := out.String()
	restorePath := filepath.Join(tg.AppPath, "Contents", "Frameworks", "Squirrel.framework")
	if !strings.Contains(log, "target=claude step 5c: restore preserved nested code") {
		t.Fatalf("Patch dry-run log missing Squirrel restore step\nlog:\n%s", log)
	}
	if strings.Contains(log, "target=claude step 6: re-sign nested code object "+restorePath) {
		t.Fatalf("Patch dry-run log re-signs preserved Squirrel framework\nlog:\n%s", log)
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
