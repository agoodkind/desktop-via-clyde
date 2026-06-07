package daemon

import (
	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
)

// eventToProto converts a clioutput.Event into its wire ProgressEvent. The
// optional pointer scalars carry through, with the count fields widened to the
// int32 the proto layer uses.
func eventToProto(event clioutput.Event) *desktopviaclydev1.ProgressEvent {
	wire := &desktopviaclydev1.ProgressEvent{
		Type:      string(event.Type),
		Operation: event.Operation,
		Target:    event.Target,
		Step:      event.Step,
		Status:    event.Status,
		Detail:    event.Detail,
		Time:      event.Time,
		RunLog:    event.RunLog,
		LogFile:   event.LogFile,
	}
	if event.DryRun != nil {
		value := *event.DryRun
		wire.DryRun = &value
	}
	if event.Parallel != nil {
		value := intToInt32(*event.Parallel)
		wire.Parallel = &value
	}
	if event.DurationMS != nil {
		value := *event.DurationMS
		wire.DurationMs = &value
	}
	if event.Succeeded != nil {
		value := intToInt32(*event.Succeeded)
		wire.Succeeded = &value
	}
	if event.Failed != nil {
		value := intToInt32(*event.Failed)
		wire.Failed = &value
	}
	return wire
}

// intToInt32 narrows a count to int32, clamping rather than wrapping so a
// pathological value never silently flips sign on the wire. Progress counts are
// small in practice, so the clamp is a safety bound, not an expected path.
func intToInt32(value int) int32 {
	const maxInt32 = 1<<31 - 1
	const minInt32 = -(1 << 31)
	if value > maxInt32 {
		return maxInt32
	}
	if value < minInt32 {
		return minInt32
	}
	return int32(value)
}
