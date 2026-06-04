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

type targetState struct {
	ID             string
	Status         string
	Step           string
	Detail         string
	LogFile        string
	CurrentVersion string
	Done           int
	Failed         bool
	Skipped        bool
	Duration       string
}

type tableRow struct {
	Target string
	State  string
	Step   string
	Detail string
}

type tableLayout struct {
	TargetWidth int
	StateWidth  int
	StepWidth   int
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
		updateTargetCurrentVersion(target, event)
		updateTargetLog(target, event)
	case EventStepDone, EventStepSkipped, EventStepFailed:
		target := m.ensureTarget(event.Target)
		target.Status = statusRunning
		target.Step = event.Step
		target.Detail = event.Detail
		target.Done++
		updateTargetCurrentVersion(target, event)
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
		updateTargetCurrentVersion(target, event)
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
		ID:             id,
		Status:         statusQueued,
		Step:           "queued",
		Detail:         "",
		LogFile:        "",
		CurrentVersion: "",
		Done:           0,
		Failed:         false,
		Skipped:        false,
		Duration:       "",
	}
	m.targets[id] = target
	m.order = append(m.order, id)
	sort.Strings(m.order)
	return target
}

func updateTargetCurrentVersion(target *targetState, event Event) {
	version := versionFromDetail(target.ID, event.Detail)
	if version == "" {
		return
	}
	target.CurrentVersion = version
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
	runStatusLine := strings.Builder{}
	okCount, skippedCount, failedCount, _ := m.targetCounts()
	runState := "running"
	if m.total > 0 {
		runState = "finished"
	}
	_, _ = fmt.Fprintf(&runStatusLine, "%s%s%s%s%s", headerStyle.Render(title(m.operation)), columnGap, runState, columnGap, targetCountLabel(len(m.targets)))
	if m.total > 0 {
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
			state := renderStateCell(
				target,
				row.State,
				layout.StateWidth,
				okStyle,
				skippedStyle,
				failStyle,
				stateStyle,
			)
			runStatus.WriteString(formatTargetRow(layout, row.Target, state, row.Step, row.Detail) + "\n")
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
			Step:   displayStep(target.Step),
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
		StepWidth:   lipgloss.Width("STEP"),
	}
	for _, row := range rows {
		layout.TargetWidth = maxInt(layout.TargetWidth, lipgloss.Width(row.Target))
		layout.StateWidth = maxInt(layout.StateWidth, lipgloss.Width(row.State))
		layout.StepWidth = maxInt(layout.StepWidth, lipgloss.Width(row.Step))
	}
	return layout
}

func formatTableHeader(layout tableLayout) string {
	return formatTargetRow(layout, "TARGET", "STATE", "STEP", "DETAIL")
}

func formatTargetRow(layout tableLayout, target string, state string, step string, detail string) string {
	return strings.Join(
		[]string{
			padRight(target, layout.TargetWidth),
			padRight(state, layout.StateWidth),
			padRight(step, layout.StepWidth),
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

func displayStateCell(target *targetState, spinnerFrame string) string {
	state := targetDisplayStatus(target)
	if state == statusRunning {
		return spinnerFrame + " " + state
	}
	return state
}

func renderStateCell(
	target *targetState,
	state string,
	stateWidth int,
	okStyle lipgloss.Style,
	skippedStyle lipgloss.Style,
	failStyle lipgloss.Style,
	stateStyle lipgloss.Style,
) string {
	style := lipgloss.NewStyle()
	switch targetDisplayStatus(target) {
	case statusFailed:
		style = failStyle
	case statusSkipped:
		style = skippedStyle
	case statusOK:
		style = okStyle
	case statusRunning:
		style = stateStyle
	}
	return style.Render(padRight(state, stateWidth))
}

func displayStep(step string) string {
	if isNoUpdateStep(step) {
		return "no update available"
	}
	cleaned := strings.ReplaceAll(strings.TrimSpace(step), "_", " ")
	if cleaned == "" {
		return "progress"
	}
	if step, ok := canonicalDisplayStep(cleaned); ok {
		return step
	}
	return cleaned
}

func displayDetail(target *targetState) string {
	detail := stripTargetDetail(target.ID, target.Detail)
	if detail == "" {
		return ""
	}
	if isNoUpdateStep(target.Step) {
		return currentVersionDetail(target, detail)
	}
	if isCurrentVersionStep(target.Step) {
		return currentVersionDetail(target, detail)
	}
	if target.Step == "upgrade_complete" {
		return upgradedVersionDetail(detail)
	}
	step := displayStep(target.Step)
	if step == "using sccache" {
		return sccacheDetail(detail)
	}
	if step == "installing fresh bundle" {
		return freshBundleDetail(detail)
	}
	if step == "install complete" {
		return installCompleteDetail(detail)
	}
	if step == "installing rust toolchain" {
		return rustToolchainDetail(detail)
	}
	if step == "checking update manifest" {
		return manifestDetail(detail)
	}
	return trimDisplayedStepPrefix(detail, step)
}

func canonicalDisplayStep(step string) (string, bool) {
	if strings.HasPrefix(step, "current version") {
		return "checking current version", true
	}
	if strings.HasPrefix(step, "install complete") {
		return "install complete", true
	}
	if strings.HasPrefix(step, "installing or updating upstream rust toolchain") {
		return "installing rust toolchain", true
	}
	if strings.HasPrefix(step, "manifest") {
		return "checking update manifest", true
	}
	for _, prefix := range []string{
		"using existing sccache wrapper",
		"using sccache wrapper",
		"using sccache",
	} {
		if strings.HasPrefix(step, prefix) {
			return "using sccache", true
		}
	}
	for _, prefix := range []string{
		"building codex entrypoint",
		"installing fresh bundle",
		"sccache disabled",
		"sccache not found",
		"could not read sccache stats",
	} {
		if strings.HasPrefix(step, prefix) {
			return prefix, true
		}
	}
	return "", false
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

func isNoUpdateStep(step string) bool {
	return step == "already_on_version" || step == "no_update_available"
}

func isCurrentVersionStep(step string) bool {
	return strings.HasPrefix(step, "current_version")
}

func currentVersionDetail(target *targetState, detail string) string {
	version := target.CurrentVersion
	if version == "" {
		version = versionFromDetail(target.ID, detail)
	}
	if version == "" {
		return ""
	}
	return "current version: " + version
}

func versionFromDetail(targetID string, detail string) string {
	cleaned := stripTargetDetail(targetID, detail)
	if version := versionFromAlreadyOnVersion(cleaned); version != "" {
		return version
	}
	return versionFromCurrentVersionDetail(cleaned)
}

func versionFromAlreadyOnVersion(detail string) string {
	const prefix = "already on version "
	rest, ok := strings.CutPrefix(strings.TrimSpace(detail), prefix)
	if !ok {
		return ""
	}
	version, _, ok := strings.Cut(rest, ";")
	if !ok {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(version)
}

func versionFromCurrentVersionDetail(detail string) string {
	for _, prefix := range []string{"current version=", "current version "} {
		rest, ok := strings.CutPrefix(strings.TrimSpace(detail), prefix)
		if !ok {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(rest))
		if len(fields) == 0 {
			return ""
		}
		return strings.Trim(strings.TrimSuffix(fields[0], ";"), ",")
	}
	return ""
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
	if lowerDetail == lowerStep {
		return ""
	}
	if !strings.HasPrefix(lowerDetail, lowerStep+" ") {
		return trimmedDetail
	}
	return strings.TrimSpace(trimmedDetail[len(trimmedStep):])
}

func sccacheDetail(detail string) string {
	trimmed := strings.TrimSpace(detail)
	for _, prefix := range []string{
		"using existing sccache wrapper ",
		"using sccache wrapper ",
		"using sccache ",
	} {
		if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			continue
		}
		rest := strings.TrimSpace(trimmed[len(prefix):])
		if rest != "" {
			return trimDuplicateStatusDetail(rest)
		}
	}
	return trimDisplayedStepPrefix(trimmed, "using sccache")
}

func freshBundleDetail(detail string) string {
	const prefix = "installing fresh bundle "
	trimmed := strings.TrimSpace(detail)
	if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
		return trimDisplayedStepPrefix(detail, "installing fresh bundle")
	}
	rest := trimmed[len(prefix):]
	source, destination, ok := strings.Cut(strings.TrimSpace(rest), " -> ")
	if !ok {
		return trimDuplicateStatusDetail(source)
	}
	name := pathBase(destination)
	if name == "" {
		name = pathBase(source)
	}
	if name == "" {
		return ""
	}
	return "replacing " + name
}

func installCompleteDetail(detail string) string {
	trimmed := strings.TrimSpace(detail)
	if release := valueAfterKey(trimmed, "release="); release != "" {
		return "release: " + pathBase(release)
	}
	rest := trimPrefixFold(trimmed, "install complete")
	return trimDuplicateStatusDetail(rest)
}

func rustToolchainDetail(detail string) string {
	trimmed := strings.TrimSpace(detail)
	rest := trimPrefixFold(trimmed, "installing or updating upstream rust toolchain from")
	if rest == "" {
		return trimDisplayedStepPrefix(detail, "installing rust toolchain")
	}
	return "from " + pathBase(rest)
}

func manifestDetail(detail string) string {
	trimmed := strings.TrimSpace(detail)
	if version := valueAfterKey(trimmed, "name="); version != "" {
		return "available version: " + version
	}
	rest := trimPrefixFold(trimmed, "manifest:")
	return trimDuplicateStatusDetail(rest)
}

func trimDuplicateStatusDetail(detail string) string {
	trimmed := strings.TrimSpace(detail)
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(trimmed, "wrapper") {
		return ""
	}
	return trimmed
}

func trimPrefixFold(value string, prefix string) string {
	trimmed := strings.TrimSpace(value)
	lowerPrefix := strings.ToLower(strings.TrimSpace(prefix))
	if !strings.HasPrefix(strings.ToLower(trimmed), lowerPrefix) {
		return trimmed
	}
	return strings.TrimSpace(trimmed[len(lowerPrefix):])
}

func valueAfterKey(detail string, key string) string {
	lowerDetail := strings.ToLower(detail)
	lowerKey := strings.ToLower(key)
	index := strings.Index(lowerDetail, lowerKey)
	if index == -1 {
		return ""
	}
	rest := strings.TrimSpace(detail[index+len(key):])
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(strings.TrimSuffix(fields[0], ";"), ",")
}

func pathBase(path string) string {
	trimmed := strings.Trim(strings.TrimSpace(path), "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
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
