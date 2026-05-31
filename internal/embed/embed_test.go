package shimembed

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexShimDryRunUsesCodexCAWithoutSSLCertFile(t *testing.T) {
	output := runShimDryRun(t, "Codex")

	assertContains(t, output, "target-policy: codex")
	assertContains(t, output, "launch-cwd: ")
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
	assertContains(t, output, "launch-cwd: ")
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
	output := runShimDryRunWithEnv(t, "Cursor", nil, map[string]string{
		"ELECTRON_RUN_AS_NODE": "1",
	})

	assertContains(t, output, "target-policy: default")
	assertContains(t, output, "electron-run-as-node: true")
	assertContains(t, output, "launch-cwd: ")
	assertNotContains(t, output, "--proxy-server=http://[::1]:48723")
	assertNotContains(t, output, "--ignore-certificate-errors")
	assertContains(t, output, "env NODE_EXTRA_CA_CERTS=")
	assertContains(t, output, "env SSL_CERT_FILE=")
}

func TestCodexShimExecUsesHomeAsLaunchCwd(t *testing.T) {
	homeDir := t.TempDir()
	recordDir := t.TempDir()
	recordPath := filepath.Join(recordDir, "pwd.txt")
	launcherCwd := t.TempDir()

	shimDir := t.TempDir()
	shimPath := filepath.Join(shimDir, "Codex")
	if err := os.WriteFile(shimPath, ShimBinary, 0o755); err != nil {
		t.Fatalf("write shim fixture: %v", err)
	}
	realPath := filepath.Join(shimDir, "Codex.real")
	script := "#!/bin/sh\npwd > " + shellQuote(recordPath) + "\n"
	if err := os.WriteFile(realPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write real fixture: %v", err)
	}

	fakeCA := filepath.Join(t.TempDir(), "clyde-mitm-ca.crt")
	if err := os.WriteFile(fakeCA, []byte("fake-ca"), 0o644); err != nil {
		t.Fatalf("write fake ca: %v", err)
	}
	configHome := t.TempDir()
	writeShimConfig(t, configHome, fakeCA, homeDir)
	cleanupListener := ensureProxyReachable(t)
	defer cleanupListener()

	command := exec.Command(shimPath)
	command.Dir = launcherCwd
	command.Env = envWithOverrides(os.Environ(),
		"HOME="+homeDir,
		"XDG_CONFIG_HOME="+configHome,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shim exec failed: %v\n%s", err, output)
	}
	recorded, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read recorded cwd: %v", err)
	}
	got := strings.TrimSpace(string(recorded))
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("resolve recorded cwd: %v", err)
	}
	homeResolved, err := filepath.EvalSymlinks(homeDir)
	if err != nil {
		t.Fatalf("resolve expected home cwd: %v", err)
	}
	if gotResolved != homeResolved {
		t.Fatalf("recorded cwd = %q want %q", gotResolved, homeResolved)
	}
	launcherResolved, err := filepath.EvalSymlinks(launcherCwd)
	if err != nil {
		t.Fatalf("resolve launcher cwd: %v", err)
	}
	if gotResolved == launcherResolved {
		t.Fatalf("recorded cwd unexpectedly preserved launcher cwd %q", launcherResolved)
	}
}

func runShimDryRun(t *testing.T, executableName string) string {
	t.Helper()

	return runShimDryRunWithEnv(t, executableName, nil, nil)
}

func runShimDryRunWithEnv(
	t *testing.T,
	executableName string,
	launchWorkingDirectory *string,
	extraEnv map[string]string,
) string {
	t.Helper()

	tempDir := t.TempDir()
	shimPath := filepath.Join(tempDir, executableName)
	if err := os.WriteFile(shimPath, ShimBinary, 0o755); err != nil {
		t.Fatalf("write shim fixture: %v", err)
	}

	homeDir := t.TempDir()
	configHome := t.TempDir()
	caPath := filepath.Join(t.TempDir(), "clyde-mitm-ca.crt")
	cwd := homeDir
	if launchWorkingDirectory != nil {
		cwd = *launchWorkingDirectory
	}
	writeShimConfig(t, configHome, caPath, cwd)

	command := exec.Command(shimPath, "--clyde-dry-run")
	command.Env = envWithOverrides(os.Environ(),
		"HOME="+homeDir,
		"XDG_CONFIG_HOME="+configHome,
	)
	for key, value := range extraEnv {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shim dry-run failed: %v\n%s", err, output)
	}
	return string(output)
}

func writeShimConfig(t *testing.T, xdgConfigHome string, caPath string, launchWorkingDirectory string) {
	t.Helper()

	configRoot := filepath.Join(xdgConfigHome, "desktop-via-clyde")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll config root: %v", err)
	}
	contents := fmt.Sprintf(`[proxy]
host = "::1"
port = 48723
ca_certificate = %q
no_proxy = "localhost,127.0.0.1,::1,[::1]"
launch_working_directory = %q

[apps.cursor]
target_policy = "default"

[apps.codex]
target_policy = "codex"

[apps.claude]
target_policy = "default"
`, caPath, launchWorkingDirectory)
	if err := os.WriteFile(filepath.Join(configRoot, "config.toml"), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile config.toml: %v", err)
	}
}

func envWithOverrides(base []string, overrides ...string) []string {
	remove := map[string]bool{}
	for _, override := range overrides {
		parts := strings.SplitN(override, "=", 2)
		if len(parts) == 2 && parts[0] != "" {
			remove[parts[0]] = true
		}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !remove[key] {
			out = append(out, entry)
		}
	}
	out = append(out, overrides...)
	return out
}

func ensureProxyReachable(t *testing.T) func() {
	t.Helper()
	listener, err := net.Listen("tcp", "[::1]:48723")
	if err == nil {
		return func() {
			_ = listener.Close()
		}
	}
	connection, dialErr := net.DialTimeout("tcp", "[::1]:48723", 500*time.Millisecond)
	if dialErr != nil {
		t.Fatalf("neither bound nor reachable on [::1]:48723: listen=%v dial=%v", err, dialErr)
	}
	_ = connection.Close()
	return func() {}
}

func shellQuote(path string) string {
	if path == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
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
