package patch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestNestedCodeSignPathsAddsDiscoveredRuntimeBundlesChildFirst(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "App.app")
	writeRuntimeBundle(t, appPath, "example.app", "APPL", "App", "Contents/MacOS/App")
	writeRuntimeBundle(
		t,
		filepath.Join(appPath, "Contents/Frameworks/Parent.framework"),
		"example.parent",
		"FMWK",
		"Parent",
		"Versions/A/Parent",
	)
	writePlistForRuntimeTest(
		t,
		filepath.Join(appPath, "Contents/Frameworks/Parent.framework/Versions/A/Resources/Info.plist"),
		"example.parent",
		"FMWK",
		"Parent",
	)
	writeRuntimeBundle(
		t,
		filepath.Join(appPath, "Contents/Frameworks/Parent.framework/Versions/A/Helpers/Child.app"),
		"example.child",
		"APPL",
		"Child",
		"Contents/MacOS/Child",
	)
	writeRuntimeBundle(
		t,
		filepath.Join(appPath, "Contents/Frameworks/Squirrel.framework"),
		"example.squirrel",
		"FMWK",
		"Squirrel",
		"Versions/A/Squirrel",
	)

	runner := NewRunner(context.Background(), true, io.Discard)
	target := targets.Target{
		ID:                       "fake",
		AppPath:                  appPath,
		NestedSignPaths:          []string{"Contents/Resources/plain-binary"},
		PreservedNestedCodePaths: []string{"Contents/Frameworks/Squirrel.framework"},
	}
	paths, err := nestedCodeSignPaths(context.Background(), runner, target)
	if err != nil {
		t.Fatalf("nestedCodeSignPaths: %v", err)
	}

	childPath := filepath.Join(appPath, "Contents/Frameworks/Parent.framework/Versions/A/Helpers/Child.app")
	parentPath := filepath.Join(appPath, "Contents/Frameworks/Parent.framework")
	childIndex := slices.Index(paths, childPath)
	parentIndex := slices.Index(paths, parentPath)
	if childIndex == -1 {
		t.Fatalf("missing child runtime bundle path: %v", paths)
	}
	if parentIndex == -1 {
		t.Fatalf("missing parent runtime bundle path: %v", paths)
	}
	if childIndex > parentIndex {
		t.Fatalf("child path signed after parent: %v", paths)
	}
	squirrelPath := filepath.Join(appPath, "Contents/Frameworks/Squirrel.framework")
	if slices.Contains(paths, squirrelPath) {
		t.Fatalf("preserved Squirrel framework was included: %v", paths)
	}
}

func writeRuntimeBundle(
	t *testing.T,
	root string,
	bundleID string,
	packageType string,
	executable string,
	executableRelPath string,
) {
	t.Helper()
	writePlistForRuntimeTest(t, filepath.Join(root, "Contents/Info.plist"), bundleID, packageType, executable)
	if executableRelPath == "" {
		return
	}
	executablePath := filepath.Join(root, filepath.FromSlash(executableRelPath))
	if err := os.MkdirAll(filepath.Dir(executablePath), 0o755); err != nil {
		t.Fatalf("mkdir executable parent: %v", err)
	}
	if err := os.WriteFile(executablePath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
}

func writePlistForRuntimeTest(t *testing.T, path string, bundleID string, packageType string, executable string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir plist parent: %v", err)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
<key>CFBundleIdentifier</key>
<string>` + bundleID + `</string>
<key>CFBundlePackageType</key>
<string>` + packageType + `</string>
<key>CFBundleExecutable</key>
<string>` + executable + `</string>
</dict>
</plist>
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
}
