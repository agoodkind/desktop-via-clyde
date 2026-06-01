package shimembed

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/spec"
)

func TestCodexShimDryRunUsesCodexCAWithoutSSLCertFile(t *testing.T) {
	output := runShimDryRun(t, "Codex")

	assertContains(t, output, ".launch-policy.json")
	assertContains(t, output, "launch-cwd: ")
	assertContains(t, output, "--proxy-server=http://[::1]:48723")
	assertContains(t, output, "env CODEX_CA_CERTIFICATE=")
	assertContains(t, output, "env NODE_EXTRA_CA_CERTS=")
	assertContains(t, output, "env NO_PROXY=localhost,127.0.0.1,::1,[::1]")
	assertContains(t, output, "env no_proxy=localhost,127.0.0.1,::1,[::1]")
	assertContains(t, output, "env SSL_CERT_FILE=<unset>")
	assertNotContains(t, output, "env SSL_CERT_FILE=/")
}

func TestCodexShimDryRunPrintsCLIWrapperEnvironment(t *testing.T) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen localhost: %v", err)
	}
	defer listener.Close()
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr = %T, want *net.TCPAddr", listener.Addr())
	}

	tempDir := t.TempDir()
	shimPath := filepath.Join(tempDir, "Codex")
	if err := os.WriteFile(shimPath, ShimBinary, 0o755); err != nil {
		t.Fatalf("write shim fixture: %v", err)
	}

	homeDir := t.TempDir()
	caPath := filepath.Join(t.TempDir(), "clyde-mitm-ca.crt")
	if err := os.WriteFile(caPath, []byte("fake-ca"), 0o644); err != nil {
		t.Fatalf("write fake ca: %v", err)
	}
	policy := codexPolicyForTest(caPath, homeDir, "localhost", tcpAddr.Port, "http://localhost:"+strconv.Itoa(tcpAddr.Port))
	policy.Environment = append(policy.Environment,
		spec.EnvActionSpec{Action: "set", Key: "CODEX_CLI_PATH", Value: "/tmp/dvc-codex-cli-shim"},
		spec.EnvActionSpec{Action: "set", Key: "DVC_CODEX_REAL_CLI", Value: "/tmp/Codex.app/Contents/Resources/codex"},
		spec.EnvActionSpec{Action: "set", Key: "DVC_CODEX_CHATGPT_BASE_URL", Value: "http://localhost:48730/backend-api"},
	)
	writeLaunchPolicy(t, shimPath, policy)

	command := exec.Command(shimPath, "--clyde-dry-run")
	command.Env = envWithOverrides(os.Environ(), "HOME="+homeDir)
	outputBytes, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shim dry-run failed: %v\n%s", err, outputBytes)
	}
	output := string(outputBytes)
	assertContains(t, output, "env CODEX_CLI_PATH=/tmp/dvc-codex-cli-shim")
	assertContains(t, output, "env DVC_CODEX_REAL_CLI=/tmp/Codex.app/Contents/Resources/codex")
	assertContains(t, output, "env DVC_CODEX_CHATGPT_BASE_URL=http://localhost:48730/backend-api")
}

func TestDefaultShimDryRunKeepsSSLCertFile(t *testing.T) {
	output := runShimDryRun(t, "Cursor")

	assertContains(t, output, ".launch-policy.json")
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

	assertContains(t, output, ".launch-policy.json")
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
	writeShimConfig(t, shimPath, "Codex", fakeCA, homeDir)
	cleanupListener := ensureProxyReachable(t)
	defer cleanupListener()

	command := exec.Command(shimPath)
	command.Dir = launcherCwd
	command.Env = envWithOverrides(os.Environ(), "HOME="+homeDir)
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

func TestShimExecAcceptsLocalhostProxyPolicy(t *testing.T) {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen localhost: %v", err)
	}
	defer listener.Close()
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener addr = %T, want *net.TCPAddr", listener.Addr())
	}
	proxyURL := "http://localhost:" + strconv.Itoa(tcpAddr.Port)

	recordDir := t.TempDir()
	recordPath := filepath.Join(recordDir, "proxy.txt")
	shimDir := t.TempDir()
	shimPath := filepath.Join(shimDir, "Cursor")
	if err := os.WriteFile(shimPath, ShimBinary, 0o755); err != nil {
		t.Fatalf("write shim fixture: %v", err)
	}
	realPath := filepath.Join(shimDir, "Cursor.real")
	script := "#!/bin/sh\nprintf '%s\\n' $HTTPS_PROXY $HTTP_PROXY $ALL_PROXY $1 > " + shellQuote(recordPath) + "\n"
	if err := os.WriteFile(realPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write real fixture: %v", err)
	}

	homeDir := t.TempDir()
	fakeCA := filepath.Join(t.TempDir(), "clyde-mitm-ca.crt")
	if err := os.WriteFile(fakeCA, []byte("fake-ca"), 0o644); err != nil {
		t.Fatalf("write fake ca: %v", err)
	}
	writeShimConfigWithProxy(t, shimPath, "Cursor", fakeCA, homeDir, "localhost", tcpAddr.Port, proxyURL)

	command := exec.Command(shimPath)
	command.Env = envWithOverrides(os.Environ(), "HOME="+homeDir)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shim exec failed: %v\n%s", err, output)
	}
	recorded, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read recorded proxy env: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	if len(lines) != 4 {
		t.Fatalf("recorded proxy lines = %#v", lines)
	}
	for index, got := range lines {
		want := proxyURL
		if index == 3 {
			want = "--proxy-server=" + proxyURL
		}
		if got != want {
			t.Fatalf("recorded proxy line %d = %q, want %q", index, got, want)
		}
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
	caPath := filepath.Join(t.TempDir(), "clyde-mitm-ca.crt")
	if err := os.WriteFile(caPath, []byte("fake-ca"), 0o644); err != nil {
		t.Fatalf("write fake ca: %v", err)
	}
	cwd := homeDir
	if launchWorkingDirectory != nil {
		cwd = *launchWorkingDirectory
	}
	writeShimConfig(t, shimPath, executableName, caPath, cwd)

	command := exec.Command(shimPath, "--clyde-dry-run")
	command.Env = envWithOverrides(os.Environ(), "HOME="+homeDir)
	for key, value := range extraEnv {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("shim dry-run failed: %v\n%s", err, output)
	}
	return string(output)
}

func writeShimConfig(t *testing.T, shimPath string, executableName string, caPath string, launchWorkingDirectory string) {
	t.Helper()

	proxyURL := "http://[::1]:48723"
	writeShimConfigWithProxy(t, shimPath, executableName, caPath, launchWorkingDirectory, "::1", 48723, proxyURL)
}

func writeShimConfigWithProxy(
	t *testing.T,
	shimPath string,
	executableName string,
	caPath string,
	launchWorkingDirectory string,
	proxyHost string,
	proxyPort int,
	proxyURL string,
) {
	t.Helper()

	noProxy := "localhost,127.0.0.1,::1,[::1]"
	policy := spec.LaunchPolicySpec{
		ProxyHost:              proxyHost,
		ProxyPort:              proxyPort,
		CACertificate:          caPath,
		NoProxy:                noProxy,
		LaunchWorkingDirectory: launchWorkingDirectory,
		Arguments: []spec.ArgActionSpec{
			{Action: "append", Value: "--proxy-server=" + proxyURL},
			{Action: "append", Value: "--ignore-certificate-errors"},
		},
		Preflights: []spec.PreflightSpec{
			{Kind: "file_exists", Path: caPath},
			{Kind: "tcp_reachable", Host: proxyHost, Port: proxyPort, Timeout: 1000},
		},
	}

	switch strings.ToLower(executableName) {
	case "codex":
		policy.Environment = []spec.EnvActionSpec{
			{Action: "set", Key: "CODEX_CA_CERTIFICATE", Value: caPath},
			{Action: "set", Key: "NODE_EXTRA_CA_CERTS", Value: caPath},
			{Action: "set", Key: "NODE_OPTIONS", Value: "--use-openssl-ca"},
			{Action: "set", Key: "NODE_TLS_REJECT_UNAUTHORIZED", Value: "0"},
			{Action: "set", Key: "HTTPS_PROXY", Value: proxyURL},
			{Action: "set", Key: "HTTP_PROXY", Value: proxyURL},
			{Action: "set", Key: "ALL_PROXY", Value: proxyURL},
			{Action: "set", Key: "NO_PROXY", Value: noProxy},
			{Action: "set", Key: "no_proxy", Value: noProxy},
			{Action: "unset", Key: "SSL_CERT_FILE"},
		}
	default:
		policy.Environment = []spec.EnvActionSpec{
			{Action: "set", Key: "NODE_EXTRA_CA_CERTS", Value: caPath},
			{Action: "set", Key: "SSL_CERT_FILE", Value: caPath},
			{Action: "set", Key: "NODE_OPTIONS", Value: "--use-openssl-ca"},
			{Action: "set", Key: "NODE_TLS_REJECT_UNAUTHORIZED", Value: "0"},
			{Action: "set", Key: "HTTPS_PROXY", Value: proxyURL},
			{Action: "set", Key: "HTTP_PROXY", Value: proxyURL},
			{Action: "set", Key: "ALL_PROXY", Value: proxyURL},
			{Action: "set", Key: "NO_PROXY", Value: noProxy},
			{Action: "set", Key: "no_proxy", Value: noProxy},
		}
	}

	writeLaunchPolicy(t, shimPath, policy)
}

func codexPolicyForTest(
	caPath string,
	launchWorkingDirectory string,
	proxyHost string,
	proxyPort int,
	proxyURL string,
) spec.LaunchPolicySpec {
	noProxy := "localhost,127.0.0.1,::1,[::1]"
	return spec.LaunchPolicySpec{
		ProxyHost:              proxyHost,
		ProxyPort:              proxyPort,
		CACertificate:          caPath,
		NoProxy:                noProxy,
		LaunchWorkingDirectory: launchWorkingDirectory,
		Environment: []spec.EnvActionSpec{
			{Action: "set", Key: "CODEX_CA_CERTIFICATE", Value: caPath},
			{Action: "set", Key: "NODE_EXTRA_CA_CERTS", Value: caPath},
			{Action: "set", Key: "NODE_OPTIONS", Value: "--use-openssl-ca"},
			{Action: "set", Key: "NODE_TLS_REJECT_UNAUTHORIZED", Value: "0"},
			{Action: "set", Key: "HTTPS_PROXY", Value: proxyURL},
			{Action: "set", Key: "HTTP_PROXY", Value: proxyURL},
			{Action: "set", Key: "ALL_PROXY", Value: proxyURL},
			{Action: "set", Key: "NO_PROXY", Value: noProxy},
			{Action: "set", Key: "no_proxy", Value: noProxy},
			{Action: "unset", Key: "SSL_CERT_FILE"},
		},
		Arguments: []spec.ArgActionSpec{
			{Action: "append", Value: "--proxy-server=" + proxyURL},
			{Action: "append", Value: "--ignore-certificate-errors"},
		},
		Preflights: []spec.PreflightSpec{
			{Kind: "file_exists", Path: caPath},
			{Kind: "tcp_reachable", Host: proxyHost, Port: proxyPort, Timeout: 1000},
		},
	}
}

func writeLaunchPolicy(t *testing.T, shimPath string, policy spec.LaunchPolicySpec) {
	t.Helper()
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		t.Fatalf("Marshal launch policy: %v", err)
	}
	if err := os.WriteFile(shimPath+".launch-policy.json", data, 0o644); err != nil {
		t.Fatalf("WriteFile launch policy: %v", err)
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
