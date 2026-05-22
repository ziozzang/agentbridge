package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type tuiStatusSurface struct {
	model tuiModel
}

func (s tuiStatusSurface) Notice() string {
	m := s.model
	if m.width <= 0 {
		return ""
	}
	if m.overlay != nil {
		return s.truncateNotice(tuiWarnStyle.Render("approval requested") + " choose with numbers, arrows, enter, or esc")
	}
	if m.escArmed {
		return s.truncateNotice(tuiWarnStyle.Render("stop current turn?") + " press ESC again to stop · Ctrl-C stops immediately · Ctrl-C again exits")
	}
	if m.state.Busy {
		parts := []string{"running " + s.turnElapsed()}
		if strings.TrimSpace(m.activity) != "" {
			parts = append(parts, m.activity)
		}
		if progress := s.Progress(); progress != "" {
			parts = append(parts, progress)
		}
		parts = append(parts, "ESC: confirm stop", "Ctrl-C: stop", "Ctrl-D: exit")
		return s.truncateNotice(strings.Join(parts, " · "))
	}
	if m.commandRuns > 0 {
		parts := []string{fmt.Sprintf("command running %d", m.commandRuns)}
		if strings.TrimSpace(m.activity) != "" {
			parts = append(parts, m.activity)
		}
		parts = append(parts, "Ctrl-C: exit", "Ctrl-D: exit")
		return s.truncateNotice(strings.Join(parts, " · "))
	}
	if hint := m.completionHint(); hint != "" {
		return s.truncateNotice(tuiHintStyle.Render(hint))
	}
	return s.truncateNotice("Ctrl-D: exit · /help")
}

func (s tuiStatusSurface) truncateNotice(line string) string {
	width := s.model.width
	if width <= 0 {
		return ""
	}
	return truncateStatusLine(line, width)
}

func (s tuiStatusSurface) Progress() string {
	m := s.model
	var parts []string
	if m.answerRunes > 0 {
		parts = append(parts, fmt.Sprintf("answer %d chars", m.answerRunes))
	}
	if m.thinkingRunes > 0 {
		parts = append(parts, fmt.Sprintf("reasoning %d chars", m.thinkingRunes))
	}
	if m.toolEvents > 0 {
		parts = append(parts, fmt.Sprintf("tool events %d", m.toolEvents))
	}
	if !m.lastEventAt.IsZero() {
		age := m.now.Sub(m.lastEventAt)
		if age < 0 {
			age = 0
		}
		label := firstNonEmpty(m.lastEventKind, "event")
		parts = append(parts, fmt.Sprintf("%s %s ago", label, compactDuration(age)))
	}
	return strings.Join(parts, " · ")
}

func (s tuiStatusSurface) Status() string {
	m := s.model
	state := m.state
	status := "Ready"
	if state.Busy {
		status = m.spinner.View() + " Working"
	} else if m.commandRuns > 0 {
		status = m.spinner.View() + " Command"
	}
	if m.activity != "" {
		status += ": " + m.activity
	}
	if state.Busy {
		status += " · " + s.turnElapsed()
		if progress := s.Progress(); progress != "" {
			status += " · " + progress
		}
	}
	parts := []string{
		tuiStateStyle.Render(status),
		tuiContextStyle.Render(contextLabel(state.Context)),
	}
	for _, part := range limitLabels(state.Limits) {
		parts = append(parts, tuiQuotaStyle.Render(part))
	}
	parts = append(parts,
		firstNonEmpty(state.Model, "(model)"),
		firstNonEmpty(state.Mode, "default"),
		shortPath(state.Cwd),
		workerLabel(state.Worker),
		tuiPermStyle.Render(permissionLabel(firstNonEmpty(m.opts.Permission, "prompt"))),
	)
	if state.QueueLen > 0 {
		parts = append(parts, fmt.Sprintf("Queue %d", state.QueueLen))
	}
	if state.Subagents > 0 {
		parts = append(parts, fmt.Sprintf("Subagents %d", state.Subagents))
	}
	if state.Tools > 0 {
		parts = append(parts, fmt.Sprintf("Tools %d", state.Tools))
	}
	if m.commandRuns > 0 {
		parts = append(parts, fmt.Sprintf("Commands %d", m.commandRuns))
	}
	if scroll := s.scrollLabel(); scroll != "" {
		parts = append(parts, scroll)
	}
	parts = append(parts, agentVersionShort(), shortSession(state.SessionID))
	line := strings.Join(nonEmpty(parts), " · ")
	if lipgloss.Width(line) > m.width && m.width > 0 {
		return truncateStatusLine(line, m.width)
	}
	return line
}

func workerLabel(w workerState) string {
	if w.Kind == "" {
		return ""
	}
	if len(w.Capabilities) > 0 {
		return fmt.Sprintf("Worker %s:%d", w.Kind, len(w.Capabilities))
	}
	return "Worker " + w.Kind
}

func truncateStatusLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = strings.ReplaceAll(line, "\r", " ")
	line = strings.ReplaceAll(line, "\n", " ")
	return ansi.Truncate(line, width, "…")
}

func (s tuiStatusSurface) scrollLabel() string {
	m := s.model
	if m.autoFollow || m.viewport.AtBottom() {
		return ""
	}
	return fmt.Sprintf("Scroll %.0f%%", m.viewport.ScrollPercent()*100)
}

func (s tuiStatusSurface) turnElapsed() string {
	m := s.model
	if m.turnAt.IsZero() {
		return "0s"
	}
	now := m.now
	if now.IsZero() {
		now = m.turnAt
	}
	if now.Before(m.turnAt) {
		now = m.turnAt
	}
	return compactDuration(now.Sub(m.turnAt))
}

func (m tuiModel) statusSurface() tuiStatusSurface {
	return tuiStatusSurface{model: m}
}

func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds %= 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes %= 60
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}
