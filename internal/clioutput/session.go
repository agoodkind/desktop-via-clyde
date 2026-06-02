package clioutput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
)

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
		renderer:   newRenderer(metadata, opts.Out, opts.Format),
		mu:         sync.Mutex{},
	}
	dryRun := opts.DryRun
	parallel := opts.Parallel
	event := NewEvent(EventRunStarted, opts.Operation)
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

// ProgressWriter returns a writer that converts milestone lines into events.
func (s *Session) ProgressWriter(target string) io.Writer {
	return &lineEventWriter{
		session: s,
		target:  target,
		pending: nil,
		mu:      sync.Mutex{},
	}
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

type lineEventWriter struct {
	session *Session
	target  string
	pending []byte
	mu      sync.Mutex
}

func (w *lineEventWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, data...)
	for {
		newlineIndex := bytes.IndexByte(w.pending, '\n')
		if newlineIndex == -1 {
			return len(data), nil
		}
		line := strings.TrimSpace(string(w.pending[:newlineIndex]))
		w.pending = append([]byte(nil), w.pending[newlineIndex+1:]...)
		if line == "" {
			continue
		}
		for _, event := range eventsFromLine(w.session.operation, w.target, line) {
			if err := w.session.Emit(event); err != nil {
				return 0, err
			}
		}
	}
}

func eventsFromLine(operation string, target string, line string) []Event {
	cleaned := stripKnownPrefixes(line)
	status := statusOK
	eventType := EventStepDone
	lower := strings.ToLower(cleaned)
	if strings.Contains(lower, "failed") || strings.Contains(lower, "error") {
		status = statusFailed
		eventType = EventStepFailed
	}
	if strings.Contains(lower, "skipped") || strings.Contains(lower, "skipping") || strings.Contains(lower, "nothing to do") {
		status = "skipped"
		eventType = EventStepSkipped
	}
	if parsedTarget, ok := parseTarget(cleaned); ok && target == "" {
		target = parsedTarget
	}
	step := stepName(cleaned)
	started := NewEvent(EventStepStarted, operation)
	started.Target = target
	started.Step = step
	started.Status = statusRunning
	started.Detail = cleaned
	event := NewEvent(eventType, operation)
	event.Target = target
	event.Step = step
	event.Status = status
	event.Detail = cleaned
	return []Event{started, event}
}

func stripKnownPrefixes(line string) string {
	cleaned := strings.TrimSpace(line)
	for _, prefix := range []string{"[run]", "[dry-run]"} {
		cleaned = strings.TrimSpace(strings.TrimPrefix(cleaned, prefix))
	}
	if strings.HasPrefix(cleaned, "[") {
		if closeIndex := strings.Index(cleaned, "]"); closeIndex > 0 {
			cleaned = strings.TrimSpace(cleaned[closeIndex+1:])
		}
	}
	return cleaned
}

func parseTarget(line string) (string, bool) {
	if !strings.HasPrefix(line, "target=") {
		return "", false
	}
	rest := strings.TrimPrefix(line, "target=")
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

func stepName(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return "progress"
	}
	if codexDetail, ok := strings.CutPrefix(detail, "codex-cli:"); ok {
		return normalizeStep(strings.TrimSpace(codexDetail))
	}
	if strings.HasPrefix(detail, "target=") {
		fields := strings.Fields(detail)
		if len(fields) > 1 {
			targetDetail := strings.Join(fields[1:], " ")
			if knownStep := knownTargetStep(targetDetail); knownStep != "" {
				return knownStep
			}
			return normalizeStep(strings.Join(fields[1:minInt(len(fields), 4)], " "))
		}
	}
	if colonIndex := strings.Index(detail, ":"); colonIndex > 0 && colonIndex < 32 {
		return normalizeStep(detail[:colonIndex])
	}
	fields := strings.Fields(detail)
	if len(fields) == 0 {
		return "progress"
	}
	return normalizeStep(strings.Join(fields[:minInt(len(fields), 3)], " "))
}

func normalizeStep(raw string) string {
	replacer := strings.NewReplacer(
		"=", " ",
		":", " ",
		"/", " ",
		".", " ",
		"-", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
		"\"", " ",
	)
	cleaned := strings.Join(strings.Fields(replacer.Replace(strings.ToLower(raw))), "_")
	if cleaned == "" {
		return "progress"
	}
	return cleaned
}

func knownTargetStep(detail string) string {
	if strings.Contains(detail, "read Info.plist") {
		return "read_info_plist"
	}
	if strings.Contains(detail, "capture upstream DR from clean executable") {
		return "capture_original_dr"
	}
	if strings.Contains(detail, "would find keychain") || strings.Contains(detail, "found") {
		return "keychain_capture"
	}
	if strings.Contains(detail, "entitlements: extract entitlements") {
		return "extract_entitlements"
	}
	if strings.Contains(detail, "augment entitlements") {
		return "augment_entitlements"
	}
	if strings.Contains(detail, "move original executable") {
		return "move_original_executable"
	}
	if strings.Contains(detail, "install shim") {
		return "install_shim"
	}
	if strings.Contains(detail, "install launch policy") {
		return "install_launch_policy"
	}
	if strings.Contains(detail, "re-sign with") {
		return "sign_bundle"
	}
	if strings.Contains(detail, "remove quarantine") {
		return "remove_quarantine"
	}
	if strings.Contains(detail, "would restore keychain") || strings.Contains(detail, "restored keychain") {
		return "keychain_restore"
	}
	if strings.Contains(detail, "write patch state") {
		return "write_state"
	}
	if strings.Contains(detail, "verify bundle signature") {
		return "verify_bundle"
	}
	if strings.Contains(detail, "patch complete") {
		return "patch_complete"
	}
	if strings.Contains(detail, "upgrade to") {
		return "upgrade_complete"
	}
	return ""
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

type eventMsg struct {
	Event Event
}

type closeMsg struct{}

type targetState struct {
	ID       string
	Status   string
	Step     string
	Detail   string
	LogFile  string
	Done     int
	Failed   bool
	Duration string
}

type liveModel struct {
	operation string
	runLog    string
	started   string
	targets   map[string]*targetState
	order     []string
	done      int
	failed    int
	total     int
	progress  progress.Model
}

func newLiveModel() liveModel {
	return liveModel{
		operation: "",
		runLog:    "",
		started:   "",
		targets:   map[string]*targetState{},
		order:     []string{},
		done:      0,
		failed:    0,
		total:     0,
		progress:  progress.New(progress.WithDefaultGradient()),
	}
}

func (m *liveModel) apply(event Event) {
	switch event.Type {
	case EventRunStarted:
		m.operation = event.Operation
		m.runLog = event.RunLog
		m.started = shortTime(event.Time)
	case EventTargetQueued:
		target := m.ensureTarget(event.Target)
		target.Status = statusQueued
		target.Step = "queued"
	case EventTargetStarted:
		target := m.ensureTarget(event.Target)
		target.Status = statusRunning
		target.Step = "starting"
		target.LogFile = event.LogFile
	case EventStepStarted:
		target := m.ensureTarget(event.Target)
		target.Status = statusRunning
		target.Step = event.Step
		target.Detail = event.Detail
	case EventStepDone, EventStepSkipped, EventStepFailed:
		target := m.ensureTarget(event.Target)
		target.Status = statusRunning
		target.Step = event.Step
		target.Detail = event.Detail
		target.Done++
		if event.Type == EventStepSkipped {
			target.Status = "skipped"
		}
		if event.Type == EventStepFailed {
			target.Failed = true
			target.Status = statusFailed
		}
	case EventTargetDone:
		target := m.ensureTarget(event.Target)
		target.Status = event.Status
		target.Duration = durationValue(event.DurationMS)
		if event.Status == statusFailed {
			target.Failed = true
		}
	case EventRunDone:
		m.done = intValueFromPointer(event.Succeeded)
		m.failed = intValueFromPointer(event.Failed)
		m.total = m.done + m.failed
	}
}

func (m *liveModel) ensureTarget(id string) *targetState {
	if id == "" {
		id = "run"
	}
	if target, ok := m.targets[id]; ok {
		return target
	}
	target := &targetState{
		ID:       id,
		Status:   statusQueued,
		Step:     "queued",
		Detail:   "",
		LogFile:  "",
		Done:     0,
		Failed:   false,
		Duration: "",
	}
	m.targets[id] = target
	m.order = append(m.order, id)
	sort.Strings(m.order)
	return target
}

func (m *liveModel) View() string {
	headerStyle := lipgloss.NewStyle().Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	failStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	runStatus := strings.Builder{}
	runStatus.WriteString(headerStyle.Render(title(m.operation)) + "\n")
	if m.started != "" {
		runStatus.WriteString("started  " + m.started + "\n")
	}
	if m.runLog != "" {
		runStatus.WriteString("run log  " + m.runLog + "\n")
	}
	runStatus.WriteString("\n")
	for _, id := range m.order {
		target := m.targets[id]
		status := target.Status
		if target.Failed {
			status = failStyle.Render(status)
		} else if status == statusOK {
			status = okStyle.Render(status)
		}
		_, _ = fmt.Fprintf(&runStatus, "%-8s %-10s %s\n", target.ID, status, target.Step)
		if target.LogFile != "" {
			runStatus.WriteString(mutedStyle.Render("log     "+target.LogFile) + "\n")
		}
		if target.Detail != "" {
			runStatus.WriteString("  " + target.Detail + "\n")
		}
		runStatus.WriteString("\n")
	}
	totalSteps := 0
	for _, target := range m.targets {
		totalSteps += target.Done
	}
	if len(m.targets) > 0 {
		runStatus.WriteString(m.progress.ViewAs(float64(totalSteps)/float64(maxInt(totalSteps, 1))) + "\n")
	}
	if m.total > 0 {
		_, _ = fmt.Fprintf(&runStatus, "Result  completed %d   failed %d\n", m.done, m.failed)
	}
	return runStatus.String()
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

func intValueFromPointer(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func wrapWriteError(err error) error {
	if err == nil {
		return nil
	}
	slog.Warn("clioutput.text.write_failed", "err", err)
	return fmt.Errorf("write text progress event: %w", err)
}
