package daemon

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/operations"
)

// newOperationJob builds the job that runs one app operation (patch, upgrade,
// hard-reset, keychain-migrate) for one target and emits its progress events.
// The job mirrors the single-target flow in internal/clispec, but its session
// renders to the broadcaster instead of a terminal so every subscribed client
// sees the same run.
func newOperationJob(capability string, operation string, targetID string, format string, flags *desktopviaclydev1.OperationFlags) operationJob {
	return func(ctx context.Context, emit func(event *desktopviaclydev1.ProgressEvent)) error {
		appTarget := lookupAppTarget(targetID)
		if appTarget == nil {
			err := fmt.Errorf("unknown target %q", targetID)
			daemonLog.ErrorContext(ctx, "daemon.job.target_missing", "err", err, "target", targetID, "operation", operation)
			return err
		}
		outputFormat := parseFormat(format)
		session, err := clioutput.NewBroadcastSession(ctx, clioutput.SessionOptions{
			Out:       io.Discard,
			Format:    outputFormat,
			Operation: operation,
			Scope:     targetID,
			Parallel:  1,
			DryRun:    flags.GetBools()["dry-run"],
		}, func(event clioutput.Event) error {
			emit(eventToProto(event))
			return nil
		})
		if err != nil {
			daemonLog.ErrorContext(ctx, "daemon.job.session_failed", "err", err, "target", targetID)
			return fmt.Errorf("create broadcast session: %w", err)
		}
		rawLog, _, err := session.OpenTargetLog(targetID)
		if err != nil {
			daemonLog.ErrorContext(ctx, "daemon.job.open_log_failed", "err", err, "target", targetID)
			return fmt.Errorf("open target log: %w", err)
		}
		started := clock.Now()
		runErr := operations.Run(ctx, operations.Request{
			Out:        rawLog,
			LogOut:     rawLog,
			Progress:   session.TargetProgress(targetID),
			App:        appTarget,
			CLI:        nil,
			Capability: capability,
			Flags:      buildFlagValues(flags),
			Format:     outputFormat,
		})
		_ = rawLog.Close()
		// The operation's outcome (success, skipped, or failed) is carried to
		// every subscriber through the terminal events finishRun emits, so the
		// job itself returns nil; only setup failures above propagate as errors.
		finishRun(ctx, session, targetID, started, runErr)
		return nil
	}
}

// finishRun emits the operation-level failure (if any) and the authoritative
// terminal events, then closes the session. Emit failures are logged rather than
// returned because the run already produced its result.
func finishRun(ctx context.Context, session *clioutput.Session, targetID string, started time.Time, runErr error) {
	if runErr != nil {
		if emitErr := session.EmitStepFailed(targetID, runErr.Error()); emitErr != nil {
			daemonLog.WarnContext(ctx, "daemon.job.emit_step_failed", "err", emitErr, "target", targetID)
		}
	}
	duration := clock.Since(started)
	if emitErr := session.EmitTargetDone(targetID, runErr, duration); emitErr != nil {
		daemonLog.WarnContext(ctx, "daemon.job.emit_target_done_failed", "err", emitErr, "target", targetID)
	}
	if closeErr := session.Close([]clioutput.TargetResult{
		clioutput.NewTargetResult(targetID, "app", runErr, duration),
	}); closeErr != nil {
		daemonLog.WarnContext(ctx, "daemon.job.close_failed", "err", closeErr, "target", targetID)
	}
}

// buildFlagValues rebuilds operations.FlagValues from the wire flag maps.
func buildFlagValues(flags *desktopviaclydev1.OperationFlags) operations.FlagValues {
	values := operations.NewFlagValues()
	for name, value := range flags.GetStrings() {
		values.SetString(name, value)
	}
	for name, value := range flags.GetBools() {
		values.SetBool(name, value)
	}
	return values
}

// parseFormat resolves the wire format string, defaulting to text when empty or
// unrecognized so a daemon run never fails on a missing format.
func parseFormat(format string) clioutput.Format {
	trimmed := strings.TrimSpace(format)
	if trimmed == "" {
		return clioutput.FormatText
	}
	parsed, err := clioutput.ParseFormat(trimmed)
	if err != nil {
		return clioutput.FormatText
	}
	return parsed
}
