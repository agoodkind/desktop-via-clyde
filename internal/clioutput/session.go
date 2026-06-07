package clioutput

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"

	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/response"
)

// EventType names one progress event emitted by patch and upgrade commands.
type EventType string

const (
	// EventRunStarted marks the start of a command run.
	EventRunStarted EventType = "run_started"
	// EventTargetQueued marks a target waiting for an execution slot.
	EventTargetQueued EventType = "target_queued"
	// EventTargetStarted marks a target beginning execution.
	EventTargetStarted EventType = "target_started"
	// EventStepStarted marks one user-visible milestone beginning.
	EventStepStarted EventType = "step_started"
	// EventStepDone marks one user-visible milestone completing.
	EventStepDone EventType = "step_done"
	// EventStepSkipped marks one user-visible milestone being skipped.
	EventStepSkipped EventType = "step_skipped"
	// EventStepFailed marks one user-visible milestone failing.
	EventStepFailed EventType = "step_failed"
	// EventTargetDone marks one target completing.
	EventTargetDone EventType = "target_done"
	// EventRunDone marks the full command run completing.
	EventRunDone EventType = "run_done"

	statusQueued  = "queued"
	statusRunning = "running"
	statusOK      = "ok"
	statusFailed  = "failed"
	statusSkipped = "skipped"
)

// Outcome is a target's terminal run outcome, set explicitly by the operation
// (or derived from its returned error). It reuses the existing wire status
// strings, so the JSON contract is unchanged.
type Outcome string

const (
	// OutcomeOK marks a target whose run performed work successfully.
	OutcomeOK Outcome = statusOK
	// OutcomeSkipped marks a target whose run had nothing to do (already current,
	// no update available).
	OutcomeSkipped Outcome = statusSkipped
	// OutcomeFailed marks a target whose run failed.
	OutcomeFailed Outcome = statusFailed
)

// Progress is the typed milestone sink operations use instead of printing prose.
// The renderer derives no status from these calls: Step is always a successful
// milestone, Skip and Fail are display-only step markers, and only SetOutcome
// (or a returned error) sets the target's terminal status.
type Progress interface {
	Step(detail string)
	Skip(detail string)
	Fail(detail string)
	SetOutcome(outcome Outcome, detail string)
}

// Event is one structured progress event for patch and upgrade commands.
type Event struct {
	Type       EventType `json:"type"`
	Operation  string    `json:"operation,omitempty"`
	Target     string    `json:"target,omitempty"`
	Step       string    `json:"step,omitempty"`
	Status     string    `json:"status,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Time       string    `json:"time,omitempty"`
	RunLog     string    `json:"run_log,omitempty"`
	LogFile    string    `json:"log_file,omitempty"`
	DryRun     *bool     `json:"dry_run,omitempty"`
	Parallel   *int      `json:"parallel,omitempty"`
	DurationMS *int64    `json:"duration_ms,omitempty"`
	Succeeded  *int      `json:"succeeded,omitempty"`
	Failed     *int      `json:"failed,omitempty"`
}

// TargetResult records the outcome for one rendered target.
type TargetResult struct {
	ID       string
	Kind     string
	Err      error
	Duration time.Duration
}

// NewEvent creates one progress event with every field initialized for lint.
func NewEvent(eventType EventType, operation string) Event {
	return Event{
		Type:       eventType,
		Operation:  operation,
		Target:     "",
		Step:       "",
		Status:     "",
		Detail:     "",
		Time:       "",
		RunLog:     "",
		LogFile:    "",
		DryRun:     nil,
		Parallel:   nil,
		DurationMS: nil,
		Succeeded:  nil,
		Failed:     nil,
	}
}

// NewTargetResult creates one rendered target result.
func NewTargetResult(id string, kind string, err error, duration time.Duration) TargetResult {
	return TargetResult{
		ID:       id,
		Kind:     kind,
		Err:      err,
		Duration: duration,
	}
}

// SessionOptions configures one command-output session.
type SessionOptions struct {
	Out       io.Writer
	Format    Format
	Operation string
	Scope     string
	Parallel  int
	DryRun    bool
}

// Session owns stdout rendering and per-run log files.
type Session struct {
	operation  string
	metadata   response.Metadata
	started    time.Time
	runLog     *os.File
	runLogPath string
	renderer   renderer
	mu         sync.Mutex
	outcomes   map[string]recordedOutcome
}

type recordedOutcome struct {
	outcome Outcome
	detail  string
}

type renderer interface {
	Emit(Event) error
	Close() error
}

// NewSession creates stdout renderers and writes the opening run event.
func NewSession(ctx context.Context, opts SessionOptions) (*Session, error) {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	return newSession(ctx, opts, newRenderer(response.FromContext(ctx), opts.Out, opts.Format))
}

// NewBroadcastSession creates a session whose events are delivered to emit
// instead of being rendered to a terminal. The daemon uses it to fan a run's
// progress events to every subscribed client stream, so a streamed run renders
// through the same live model a local run uses.
func NewBroadcastSession(ctx context.Context, opts SessionOptions, emit func(Event) error) (*Session, error) {
	return newSession(ctx, opts, &emitterRenderer{emit: emit})
}

// newSession opens the run log and writes the opening run event against the
// supplied renderer, which is either the stdout renderer or the broadcast
// emitter.
func newSession(ctx context.Context, opts SessionOptions, eventRenderer renderer) (*Session, error) {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	started := clock.Now()
	scope := strings.TrimSpace(opts.Scope)
	if scope == "" {
		scope = strings.TrimSpace(opts.Operation)
	}
	runLogPath := filepath.Join(operationLogDir(opts.Operation), scope+"-"+timestamp(started)+".jsonl")
	if err := os.MkdirAll(filepath.Dir(runLogPath), 0o755); err != nil {
		slog.WarnContext(ctx, "clioutput.session.create_log_dir_failed", "err", err, "path", filepath.Dir(runLogPath))
		return nil, fmt.Errorf("create operation log dir: %w", err)
	}
	runLog, err := os.OpenFile(runLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.WarnContext(ctx, "clioutput.session.open_run_log_failed", "err", err, "path", runLogPath)
		return nil, fmt.Errorf("open run log: %w", err)
	}
	metadata := response.FromContext(ctx)
	session := &Session{
		operation:  opts.Operation,
		metadata:   metadata,
		started:    started,
		runLog:     runLog,
		runLogPath: runLogPath,
		renderer:   eventRenderer,
		mu:         sync.Mutex{},
		outcomes:   map[string]recordedOutcome{},
	}
	dryRun := opts.DryRun
	parallel := opts.Parallel
	event := NewEvent(EventRunStarted, opts.Operation)
	event.Target = scope
	event.Time = started.Format(time.RFC3339)
	event.RunLog = runLogPath
	event.DryRun = &dryRun
	event.Parallel = &parallel
	if err := session.Emit(event); err != nil {
		_ = runLog.Close()
		return nil, err
	}
	return session, nil
}

// TargetProgress returns the typed milestone sink for one target. Operations
// call its methods directly; nothing is parsed back out of log prose.
func (s *Session) TargetProgress(target string) Progress {
	return &targetProgress{session: s, target: target}
}

type targetProgress struct {
	session *Session
	target  string
}

func (p *targetProgress) Step(detail string) {
	p.emit(EventStepDone, statusOK, detail)
}

func (p *targetProgress) Skip(detail string) {
	p.emit(EventStepSkipped, statusSkipped, detail)
}

func (p *targetProgress) Fail(detail string) {
	p.emit(EventStepFailed, statusFailed, detail)
}

func (p *targetProgress) emit(eventType EventType, status string, detail string) {
	started := NewEvent(EventStepStarted, p.session.operation)
	started.Target = p.target
	started.Status = statusRunning
	started.Detail = detail
	if err := p.session.Emit(started); err != nil {
		slog.Warn("clioutput.progress.emit_started_failed", "err", err, "target", p.target)
	}
	event := NewEvent(eventType, p.session.operation)
	event.Target = p.target
	event.Status = status
	event.Detail = detail
	if err := p.session.Emit(event); err != nil {
		slog.Warn("clioutput.progress.emit_step_failed", "err", err, "target", p.target)
	}
}

func (p *targetProgress) SetOutcome(outcome Outcome, detail string) {
	p.session.recordOutcome(p.target, outcome, detail)
}

func (s *Session) recordOutcome(target string, outcome Outcome, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes[target] = recordedOutcome{outcome: outcome, detail: detail}
}

// EmitTargetDone writes the authoritative terminal event for one target. The
// status comes from runErr (failed) or the outcome the operation recorded
// (defaulting to ok); step-level outcomes never reach this decision.
func (s *Session) EmitTargetDone(target string, runErr error, duration time.Duration) error {
	status := statusOK
	detail := ""
	switch {
	case runErr != nil:
		status = statusFailed
		detail = strings.TrimSpace(runErr.Error())
	default:
		s.mu.Lock()
		recorded, ok := s.outcomes[target]
		s.mu.Unlock()
		if ok {
			status = string(recorded.outcome)
			detail = recorded.detail
		}
	}
	durationMS := duration.Milliseconds()
	event := NewEvent(EventTargetDone, s.operation)
	event.Target = target
	event.Status = status
	event.DurationMS = &durationMS
	if detail != "" {
		event.Detail = detail
	}
	return s.Emit(event)
}

// OpenTargetLog creates the raw log file for one target and emits target start.
func (s *Session) OpenTargetLog(target string) (*os.File, string, error) {
	logPath := s.targetLogPath(target)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		slog.Warn("clioutput.session.create_target_log_dir_failed", "err", err, "path", filepath.Dir(logPath))
		return nil, "", fmt.Errorf("create target log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Warn("clioutput.session.open_target_log_failed", "err", err, "path", logPath)
		return nil, "", fmt.Errorf("open target log: %w", err)
	}
	event := NewEvent(EventTargetStarted, s.operation)
	event.Target = target
	event.Status = statusRunning
	event.LogFile = logPath
	if err := s.Emit(event); err != nil {
		_ = logFile.Close()
		return nil, "", err
	}
	return logFile, logPath, nil
}

// EmitStepFailed records an operation-level target failure.
func (s *Session) EmitStepFailed(target string, detail string) error {
	event := NewEvent(EventStepFailed, s.operation)
	event.Target = target
	event.Step = "operation_failed"
	event.Status = statusFailed
	event.Detail = strings.TrimSpace(detail)
	return s.Emit(event)
}

// Emit writes one event to the run log and stdout renderer.
func (s *Session) Emit(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.Operation == "" {
		event.Operation = s.operation
	}
	if event.Time == "" {
		event.Time = clock.Now().Format(time.RFC3339)
	}
	if event.RunLog == "" && event.Type == EventRunStarted {
		event.RunLog = s.runLogPath
	}
	if event.RunLog == "" {
		event.RunLog = s.runLogPath
	}
	if event.Target != "" && event.LogFile == "" {
		event.LogFile = s.targetLogPath(event.Target)
	}
	if err := writeEventJSONLine(s.runLog, s.metadata, event); err != nil {
		return err
	}
	if err := s.renderer.Emit(event); err != nil {
		slog.Warn("clioutput.session.emit_renderer_failed", "err", err, "event_type", event.Type)
		return fmt.Errorf("emit progress event: %w", err)
	}
	return nil
}

func (s *Session) targetLogPath(target string) string {
	return filepath.Join(operationLogDir(s.operation), target+"-"+timestamp(s.started)+".log")
}

// Close writes the final summary event and closes renderers and logs.
func (s *Session) Close(results []TargetResult) error {
	failedCount := 0
	for _, result := range results {
		if result.Err != nil {
			failedCount++
		}
	}
	status := statusOK
	if failedCount > 0 {
		status = statusFailed
	}
	duration := clock.Since(s.started)
	durationMS := duration.Milliseconds()
	succeeded := len(results) - failedCount
	event := NewEvent(EventRunDone, s.operation)
	event.Status = status
	event.DurationMS = &durationMS
	event.Succeeded = &succeeded
	event.Failed = &failedCount
	renderErr := s.Emit(event)
	closeErr := s.renderer.Close()
	logErr := s.runLog.Close()
	if err := errors.Join(renderErr, closeErr, logErr); err != nil {
		slog.Warn("clioutput.session.close_failed", "err", err)
		return err
	}
	return nil
}

type jsonRenderer struct {
	metadata response.Metadata
	out      io.Writer
}

func (r *jsonRenderer) Emit(event Event) error {
	return writeEventJSONLine(r.out, r.metadata, event)
}

func (r *jsonRenderer) Close() error {
	return nil
}

type textRenderer struct {
	out io.Writer
}

func (r *textRenderer) Emit(event Event) error {
	switch event.Type {
	case EventRunStarted:
		_, err := fmt.Fprintf(r.out, "%s %s\nmode dry-run=%s parallel=%s\nrun log %s\n\n",
			title(event.Operation),
			scopeLabel(event),
			boolValue(event.DryRun),
			intValue(event.Parallel),
			event.RunLog)
		return wrapWriteError(err)
	case EventTargetQueued:
		_, err := fmt.Fprintf(r.out, "[%s] %s queued\n", shortTime(event.Time), event.Target)
		return wrapWriteError(err)
	case EventTargetStarted:
		_, err := fmt.Fprintf(r.out, "[%s] %s started log=%s\n", shortTime(event.Time), event.Target, event.LogFile)
		return wrapWriteError(err)
	case EventStepStarted:
		_, err := fmt.Fprintf(r.out, "[%s] %s %s started detail=%s\n", shortTime(event.Time), event.Target, event.Step, strconv.Quote(event.Detail))
		return wrapWriteError(err)
	case EventStepDone, EventStepSkipped, EventStepFailed:
		_, err := fmt.Fprintf(r.out, "[%s] %s %s %s detail=%s\n", shortTime(event.Time), event.Target, event.Step, event.Status, strconv.Quote(event.Detail))
		return wrapWriteError(err)
	case EventTargetDone:
		_, err := fmt.Fprintf(r.out, "[%s] %s completed status=%s duration=%s\n", shortTime(event.Time), event.Target, event.Status, durationValue(event.DurationMS))
		return wrapWriteError(err)
	case EventRunDone:
		_, err := fmt.Fprintf(r.out, "\nResult completed=%s failed=%s duration=%s\n", intValue(event.Succeeded), intValue(event.Failed), durationValue(event.DurationMS))
		return wrapWriteError(err)
	default:
		return nil
	}
}

func (r *textRenderer) Close() error {
	return nil
}

type liveRenderer struct {
	program *tea.Program
}

func (r *liveRenderer) Emit(event Event) error {
	r.program.Send(eventMsg{Event: event})
	return nil
}

func (r *liveRenderer) Close() error {
	r.program.Send(closeMsg{})
	r.program.Wait()
	return nil
}

// emitterRenderer adapts an emit callback to the renderer interface so a caller
// can fan session events to a custom sink, such as the daemon's gRPC
// subscribers, instead of a terminal.
type emitterRenderer struct {
	emit func(Event) error
}

func (r *emitterRenderer) Emit(event Event) error {
	return r.emit(event)
}

func (r *emitterRenderer) Close() error {
	return nil
}

func newRenderer(metadata response.Metadata, out io.Writer, format Format) renderer {
	if format == FormatJSON {
		return &jsonRenderer{metadata: metadata, out: out}
	}
	if file, ok := out.(*os.File); ok && isTerminal(file) {
		model := newLiveModel()
		program := tea.NewProgram(newBubbleLiveModel(&model), tea.WithInput(nil), tea.WithOutput(file), tea.WithoutSignalHandler())
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("clioutput.live_renderer.panic", "err", fmt.Errorf("panic: %v", recovered))
				}
			}()
			if _, err := program.Run(); err != nil {
				slog.Warn("clioutput.live_renderer.run_failed", "err", err)
			}
		}()
		return &liveRenderer{program: program}
	}
	return &textRenderer{out: out}
}

func isTerminal(file *os.File) bool {
	if strings.TrimSpace(os.Getenv("TERM")) == "dumb" {
		return false
	}
	return isatty.IsTerminal(file.Fd())
}

type eventDocument struct {
	Meta response.Metadata `json:"_meta"`
	Event
}

func writeEventJSONLine(out io.Writer, metadata response.Metadata, event Event) error {
	document := eventDocument{
		Meta:  metadata,
		Event: event,
	}
	documentBody, err := json.Marshal(document)
	if err != nil {
		slog.Warn("clioutput.event.document_marshal_failed", "err", err)
		return fmt.Errorf("marshal progress event document: %w", err)
	}
	if _, err := out.Write(append(documentBody, '\n')); err != nil {
		slog.Warn("clioutput.event.write_failed", "err", err)
		return fmt.Errorf("write progress event: %w", err)
	}
	return nil
}

func operationLogDir(operation string) string {
	return filepath.Join(paths.LogDir(), strings.TrimSpace(operation))
}

func timestamp(t time.Time) string {
	return t.Format("20060102T150405")
}

func title(operation string) string {
	trimmed := strings.TrimSpace(operation)
	if trimmed == "patch" {
		return "Patch"
	}
	if trimmed == "upgrade" {
		return "Upgrade"
	}
	if trimmed == "" {
		return ""
	}
	return strings.ToUpper(trimmed[:1]) + trimmed[1:]
}

func scopeLabel(event Event) string {
	if event.Target == "" {
		return "all"
	}
	return event.Target
}

func shortTime(raw string) string {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return parsed.Format("15:04:05")
}

func durationValue(duration *int64) string {
	if duration == nil {
		return ""
	}
	return (time.Duration(*duration) * time.Millisecond).Round(100 * time.Millisecond).String()
}

func boolValue(value *bool) string {
	if value == nil {
		return ""
	}
	return strconv.FormatBool(*value)
}

func intValue(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func wrapWriteError(err error) error {
	if err == nil {
		return nil
	}
	slog.Warn("clioutput.text.write_failed", "err", err)
	return fmt.Errorf("write text progress event: %w", err)
}
