package patch

import (
	"context"
	"io"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

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

func TestNestedAppCodesignArgsUseTargetEntitlements(t *testing.T) {
	args := nestedCodeSignArgs("SIGN-ID", "/tmp/entitlements.plist", "/Applications/Cursor.app/Contents/Helpers/Cursor Helper (Plugin).app")
	want := []string{
		"--force",
		"--sign",
		"SIGN-ID",
		"--options",
		"runtime",
		"--entitlements",
		"/tmp/entitlements.plist",
		"/Applications/Cursor.app/Contents/Helpers/Cursor Helper (Plugin).app",
	}
	if !stringSlicesEqual(args, want) {
		t.Fatalf("nested app codesign args mismatch: got %v want %v", args, want)
	}
}

func TestNestedNonAppCodesignArgsPreserveEntitlements(t *testing.T) {
	args := nestedCodeSignArgs("SIGN-ID", "/tmp/entitlements.plist", "/Applications/Cursor.app/Contents/Frameworks/Electron Framework.framework")
	want := []string{
		"--force",
		"--sign",
		"SIGN-ID",
		"--options",
		"runtime",
		"--preserve-metadata=entitlements",
		"/Applications/Cursor.app/Contents/Frameworks/Electron Framework.framework",
	}
	if !stringSlicesEqual(args, want) {
		t.Fatalf("nested non-app codesign args mismatch: got %v want %v", args, want)
	}
}

func TestNestedEntitlementsPolicyDropsStrip(t *testing.T) {
	booleans := []string{
		"com.apple.security.cs.allow-dyld-environment-variables",
		"com.apple.security.cs.disable-library-validation",
	}
	target := targets.Target{
		ID: "cursor",
		Entitlements: &targets.EntitlementsPolicy{
			Strip:                       []string{"com.apple.security.automation.apple-events"},
			RequiredBooleanEntitlements: booleans,
		},
	}
	policy := nestedEntitlementsPolicy(target)
	if len(policy.Strip) != 0 {
		t.Fatalf("nested policy Strip = %v, want empty so helpers keep their own entitlements", policy.Strip)
	}
	if !stringSlicesEqual(policy.RequiredBooleanEntitlements, booleans) {
		t.Fatalf("nested policy RequiredBooleanEntitlements = %v, want %v", policy.RequiredBooleanEntitlements, booleans)
	}
}

func TestNestedNeedsEntitlementPropagation(t *testing.T) {
	booleans := []string{"com.apple.security.cs.allow-dyld-environment-variables"}
	cases := []struct {
		name   string
		target targets.Target
		want   bool
	}{
		{
			name: "proxy injection with required booleans propagates",
			target: targets.Target{
				Entitlements:       &targets.EntitlementsPolicy{RequiredBooleanEntitlements: booleans},
				DevelopmentSigning: &targets.DevelopmentSigningPolicy{ProxyInjection: true},
			},
			want: true,
		},
		{
			name: "no proxy injection does not propagate",
			target: targets.Target{
				Entitlements:       &targets.EntitlementsPolicy{RequiredBooleanEntitlements: booleans},
				DevelopmentSigning: &targets.DevelopmentSigningPolicy{ProxyInjection: false},
			},
			want: false,
		},
		{
			name: "nil development signing does not propagate",
			target: targets.Target{
				Entitlements: &targets.EntitlementsPolicy{RequiredBooleanEntitlements: booleans},
			},
			want: false,
		},
		{
			name: "no required booleans does not propagate",
			target: targets.Target{
				Entitlements:       &targets.EntitlementsPolicy{},
				DevelopmentSigning: &targets.DevelopmentSigningPolicy{ProxyInjection: true},
			},
			want: false,
		},
		{
			name: "nil entitlements does not propagate",
			target: targets.Target{
				DevelopmentSigning: &targets.DevelopmentSigningPolicy{ProxyInjection: true},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nestedNeedsEntitlementPropagation(tc.target); got != tc.want {
				t.Fatalf("nestedNeedsEntitlementPropagation = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveNestedCodeSignArgsPropagatesEntitlements(t *testing.T) {
	target := targets.Target{
		ID: "cursor",
		Entitlements: &targets.EntitlementsPolicy{
			Strip:                       []string{"com.apple.security.automation.apple-events"},
			RequiredBooleanEntitlements: []string{"com.apple.security.cs.allow-dyld-environment-variables"},
		},
		DevelopmentSigning: &targets.DevelopmentSigningPolicy{ProxyInjection: true},
	}
	const codePath = "/Applications/Cursor.app/Contents/Frameworks/Cursor Helper (Plugin).app/Contents/MacOS/Cursor Helper (Plugin)"
	runner := &Runner{DryRun: true, Out: io.Discard}
	args, nestedEntFile, err := resolveNestedCodeSignArgs(context.Background(), runner, target, "SIGN-ID", "/tmp/main-ent.plist", codePath, true)
	if err != nil {
		t.Fatalf("resolveNestedCodeSignArgs: %v", err)
	}
	if len(args) != 8 {
		t.Fatalf("args = %v, want 8 elements", args)
	}
	wantHead := []string{"--force", "--sign", "SIGN-ID", "--options", "runtime", "--entitlements"}
	if !stringSlicesEqual(args[:6], wantHead) {
		t.Fatalf("args head = %v, want %v", args[:6], wantHead)
	}
	if !strings.HasSuffix(args[6], ".plist") {
		t.Fatalf("entitlements arg = %q, want a .plist path", args[6])
	}
	if args[7] != codePath {
		t.Fatalf("code path arg = %q, want %q", args[7], codePath)
	}
	if nestedEntFile != args[6] {
		t.Fatalf("returned entitlements file = %q, want the codesign entitlements arg %q for cleanup", nestedEntFile, args[6])
	}
	for _, arg := range args {
		if arg == "--preserve-metadata=entitlements" {
			t.Fatalf("propagation path used --preserve-metadata; args = %v", args)
		}
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
