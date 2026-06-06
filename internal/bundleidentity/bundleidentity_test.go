package bundleidentity

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"text/template"
)

func TestScanFindsExecutableRuntimeBundles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Codex.app")
	writeBundle(t, root, "com.openai.codex.beta", "APPL", "Codex", "Contents/MacOS/Codex")
	writeBundle(
		t,
		filepath.Join(root, "Contents/Frameworks/Codex Framework.framework"),
		"com.openai.codex.framework",
		"FMWK",
		"Codex Framework",
		"Versions/148.0.7778.179/Codex Framework",
	)
	writePlist(
		t,
		filepath.Join(root, "Contents/Frameworks/Codex Framework.framework/Versions/148.0.7778.179/Resources/Info.plist"),
		"com.openai.codex.framework",
		"FMWK",
		"Codex Framework",
	)
	writeBundle(
		t,
		filepath.Join(root, "Contents/Frameworks/Codex Framework.framework/Versions/148.0.7778.179/Helpers/Codex (GPU).app"),
		"com.openai.codex.helper",
		"APPL",
		"Codex (GPU)",
		"Contents/MacOS/Codex (GPU)",
	)
	writeBundle(
		t,
		filepath.Join(root, "Contents/Resources/CodexComputerUseAuthorizationPlugin.bundle"),
		"com.openai.sky.CUAService.AuthorizationPlugin",
		"BNDL",
		"CodexComputerUseAuthorizationPlugin",
		"Contents/MacOS/CodexComputerUseAuthorizationPlugin",
	)
	writePlist(
		t,
		filepath.Join(root, "Contents/Resources/package.Appshot.resources.bundle/Contents/Info.plist"),
		"package.Appshot.resources",
		"BNDL",
		"",
	)

	entries, err := Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	runtimeIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.RuntimeCode {
			runtimeIDs = append(runtimeIDs, entry.BundleID)
		}
	}
	for _, want := range []string{
		"com.openai.codex.beta",
		"com.openai.codex.framework",
		"com.openai.codex.helper",
		"com.openai.sky.CUAService.AuthorizationPlugin",
	} {
		if !slices.Contains(runtimeIDs, want) {
			t.Fatalf("runtime IDs missing %q: %v", want, runtimeIDs)
		}
	}
	if slices.Contains(runtimeIDs, "package.Appshot.resources") {
		t.Fatalf("resource bundle reported as runtime code: %v", runtimeIDs)
	}
}

func TestRuntimeNestedEntriesSortsChildrenBeforeParentsAndSkipsPreserved(t *testing.T) {
	entries := []Entry{
		{RootPath: "/tmp/App.app", RelativePath: ".", RuntimeCode: true},
		{RootPath: "/tmp/App.app/Contents/Frameworks/Parent.framework", RelativePath: "Contents/Frameworks/Parent.framework", RuntimeCode: true},
		{RootPath: "/tmp/App.app/Contents/Frameworks/Parent.framework/Helpers/Child.app", RelativePath: "Contents/Frameworks/Parent.framework/Helpers/Child.app", RuntimeCode: true},
		{RootPath: "/tmp/App.app/Contents/Frameworks/Squirrel.framework", RelativePath: "Contents/Frameworks/Squirrel.framework", RuntimeCode: true},
	}

	got := RuntimeNestedEntries(entries, "/tmp/App.app", []string{"Contents/Frameworks/Squirrel.framework"})
	if len(got) != 2 {
		t.Fatalf("RuntimeNestedEntries length = %d, want 2: %#v", len(got), got)
	}
	if got[0].RelativePath != "Contents/Frameworks/Parent.framework/Helpers/Child.app" {
		t.Fatalf("first nested entry = %q", got[0].RelativePath)
	}
	if got[1].RelativePath != "Contents/Frameworks/Parent.framework" {
		t.Fatalf("second nested entry = %q", got[1].RelativePath)
	}
}

func writeBundle(
	t *testing.T,
	root string,
	bundleID string,
	packageType string,
	executable string,
	executableRelPath string,
) {
	t.Helper()
	writePlist(t, filepath.Join(root, "Contents/Info.plist"), bundleID, packageType, executable)
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

func writePlist(t *testing.T, path string, bundleID string, packageType string, executable string) {
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
