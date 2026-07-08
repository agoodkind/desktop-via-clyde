package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type loadAverageOS string

const (
	loadAverageOSDarwin loadAverageOS = "darwin"
	loadAverageOSLinux  loadAverageOS = "linux"
	loadAverageTimeout                = 2 * time.Second
)

func readOneMinuteLoadAverage(ctx context.Context) (float64, error) {
	daemonLog.DebugContext(ctx, "daemon.load_average.boundary", "goos", runtime.GOOS)
	switch loadAverageOS(runtime.GOOS) {
	case loadAverageOSDarwin:
		cmdCtx, cancel := context.WithTimeout(ctx, loadAverageTimeout)
		defer cancel()
		output, err := exec.CommandContext(cmdCtx, "/usr/sbin/sysctl", "-n", "vm.loadavg").Output()
		if err != nil {
			daemonLog.ErrorContext(ctx, "daemon.load_average.darwin_failed", "err", err)
			return 0, fmt.Errorf("read darwin load average: %w", err)
		}
		loadAverage, err := parseLoadAverage(output)
		if err != nil {
			daemonLog.ErrorContext(ctx, "daemon.load_average.darwin_parse_failed", "err", err)
			return 0, fmt.Errorf("parse darwin load average: %w", err)
		}
		return loadAverage, nil
	case loadAverageOSLinux:
		output, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			daemonLog.ErrorContext(ctx, "daemon.load_average.linux_failed", "err", err)
			return 0, fmt.Errorf("read linux load average: %w", err)
		}
		loadAverage, err := parseLoadAverage(output)
		if err != nil {
			daemonLog.ErrorContext(ctx, "daemon.load_average.linux_parse_failed", "err", err)
			return 0, fmt.Errorf("parse linux load average: %w", err)
		}
		return loadAverage, nil
	default:
		err := fmt.Errorf("load average is unsupported on %s", runtime.GOOS)
		daemonLog.ErrorContext(ctx, "daemon.load_average.unsupported_os", "err", err, "goos", runtime.GOOS)
		return 0, err
	}
}

func parseLoadAverage(output []byte) (float64, error) {
	trimmed := strings.TrimSpace(string(output))
	trimmed = strings.Trim(trimmed, "{}")
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return 0, fmt.Errorf("load average output is empty")
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		daemonLog.Error("daemon.load_average.parse_float_failed", "err", err, "value", fields[0])
		return 0, fmt.Errorf("parse one minute load average %q failed: %w", fields[0], err)
	}
	return value, nil
}
