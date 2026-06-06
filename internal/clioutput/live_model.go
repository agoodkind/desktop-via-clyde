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

const columnGap = "    "

// targetState is the live view's per-target row. Terminal status is set only by
// EventTargetDone; step events update the displayed detail and keep the row in
// the running state. No status is inferred from log prose.
type targetState struct {
	ID       string
	Status   string
	Detail   string
	LogFile  string
	Terminal bool
	Duration string
}

type tableRow struct {
	Target string
	State  string
	Detail string
}

type tableLayout struct {
	TargetWidth int
	StateWidth  int
}

type liveModel struct {
	operation string
	runLog    string
	started   string
	targets   map[string]*targetState
	order     []string
	spinner   spinner.Model
}

func newLiveModel() liveModel {
	return liveModel{
		operation: "",
		runLog:    "",
		started:   "",
		targets:   map[string]*targetState{},
		order:     []string{},
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
		if !target.Terminal {
			target.Status = statusQueued
		}
		updateTargetLog(target, event)
	case EventTargetStarted:
		target := m.ensureTarget(event.Target)
		if !target.Terminal {
			target.Status = statusRunning
		}
		updateTargetLog(target, event)
	case EventStepStarted, EventStepDone, EventStepSkipped, EventStepFailed:
		target := m.ensureTarget(event.Target)
		if !target.Terminal {
			target.Status = statusRunning
		}
		if event.Detail != "" {
			target.Detail = event.Detail
		}
		updateTargetLog(target, event)
	case EventTargetDone:
		target := m.ensureTarget(event.Target)
		target.Status = event.Status
		target.Terminal = true
		target.Duration = durationValue(event.DurationMS)
		if event.Detail != "" {
			target.Detail = event.Detail
		}
		updateTargetLog(target, event)
	case EventRunDone:
		// The per-target EventTargetDone events are the authoritative source for
		// the aggregate counts, so the run summary needs no extra state here.
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
		Detail:   "",
		LogFile:  "",
		Terminal: false,
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
	runningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("69"))
	runStatus := strings.Builder{}
	runStatusLine := strings.Builder{}
	okCount, skippedCount, failedCount, activeCount := m.targetCounts()
	runState := "running"
	if activeCount == 0 && len(m.targets) > 0 {
		runState = "finished"
	}
	_, _ = fmt.Fprintf(&runStatusLine, "%s%s%s%s%s", headerStyle.Render(title(m.operation)), columnGap, runState, columnGap, targetCountLabel(len(m.targets)))
	if runState == "finished" {
		_, _ = fmt.Fprintf(&runStatusLine, "%s%d ok%s%d skipped%s%d failed", columnGap, okCount, columnGap, skippedCount, columnGap, failedCount)
	}
	if m.started != "" {
		runStatusLine.WriteString(columnGap + "started " + m.started)
	}
	if m.runLog != "" {
		runStatus.WriteString("run log" + columnGap + m.runLog + "\n")
	}
	runStatus.WriteString(runStatusLine.String())
	runStatus.WriteString("\n")
	if len(m.targets) > 0 {
		rows := m.tableRows()
		layout := tableLayoutForRows(rows)
		runStatus.WriteString(mutedStyle.Render(formatTableHeader(layout)) + "\n")
		for _, row := range rows {
			target := m.targets[row.Target]
			state := renderStateCell(target, row.State, layout.StateWidth, okStyle, skippedStyle, failStyle, runningStyle)
			runStatus.WriteString(formatTargetRow(layout, row.Target, state, row.Detail) + "\n")
		}
	}
	if hasTargetLogs(m) {
		runStatus.WriteString("\n")
		runStatus.WriteString(headerStyle.Render("Logs") + "\n")
		logTargetWidth := m.logTargetWidth()
		for _, id := range m.order {
			target := m.targets[id]
			if target.LogFile == "" {
				continue
			}
			runStatus.WriteString(mutedStyle.Render(formatLogRow(target.ID, target.LogFile, logTargetWidth)) + "\n")
		}
	}
	return runStatus.String()
}

func (m *liveModel) tableRows() []tableRow {
	rows := make([]tableRow, 0, len(m.order))
	for _, id := range m.order {
		target := m.targets[id]
		row := tableRow{
			Target: target.ID,
			State:  displayStateCell(target, m.spinner.View()),
			Detail: displayDetail(target),
		}
		rows = append(rows, row)
	}
	return rows
}

func tableLayoutForRows(rows []tableRow) tableLayout {
	layout := tableLayout{
		TargetWidth: lipgloss.Width("TARGET"),
		StateWidth:  lipgloss.Width("STATE"),
	}
	for _, row := range rows {
		layout.TargetWidth = maxInt(layout.TargetWidth, lipgloss.Width(row.Target))
		layout.StateWidth = maxInt(layout.StateWidth, lipgloss.Width(row.State))
	}
	return layout
}

func formatTableHeader(layout tableLayout) string {
	return formatTargetRow(layout, "TARGET", "STATE", "DETAIL")
}

func formatTargetRow(layout tableLayout, target string, state string, detail string) string {
	return strings.Join(
		[]string{
			padRight(target, layout.TargetWidth),
			padRight(state, layout.StateWidth),
			detail,
		},
		columnGap,
	)
}

func (m *liveModel) logTargetWidth() int {
	width := 0
	for _, target := range m.targets {
		if target.LogFile == "" {
			continue
		}
		width = maxInt(width, lipgloss.Width(target.ID))
	}
	return width
}

func formatLogRow(target string, logFile string, targetWidth int) string {
	return padRight(target, targetWidth) + columnGap + logFile
}

func hasTargetLogs(m *liveModel) bool {
	for _, target := range m.targets {
		if target.LogFile != "" {
			return true
		}
	}
	return false
}

// targetCounts tallies terminal target statuses. Each target contributes to
// exactly one bucket once it reaches EventTargetDone, so ok+skipped+failed plus
// the still-active targets equals the target count.
func (m *liveModel) targetCounts() (int, int, int, int) {
	okCount := 0
	skippedCount := 0
	failedCount := 0
	activeCount := 0
	for _, target := range m.targets {
		switch target.Status {
		case statusFailed:
			failedCount++
		case statusSkipped:
			skippedCount++
		case statusOK:
			okCount++
		default:
			activeCount++
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

func displayStateCell(target *targetState, spinnerFrame string) string {
	if target.Status == statusRunning {
		return spinnerFrame + " " + statusRunning
	}
	return target.Status
}

func renderStateCell(
	target *targetState,
	state string,
	stateWidth int,
	okStyle lipgloss.Style,
	skippedStyle lipgloss.Style,
	failStyle lipgloss.Style,
	runningStyle lipgloss.Style,
) string {
	style := lipgloss.NewStyle()
	switch target.Status {
	case statusFailed:
		style = failStyle
	case statusSkipped:
		style = skippedStyle
	case statusOK:
		style = okStyle
	case statusRunning:
		style = runningStyle
	}
	return style.Render(padRight(state, stateWidth))
}

// displayDetail renders the latest milestone detail for a target, stripping the
// "target=<id> " prefix the operations prepend. It performs no status inference.
func displayDetail(target *targetState) string {
	return stripTargetDetail(target.ID, target.Detail)
}

func stripTargetDetail(targetID string, detail string) string {
	cleaned := strings.TrimSpace(detail)
	if cleaned == "" {
		return ""
	}
	if trimmed, ok := strings.CutPrefix(cleaned, "target="+targetID+" "); ok {
		cleaned = strings.TrimSpace(trimmed)
	}
	if trimmed, ok := strings.CutPrefix(cleaned, targetID+":"); ok {
		cleaned = strings.TrimSpace(trimmed)
	}
	return cleaned
}

func padRight(value string, width int) string {
	paddingWidth := width - lipgloss.Width(value)
	if paddingWidth <= 0 {
		return value
	}
	return value + strings.Repeat(" ", paddingWidth)
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
