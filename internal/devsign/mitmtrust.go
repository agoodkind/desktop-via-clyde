package devsign

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

const (
	macOSSystemKeychainPath = "/Library/Keychains/System.keychain"
	loginKeychainRelPath    = "Library/Keychains/login.keychain-db"
	securityBinaryPath      = "/usr/bin/security"
)

var (
	trustSearchOutput = defaultTrustSearchOutput
	userHomeDir       = os.UserHomeDir
)

type mitmTrustKeychainStatus struct {
	Path         string
	Fingerprints []string
}

type mitmTrustStatus struct {
	Required            bool
	CertPath            string
	CommonName          string
	ExpectedFingerprint string
	MatchedKeychains    []string
	StaleKeychains      []mitmTrustKeychainStatus
}

func (s mitmTrustStatus) trusted() bool {
	return len(s.MatchedKeychains) > 0
}

func requiresTrustedMITMCA(t targets.Target) bool {
	return t.DevelopmentSigning != nil &&
		t.DevelopmentSigning.Enabled &&
		t.DevelopmentSigning.ProxyInjection
}

// TrustedMITMCADrift reports whether the configured MITM CA needed by the
// Codex development-signing path is missing or stale in the local trust store.
func TrustedMITMCADrift(ctx context.Context, t targets.Target) string {
	status, err := mitmTrustStatusForTarget(ctx, t)
	if err != nil {
		return err.Error()
	}
	if !status.Required || status.trusted() {
		return ""
	}
	keychains := trustKeychainPaths()
	if len(status.StaleKeychains) > 0 {
		return fmt.Sprintf(
			"launch policy CA %s (%s) is trusted with a stale fingerprint in %s; want sha256 %s",
			status.CertPath,
			status.CommonName,
			joinKeychains(status.StaleKeychains),
			status.ExpectedFingerprint,
		)
	}
	return fmt.Sprintf(
		"launch policy CA %s (%s) is not trusted in %s; want sha256 %s",
		status.CertPath,
		status.CommonName,
		strings.Join(keychains, " or "),
		status.ExpectedFingerprint,
	)
}

// EnsureTrustedMITMCA blocks a real patch or upgrade when the MITM CA required
// by native-root Codex subprocesses is missing or stale in the local trust
// store.
func EnsureTrustedMITMCA(ctx context.Context, t targets.Target) error {
	status, err := mitmTrustStatusForTarget(ctx, t)
	if err != nil {
		return err
	}
	if !status.Required || status.trusted() {
		return nil
	}
	drift := TrustedMITMCADrift(ctx, t)
	if drift == "" {
		return nil
	}
	return fmt.Errorf(
		"%s; native-root Codex subprocesses, including the remote-control websocket, cannot stay on the MITM audit path until this CA is trusted. Install it with `/usr/bin/security add-trusted-cert -d -r trustRoot -k %s %s` or `/usr/bin/security add-trusted-cert -d -r trustRoot -k %s %s`",
		drift,
		macOSSystemKeychainPath,
		status.CertPath,
		loginKeychainPath(),
		status.CertPath,
	)
}

func mitmTrustStatusForTarget(ctx context.Context, t targets.Target) (mitmTrustStatus, error) {
	devsignLog.DebugContext(ctx, "devsign.mitm_trust_status.boundary", "target", t.ID)
	status := mitmTrustStatus{
		Required:            requiresTrustedMITMCA(t),
		CertPath:            "",
		CommonName:          "",
		ExpectedFingerprint: "",
		MatchedKeychains:    nil,
		StaleKeychains:      nil,
	}
	if !status.Required {
		return status, nil
	}
	certPath := strings.TrimSpace(t.LaunchPolicy.CACertificate)
	if certPath == "" {
		return mitmTrustStatus{}, logDevsignError(ctx, "devsign.mitm_trust_empty_cert_path", fmt.Errorf("launch policy CA certificate is empty for target %s", t.ID))
	}
	commonName, fingerprint, err := loadMITMCACertificate(certPath)
	if err != nil {
		return mitmTrustStatus{}, err
	}
	status.CertPath = certPath
	status.CommonName = commonName
	status.ExpectedFingerprint = fingerprint
	for _, keychainPath := range trustKeychainPaths() {
		fingerprints, err := keychainFingerprints(ctx, keychainPath, commonName)
		if err != nil {
			return mitmTrustStatus{}, err
		}
		if len(fingerprints) == 0 {
			continue
		}
		if containsString(fingerprints, fingerprint) {
			status.MatchedKeychains = append(status.MatchedKeychains, keychainPath)
			continue
		}
		status.StaleKeychains = append(status.StaleKeychains, mitmTrustKeychainStatus{
			Path:         keychainPath,
			Fingerprints: fingerprints,
		})
	}
	return status, nil
}

func trustKeychainPaths() []string {
	paths := []string{macOSSystemKeychainPath}
	loginPath := loginKeychainPath()
	if loginPath != "" && !containsString(paths, loginPath) {
		paths = append(paths, loginPath)
	}
	return paths
}

func loginKeychainPath() string {
	homeDir, err := userHomeDir()
	if err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(homeDir)
	if trimmed == "" {
		return ""
	}
	return filepath.Join(trimmed, loginKeychainRelPath)
}

func loadMITMCACertificate(certPath string) (string, string, error) {
	body, err := os.ReadFile(certPath)
	if err != nil {
		return "", "", logDevsignErrorNoContext("devsign.mitm_trust_read_cert_failed", fmt.Errorf("read launch policy CA certificate %s: %w", certPath, err))
	}
	block, _ := pem.Decode(body)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", "", logDevsignErrorNoContext("devsign.mitm_trust_missing_pem_block", fmt.Errorf("launch policy CA certificate %s has no PEM CERTIFICATE block", certPath))
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", "", logDevsignErrorNoContext("devsign.mitm_trust_parse_cert_failed", fmt.Errorf("parse launch policy CA certificate %s: %w", certPath, err))
	}
	commonName := strings.TrimSpace(cert.Subject.CommonName)
	if commonName == "" {
		return "", "", logDevsignErrorNoContext("devsign.mitm_trust_empty_common_name", fmt.Errorf("launch policy CA certificate %s has an empty subject common name", certPath))
	}
	sum := sha256.Sum256(cert.Raw)
	return commonName, hex.EncodeToString(sum[:]), nil
}

func defaultTrustSearchOutput(ctx context.Context, keychainPath string, commonName string) ([]byte, error) {
	devsignLog.DebugContext(ctx, "devsign.mitm_trust_search.boundary", "keychain", keychainPath, "common_name", commonName)
	cmd := exec.CommandContext(ctx, securityBinaryPath, "find-certificate", "-c", commonName, "-Z", keychainPath)
	output, err := cmd.CombinedOutput()
	if err != nil && isSecurityNotFound(string(output)) {
		return nil, nil
	}
	if err != nil {
		return nil, logDevsignError(ctx, "devsign.mitm_trust_search_failed", fmt.Errorf("security find-certificate -c %q -Z %s: %w (%s)", commonName, keychainPath, err, strings.TrimSpace(string(output))))
	}
	return output, nil
}

func keychainFingerprints(ctx context.Context, keychainPath string, commonName string) ([]string, error) {
	output, err := trustSearchOutput(ctx, keychainPath, commonName)
	if err != nil {
		return nil, err
	}
	if len(output) == 0 {
		return nil, nil
	}
	fingerprints := make([]string, 0)
	for line := range strings.SplitSeq(string(output), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "SHA-256 hash:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "SHA-256 hash:"))
		value = strings.ToLower(strings.ReplaceAll(value, ":", ""))
		if value != "" {
			fingerprints = append(fingerprints, value)
		}
	}
	return fingerprints, nil
}

func isSecurityNotFound(output string) bool {
	lower := strings.ToLower(strings.TrimSpace(output))
	return strings.Contains(lower, "could not be found") ||
		strings.Contains(lower, "specified item could not be found") ||
		strings.Contains(lower, "no matching")
}

func joinKeychains(keychains []mitmTrustKeychainStatus) string {
	paths := make([]string, 0, len(keychains))
	for _, keychain := range keychains {
		paths = append(paths, keychain.Path)
	}
	return strings.Join(paths, " or ")
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}
