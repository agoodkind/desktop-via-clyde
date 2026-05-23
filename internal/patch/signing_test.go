package patch

import "testing"

func TestCodesignRuntimeEntitlementsArgs(t *testing.T) {
	args := codesignRuntimeEntitlementsArgs("SIGN-ID", "/tmp/entitlements.plist", "/Applications/Codex.app")
	want := []string{
		"--force",
		"--sign",
		"SIGN-ID",
		"--options",
		"runtime",
		"--entitlements",
		"/tmp/entitlements.plist",
		"/Applications/Codex.app",
	}
	if !stringSlicesEqual(args, want) {
		t.Fatalf("codesignRuntimeEntitlementsArgs mismatch: got %v want %v", args, want)
	}
}

func TestCodesignRuntimeArgs(t *testing.T) {
	args := codesignRuntimeArgs("SIGN-ID", "/Applications/Codex.app")
	want := []string{
		"--force",
		"--sign",
		"SIGN-ID",
		"--options",
		"runtime",
		"/Applications/Codex.app",
	}
	if !stringSlicesEqual(args, want) {
		t.Fatalf("codesignRuntimeArgs mismatch: got %v want %v", args, want)
	}
}

func stringSlicesEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
