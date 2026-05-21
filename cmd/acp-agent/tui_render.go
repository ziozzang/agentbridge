package main

import (
	"fmt"
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
		transcript = overlayBlock(m.width, m.height, transcript, m.renderOverlay())
	}
	statusSurface := m.statusSurface()
	notice := tuiNoticeStyle.Width(m.width).Render(statusSurface.Notice())
	composer := tuiComposerStyle.Width(m.width).Render(m.input.View())
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
	if m.overlay != nil {
		return ""
	}
	value := m.input.Value()
	switch {
	case strings.HasPrefix(value, "/permission "):
		return "/permission allow|deny|reject|prompt|cancel"
	case strings.HasPrefix(value, "/goal "):
		return "/goal status|set|run|clear"
	case strings.HasPrefix(value, "/thinking "):
		return "/thinking on|off|toggle"
	case strings.HasPrefix(value, "/tools "):
		return "/tools on|off|toggle"
	case strings.HasPrefix(value, "/raw "):
		return "/raw on|off|toggle"
	case strings.HasPrefix(value, "/mode "):
		return "/mode default|accept_edits|bypass_permissions"
	}
	matches := m.input.MatchedSuggestions()
	if len(matches) == 0 {
		return ""
	}
	if group := compactSlashHint(value, matches); group != "" {
		return group
	}
	visible := matches
	if len(visible) > 5 {
		visible = visible[:5]
	}
	return "tab complete: " + strings.Join(visible, "  ")
}

func compactSlashHint(value string, matches []string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		return ""
	}
	root := value
	if i := strings.IndexByte(strings.TrimPrefix(root, "/"), ' '); i >= 0 {
		root = root[:i+1]
	}
	seen := map[string]bool{}
	var args []string
	for _, item := range matches {
		if item == root {
			continue
		}
		if strings.HasPrefix(item, root+" ") {
			arg := strings.TrimSpace(strings.TrimPrefix(item, root))
			if arg == "" || strings.Contains(arg, " ") || seen[arg] {
				continue
			}
			seen[arg] = true
			args = append(args, arg)
		}
	}
	if len(args) == 0 {
		return ""
	}
	return root + " " + strings.Join(args, "|")
}

func (m tuiModel) renderOverlay() string {
	if m.overlay == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(tuiToolStyle.Render(m.overlay.Title))
	if strings.TrimSpace(m.overlay.Detail) != "" {
		b.WriteString("\n")
		b.WriteString(m.overlay.Detail)
	}
	if help := m.overlayHelp(); help != "" {
		b.WriteString("\n")
		b.WriteString(tuiThinkingStyle.Render(help))
	}
	for i, opt := range m.overlay.Options {
		key := opt.Key
		if key == "" {
			key = fmt.Sprintf("%d", i+1)
		}
		prefix := "  "
		if i == m.choice {
			prefix = "› "
		}
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%s%s. %s", prefix, key, opt.Label))
	}
	return tuiOverlayStyle.Width(minInt(72, maxInt(36, m.width-4))).Render(b.String())
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

func overlayBlock(width, height int, base, overlay string) string {
	if overlay == "" || width <= 0 || height <= 0 {
		return base
	}
	lines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	start := maxInt(0, (len(lines)-len(overlayLines))/2)
	for i, line := range overlayLines {
		idx := start + i
		if idx >= len(lines) {
			lines = append(lines, "")
		}
		pad := maxInt(0, (width-lipgloss.Width(line))/2)
		lines[idx] = strings.Repeat(" ", pad) + line
	}
	return strings.Join(lines, "\n")
}
