package main

import (
	"strings"
	"time"
)

func (m *tuiModel) applyEvent(ev uiEvent) {
	switch ev := ev.(type) {
	case uiStateEvent:
		wasBusy := m.state.Busy
		m.state = ev.State
		m.opts = ev.Opts
		switch {
		case ev.State.Busy && !wasBusy:
			m.turnAt = time.Now()
			m.now = m.turnAt
			m.escArmed = false
			m.ctrlCArmed = false
			m.answerRunes = 0
			m.thinkingRunes = 0
			m.toolEvents = 0
			m.lastEventAt = time.Time{}
			m.lastEventKind = ""
		case !ev.State.Busy:
			m.activity = ""
			m.turnAt = time.Time{}
			m.escArmed = false
			m.ctrlCArmed = false
		}
	case uiUserEvent:
		m.activity = "waiting for model"
		m.appendCell(tuiCell{Kind: "user", Title: "user", Body: ev.Text})
	case uiAssistantDeltaEvent:
		m.activity = "answering"
		m.answerRunes += len([]rune(ev.Text))
		m.markEvent("answer")
		m.appendDelta("assistant", "assistant", ev.Text)
	case uiThinkingDeltaEvent:
		m.activity = "reasoning"
		m.thinkingRunes += len([]rune(ev.Text))
		m.markEvent("reasoning")
		m.appendDelta("thinking", "reasoning", ev.Text)
	case uiToolEvent:
		m.toolEvents++
		title := strings.TrimSpace(firstNonEmpty(ev.Title, ev.Status))
		m.activity = "tool: " + title
		m.markEvent("tool")
		m.appendCell(tuiCell{Kind: "tool", Title: toolCellTitle(ev.Status, ev.Title), Body: ev.Detail})
	case uiInfoEvent:
		if ev.Title != "" {
			m.activity = ev.Title
		}
		m.appendCell(tuiCell{Kind: "info", Title: ev.Title, Body: ev.Body})
	case uiErrorEvent:
		m.appendCell(tuiCell{Kind: "error", Title: "error", Body: ev.Message})
	case uiPermissionRequest:
		m.overlay = &ev
		m.choice = 0
	}
	m.refreshViewport()
}

func (m *tuiModel) markEvent(kind string) {
	now := time.Now()
	m.lastEventAt = now
	m.lastEventKind = kind
	if m.now.IsZero() || now.After(m.now) {
		m.now = now
	}
}

func toolCellTitle(status, title string) string {
	status = strings.TrimSpace(status)
	title = strings.TrimSpace(title)
	switch {
	case title == "" && status == "":
		return "tool"
	case title == "":
		return "tool " + status
	case status == "":
		return "tool " + title
	default:
		return "tool " + status + " · " + title
	}
}

func (m *tuiModel) appendDelta(kind, title, text string) {
	if text == "" {
		return
	}
	if len(m.cells) > 0 && m.cells[len(m.cells)-1].Kind == kind {
		m.cells[len(m.cells)-1].Body += text
		return
	}
	m.appendCell(tuiCell{Kind: kind, Title: title, Body: text})
}

func (m *tuiModel) appendCell(cell tuiCell) {
	if strings.TrimSpace(cell.Title) == "" {
		cell.Title = cell.Kind
	}
	m.cells = append(m.cells, cell)
	if len(m.cells) > 400 {
		m.cells = append([]tuiCell(nil), m.cells[len(m.cells)-400:]...)
	}
}

func (m *tuiModel) reflow() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	m.input.Width = maxInt(1, m.width-3)
	m.viewport.Width = m.width
	m.viewport.Height = maxInt(1, m.height-3)
	m.refreshViewport()
}

func (m *tuiModel) refreshViewport() {
	shouldFollow := m.autoFollow || m.viewport.AtBottom()
	m.viewport.SetContent(m.transcript().View())
	if shouldFollow {
		m.viewport.GotoBottom()
		m.autoFollow = true
		return
	}
	if m.viewport.PastBottom() {
		m.viewport.GotoBottom()
		m.autoFollow = true
	}
}
