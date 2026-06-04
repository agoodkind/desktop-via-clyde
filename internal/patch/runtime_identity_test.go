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

func TestNestedCodeSignPathsAddsMachOFilesAndSkipsMainRealAndPreserved(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "App.app")
	writeMachOFile(t, filepath.Join(appPath, "Contents/MacOS/App"))
	writeMachOFile(t, filepath.Join(appPath, "Contents/MacOS/App.real"))
	nativePath := filepath.Join(appPath, "Contents/Resources/native/upstream.node")
	writeMachOFile(t, nativePath)
	loaderPath := filepath.Join(appPath, "Contents/Frameworks/App Framework.framework/Versions/A/Helpers/app_mode_loader")
	writeMachOFile(t, loaderPath)
	preservedPath := filepath.Join(appPath, "Contents/Frameworks/Squirrel.framework/Versions/A/Squirrel")
	writeMachOFile(t, preservedPath)
	writeTextFile(t, filepath.Join(appPath, "Contents/Resources/readme.txt"))

	runner := NewRunner(context.Background(), true, io.Discard)
	target := targets.Target{
		ID:                       "fake",
		AppPath:                  appPath,
		ExecName:                 "App",
		PreservedNestedCodePaths: []string{"Contents/Frameworks/Squirrel.framework"},
	}
	paths, err := nestedCodeSignPaths(context.Background(), runner, target)
	if err != nil {
		t.Fatalf("nestedCodeSignPaths: %v", err)
	}

	for _, want := range []string{nativePath, loaderPath} {
		if !slices.Contains(paths, want) {
			t.Fatalf("missing Mach-O code path %s in %v", want, paths)
		}
	}
	for _, forbidden := range []string{
		filepath.Join(appPath, "Contents/MacOS/App"),
		filepath.Join(appPath, "Contents/MacOS/App.real"),
		preservedPath,
		filepath.Join(appPath, "Contents/Resources/readme.txt"),
	} {
		if slices.Contains(paths, forbidden) {
			t.Fatalf("unexpected code path %s in %v", forbidden, paths)
		}
	}
}

func TestIsMachOFileRecognizesThinAndUniversalMagic(t *testing.T) {
	for name, data := range map[string][]byte{
		"thin64":    {0xcf, 0xfa, 0xed, 0xfe},
		"universal": {0xca, 0xfe, 0xba, 0xbe},
	} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		got, err := isMachOFile(context.Background(), path)
		if err != nil {
			t.Fatalf("isMachOFile(%s): %v", name, err)
		}
		if !got {
			t.Fatalf("isMachOFile(%s) = false, want true", name)
		}
	}

	textPath := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(textPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write plain text: %v", err)
	}
	got, err := isMachOFile(context.Background(), textPath)
	if err != nil {
		t.Fatalf("isMachOFile(plain): %v", err)
	}
	if got {
		t.Fatal("isMachOFile(plain) = true, want false")
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

func writeMachOFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir Mach-O parent: %v", err)
	}
	data := []byte{0xcf, 0xfa, 0xed, 0xfe, 0x00, 0x00, 0x00, 0x00}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("write Mach-O file: %v", err)
	}
}

func writeTextFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir text parent: %v", err)
	}
	if err := os.WriteFile(path, []byte("not mach-o"), 0o644); err != nil {
		t.Fatalf("write text file: %v", err)
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
