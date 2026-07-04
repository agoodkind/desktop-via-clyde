package devsign

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestEncodeInjectorPolicyWritesNULRecords(t *testing.T) {
	policy := spec.LaunchPolicySpec{
		ProxyPort: 48727,
		Environment: []spec.EnvActionSpec{
			{Action: "set", Key: "HTTPS_PROXY", Value: "http://localhost:48727"},
			{Action: "unset", Key: "SSL_CERT_FILE"},
		},
		Arguments: []spec.ArgActionSpec{
			{Action: "append", Value: "--proxy-server=http://localhost:48727"},
		},
	}

	got, err := EncodeInjectorPolicy(policy)
	if err != nil {
		t.Fatalf("EncodeInjectorPolicy: %v", err)
	}
	want := []byte("set\x00HTTPS_PROXY\x00http://localhost:48727\x00unset\x00SSL_CERT_FILE\x00set\x00DVC_CLYDE_REDIRECT_PORT\x0048727\x00append-argv\x00--proxy-server=http://localhost:48727\x00")
	if !bytes.Equal(got, want) {
		t.Fatalf("policy bytes = %q, want %q", got, want)
	}
}

func TestEncodeInjectorPolicyOmitsRedirectPortWhenUnset(t *testing.T) {
	policy := spec.LaunchPolicySpec{
		Environment: []spec.EnvActionSpec{
			{Action: "set", Key: "HTTPS_PROXY", Value: "http://localhost:48727"},
		},
	}

	got, err := EncodeInjectorPolicy(policy)
	if err != nil {
		t.Fatalf("EncodeInjectorPolicy: %v", err)
	}
	if bytes.Contains(got, []byte("DVC_CLYDE_REDIRECT_PORT")) {
		t.Fatalf("policy bytes include redirect port when ProxyPort is unset: %q", got)
	}
}

func TestMissingAssetsDoesNotRequireDefaultInjector(t *testing.T) {
	dir := t.TempDir()
	policy := targets.DevelopmentSigningPolicy{
		Enabled:         true,
		ProfilePath:     writeAsset(t, dir, "dev.provisionprofile"),
		P12Path:         writeAsset(t, dir, "dev.p12"),
		P12PasswordFile: writeAsset(t, dir, "p12-password"),
		ProxyInjection:  true,
	}

	missing := MissingAssets(policy)
	if len(missing) != 0 {
		t.Fatalf("MissingAssets = %#v, want none", missing)
	}
}

func TestMissingAssetsRequiresConfiguredInjectorOverride(t *testing.T) {
	dir := t.TempDir()
	policy := targets.DevelopmentSigningPolicy{
		Enabled:           true,
		ProfilePath:       writeAsset(t, dir, "dev.provisionprofile"),
		P12Path:           writeAsset(t, dir, "dev.p12"),
		P12PasswordFile:   writeAsset(t, dir, "p12-password"),
		InjectorDylibPath: filepath.Join(dir, "missing.dylib"),
		ProxyInjection:    true,
	}

	missing := MissingAssets(policy)
	if len(missing) != 1 {
		t.Fatalf("MissingAssets count = %d, want 1: %#v", len(missing), missing)
	}
	if !strings.Contains(missing[0].Label, "injector dylib override") {
		t.Fatalf("missing label = %q", missing[0].Label)
	}
}

func writeAsset(t *testing.T, dir string, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("asset"), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}
