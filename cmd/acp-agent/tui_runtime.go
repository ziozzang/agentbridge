package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func runBubbleTUI(ctx context.Context, c *client) error {
	c.events = make(chan uiEvent, 512)
	c.stream = nil
	c.emitState()

	input := textinput.New()
	input.Placeholder = "Type a message or /help"
	input.Prompt = " › "
	input.Focus()
	input.CharLimit = 0
	input.ShowSuggestions = true
	input.SetSuggestions(slashCommandSuggestions)
	input.CompletionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	input.KeyMap.NextSuggestion = key.NewBinding(key.WithKeys("ctrl+n"))
	input.KeyMap.PrevSuggestion = key.NewBinding(key.WithKeys("ctrl+p"))

	sp := spinner.New()
	sp.Spinner = spinner.Line

	m := tuiModel{
		ctx:      ctx,
		client:   c,
		events:   c.events,
		state:    c.state,
		opts:     c.opts,
		viewport: viewport.New(80, 20),
		input:    input,
		spinner:  sp,
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
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
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.reflow()
	case tea.KeyMsg:
		if m.overlay != nil {
			return m.updateOverlay(msg)
		}
		switch msg.String() {
		case "ctrl+c":
			if m.client.Interrupt(m.ctx) {
				m.appendCell(tuiCell{Kind: "info", Title: "interrupt", Body: "cancelled current session prompt"})
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+d":
			return m, tea.Quit
		case "enter":
			line := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			if line == "" {
				return m, nil
			}
			if line == "/exit" || line == "/quit" {
				return m, tea.Quit
			}
			cmds = append(cmds, m.runLine(line))
		}
	case tuiEventMsg:
		m.applyEvent(msg.Event)
		cmds = append(cmds, waitTUIEvent(m.events))
	case commandDoneMsg:
		if msg.Err != nil {
			m.appendCell(tuiCell{Kind: "error", Title: "error", Body: msg.Err.Error()})
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.refreshViewport()
	return m, tea.Batch(cmds...)
}

func (m tuiModel) updateOverlay(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.overlay == nil {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		if m.choice > 0 {
			m.choice--
		}
	case "down", "j":
		if m.choice < len(m.overlay.Options)-1 {
			m.choice++
		}
	case "esc":
		m.cancelOverlay()
	case "enter":
		m.replyOverlayChoice(m.choice)
	default:
		if m.hasOverlayChoiceKey(msg.String()) {
			m.chooseOverlayKey(msg.String())
		}
	}
	return m, nil
}

func (m tuiModel) overlayHelp() string {
	if m.overlay == nil {
		return ""
	}
	return "enter: choose · 1/2/3: direct choice · up/down: move · esc: reject"
}

func (m *tuiModel) cancelOverlay() {
	if m.overlay == nil {
		return
	}
	m.overlay.Reply <- "3"
	m.overlay = nil
}

func (m *tuiModel) chooseOverlayKey(key string) {
	if m.overlay == nil {
		return
	}
	switch key {
	case "y":
		m.overlay.Reply <- "1"
	case "n":
		m.overlay.Reply <- "3"
	default:
		if approvalChoiceKeys[key] {
			m.overlay.Reply <- key
		} else {
			return
		}
	}
	m.overlay = nil
}

func (m tuiModel) hasOverlayChoiceKey(key string) bool {
	if m.overlay == nil {
		return false
	}
	for i, opt := range m.overlay.Options {
		optKey := opt.Key
		if optKey == "" {
			optKey = fmt.Sprintf("%d", i+1)
		}
		if key == optKey {
			return true
		}
	}
	return key == "0" || key == "y" || key == "n"
}

func (m *tuiModel) replyOverlayChoice(idx int) {
	if m.overlay == nil {
		return
	}
	if idx < 0 || idx >= len(m.overlay.Options) {
		idx = 0
	}
	key := m.overlay.Options[idx].Key
	if key == "" {
		key = fmt.Sprintf("%d", idx+1)
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
