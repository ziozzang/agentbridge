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
	var b strings.Builder
	b.WriteString(tuiToolStyle.Render(s.req.Title))
	if strings.TrimSpace(s.req.Detail) != "" {
		b.WriteString("\n")
		b.WriteString(s.req.Detail)
	}
	if help := s.Help(); help != "" {
		b.WriteString("\n")
		b.WriteString(tuiThinkingStyle.Render(help))
	}
	for i, opt := range s.req.Options {
		key := overlayOptionKey(i, opt)
		prefix := "  "
		if i == s.choice {
			prefix = "› "
		}
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%s%s. %s", prefix, key, opt.Label))
	}
	if s.inputActive {
		b.WriteString("\n")
		b.WriteString(tuiThinkingStyle.Render("enter: submit replacement · esc: reject"))
		b.WriteString("\n")
		b.WriteString(s.inputView)
	}
	return tuiOverlayStyle.Width(minInt(72, maxInt(36, s.width-4))).Render(b.String())
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
