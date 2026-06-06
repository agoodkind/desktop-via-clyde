package patch

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"text/template"

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

func TestNestedCodeSignPathsSkipsDSYMDebugBundles(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "App.app")
	writeMachOFile(t, filepath.Join(appPath, "Contents/MacOS/App"))
	realNode := filepath.Join(appPath, "Contents/Resources/app.asar.unpacked/node_modules/node-pty/build/Release/pty.node")
	writeMachOFile(t, realNode)
	dsymDwarf := filepath.Join(appPath, "Contents/Resources/app.asar.unpacked/node_modules/node-pty/build/Release/pty.node.dSYM/Contents/Resources/DWARF/pty.node")
	writeMachOFile(t, dsymDwarf)

	runner := NewRunner(context.Background(), true, io.Discard)
	target := targets.Target{
		ID:       "fake",
		AppPath:  appPath,
		ExecName: "App",
	}
	paths, err := nestedCodeSignPaths(context.Background(), runner, target)
	if err != nil {
		t.Fatalf("nestedCodeSignPaths: %v", err)
	}
	if !slices.Contains(paths, realNode) {
		t.Fatalf("expected real .node %s to be signed; got %v", realNode, paths)
	}
	if slices.Contains(paths, dsymDwarf) {
		t.Fatalf("dSYM DWARF Mach-O %s must not be signed; got %v", dsymDwarf, paths)
	}
}

func TestNestedCodeSignPathsDiscoversTCCActiveResourceExecutables(t *testing.T) {
	// TCC-active resource executables must always be signed. They are no longer
	// hardcoded in config, so discovery must find them by Mach-O magic.
	appPath := filepath.Join(t.TempDir(), "Codex.app")
	writeMachOFile(t, filepath.Join(appPath, "Contents/MacOS/Codex (Beta)"))
	required := []string{
		"Contents/Resources/codex",
		"Contents/Resources/codex_chronicle",
		"Contents/Resources/node",
		"Contents/Resources/node_repl",
		"Contents/Resources/native/bare-modifier-monitor",
	}
	for _, rel := range required {
		writeMachOFile(t, filepath.Join(appPath, filepath.FromSlash(rel)))
	}

	runner := NewRunner(context.Background(), true, io.Discard)
	target := targets.Target{
		ID:       "codex",
		AppPath:  appPath,
		ExecName: "Codex (Beta)",
	}
	signPaths, err := nestedCodeSignPaths(context.Background(), runner, target)
	if err != nil {
		t.Fatalf("nestedCodeSignPaths: %v", err)
	}
	for _, rel := range required {
		want := filepath.Join(appPath, filepath.FromSlash(rel))
		if !slices.Contains(signPaths, want) {
			t.Fatalf("discovery missed TCC-active executable %q in %v", rel, signPaths)
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
	body := renderBundleInfoPlist(t, map[string]string{
		"BundleID":    bundleID,
		"PackageType": packageType,
		"Executable":  executable,
	})
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
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
