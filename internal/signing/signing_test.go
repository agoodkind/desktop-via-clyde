package signing

import "testing"

func TestRuntimeTimestampEntitlementsArgs(t *testing.T) {
	args := RuntimeTimestampEntitlementsArgs("SIGN-ID", "/tmp/codex.entitlements.plist", "/tmp/codex")
	want := []string{
		"--force",
		"--options",
		"runtime",
		"--timestamp",
		"--entitlements",
		"/tmp/codex.entitlements.plist",
		"--sign",
		"SIGN-ID",
		"/tmp/codex",
	}
	if !stringSlicesEqual(args, want) {
		t.Fatalf("RuntimeTimestampEntitlementsArgs mismatch: got %v want %v", args, want)
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
