package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type tuiOverlaySurface struct {
	req         *uiPermissionRequest
	choice      int
	width       int
	inputActive bool
	inputView   string
}

func (s tuiOverlaySurface) Active() bool {
	return s.req != nil
}

func (s tuiOverlaySurface) Help() string {
	if !s.Active() {
		return ""
	}
	return "enter: choose · 1/2/3: direct choice · up/down: move · esc: reject"
}

func (s tuiOverlaySurface) View() string {
	if !s.Active() {
		return ""
	}
	width := s.bodyWidth()
	var b strings.Builder
	b.WriteString(tuiToolStyle.Render(wrapTranscriptText(s.req.Title, width)))
	if strings.TrimSpace(s.req.Detail) != "" {
		b.WriteString("\n")
		b.WriteString(wrapTranscriptText(s.req.Detail, width))
	}
	if help := s.Help(); help != "" {
		b.WriteString("\n")
		b.WriteString(tuiThinkingStyle.Render(wrapTranscriptText(help, width)))
	}
	for i, opt := range s.req.Options {
		key := overlayOptionKey(i, opt)
		prefix := "  "
		if i == s.choice {
			prefix = "› "
		}
		b.WriteString("\n")
		b.WriteString(indentWrappedLines(fmt.Sprintf("%s. %s", key, opt.Label), prefix, width))
	}
	if s.inputActive {
		b.WriteString("\n")
		b.WriteString(tuiThinkingStyle.Render(wrapTranscriptText("enter: submit replacement · esc: reject", width)))
		b.WriteString("\n")
		b.WriteString(truncateStatusLine(s.inputView, width))
	}
	return tuiOverlayStyle.Width(width).Render(b.String())
}

func (s tuiOverlaySurface) bodyWidth() int {
	if s.width <= 0 {
		return 1
	}
	return maxInt(1, minInt(72, s.width-4))
}

func (s tuiOverlaySurface) HasChoiceKey(key string) bool {
	if !s.Active() {
		return false
	}
	for i, opt := range s.req.Options {
		if key == overlayOptionKey(i, opt) {
			return true
		}
	}
	return key == "0" || key == "y" || key == "n"
}

func (s tuiOverlaySurface) ReplyForKey(key string) (string, bool) {
	if !s.Active() {
		return "", false
	}
	switch key {
	case "y":
		return "1", true
	case "n":
		return "3", true
	default:
		if s.HasChoiceKey(key) {
			return key, true
		}
		return "", false
	}
}

func (s tuiOverlaySurface) ReplyForChoice() string {
	if !s.Active() {
		return ""
	}
	idx := s.choice
	if idx < 0 || idx >= len(s.req.Options) {
		idx = 0
	}
	if len(s.req.Options) == 0 {
		return "1"
	}
	return overlayOptionKey(idx, s.req.Options[idx])
}

func overlayOptionKey(idx int, opt choiceOption) string {
	if opt.Key != "" {
		return opt.Key
	}
	return fmt.Sprintf("%d", idx+1)
}

func choiceOptionRequestsText(opt choiceOption) bool {
	label := strings.ToLower(strings.TrimSpace(opt.Label))
	return strings.Contains(label, "other command") || strings.Contains(label, "replacement command")
}

func (s tuiOverlaySurface) SelectedOptionRequestsText() bool {
	if !s.Active() || s.choice < 0 || s.choice >= len(s.req.Options) {
		return false
	}
	return choiceOptionRequestsText(s.req.Options[s.choice])
}

func (m tuiModel) overlaySurface() tuiOverlaySurface {
	return tuiOverlaySurface{
		req:         m.overlay,
		choice:      m.choice,
		width:       m.width,
		inputActive: m.overlayTyping,
		inputView:   m.overlayInput.View(),
	}
}

func overlayBlock(width, height int, base, overlay string) string {
	if overlay == "" || width <= 0 || height <= 0 {
		return base
	}
	lines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	if len(overlayLines) > len(lines) {
		overlayLines = overlayLines[:len(lines)]
	}
	start := maxInt(0, (len(lines)-len(overlayLines))/2)
	for i, line := range overlayLines {
		idx := start + i
		if idx >= len(lines) {
			break
		}
		line = truncateStatusLine(line, width)
		pad := maxInt(0, (width-lipgloss.Width(line))/2)
		lines[idx] = truncateStatusLine(strings.Repeat(" ", pad)+line, width)
	}
	return strings.Join(lines, "\n")
}
