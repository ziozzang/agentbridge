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
		batch := []uiEvent{ev}
		for len(batch) < 64 {
			select {
			case next, ok := <-events:
				if !ok {
					return tuiEventMsg{Events: batch}
				}
				batch = append(batch, next)
			default:
				return tuiEventMsg{Events: batch}
			}
		}
		return tuiEventMsg{Events: batch}
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(waitTUIEvent(m.events), m.spinner.Tick)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var handled bool
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.handleWindowSize(msg)
	case tea.KeyMsg:
		m, cmd, handled = m.routeKey(msg, cmds)
		if handled {
			m.refreshViewport()
			return m, cmd
		}
		m, cmd = m.updateComposer(msg)
		cmds = append(cmds, cmd)
	case tuiEventMsg:
		cmds = m.handleTUIEvent(msg, cmds)
	case commandDoneMsg:
		m.handleCommandDone(msg)
	case interruptDoneMsg:
		m.handleInterruptDone(msg)
	case spinner.TickMsg:
		m, cmd = m.handleSpinnerTick(msg)
		cmds = append(cmds, cmd)
	}
	m.refreshViewport()
	return m, tea.Batch(cmds...)
}

func (m *tuiModel) handleWindowSize(msg tea.WindowSizeMsg) {
	m.width = msg.Width
	m.height = msg.Height
	m.reflow()
}

func (m tuiModel) routeKey(msg tea.KeyMsg, cmds []tea.Cmd) (tuiModel, tea.Cmd, bool) {
	keyName := tuiKeyName(msg)
	switch {
	case isGlobalInterruptKey(keyName):
		next, cmd := m.handleCtrlC()
		return next, cmd, true
	case isGlobalExitKey(keyName):
		m.prepareExit()
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
	case isCompletionAcceptKey(keyName):
		next, handled := m.acceptCompletion()
		if handled {
			return next, nil, true
		}
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
	for _, ev := range msg.Events {
		m.applyEventState(ev)
	}
	return append(cmds, waitTUIEvent(m.events))
}

func (m *tuiModel) handleCommandDone(msg commandDoneMsg) {
	if msg.Line != "" && m.commandRuns > 0 {
		m.commandRuns--
	}
	if msg.Err != nil {
		m.appendCell(tuiCell{Kind: "error", Title: "error", Body: msg.Err.Error()})
	}
	if m.commandRuns == 0 && !m.state.Busy {
		m.activity = ""
		m.commandCancel = nil
	}
}

func (m *tuiModel) handleInterruptDone(msg interruptDoneMsg) {
	_ = msg
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

func (m tuiModel) acceptCompletion() (tuiModel, bool) {
	value := m.input.Value()
	next := completeSlashValue(value, slashMatches(value))
	if next == "" || next == value {
		return m, false
	}
	m.input.SetValue(next)
	m.input.CursorEnd()
	return m, true
}

func (m tuiModel) updateViewport(msg tea.KeyMsg) (tuiModel, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.autoFollow = m.viewport.AtBottom()
	return m, cmd
}

func (m tuiModel) submitInput(cmds []tea.Cmd) (tuiModel, tea.Cmd) {
	m.escArmed = false
	m.ctrlCArmed = false
	m.autoFollow = true
	line := strings.TrimSpace(m.input.Value())
	m.input.Reset()
	if line == "" {
		return m, nil
	}
	if line == "/exit" || line == "/quit" {
		m.prepareExit()
		return m, tea.Quit
	}
	runCtx := m.ctx
	if strings.HasPrefix(line, "/") {
		m.commandRuns++
		m.activity = "running command"
		var cancel context.CancelFunc
		runCtx, cancel = context.WithCancel(m.ctx)
		m.commandCancel = cancel
	}
	cmds = append(cmds, m.runLine(runCtx, line))
	return m, tea.Batch(cmds...)
}

func (m tuiModel) handleCtrlC() (tuiModel, tea.Cmd) {
	if m.ctrlCArmed {
		m.prepareExit()
		return m, tea.Quit
	}
	if m.state.Busy {
		if m.overlay != nil {
			m.cancelOverlay()
		}
		cmd := m.requestStop("interrupt", "Ctrl-C cancelled current turn. Press Ctrl-C again to exit client.")
		m.ctrlCArmed = true
		m.escArmed = false
		return m, cmd
	}
	if m.commandRuns > 0 {
		m.cancelLocalCommand("interrupt", "Ctrl-C cancelled local command. Press Ctrl-C again to exit client.")
		m.ctrlCArmed = true
		m.escArmed = false
		return m, nil
	}
	if m.overlay != nil {
		m.cancelOverlay()
	}
	return m, tea.Quit
}

func (m *tuiModel) prepareExit() {
	if m.overlay != nil {
		m.cancelOverlay()
	}
	if m.commandCancel != nil {
		m.commandCancel()
		m.commandCancel = nil
	}
	if m.client != nil {
		m.client.CancelLocalWaiters()
	}
}

func (m tuiModel) handleEsc() (tuiModel, tea.Cmd) {
	if !m.state.Busy {
		m.escArmed = false
		return m, nil
	}
	if !m.escArmed {
		m.escArmed = true
		m.ctrlCArmed = false
		return m, nil
	}
	cmd := m.requestStop("stop", "ESC stopped current turn.")
	m.escArmed = false
	m.ctrlCArmed = false
	return m, cmd
}

func (m *tuiModel) requestStop(title, body string) tea.Cmd {
	client := m.client
	ctx := m.ctx
	m.appendCell(tuiCell{Kind: "info", Title: title, Body: body})
	return func() tea.Msg {
		if client == nil {
			return interruptDoneMsg{}
		}
		return interruptDoneMsg{Stopped: client.Interrupt(ctx)}
	}
}

func (m *tuiModel) cancelLocalCommand(title, body string) {
	if m.commandCancel != nil {
		m.commandCancel()
	}
	m.appendCell(tuiCell{Kind: "info", Title: title, Body: body})
}

func (m tuiModel) updateOverlay(msg tea.KeyMsg) (tuiModel, tea.Cmd) {
	if m.overlay == nil {
		return m, nil
	}
	keyName := tuiKeyName(msg)
	if m.overlayTyping {
		return m.updateOverlayInput(msg, keyName)
	}
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

func (m tuiModel) updateOverlayInput(msg tea.KeyMsg, keyName string) (tuiModel, tea.Cmd) {
	switch {
	case isOverlayCancelKey(keyName):
		m.cancelOverlay()
		return m, nil
	case isOverlaySubmitKey(keyName):
		if m.overlay == nil || m.choice < 0 || m.choice >= len(m.overlay.Options) {
			m.cancelOverlay()
			return m, nil
		}
		line := strings.TrimSpace(m.overlayInput.Value())
		if line == "" {
			m.cancelOverlay()
			return m, nil
		}
		m.replyOverlay(overlayOptionKey(m.choice, m.overlay.Options[m.choice]) + ":" + line)
		return m, nil
	}
	var cmd tea.Cmd
	m.overlayInput, cmd = m.overlayInput.Update(msg)
	return m, cmd
}

func (m *tuiModel) handleOverlayActionKey(keyName string) {
	surface := m.overlaySurface()
	switch {
	case isOverlayCancelKey(keyName):
		m.cancelOverlay()
	case isOverlaySubmitKey(keyName):
		if surface.SelectedOptionRequestsText() {
			m.startOverlayInput()
			return
		}
		m.replyOverlayChoice(m.choice)
	default:
		if reply, ok := surface.ReplyForKey(keyName); ok {
			if idx := m.overlayChoiceIndex(reply); idx >= 0 && choiceOptionRequestsText(m.overlay.Options[idx]) {
				m.choice = idx
				m.startOverlayInput()
				return
			}
			m.replyOverlay(reply)
		}
	}
}

func (m *tuiModel) startOverlayInput() {
	m.overlayTyping = true
	m.overlayInput = newTUIOverlayInput()
	m.overlayInput.Width = tuiOverlayInputWidth(m.width)
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
	m.overlayTyping = false
	m.overlayInput.Reset()
}

func (m tuiModel) runLine(ctx context.Context, line string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.runCommand(ctx, line)
		return commandDoneMsg{Line: line, Err: err}
	}
}

func (m tuiModel) overlayChoiceIndex(key string) int {
	if m.overlay == nil {
		return -1
	}
	for i, opt := range m.overlay.Options {
		if overlayOptionKey(i, opt) == key {
			return i
		}
	}
	return -1
}
