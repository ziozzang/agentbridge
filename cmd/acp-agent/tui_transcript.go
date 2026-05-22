package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type tuiTranscript struct {
	cells []tuiCell
	width int
}

func (t tuiTranscript) View() string {
	var b strings.Builder
	for _, cell := range t.cells {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(t.renderCell(cell))
	}
	return b.String()
}

func (t tuiTranscript) renderCell(cell tuiCell) string {
	title := cell.Title
	var b strings.Builder
	switch cell.Kind {
	case "user":
		b.WriteString(tuiUserStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(indentWrappedLines(cell.Body, "  > ", t.width))
	case "assistant":
		b.WriteString(tuiAssistantStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(wrapTranscriptText(cell.Body, t.width))
	case "thinking":
		b.WriteString(tuiThinkingStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(tuiThinkingStyle.Render(indentWrappedLines(cell.Body, "  ", t.width)))
	case "tool":
		b.WriteString(tuiToolStyle.Render(title))
		if strings.TrimSpace(cell.Body) != "" {
			b.WriteString("\n")
			b.WriteString(tuiThinkingStyle.Render(indentWrappedLines(cell.Body, "  ", t.width)))
		}
	case "error":
		b.WriteString(tuiErrorStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(indentWrappedLines(cell.Body, "  ", t.width))
	default:
		b.WriteString(tuiInfoStyle.Render(title))
		if strings.TrimSpace(cell.Body) != "" {
			b.WriteString("\n")
			b.WriteString(indentWrappedLines(cell.Body, "  ", t.width))
		}
	}
	return b.String()
}

func wrapTranscriptText(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := splitDisplayLines(s)
	for i, line := range lines {
		lines[i] = ansi.Wrap(line, width, "")
	}
	return strings.Join(lines, "\n")
}

func indentWrappedLines(s, prefix string, width int) string {
	wrapWidth := 0
	if width > 0 {
		wrapWidth = maxInt(1, width-len(prefix))
	}
	lines := splitDisplayLines(s)
	for i, line := range lines {
		if wrapWidth > 0 {
			line = ansi.Wrap(line, wrapWidth, "")
		}
		if strings.Contains(line, "\n") {
			parts := strings.Split(line, "\n")
			for j, part := range parts {
				parts[j] = prefix + part
			}
			lines[i] = strings.Join(parts, "\n")
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) transcript() tuiTranscript {
	return tuiTranscript{cells: m.cells, width: m.viewport.Width}
}
