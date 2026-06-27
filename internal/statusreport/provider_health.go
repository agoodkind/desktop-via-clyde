package statusreport

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if !providerTLSHealthEnabled(target) {
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

	host, err := latestUnclearedProviderTLSFailureHost(file, target.ID, statusNowFn().Add(-providerTLSFailureWindow))
	if err != nil {
		statusReportLog.Debug("statusreport.provider_health_scan_failed", "target", target.ID, "path", logPath, "err", err)
		return ""
	}
	if host == "" {
		return ""
	}
	return "provider-health=client-tls-failed host=" + host
}

func providerTLSHealthEnabled(target targets.Target) bool {
	if target.ID == "" {
		return false
	}
	return target.LaunchPolicy.ProxyHost != "" && target.LaunchPolicy.ProxyPort != 0
}

func latestUnclearedProviderTLSFailureHost(reader io.Reader, targetID string, cutoff time.Time) (string, error) {
	failedHosts, successTimesByHost, err := readProviderTLSWindow(reader, targetID, cutoff)
	if err != nil {
		return "", err
	}
	return latestUnclearedFailureHost(failedHosts, successTimesByHost), nil
}

func readProviderTLSWindow(reader io.Reader, targetID string, cutoff time.Time) (map[string]time.Time, map[string]time.Time, error) {
	failedHosts := make(map[string]time.Time)
	successTimesByHost := make(map[string]time.Time)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		entry, parsedTime, ok := parseProviderLifecycleEntry(scanner.Bytes(), targetID, cutoff)
		if !ok {
			continue
		}
		recordProviderLifecycleEntry(entry, parsedTime, failedHosts, successTimesByHost)
	}
	if err := scanner.Err(); err != nil {
		statusReportLog.Warn("statusreport.provider_health_scan_failed", "target", targetID, "err", err)
		return nil, nil, fmt.Errorf("scan provider lifecycle log: %w", err)
	}
	return failedHosts, successTimesByHost, nil
}

func parseProviderLifecycleEntry(line []byte, targetID string, cutoff time.Time) (mitmLifecycleEntry, time.Time, bool) {
	var entry mitmLifecycleEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return emptyMitmLifecycleEntry(), time.Time{}, false
	}
	if entry.Provider != targetID {
		return emptyMitmLifecycleEntry(), time.Time{}, false
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, entry.Time)
	if err != nil {
		return emptyMitmLifecycleEntry(), time.Time{}, false
	}
	if parsedTime.Before(cutoff) {
		return emptyMitmLifecycleEntry(), time.Time{}, false
	}
	return entry, parsedTime, true
}

func recordProviderLifecycleEntry(entry mitmLifecycleEntry, parsedTime time.Time, failedHosts map[string]time.Time, successTimesByHost map[string]time.Time) {
	switch entry.Message {
	case mitmLifecycleMessageClientTLSFailed:
		if parsedTime.After(failedHosts[entry.Host]) {
			failedHosts[entry.Host] = parsedTime
		}
	case mitmLifecycleMessageInterceptOpen:
		if parsedTime.After(successTimesByHost[entry.Host]) {
			successTimesByHost[entry.Host] = parsedTime
		}
	}
}

func latestUnclearedFailureHost(failedHosts map[string]time.Time, successTimesByHost map[string]time.Time) string {
	latestUnclearedFailureTime := time.Time{}
	latestUnclearedFailureHost := ""
	for host, failureTime := range failedHosts {
		successTime := successTimesByHost[host]
		if !successTime.IsZero() && successTime.After(failureTime) {
			continue
		}
		if failureTime.After(latestUnclearedFailureTime) {
			latestUnclearedFailureTime = failureTime
			latestUnclearedFailureHost = host
		}
	}
	return latestUnclearedFailureHost
}

func emptyMitmLifecycleEntry() mitmLifecycleEntry {
	return mitmLifecycleEntry{
		Time:     "",
		Message:  "",
		Provider: "",
		Host:     "",
	}
}
