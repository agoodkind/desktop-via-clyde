package shimembed

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexShimDryRunUsesCodexCAWithoutSSLCertFile(t *testing.T) {
	output := runShimDryRun(t, "Codex")

	assertContains(t, output, "target-policy: codex")
	assertContains(t, output, "env CODEX_CA_CERTIFICATE=")
	assertContains(t, output, "env NODE_EXTRA_CA_CERTS=")
	assertContains(t, output, "env NO_PROXY=localhost,127.0.0.1,::1,[::1]")
	assertContains(t, output, "env no_proxy=localhost,127.0.0.1,::1,[::1]")
	assertContains(t, output, "env SSL_CERT_FILE=<unset>")
	assertNotContains(t, output, "env SSL_CERT_FILE=/")
}

func TestDefaultShimDryRunKeepsSSLCertFile(t *testing.T) {
	output := runShimDryRun(t, "Cursor")

	assertContains(t, output, "target-policy: default")
	assertContains(t, output, "electron-run-as-node: false")
	assertContains(t, output, "--proxy-server=http://[::1]:48723")
	assertContains(t, output, "--ignore-certificate-errors")
	assertContains(t, output, "env NODE_EXTRA_CA_CERTS=")
	assertContains(t, output, "env SSL_CERT_FILE=")
	assertContains(t, output, "env NO_PROXY=localhost,127.0.0.1,::1,[::1]")
	assertContains(t, output, "env no_proxy=localhost,127.0.0.1,::1,[::1]")
	assertNotContains(t, output, "env CODEX_CA_CERTIFICATE=")
	assertNotContains(t, output, "env SSL_CERT_FILE=<unset>")
}

func TestDefaultShimDryRunDoesNotInjectChromiumFlagsInElectronNodeMode(t *testing.T) {
	output := runShimDryRunWithEnv(t, "Cursor", map[string]string{
		"ELECTRON_RUN_AS_NODE": "1",
	})

	assertContains(t, output, "target-policy: default")
	assertContains(t, output, "electron-run-as-node: true")
	assertNotContains(t, output, "--proxy-server=http://[::1]:48723")
	assertNotContains(t, output, "--ignore-certificate-errors")
	assertContains(t, output, "env NODE_EXTRA_CA_CERTS=")
	assertContains(t, output, "env SSL_CERT_FILE=")
}

func runShimDryRun(t *testing.T, executableName string) string {
	t.Helper()

	return runShimDryRunWithEnv(t, executableName, nil)
}

func runShimDryRunWithEnv(
	t *testing.T,
	executableName string,
	extraEnv map[string]string,
) string {
	t.Helper()

	tempDir := t.TempDir()
	shimPath := filepath.Join(tempDir, executableName)
	if err := os.WriteFile(shimPath, ShimBinary, 0o755); err != nil {
		t.Fatalf("write shim fixture: %v", err)
	}

	command := exec.Command(shimPath, "--clyde-dry-run")
	command.Env = os.Environ()
	for key, value := range extraEnv {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shim dry-run failed: %v\n%s", err, output)
	}
	return string(output)
}

func assertContains(t *testing.T, haystack string, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected output to contain %q\n%s", needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack string, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("expected output not to contain %q\n%s", needle, haystack)
	}
}
