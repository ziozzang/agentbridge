package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	tuiUserStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	tuiAssistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	tuiThinkingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiToolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	tuiInfoStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	tuiErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	tuiStatusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("236"))
	tuiStateStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Background(lipgloss.Color("236")).Bold(true)
	tuiContextStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Background(lipgloss.Color("236"))
	tuiQuotaStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Background(lipgloss.Color("236"))
	tuiPermStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Background(lipgloss.Color("236"))
	tuiHintStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Background(lipgloss.Color("236"))
	tuiNoticeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("238"))
	tuiWarnStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Background(lipgloss.Color("238")).Bold(true)
	tuiComposerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("235"))
	tuiOverlayStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).BorderForeground(lipgloss.Color("11"))
)

func (m tuiModel) View() string {
	if m.width <= 0 {
		return ""
	}
	transcript := m.viewport.View()
	if m.overlay != nil {
		transcript = overlayBlock(m.width, m.height, transcript, m.overlaySurface().View())
	}
	statusSurface := m.statusSurface()
	notice := tuiNoticeStyle.Width(m.width).Render(statusSurface.Notice())
	composer := m.composerSurface().View()
	status := tuiStatusStyle.Width(m.width).Render(statusSurface.Status())
	return lipgloss.JoinVertical(lipgloss.Left, transcript, notice, composer, status)
}

func (m tuiModel) noticeLine() string {
	return m.statusSurface().Notice()
}

func (m tuiModel) progressLine() string {
	return m.statusSurface().Progress()
}

func (m tuiModel) completionHint() string {
	return m.completionSurface().Hint()
}

func (m tuiModel) statusLine() string {
	return m.statusSurface().Status()
}

func (m tuiModel) scrollLabel() string {
	return m.statusSurface().scrollLabel()
}

func (m tuiModel) turnElapsed() string {
	return m.statusSurface().turnElapsed()
}

func indentLines(s, prefix string) string {
	lines := splitDisplayLines(s)
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
