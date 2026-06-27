package statusreport

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestProviderTLSFailureNoteReturnsLatestRecentFailure(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := time.Date(2026, 6, 27, 0, 10, 0, 0, time.UTC)
	restoreNow := stubStatusNow(t, base)
	defer restoreNow()

	logPath := filepath.Join(
		os.Getenv("XDG_STATE_HOME"),
		"clyde",
		"logs",
		"providers",
		"mitm",
		"lifecycle.jsonl",
	)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"time":"2026-06-27T00:05:00Z","msg":"mitm.provider.connect.client_tls_failed","provider":"conductor","host":"api.conductor.build"}` + "\n" +
		`{"time":"2026-06-27T00:09:30Z","msg":"mitm.provider.connect.client_tls_failed","provider":"conductor","host":"api.github.com"}` + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	target := targets.Target{
		ID: "conductor",
		LaunchPolicy: spec.LaunchPolicySpec{
			ProxyHost: "127.0.0.1",
			ProxyPort: 48731,
		},
	}
	if got, want := providerTLSFailureNote(target), "provider-health=client-tls-failed host=api.github.com"; got != want {
		t.Fatalf("providerTLSFailureNote = %q, want %q", got, want)
	}
}

func TestProviderTLSFailureNoteIgnoresOldFailures(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := time.Date(2026, 6, 27, 1, 0, 0, 0, time.UTC)
	restoreNow := stubStatusNow(t, base)
	defer restoreNow()

	logPath := filepath.Join(
		os.Getenv("XDG_STATE_HOME"),
		"clyde",
		"logs",
		"providers",
		"mitm",
		"lifecycle.jsonl",
	)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"time":"2026-06-27T00:00:00Z","msg":"mitm.provider.connect.client_tls_failed","provider":"conductor","host":"api.conductor.build"}` + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	target := targets.Target{
		ID: "conductor",
		LaunchPolicy: spec.LaunchPolicySpec{
			ProxyHost: "127.0.0.1",
			ProxyPort: 48731,
		},
	}
	if got := providerTLSFailureNote(target); got != "" {
		t.Fatalf("providerTLSFailureNote = %q, want none", got)
	}
}

func TestProviderTLSFailureNoteClearsAfterLaterInterceptOpen(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := time.Date(2026, 6, 27, 0, 10, 0, 0, time.UTC)
	restoreNow := stubStatusNow(t, base)
	defer restoreNow()

	logPath := filepath.Join(
		os.Getenv("XDG_STATE_HOME"),
		"clyde",
		"logs",
		"providers",
		"mitm",
		"lifecycle.jsonl",
	)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"time":"2026-06-27T00:05:00Z","msg":"mitm.provider.connect.client_tls_failed","provider":"conductor","host":"api.conductor.build"}` + "\n" +
		`{"time":"2026-06-27T00:06:00Z","msg":"mitm.provider.connect.intercept_open","provider":"conductor","host":"api.conductor.build"}` + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	target := targets.Target{
		ID: "conductor",
		LaunchPolicy: spec.LaunchPolicySpec{
			ProxyHost: "127.0.0.1",
			ProxyPort: 48731,
		},
	}
	if got := providerTLSFailureNote(target); got != "" {
		t.Fatalf("providerTLSFailureNote = %q, want none", got)
	}
}

func TestProviderTLSFailureNotePreservesOlderUnclearedFailure(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := time.Date(2026, 6, 27, 0, 10, 0, 0, time.UTC)
	restoreNow := stubStatusNow(t, base)
	defer restoreNow()

	logPath := filepath.Join(
		os.Getenv("XDG_STATE_HOME"),
		"clyde",
		"logs",
		"providers",
		"mitm",
		"lifecycle.jsonl",
	)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := `{"time":"2026-06-27T00:05:00Z","msg":"mitm.provider.connect.client_tls_failed","provider":"conductor","host":"api.conductor.build"}` + "\n" +
		`{"time":"2026-06-27T00:06:00Z","msg":"mitm.provider.connect.client_tls_failed","provider":"conductor","host":"api.github.com"}` + "\n" +
		`{"time":"2026-06-27T00:07:00Z","msg":"mitm.provider.connect.intercept_open","provider":"conductor","host":"api.github.com"}` + "\n"
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	target := targets.Target{
		ID: "conductor",
		LaunchPolicy: spec.LaunchPolicySpec{
			ProxyHost: "127.0.0.1",
			ProxyPort: 48731,
		},
	}
	if got, want := providerTLSFailureNote(target), "provider-health=client-tls-failed host=api.conductor.build"; got != want {
		t.Fatalf("providerTLSFailureNote = %q, want %q", got, want)
	}
}

func stubStatusNow(t *testing.T, now time.Time) func() {
	t.Helper()
	original := statusNowFn
	statusNowFn = func() time.Time { return now }
	return func() {
		statusNowFn = original
	}
}
