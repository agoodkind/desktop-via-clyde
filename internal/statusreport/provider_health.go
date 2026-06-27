package statusreport

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

const providerTLSFailureWindow = 30 * time.Minute

var (
	providerTLSFailureNoteFn = providerTLSFailureNote
	statusNowFn              = time.Now
)

type mitmLifecycleMessage string

const (
	mitmLifecycleMessageClientTLSFailed mitmLifecycleMessage = "mitm.provider.connect.client_tls_failed"
	mitmLifecycleMessageInterceptOpen   mitmLifecycleMessage = "mitm.provider.connect.intercept_open"
)

type mitmLifecycleEntry struct {
	Time     string               `json:"time"`
	Message  mitmLifecycleMessage `json:"msg"`
	Provider string               `json:"provider"`
	Host     string               `json:"host"`
}

func providerTLSFailureNote(target targets.Target) string {
	if target.ID == "" {
		return ""
	}
	if target.LaunchPolicy.ProxyHost == "" || target.LaunchPolicy.ProxyPort == 0 {
		return ""
	}
	logPath := filepath.Join(paths.StateRoot(), "logs", "providers", "mitm", "lifecycle.jsonl")
	file, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		statusReportLog.Debug("statusreport.provider_health_open_failed", "target", target.ID, "path", logPath, "err", err)
		return ""
	}
	defer func() { _ = file.Close() }()

	cutoff := statusNowFn().Add(-providerTLSFailureWindow)
	latestFailureTime := time.Time{}
	latestFailureHost := ""
	successTimesByHost := make(map[string]time.Time)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry mitmLifecycleEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Provider != target.ID {
			continue
		}
		parsedTime, err := time.Parse(time.RFC3339Nano, entry.Time)
		if err != nil {
			continue
		}
		if parsedTime.Before(cutoff) {
			continue
		}
		switch entry.Message {
		case mitmLifecycleMessageClientTLSFailed:
			if parsedTime.After(latestFailureTime) {
				latestFailureTime = parsedTime
				latestFailureHost = entry.Host
			}
		case mitmLifecycleMessageInterceptOpen:
			if parsedTime.After(successTimesByHost[entry.Host]) {
				successTimesByHost[entry.Host] = parsedTime
			}
		}
	}
	if err := scanner.Err(); err != nil {
		statusReportLog.Debug("statusreport.provider_health_scan_failed", "target", target.ID, "path", logPath, "err", err)
		return ""
	}
	if latestFailureTime.IsZero() {
		return ""
	}
	if successTime := successTimesByHost[latestFailureHost]; !successTime.IsZero() && successTime.After(latestFailureTime) {
		return ""
	}
	if latestFailureHost == "" {
		return "provider-health=client-tls-failed"
	}
	return "provider-health=client-tls-failed host=" + latestFailureHost
}
