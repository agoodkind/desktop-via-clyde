package boundary_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenericProductionLayersDoNotNameProviders(t *testing.T) {
	root := repoRoot(t)
	paths := []string{
		"cmd/desktop-via-clyde/main.go",
		"internal/catalog",
		"internal/config",
		"internal/operations",
		"internal/patch",
		"internal/spec",
		"internal/targets",
		"internal/upgrade/upgrade.go",
		"shim/Sources/Shim/main.swift",
	}
	forbidden := []string{
		"Anysphere",
		"Claude",
		"Codex",
		"Cursor",
		"anthropic",
		"claude",
		"codex",
		"cursor",
		"oaistatic",
		"openai",
	}
	for _, relativePath := range paths {
		assertPathHasNoProviderNames(t, root, relativePath, forbidden)
	}
}

func TestGenericProductionLayersDoNotImportExtensionImplementations(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{
		"goodkind.io/desktop-via-clyde/internal/bundledclitee",
		"goodkind.io/desktop-via-clyde/internal/claudetee",
		"goodkind.io/desktop-via-clyde/internal/computeruseext",
		"goodkind.io/desktop-via-clyde/internal/codexcli",
	}
	paths := []string{
		"internal/config",
		"internal/operations",
		"internal/patch",
		"internal/spec",
		"internal/targets",
		"internal/upgrade/upgrade.go",
	}
	for _, relativePath := range paths {
		assertPathHasNoImports(t, root, relativePath, forbidden)
	}
}

func assertPathHasNoProviderNames(t *testing.T, root string, relativePath string, forbidden []string) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.IsDir() {
		assertFileHasNoProviderNames(t, path, forbidden)
		return
	}
	err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(candidate, "_test.go") {
			return nil
		}
		if filepath.Ext(candidate) != ".go" {
			return nil
		}
		assertFileHasNoProviderNames(t, candidate, forbidden)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", path, err)
	}
}

func assertFileHasNoProviderNames(t *testing.T, path string, forbidden []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := string(data)
	for _, value := range forbidden {
		if strings.Contains(body, value) {
			t.Fatalf("%s contains provider name %q", path, value)
		}
	}
}

func assertPathHasNoImports(t *testing.T, root string, relativePath string, forbidden []string) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.IsDir() {
		assertFileHasNoImports(t, path, forbidden)
		return
	}
	err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(candidate, "_test.go") {
			return nil
		}
		if filepath.Ext(candidate) != ".go" {
			return nil
		}
		assertFileHasNoImports(t, candidate, forbidden)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", path, err)
	}
}

func assertFileHasNoImports(t *testing.T, path string, forbidden []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := string(data)
	for _, value := range forbidden {
		if strings.Contains(body, `"`+value+`"`) {
			t.Fatalf("%s imports extension implementation %q", path, value)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	return filepath.Clean(filepath.Join(workingDir, "..", ".."))
}
