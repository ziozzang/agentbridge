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
		case !ev.State.Busy:
			m.activity = ""
			m.turnAt = time.Time{}
			m.escArmed = false
			m.ctrlCArmed = false
		}
	case uiUserEvent:
		m.activity = "prompt queued"
		m.appendCell(tuiCell{Kind: "user", Title: "user", Body: ev.Text})
	case uiAssistantDeltaEvent:
		m.activity = "answering"
		m.appendDelta("assistant", "assistant", ev.Text)
	case uiThinkingDeltaEvent:
		m.activity = "thinking"
		m.appendDelta("thinking", "thinking", ev.Text)
	case uiToolEvent:
		m.activity = "tool: " + strings.TrimSpace(firstNonEmpty(ev.Title, ev.Status))
		m.appendCell(tuiCell{Kind: "tool", Title: firstNonEmpty(ev.Status, "tool") + " " + ev.Title, Body: ev.Detail})
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
	m.input.Width = m.width - 3
	m.viewport.Width = m.width
	m.viewport.Height = maxInt(1, m.height-3)
	m.refreshViewport()
}

func (m *tuiModel) refreshViewport() {
	m.viewport.SetContent(m.renderTranscript())
	m.viewport.GotoBottom()
}
