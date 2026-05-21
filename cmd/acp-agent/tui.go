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

type tuiEventMsg struct{ Event uiEvent }

type commandDoneMsg struct{ Err error }

type tuiCell struct {
	Kind  string
	Title string
	Body  string
}

var slashCommandSuggestions = []string{
	"/help",
	"/status",
	"/sessions",
	"/resume ",
	"/session-load ",
	"/save ",
	"/list",
	"/load ",
	"/compact ",
	"/context",
	"/attach ",
	"/files",
	"/clear-files",
	"/structure",
	"/lua ",
	"/goal",
	"/goal status",
	"/goal set ",
	"/goal run",
	"/goal clear",
	"/new",
	"/stop",
	"/queue",
	"/skill list",
	"/skill status",
	"/skill clear",
	"/skill ",
	"/model",
	"/model ",
	"/mode",
	"/mode ",
	"/permission",
	"/permission prompt",
	"/permission allow",
	"/permission reject",
	"/permission cancel",
	"/thinking on",
	"/thinking off",
	"/thinking toggle",
	"/tools on",
	"/tools off",
	"/tools toggle",
	"/raw on",
	"/raw off",
	"/raw toggle",
	"/quit",
	"/exit",
}

var approvalChoiceKeys = map[string]bool{
	"0": true,
	"1": true,
	"2": true,
	"3": true,
	"4": true,
	"5": true,
	"6": true,
	"7": true,
	"8": true,
	"9": true,
}

type tuiModel struct {
	ctx      context.Context
	client   *client
	events   <-chan uiEvent
	state    clientState
	opts     clientOptions
	width    int
	height   int
	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model
	cells    []tuiCell
	overlay  *uiPermissionRequest
	choice   int
	err      string
}

var (
	tuiUserStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("13")).Bold(true)
	tuiAssistantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	tuiThinkingStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	tuiToolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	tuiInfoStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	tuiErrorStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	tuiStatusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("236"))
	tuiComposerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("235"))
	tuiOverlayStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).BorderForeground(lipgloss.Color("11"))
)

func runBubbleTUI(ctx context.Context, c *client) error {
	c.events = make(chan uiEvent, 512)
	c.ui = nil
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
	return "enter: choose · 1/2/3: direct choice · ↑/↓: move · esc: reject"
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

func (m *tuiModel) applyEvent(ev uiEvent) {
	switch ev := ev.(type) {
	case uiStateEvent:
		m.state = ev.State
		m.opts = ev.Opts
	case uiUserEvent:
		m.appendCell(tuiCell{Kind: "user", Title: "user", Body: ev.Text})
	case uiAssistantDeltaEvent:
		m.appendDelta("assistant", "assistant", ev.Text)
	case uiThinkingDeltaEvent:
		m.appendDelta("thinking", "thinking", ev.Text)
	case uiToolEvent:
		m.appendCell(tuiCell{Kind: "tool", Title: firstNonEmpty(ev.Status, "tool") + " " + ev.Title, Body: ev.Detail})
	case uiInfoEvent:
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
	m.viewport.Height = maxInt(1, m.height-2)
	m.refreshViewport()
}

func (m *tuiModel) refreshViewport() {
	m.viewport.SetContent(m.renderTranscript())
	m.viewport.GotoBottom()
}

func (m tuiModel) View() string {
	if m.width <= 0 {
		return ""
	}
	transcript := m.viewport.View()
	if m.overlay != nil {
		transcript = overlayBlock(m.width, m.height, transcript, m.renderOverlay())
	}
	composer := tuiComposerStyle.Width(m.width).Render(m.input.View())
	status := tuiStatusStyle.Width(m.width).Render(m.statusLine())
	return lipgloss.JoinVertical(lipgloss.Left, transcript, composer, status)
}

func (m tuiModel) completionHint() string {
	if m.overlay != nil {
		return ""
	}
	matches := m.input.MatchedSuggestions()
	if len(matches) == 0 {
		return ""
	}
	visible := matches
	if len(visible) > 5 {
		visible = visible[:5]
	}
	return "tab complete: " + strings.Join(visible, "  ")
}

func (m tuiModel) renderTranscript() string {
	var b strings.Builder
	for _, cell := range m.cells {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		title := cell.Title
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
	}
	return b.String()
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
	state := m.state
	status := "Ready"
	if state.Busy {
		status = m.spinner.View() + " Working"
	}
	parts := []string{
		status,
		contextLabel(state.Context),
	}
	parts = append(parts, limitLabels(state.Limits)...)
	parts = append(parts,
		firstNonEmpty(state.Model, "(model)"),
		firstNonEmpty(state.Mode, "default"),
		shortPath(state.Cwd),
		permissionLabel(firstNonEmpty(m.opts.Permission, "prompt")),
	)
	if state.QueueLen > 0 {
		parts = append(parts, fmt.Sprintf("Queue %d", state.QueueLen))
	}
	if state.Subagents > 0 {
		parts = append(parts, fmt.Sprintf("Subagents %d", state.Subagents))
	}
	if state.Tools > 0 {
		parts = append(parts, fmt.Sprintf("Tools %d", state.Tools))
	}
	if hint := m.completionHint(); hint != "" {
		parts = append(parts, hint)
	}
	parts = append(parts, agentVersionShort(), shortSession(state.SessionID))
	return truncateLine(strings.Join(nonEmpty(parts), " · "), maxInt(20, m.width))
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
