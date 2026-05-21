package main

import "strings"

type tuiTranscript struct {
	cells []tuiCell
}

func (t tuiTranscript) View() string {
	var b strings.Builder
	for _, cell := range t.cells {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(renderTranscriptCell(cell))
	}
	return b.String()
}

func renderTranscriptCell(cell tuiCell) string {
	title := cell.Title
	var b strings.Builder
	switch cell.Kind {
	case "user":
		b.WriteString(tuiUserStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(indentLines(cell.Body, "  > "))
	case "assistant":
		b.WriteString(tuiAssistantStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(cell.Body)
	case "thinking":
		b.WriteString(tuiThinkingStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(tuiThinkingStyle.Render(indentLines(cell.Body, "  ")))
	case "tool":
		b.WriteString(tuiToolStyle.Render(title))
		if strings.TrimSpace(cell.Body) != "" {
			b.WriteString("\n")
			b.WriteString(tuiThinkingStyle.Render(indentLines(cell.Body, "  ")))
		}
	case "error":
		b.WriteString(tuiErrorStyle.Render(title))
		b.WriteString("\n")
		b.WriteString(indentLines(cell.Body, "  "))
	default:
		b.WriteString(tuiInfoStyle.Render(title))
		if strings.TrimSpace(cell.Body) != "" {
			b.WriteString("\n")
			b.WriteString(indentLines(cell.Body, "  "))
		}
	}
	return b.String()
}

func (m tuiModel) transcript() tuiTranscript {
	return tuiTranscript{cells: m.cells}
}
