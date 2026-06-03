package clioutput

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

type eventMsg struct {
	Event Event
}

type closeMsg struct{}

const (
	targetColumnWidth = 11
	stateColumnWidth  = 11
	stepColumnWidth   = 30
	columnGap         = "    "
)

type targetState struct {
	ID       string
	Status   string
	Step     string
	Detail   string
	LogFile  string
	Done     int
	Failed   bool
	Skipped  bool
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
	spinner   spinner.Model
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
		spinner:   spinner.New(spinner.WithSpinner(spinner.Dot)),
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
		updateTargetLog(target, event)
	case EventTargetStarted:
		target := m.ensureTarget(event.Target)
		target.Status = statusRunning
		target.Step = "starting"
		updateTargetLog(target, event)
	case EventStepStarted:
		target := m.ensureTarget(event.Target)
		target.Status = statusRunning
		target.Step = event.Step
		target.Detail = event.Detail
		updateTargetLog(target, event)
	case EventStepDone, EventStepSkipped, EventStepFailed:
		target := m.ensureTarget(event.Target)
		target.Status = statusRunning
		target.Step = event.Step
		target.Detail = event.Detail
		target.Done++
		updateTargetLog(target, event)
		if event.Type == EventStepSkipped {
			target.Status = statusSkipped
			target.Skipped = true
		}
		if event.Type == EventStepFailed {
			target.Failed = true
			target.Status = statusFailed
		}
	case EventTargetDone:
		target := m.ensureTarget(event.Target)
		target.Status = event.Status
		target.Duration = durationValue(event.DurationMS)
		if event.Step != "" {
			target.Step = event.Step
		}
		if event.Detail != "" {
			target.Detail = event.Detail
		}
		updateTargetLog(target, event)
		switch {
		case event.Status == statusFailed:
			target.Failed = true
			target.Skipped = false
		case target.Skipped || target.Status == statusSkipped:
			target.Failed = false
			target.Status = statusSkipped
			target.Skipped = true
		default:
			target.Failed = false
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
		Skipped:  false,
		Duration: "",
	}
	m.targets[id] = target
	m.order = append(m.order, id)
	sort.Strings(m.order)
	return target
}

func updateTargetLog(target *targetState, event Event) {
	if event.LogFile == "" {
		return
	}
	target.LogFile = event.LogFile
}

func (m *liveModel) View() string {
	headerStyle := lipgloss.NewStyle().Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	skippedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	failStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	stateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("69"))
	runStatus := strings.Builder{}
	okCount, skippedCount, failedCount, _ := m.targetCounts()
	runState := "running"
	if m.total > 0 {
		runState = "finished"
	}
	_, _ = fmt.Fprintf(&runStatus, "%s%s%s%s%s", headerStyle.Render(title(m.operation)), columnGap, runState, columnGap, targetCountLabel(len(m.targets)))
	if m.total > 0 {
		_, _ = fmt.Fprintf(&runStatus, "%s%d ok%s%d skipped%s%d failed", columnGap, okCount, columnGap, skippedCount, columnGap, failedCount)
	}
	if m.started != "" {
		runStatus.WriteString(columnGap + "started " + m.started)
	}
	runStatus.WriteString("\n")
	if m.runLog != "" {
		runStatus.WriteString("run log" + columnGap + m.runLog + "\n")
	}
	runStatus.WriteString("\n")
	if len(m.targets) > 0 {
		runStatus.WriteString(mutedStyle.Render(formatTableHeader()) + "\n")
	}
	for _, id := range m.order {
		target := m.targets[id]
		state := renderStateCell(target, m.spinner.View(), okStyle, skippedStyle, failStyle, stateStyle)
		runStatus.WriteString(formatTargetRow(target.ID, state, displayStep(target.Step), displayDetail(target)) + "\n")
	}
	if hasTargetLogs(m) {
		runStatus.WriteString("\n")
		runStatus.WriteString(headerStyle.Render("Logs") + "\n")
		for _, id := range m.order {
			target := m.targets[id]
			if target.LogFile == "" {
				continue
			}
			runStatus.WriteString(mutedStyle.Render(formatLogRow(target.ID, target.LogFile)) + "\n")
		}
	}
	return runStatus.String()
}

func formatTableHeader() string {
	return formatTargetRow("TARGET", "STATE", "STEP", "DETAIL")
}

func formatTargetRow(target string, state string, step string, detail string) string {
	return fmt.Sprintf(
		"%-*s%s%-*s%s%-*s%s%s",
		targetColumnWidth,
		target,
		columnGap,
		stateColumnWidth,
		state,
		columnGap,
		stepColumnWidth,
		step,
		columnGap,
		detail,
	)
}

func formatLogRow(target string, logFile string) string {
	return fmt.Sprintf("%-*s%s%s", targetColumnWidth, target, columnGap, logFile)
}

func hasTargetLogs(m *liveModel) bool {
	for _, target := range m.targets {
		if target.LogFile != "" {
			return true
		}
	}
	return false
}

func (m *liveModel) targetCounts() (int, int, int, int) {
	okCount := 0
	skippedCount := 0
	failedCount := 0
	activeCount := 0
	for _, target := range m.targets {
		switch targetDisplayStatus(target) {
		case statusFailed:
			failedCount++
		case statusSkipped:
			skippedCount++
		case statusRunning, statusQueued:
			activeCount++
		case statusOK:
			okCount++
		}
	}
	return okCount, skippedCount, failedCount, activeCount
}

func targetCountLabel(count int) string {
	if count == 1 {
		return "1 target"
	}
	return fmt.Sprintf("%d targets", count)
}

func targetDisplayStatus(target *targetState) string {
	switch {
	case target.Failed || target.Status == statusFailed:
		return statusFailed
	case target.Skipped || target.Status == statusSkipped:
		return statusSkipped
	case target.Status == statusOK:
		return statusOK
	case target.Status == statusRunning:
		return statusRunning
	case target.Status == statusQueued:
		return statusQueued
	default:
		return target.Status
	}
}

func renderStateCell(
	target *targetState,
	spinnerFrame string,
	okStyle lipgloss.Style,
	skippedStyle lipgloss.Style,
	failStyle lipgloss.Style,
	stateStyle lipgloss.Style,
) string {
	state := targetDisplayStatus(target)
	style := lipgloss.NewStyle()
	switch state {
	case statusFailed:
		style = failStyle
	case statusSkipped:
		style = skippedStyle
	case statusOK:
		style = okStyle
	case statusRunning:
		state = spinnerFrame + " " + state
		style = stateStyle
	}
	return style.Render(fmt.Sprintf("%-*s", stateColumnWidth, state))
}

func displayStep(step string) string {
	if step == "already_on_version" {
		return "version current"
	}
	cleaned := strings.ReplaceAll(strings.TrimSpace(step), "_", " ")
	if cleaned == "" {
		return "progress"
	}
	return cleaned
}

func displayDetail(target *targetState) string {
	detail := strings.TrimSpace(target.Detail)
	if detail == "" {
		return ""
	}
	if trimmed, ok := strings.CutPrefix(detail, "target="+target.ID+" "); ok {
		detail = strings.TrimSpace(trimmed)
	}
	if trimmed, ok := strings.CutPrefix(detail, target.ID+":"); ok {
		detail = strings.TrimSpace(trimmed)
	}
	if target.Step == "already_on_version" {
		return currentVersionDetail(detail)
	}
	if target.Step == "upgrade_complete" {
		return upgradedVersionDetail(detail)
	}
	return trimDisplayedStepPrefix(detail, displayStep(target.Step))
}

func currentVersionDetail(detail string) string {
	version := versionFromAlreadyOnVersion(detail)
	if version == detail {
		return detail
	}
	return "installed " + version
}

func versionFromAlreadyOnVersion(detail string) string {
	const prefix = "already on version "
	rest := strings.TrimSpace(strings.TrimPrefix(detail, prefix))
	if rest == detail {
		return detail
	}
	version, _, ok := strings.Cut(rest, ";")
	if !ok {
		return rest
	}
	return strings.TrimSpace(version)
}

func upgradedVersionDetail(detail string) string {
	const prefix = "upgrade to "
	rest := strings.TrimSpace(strings.TrimPrefix(detail, prefix))
	if rest == detail {
		return detail
	}
	return "upgraded to " + strings.TrimSpace(strings.TrimSuffix(rest, " complete"))
}

func trimDisplayedStepPrefix(detail string, step string) string {
	trimmedDetail := strings.TrimSpace(detail)
	trimmedStep := strings.TrimSpace(step)
	if trimmedDetail == "" || trimmedStep == "" {
		return trimmedDetail
	}
	lowerDetail := strings.ToLower(trimmedDetail)
	lowerStep := strings.ToLower(trimmedStep)
	if !strings.HasPrefix(lowerDetail, lowerStep+" ") {
		return trimmedDetail
	}
	return strings.TrimSpace(trimmedDetail[len(trimmedStep):])
}
