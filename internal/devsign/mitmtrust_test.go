package devsign

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestTrustedMITMCADriftSkipsTargetsWithoutProxyInjection(t *testing.T) {
	target := targets.Target{ID: "codex"}
	if drift := TrustedMITMCADrift(context.Background(), target); drift != "" {
		t.Fatalf("drift = %q, want none", drift)
	}
	if err := EnsureTrustedMITMCA(context.Background(), target); err != nil {
		t.Fatalf("EnsureTrustedMITMCA = %v, want nil", err)
	}
}

func TestTrustedMITMCADriftRequiresCertificatePath(t *testing.T) {
	target := trustRequiredTarget("")
	drift := TrustedMITMCADrift(context.Background(), target)
	if !strings.Contains(drift, "launch policy CA certificate is empty") {
		t.Fatalf("drift = %q, want empty certificate error", drift)
	}
}

func TestTrustedMITMCADriftReportsMissingTrust(t *testing.T) {
	restore := stubTrustSearch(t, map[string][]byte{})
	defer restore()

	certPath := writeTestCertificate(t)
	target := trustRequiredTarget(certPath)
	drift := TrustedMITMCADrift(context.Background(), target)
	if !strings.Contains(drift, "is not trusted") {
		t.Fatalf("drift = %q, want not trusted", drift)
	}
	if err := EnsureTrustedMITMCA(context.Background(), target); err == nil || !strings.Contains(err.Error(), "native-root Codex subprocesses") {
		t.Fatalf("EnsureTrustedMITMCA = %v, want actionable trust error", err)
	}
}

func TestTrustedMITMCADriftAcceptsMatchingSystemKeychain(t *testing.T) {
	certPath := writeTestCertificate(t)
	commonName, fingerprint, err := loadMITMCACertificate(certPath)
	if err != nil {
		t.Fatalf("loadMITMCACertificate: %v", err)
	}
	restore := stubTrustSearch(t, map[string][]byte{
		macOSSystemKeychainPath: []byte("SHA-256 hash: " + strings.ToUpper(fingerprint) + "\n"),
	})
	defer restore()
	target := trustRequiredTarget(certPath)
	drift := TrustedMITMCADrift(context.Background(), target)
	if drift != "" {
		t.Fatalf("drift = %q, want none", drift)
	}
	status, err := mitmTrustStatusForTarget(context.Background(), target)
	if err != nil {
		t.Fatalf("mitmTrustStatusForTarget: %v", err)
	}
	if status.CommonName != commonName {
		t.Fatalf("common name = %q, want %q", status.CommonName, commonName)
	}
	if len(status.MatchedKeychains) != 1 || status.MatchedKeychains[0] != macOSSystemKeychainPath {
		t.Fatalf("matched keychains = %#v, want system keychain", status.MatchedKeychains)
	}
}

func TestTrustedMITMCADriftAcceptsMatchingLoginKeychain(t *testing.T) {
	homeDir := t.TempDir()
	restoreHome := stubUserHomeDir(t, homeDir)
	defer restoreHome()

	certPath := writeTestCertificate(t)
	_, fingerprint, err := loadMITMCACertificate(certPath)
	if err != nil {
		t.Fatalf("loadMITMCACertificate: %v", err)
	}
	restore := stubTrustSearch(t, map[string][]byte{
		loginKeychainPath(): []byte("SHA-256 hash: " + strings.ToUpper(fingerprint) + "\n"),
	})
	defer restore()
	target := trustRequiredTarget(certPath)
	if drift := TrustedMITMCADrift(context.Background(), target); drift != "" {
		t.Fatalf("drift = %q, want none", drift)
	}
}

func TestTrustedMITMCADriftReportsStaleFingerprint(t *testing.T) {
	restore := stubTrustSearch(t, map[string][]byte{
		macOSSystemKeychainPath: []byte("SHA-256 hash: deadbeef\n"),
	})
	defer restore()

	certPath := writeTestCertificate(t)
	target := trustRequiredTarget(certPath)
	drift := TrustedMITMCADrift(context.Background(), target)
	if !strings.Contains(drift, "stale fingerprint") {
		t.Fatalf("drift = %q, want stale fingerprint", drift)
	}
}

func TestTrustedMITMCADriftPropagatesSecurityErrors(t *testing.T) {
	original := trustSearchOutput
	trustSearchOutput = func(_ context.Context, _ string, _ string) ([]byte, error) {
		return nil, errors.New("security failed")
	}
	t.Cleanup(func() {
		trustSearchOutput = original
	})

	certPath := writeTestCertificate(t)
	target := trustRequiredTarget(certPath)
	drift := TrustedMITMCADrift(context.Background(), target)
	if !strings.Contains(drift, "security failed") {
		t.Fatalf("drift = %q, want security failure", drift)
	}
}

func trustRequiredTarget(certPath string) targets.Target {
	return targets.Target{
		ID: "codex",
		DevelopmentSigning: &targets.DevelopmentSigningPolicy{
			Enabled:        true,
			ProxyInjection: true,
		},
		LaunchPolicy: spec.LaunchPolicySpec{
			CACertificate: certPath,
		},
	}
}

func stubTrustSearch(t *testing.T, outputs map[string][]byte) func() {
	t.Helper()
	original := trustSearchOutput
	trustSearchOutput = func(_ context.Context, keychainPath string, _ string) ([]byte, error) {
		if output, ok := outputs[keychainPath]; ok {
			return output, nil
		}
		return nil, nil
	}
	return func() {
		trustSearchOutput = original
	}
}

func stubUserHomeDir(t *testing.T, homeDir string) func() {
	t.Helper()
	original := userHomeDir
	userHomeDir = func() (string, error) {
		return homeDir, nil
	}
	return func() {
		userHomeDir = original
	}
}

func writeTestCertificate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "Clyde MITM CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	pemBody := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPath := filepath.Join(dir, "clyde-mitm-ca.crt")
	if err := os.WriteFile(certPath, pemBody, 0o644); err != nil {
		t.Fatalf("WriteFile cert: %v", err)
	}
	return certPath
}
