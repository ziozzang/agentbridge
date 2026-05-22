package main

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func runBubbleTUI(ctx context.Context, c *client) error {
	c.events = make(chan uiEvent, 512)
	c.stream = nil
	c.emitState()

	m := newTUIModel(ctx, c)
	p := tea.NewProgram(m, tuiProgramOptions(ctx)...)
	_, err := p.Run()
	return err
}

func tuiProgramOptions(ctx context.Context) []tea.ProgramOption {
	return []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithAltScreen(),
	}
}

func waitTUIEvent(events <-chan uiEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return tea.Quit()
		}
		return tuiEventMsg{Event: ev}
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(waitTUIEvent(m.events), m.spinner.Tick)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var handled bool
	var out tea.Model
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.handleWindowSize(msg)
	case tea.KeyMsg:
		out, cmd, handled = m.routeKey(msg, cmds)
		if handled {
			return out, cmd
		}
	case tuiEventMsg:
		cmds = m.handleTUIEvent(msg, cmds)
	case commandDoneMsg:
		m.handleCommandDone(msg)
	case spinner.TickMsg:
		m, cmd = m.handleSpinnerTick(msg)
		cmds = append(cmds, cmd)
	}
	m, cmd = m.updateComposer(msg)
	cmds = append(cmds, cmd)
	m.refreshViewport()
	return m, tea.Batch(cmds...)
}

func (m *tuiModel) handleWindowSize(msg tea.WindowSizeMsg) {
	m.width = msg.Width
	m.height = msg.Height
	m.reflow()
}

func (m tuiModel) routeKey(msg tea.KeyMsg, cmds []tea.Cmd) (tea.Model, tea.Cmd, bool) {
	keyName := tuiKeyName(msg)
	switch {
	case isGlobalInterruptKey(keyName):
		next, cmd := m.handleCtrlC()
		return next, cmd, true
	case isGlobalExitKey(keyName):
		return m, tea.Quit, true
	}
	if m.overlay != nil {
		next, cmd := m.updateOverlay(msg)
		return next, cmd, true
	}
	switch {
	case keyName == "esc":
		next, cmd := m.handleEsc()
		return next, cmd, true
	case isSubmitKey(keyName):
		next, cmd := m.submitInput(cmds)
		return next, cmd, true
	case isViewportKey(keyName):
		next, cmd := m.updateViewport(msg)
		return next, cmd, true
	}
	return m, nil, false
}

func (m *tuiModel) handleTUIEvent(msg tuiEventMsg, cmds []tea.Cmd) []tea.Cmd {
	m.applyEvent(msg.Event)
	return append(cmds, waitTUIEvent(m.events))
}

func (m *tuiModel) handleCommandDone(msg commandDoneMsg) {
	if msg.Err != nil {
		m.appendCell(tuiCell{Kind: "error", Title: "error", Body: msg.Err.Error()})
	}
}

func (m tuiModel) handleSpinnerTick(msg spinner.TickMsg) (tuiModel, tea.Cmd) {
	m.now = time.Now()
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m tuiModel) updateComposer(msg tea.Msg) (tuiModel, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m tuiModel) updateViewport(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.autoFollow = m.viewport.AtBottom()
	return m, cmd
}

func (m tuiModel) submitInput(cmds []tea.Cmd) (tea.Model, tea.Cmd) {
	m.escArmed = false
	m.ctrlCArmed = false
	m.autoFollow = true
	line := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	if line == "" {
		return m, nil
	}
	if line == "/exit" || line == "/quit" {
		return m, tea.Quit
	}
	cmds = append(cmds, m.runLine(line))
	m.refreshViewport()
	return m, tea.Batch(cmds...)
}

func (m tuiModel) handleCtrlC() (tea.Model, tea.Cmd) {
	if m.ctrlCArmed {
		return m, tea.Quit
	}
	if m.state.Busy {
		m.requestStop("interrupt", "Ctrl-C cancelled current turn. Press Ctrl-C again to exit client.")
		m.ctrlCArmed = true
		m.escArmed = false
		return m, nil
	}
	return m, tea.Quit
}

func (m tuiModel) handleEsc() (tea.Model, tea.Cmd) {
	if !m.state.Busy {
		m.escArmed = false
		return m, nil
	}
	if !m.escArmed {
		m.escArmed = true
		m.ctrlCArmed = false
		return m, nil
	}
	m.requestStop("stop", "ESC stopped current turn.")
	m.escArmed = false
	m.ctrlCArmed = false
	return m, nil
}

func (m *tuiModel) requestStop(title, body string) {
	if m.client != nil {
		_ = m.client.Interrupt(m.ctx)
	}
	m.appendCell(tuiCell{Kind: "info", Title: title, Body: body})
	m.refreshViewport()
}

func (m tuiModel) updateOverlay(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.overlay == nil {
		return m, nil
	}
	keyName := tuiKeyName(msg)
	switch keyName {
	case "up", "k":
		if m.choice > 0 {
			m.choice--
		}
	case "down", "j":
		if m.choice < len(m.overlay.Options)-1 {
			m.choice++
		}
	default:
		m.handleOverlayActionKey(keyName)
	}
	return m, nil
}

func (m *tuiModel) handleOverlayActionKey(keyName string) {
	surface := m.overlaySurface()
	switch {
	case isOverlayCancelKey(keyName):
		m.cancelOverlay()
	case isOverlaySubmitKey(keyName):
		m.replyOverlayChoice(m.choice)
	default:
		if reply, ok := surface.ReplyForKey(keyName); ok {
			m.replyOverlay(reply)
		}
	}
}

func (m tuiModel) overlayHelp() string {
	if m.overlay == nil {
		return ""
	}
	return "enter: choose · 1/2/3: direct choice · up/down: move · esc: reject"
}

func (m *tuiModel) cancelOverlay() {
	m.replyOverlay("3")
}

func (m *tuiModel) replyOverlayChoice(idx int) {
	if m.overlay == nil {
		return
	}
	m.replyOverlay(tuiOverlaySurface{req: m.overlay, choice: idx, width: m.width}.ReplyForChoice())
}

func (m *tuiModel) replyOverlay(key string) {
	if m.overlay == nil {
		return
	}
	if key == "" {
		key = "1"
	}
	m.overlay.Reply <- key
	m.overlay = nil
}

func (m tuiModel) runLine(line string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.runCommand(m.ctx, line)
		return commandDoneMsg{Err: err}
	}
}
