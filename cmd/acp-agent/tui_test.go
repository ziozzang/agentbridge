package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTUIStatusLinePrioritizesState(t *testing.T) {
	m := tuiModel{width: 160, state: clientState{
		Cwd:       "/tmp/project",
		SessionID: "abcdef123456",
		Model:     "glm-5.1",
		Mode:      "default",
		Worker:    workerStateForOptions(clientOptions{Permission: "allow"}),
		Context:   contextState{Tokens: 64000, Window: 128000, UsedPercent: 50, LeftPercent: 50},
		Limits:    limitState{FiveHourPercent: 94, WeeklyPercent: 84},
	}, opts: clientOptions{Permission: "allow"}}
	got := m.statusLine()
	for _, want := range []string{"Ready", "Context 50% left", "5h 94%", "weekly 84%", "glm-5.1", "Worker terminal:6", "Full Access"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status line missing %q: %q", want, got)
		}
	}
}

func TestTUIComponentFactoryInitializesShellParts(t *testing.T) {
	c := &client{
		events: make(chan uiEvent),
		state:  clientState{Cwd: "/tmp/project", SessionID: "s1", Model: "glm-5.1"},
		opts:   clientOptions{Permission: "prompt"},
	}
	m := newTUIModel(context.Background(), c)
	if m.client != c || m.events != c.events {
		t.Fatalf("model did not keep client/event wiring")
	}
	if !m.autoFollow {
		t.Fatalf("new model should follow transcript bottom")
	}
	if !m.input.Focused() || !m.input.ShowSuggestions {
		t.Fatalf("composer should be focused with suggestions enabled")
	}
	if !m.overlayInput.Focused() || m.overlayInput.ShowSuggestions {
		t.Fatalf("overlay input should be focused without suggestions")
	}
	if got := m.input.Placeholder; got == "" {
		t.Fatalf("composer placeholder missing")
	}
	if m.viewport.Width != 80 || m.viewport.Height != 20 {
		t.Fatalf("viewport default size = %dx%d", m.viewport.Width, m.viewport.Height)
	}
	if m.spinner.Spinner.FPS == 0 {
		t.Fatalf("spinner should be configured")
	}
}

func TestTUIProgramOptionsCanBuildProgram(t *testing.T) {
	opts := tuiProgramOptions(context.Background())
	if len(opts) < 2 {
		t.Fatalf("program options should include context and alt screen")
	}
	p := tea.NewProgram(tuiModel{}, opts...)
	if p == nil {
		t.Fatalf("program was not constructed")
	}
}

func TestTUIThinkingDeltasCoalesce(t *testing.T) {
	m := tuiModel{}
	m.applyEvent(uiThinkingDeltaEvent{Text: "useful"})
	m.applyEvent(uiThinkingDeltaEvent{Text: " things"})
	if len(m.cells) != 1 {
		t.Fatalf("cells=%d", len(m.cells))
	}
	if m.cells[0].Kind != "thinking" || m.cells[0].Title != "reasoning" || m.cells[0].Body != "useful things" {
		t.Fatalf("thinking cell=%#v", m.cells[0])
	}
}

func TestTUITranscriptSurfaceRendersCellKinds(t *testing.T) {
	tr := tuiTranscript{cells: []tuiCell{
		{Kind: "user", Title: "user", Body: "hello"},
		{Kind: "user", Title: "command", Body: "/help"},
		{Kind: "assistant", Title: "assistant", Body: "world"},
		{Kind: "thinking", Title: "reasoning", Body: "step"},
		{Kind: "tool", Title: "tool in_progress", Body: "path: README.md"},
		{Kind: "error", Title: "error", Body: "boom"},
	}}
	got := stripANSI(tr.View())
	for _, want := range []string{"user", "  > hello", "command", "  > /help", "assistant", "world", "reasoning", "  step", "tool in_progress", "path: README.md", "error", "boom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q: %q", want, got)
		}
	}
}

func TestTUITranscriptSurfaceWrapsLongCellLines(t *testing.T) {
	tr := tuiTranscript{
		width: 24,
		cells: []tuiCell{
			{Kind: "assistant", Title: "assistant", Body: strings.Repeat("word ", 12)},
			{Kind: "tool", Title: "tool in_progress", Body: "path: " + strings.Repeat("segment/", 10)},
			{Kind: "error", Title: "error", Body: strings.Repeat("failure ", 10)},
		},
	}
	got := stripANSI(tr.View())
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if width := lipgloss.Width(line); width > 24 {
			t.Fatalf("transcript line width=%d > 24: %q\nview=%q", width, line, got)
		}
	}
	for _, want := range []string{"assistant", "tool in_progress", "path:", "error"} {
		if !strings.Contains(got, want) {
			t.Fatalf("wrapped transcript missing %q: %q", want, got)
		}
	}
}

func TestTUIProgressTracksStreamingEvents(t *testing.T) {
	start := time.Date(2026, 5, 22, 1, 2, 3, 0, time.UTC)
	m := tuiModel{width: 180, state: clientState{Busy: true}, turnAt: start, now: start}
	m.applyEvent(uiAssistantDeltaEvent{Text: "pong"})
	m.applyEvent(uiThinkingDeltaEvent{Text: "think"})
	m.applyEvent(uiToolEvent{Status: "in_progress", Title: "Read file", Detail: "path: README.md"})
	m.now = m.lastEventAt.Add(2 * time.Second)
	line := stripANSI(m.noticeLine())
	for _, want := range []string{"answer 4 chars", "reasoning 5 chars", "tool events 1", "tool 2s ago"} {
		if !strings.Contains(line, want) {
			t.Fatalf("notice missing %q: %q", want, line)
		}
	}
}

func TestTUICommandEventsRenderInputOutputSeparation(t *testing.T) {
	m := tuiModel{}
	m.applyEvent(uiCommandEvent{Text: "/help"})
	m.applyEvent(uiInfoEvent{Title: "help", Body: "commands"})
	if len(m.cells) != 2 {
		t.Fatalf("cells=%d", len(m.cells))
	}
	if m.cells[0].Title != "command" || m.cells[0].Body != "/help" {
		t.Fatalf("command cell=%#v", m.cells[0])
	}
	if m.cells[1].Title != "help" || m.cells[1].Body != "commands" {
		t.Fatalf("info cell=%#v", m.cells[1])
	}
}

func TestTUICommandRunStatusSurface(t *testing.T) {
	m := tuiModel{
		width:       180,
		commandRuns: 1,
		activity:    "running command",
		state:       clientState{Model: "glm-5.1", Context: contextState{LeftPercent: 80}},
		opts:        clientOptions{Permission: "prompt"},
	}
	if got := stripANSI(m.noticeLine()); !strings.Contains(got, "command running 1") || !strings.Contains(got, "running command") {
		t.Fatalf("notice missing command lifecycle: %q", got)
	}
	got := stripANSI(m.statusLine())
	for _, want := range []string{"Command: running command", "Commands 1", "glm-5.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %q: %q", want, got)
		}
	}
}

func TestTUIStatusSurfaceSeparatesNoticeProgressAndStatus(t *testing.T) {
	start := time.Date(2026, 5, 22, 1, 2, 3, 0, time.UTC)
	m := tuiModel{
		width:         220,
		state:         clientState{Busy: true, Model: "glm-5.1", Context: contextState{LeftPercent: 70}},
		opts:          clientOptions{Permission: "allow"},
		activity:      "answering",
		turnAt:        start,
		now:           start.Add(4 * time.Second),
		answerRunes:   12,
		lastEventAt:   start.Add(3 * time.Second),
		lastEventKind: "answer",
	}
	surface := m.statusSurface()
	for _, check := range []struct {
		name string
		got  string
		want string
	}{
		{"notice", stripANSI(surface.Notice()), "running 4s"},
		{"progress", stripANSI(surface.Progress()), "answer 12 chars"},
		{"status", stripANSI(surface.Status()), "Working: answering"},
	} {
		if !strings.Contains(check.got, check.want) {
			t.Fatalf("%s missing %q: %q", check.name, check.want, check.got)
		}
	}
}

func TestTUIStatusSurfaceTruncatesToSingleFixedLine(t *testing.T) {
	m := tuiModel{
		width:    48,
		activity: "tool: Very long provider tool activity that should not wrap",
		turnAt:   time.Date(2026, 5, 22, 1, 2, 3, 0, time.UTC),
		now:      time.Date(2026, 5, 22, 1, 2, 8, 0, time.UTC),
		state: clientState{
			Busy:      true,
			Cwd:       "/very/long/path/that/should/not/expand/the/status/row",
			SessionID: "abcdef1234567890",
			Model:     "glm-5.1-with-a-long-display-name",
			Mode:      "bypass_permissions",
			Context:   contextState{Tokens: 120000, Window: 200000, UsedPercent: 60, LeftPercent: 40},
			Limits:    limitState{FiveHourPercent: 94, WeeklyPercent: 84, MonthlyPercent: 77},
			QueueLen:  9,
			Tools:     3,
			Subagents: 2,
		},
		opts: clientOptions{Permission: "allow"},
	}
	got := m.statusLine()
	if strings.Contains(got, "\n") {
		t.Fatalf("status line wrapped: %q", got)
	}
	if width := lipgloss.Width(got); width > m.width {
		t.Fatalf("status width=%d want <= %d: %q", width, m.width, got)
	}
}

func TestTruncateStatusLineHandlesTinyWidth(t *testing.T) {
	if got := truncateStatusLine("abcdef", 0); got != "" {
		t.Fatalf("zero width=%q", got)
	}
	if got := truncateStatusLine("abcdef", 1); lipgloss.Width(got) > 1 {
		t.Fatalf("tiny width result too wide: %q", got)
	}
}

func TestTUIPermissionOverlayReplies(t *testing.T) {
	reply := make(chan string, 1)
	m := tuiModel{ctx: context.Background(), overlay: &uiPermissionRequest{
		Options: []choiceOption{{Key: "1", Label: "yes"}, {Key: "3", Label: "no"}},
		Reply:   reply,
	}}
	next, _ := m.updateOverlay(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(tuiModel)
	next, _ = m.updateOverlay(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(tuiModel)
	if m.overlay != nil {
		t.Fatalf("overlay still active")
	}
	if got := <-reply; got != "3" {
		t.Fatalf("reply=%q", got)
	}
}

func TestTUIPermissionOverlayAcceptsLineFeedEnter(t *testing.T) {
	reply := make(chan string, 1)
	m := tuiModel{ctx: context.Background(), overlay: &uiPermissionRequest{
		Options: []choiceOption{{Key: "1", Label: "yes"}},
		Reply:   reply,
	}}
	next, _ := m.updateOverlay(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = next.(tuiModel)
	if m.overlay != nil {
		t.Fatalf("overlay still active")
	}
	if got := <-reply; got != "1" {
		t.Fatalf("reply=%q", got)
	}
}

func TestTUIPermissionOverlayNumericChoice(t *testing.T) {
	reply := make(chan string, 1)
	m := tuiModel{ctx: context.Background(), overlay: &uiPermissionRequest{
		Options: []choiceOption{{Key: "1", Label: "yes"}, {Key: "2", Label: "same command"}, {Key: "3", Label: "no"}},
		Reply:   reply,
	}}
	next, _ := m.updateOverlay(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m = next.(tuiModel)
	if m.overlay != nil {
		t.Fatalf("overlay still active")
	}
	if got := <-reply; got != "2" {
		t.Fatalf("reply=%q", got)
	}
}

func TestTUIPermissionOverlayOtherCommandUsesTUIInput(t *testing.T) {
	reply := make(chan string, 1)
	m := newTUIModel(context.Background(), &client{events: make(chan uiEvent)})
	m.width = 120
	m.overlay = &uiPermissionRequest{
		Options: []choiceOption{
			{Key: "1", Label: "yes"},
			{Key: "4", Label: "other command"},
		},
		Reply: reply,
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	if cmd != nil {
		t.Fatalf("starting overlay input should not launch a command")
	}
	m = next.(tuiModel)
	if !m.overlayTyping || m.overlay == nil {
		t.Fatalf("overlay input mode not active")
	}
	for _, r := range []rune("pwd") {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(tuiModel)
	}
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("submitting overlay input should not launch a command")
	}
	m = next.(tuiModel)
	if m.overlay != nil || m.overlayTyping {
		t.Fatalf("overlay did not close")
	}
	if got := <-reply; got != "4:pwd" {
		t.Fatalf("reply=%q", got)
	}
}

func TestTUICtrlCCancelsActiveOverlay(t *testing.T) {
	reply := make(chan string, 1)
	m := tuiModel{ctx: context.Background(), state: clientState{Busy: true}, overlay: &uiPermissionRequest{
		Options: []choiceOption{{Key: "1", Label: "yes"}, {Key: "3", Label: "no"}},
		Reply:   reply,
	}}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("first ctrl-c should schedule async interrupt")
	}
	m = next.(tuiModel)
	if m.overlay != nil {
		t.Fatalf("overlay should be closed")
	}
	if got := <-reply; got != "3" {
		t.Fatalf("reply=%q", got)
	}
	if !m.ctrlCArmed {
		t.Fatalf("ctrl-c should arm exit after stopping")
	}
}

func TestTUIOverlaySurfaceMapsChoicesAndRenders(t *testing.T) {
	surface := tuiOverlaySurface{
		width: 120,
		req: &uiPermissionRequest{
			Title:   "tool permission",
			Detail:  "command: date",
			Options: []choiceOption{{Key: "1", Label: "yes"}, {Key: "3", Label: "no"}},
		},
		choice: 1,
	}
	view := stripANSI(surface.View())
	for _, want := range []string{"tool permission", "command: date", "1. yes", "3. no", "enter: choose"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overlay view missing %q: %q", want, view)
		}
	}
	if got := surface.ReplyForChoice(); got != "3" {
		t.Fatalf("choice reply=%q", got)
	}
	if got, ok := surface.ReplyForKey("y"); !ok || got != "1" {
		t.Fatalf("yes key = %q %v", got, ok)
	}
	if got, ok := surface.ReplyForKey("n"); !ok || got != "3" {
		t.Fatalf("no key = %q %v", got, ok)
	}
}

func TestTUIOverlayChoiceRestoresTranscriptFrame(t *testing.T) {
	reply := make(chan string, 1)
	m := newTUIModel(context.Background(), &client{events: make(chan uiEvent)})
	m.width = 120
	m.height = 12
	m.appendCell(tuiCell{Kind: "assistant", Title: "assistant", Body: "visible answer"})
	m.overlay = &uiPermissionRequest{
		Title:   "tool permission",
		Detail:  "command: date",
		Options: []choiceOption{{Key: "1", Label: "yes"}, {Key: "3", Label: "no"}},
		Reply:   reply,
	}
	m.reflow()
	if got := stripANSI(m.View()); !strings.Contains(got, "tool permission") {
		t.Fatalf("overlay should be visible before choice: %q", got)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if cmd != nil {
		t.Fatalf("overlay choice should not launch command")
	}
	m = next.(tuiModel)
	if m.overlay != nil {
		t.Fatalf("overlay still active")
	}
	if got := <-reply; got != "1" {
		t.Fatalf("reply=%q", got)
	}
	got := stripANSI(m.View())
	if strings.Contains(got, "tool permission") {
		t.Fatalf("overlay should be gone after choice: %q", got)
	}
	if !strings.Contains(got, "visible answer") || !strings.Contains(got, "Type a message or /help") {
		t.Fatalf("frame did not restore transcript/composer: %q", got)
	}
}

func TestTUIInterruptKeysBypassOverlay(t *testing.T) {
	m := tuiModel{ctx: context.Background(), state: clientState{Busy: true}, overlay: &uiPermissionRequest{
		Options: []choiceOption{{Key: "1", Label: "yes"}},
		Reply:   make(chan string, 1),
	}}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatalf("first ctrl-c should schedule async interrupt")
	}
	if !m.ctrlCArmed {
		t.Fatalf("ctrl-c was swallowed by overlay")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd == nil {
		t.Fatalf("ctrl-d should quit even when overlay is active")
	}
}

func TestTUISlashCommandSuggestions(t *testing.T) {
	m := tuiModel{width: 120}
	m.input.SetValue("/go")
	m.input.ShowSuggestions = true
	m.input.SetSuggestions(slashCommandSuggestions)
	got := m.input.MatchedSuggestions()
	if len(got) == 0 {
		t.Fatalf("no suggestions")
	}
	found := false
	for _, item := range got {
		if item == "/goal" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("/goal not suggested: %#v", got)
	}
	if !strings.Contains(m.completionHint(), "/goal") {
		t.Fatalf("hint missing /goal: %q", m.completionHint())
	}
}

func TestTUIPermissionArgumentSuggestions(t *testing.T) {
	m := tuiModel{width: 160}
	m.input.SetValue("/permission ")
	m.input.ShowSuggestions = true
	m.input.SetSuggestions(slashCommandSuggestions)
	got := strings.Join(m.input.MatchedSuggestions(), " ")
	for _, want := range []string{"/permission allow", "/permission deny", "/permission reject", "/permission prompt", "/permission cancel"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in suggestions: %q", want, got)
		}
	}
	if hint := m.completionHint(); hint != "/permission allow|deny|reject|prompt|cancel" {
		t.Fatalf("compact hint=%q", hint)
	}
}

func TestTUICompletionSurfaceOwnsSlashHints(t *testing.T) {
	surface := tuiCompletionSurface{
		value:   "/mode ",
		matches: []string{"/mode", "/mode default", "/mode accept_edits", "/mode bypass_permissions"},
		active:  true,
	}
	if got := surface.Hint(); got != "/mode default|accept_edits|bypass_permissions" {
		t.Fatalf("mode hint=%q", got)
	}
	surface = tuiCompletionSurface{
		value:   "/sk",
		matches: []string{"/skill list", "/skill status", "/skill clear", "/skill "},
		active:  true,
	}
	if got := surface.Hint(); !strings.Contains(got, "/skill") {
		t.Fatalf("slash hint=%q", got)
	}
	if got := (tuiCompletionSurface{value: "/goal ", matches: []string{"/goal run"}, active: false}).Hint(); got != "" {
		t.Fatalf("inactive hint=%q", got)
	}
}

func TestTUIComposerSurfaceRendersBottomInput(t *testing.T) {
	surface := tuiComposerSurface{width: 80, input: " › hello"}
	got := stripANSI(surface.View())
	if !strings.Contains(got, " › hello") {
		t.Fatalf("composer surface missing input: %q", got)
	}
	if width := len(got); width < 80 {
		t.Fatalf("composer surface should occupy width, got %d", width)
	}
}

func TestTUIComposerSurfaceKeepsSingleRow(t *testing.T) {
	surface := tuiComposerSurface{width: 32, input: " › " + strings.Repeat("long input ", 12)}
	got := stripANSI(surface.View())
	if strings.Contains(got, "\n") {
		t.Fatalf("composer wrapped: %q", got)
	}
	if width := lipgloss.Width(got); width > 32 {
		t.Fatalf("composer width=%d: %q", width, got)
	}
}

func TestTUIFrameSurfaceComposesFixedShellRows(t *testing.T) {
	surface := tuiFrameSurface{
		width:      100,
		height:     12,
		transcript: "assistant\nhello",
		notice:     "Ctrl-D: exit",
		composer:   tuiComposerSurface{width: 100, input: " › prompt"}.View(),
		status:     "Ready · glm-5.1",
	}
	got := stripANSI(surface.View())
	for _, want := range []string{"assistant", "hello", "Ctrl-D: exit", " › prompt", "Ready"} {
		if !strings.Contains(got, want) {
			t.Fatalf("frame missing %q: %q", want, got)
		}
	}
	if first := strings.Index(got, "assistant"); first < 0 || strings.Index(got, "Ready") < first {
		t.Fatalf("frame order is wrong: %q", got)
	}
}

func TestTUIFrameSurfaceAppliesOverlayOverTranscript(t *testing.T) {
	surface := tuiFrameSurface{
		width:      80,
		height:     8,
		transcript: strings.Repeat("line\n", 6),
		overlay:    "approval requested\n1. yes\n3. no",
		notice:     "approval requested",
		composer:   tuiComposerSurface{width: 80, input: " › "}.View(),
		status:     "Ready",
	}
	got := stripANSI(surface.View())
	for _, want := range []string{"approval requested", "1. yes", "3. no", "Ready"} {
		if !strings.Contains(got, want) {
			t.Fatalf("overlay frame missing %q: %q", want, got)
		}
	}
}

func TestTUIFrameSurfaceKeepsNoticeToSingleRow(t *testing.T) {
	surface := tuiFrameSurface{
		width:      48,
		height:     8,
		transcript: "assistant",
		notice:     strings.Repeat("very long notice ", 12),
		composer:   tuiComposerSurface{width: 48, input: " › prompt"}.View(),
		status:     "Ready",
	}
	got := stripANSI(surface.View())
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("notice should not wrap frame rows, lines=%d view=%q", len(lines), got)
	}
	if !strings.Contains(lines[1], "very long") {
		t.Fatalf("notice row missing content: %q", got)
	}
	if strings.Contains(lines[1], "prompt") || strings.Contains(lines[1], "Ready") {
		t.Fatalf("notice row overlapped adjacent rows: %q", got)
	}
}

func TestTUIStatusLineShowsActivityAndPermission(t *testing.T) {
	start := time.Now().Add(-75 * time.Second)
	m := tuiModel{width: 180, activity: "tool: Read file", turnAt: start, now: start.Add(75 * time.Second), state: clientState{
		Busy:    true,
		Model:   "glm-5.1",
		Mode:    "default",
		Context: contextState{Tokens: 64000, Window: 128000, UsedPercent: 50, LeftPercent: 50},
	}, opts: clientOptions{Permission: "reject"}}
	got := stripANSI(m.statusLine())
	for _, want := range []string{"Working: tool: Read file", "1m15s", "Context 50% left", "Read Only", "glm-5.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %q: %q", want, got)
		}
	}
}

func TestTUIEscRequiresConfirmationBeforeStop(t *testing.T) {
	m := tuiModel{width: 120, state: clientState{Busy: true}}
	next, cmd := m.handleEsc()
	m = next.(tuiModel)
	if cmd != nil {
		t.Fatalf("first esc should not quit")
	}
	if !m.escArmed {
		t.Fatalf("first esc did not arm stop")
	}
	if !strings.Contains(stripANSI(m.noticeLine()), "stop current turn") {
		t.Fatalf("missing stop notice: %q", m.noticeLine())
	}
	next, cmd = m.handleEsc()
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatalf("second esc should schedule async stop")
	}
	if m.escArmed {
		t.Fatalf("second esc should clear stop confirmation")
	}
}

func TestTUICtrlCStopsThenExits(t *testing.T) {
	m := tuiModel{state: clientState{Busy: true}}
	next, cmd := m.handleCtrlC()
	m = next.(tuiModel)
	if cmd == nil {
		t.Fatalf("first ctrl-c should schedule async interrupt")
	}
	if !m.ctrlCArmed {
		t.Fatalf("first ctrl-c did not arm exit")
	}
	_, cmd = m.handleCtrlC()
	if cmd == nil {
		t.Fatalf("second ctrl-c should quit")
	}
}

func TestTUICtrlCAddsSingleInterruptCell(t *testing.T) {
	m := tuiModel{state: clientState{Busy: true}}
	next, cmd := m.handleCtrlC()
	if cmd == nil {
		t.Fatalf("first ctrl-c should schedule async interrupt")
	}
	m = next.(tuiModel)
	count := 0
	for _, cell := range m.cells {
		if cell.Title == "interrupt" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("interrupt cell count=%d cells=%#v", count, m.cells)
	}
}

func TestTUIInterruptCommandReportsCompletion(t *testing.T) {
	m := tuiModel{state: clientState{Busy: true}}
	next, cmd := m.handleCtrlC()
	if cmd == nil {
		t.Fatalf("interrupt command missing")
	}
	m = next.(tuiModel)
	msg := cmd()
	done, ok := msg.(interruptDoneMsg)
	if !ok {
		t.Fatalf("interrupt command msg=%T", msg)
	}
	if done.Stopped {
		t.Fatalf("nil client should not report stopped")
	}
	m.handleInterruptDone(done)
	if len(m.cells) != 1 || m.cells[0].Title != "interrupt" {
		t.Fatalf("interrupt completion should not add noisy cells: %#v", m.cells)
	}
}

func TestTUIStopFeedbackRefreshesFrameImmediately(t *testing.T) {
	m := newTUIModel(context.Background(), &client{events: make(chan uiEvent)})
	m.width = 120
	m.height = 10
	m.state = clientState{Busy: true, Model: "glm-5.1", Context: contextState{LeftPercent: 90}}
	m.reflow()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("first ctrl-c should schedule async interrupt")
	}
	m = next.(tuiModel)
	got := stripANSI(m.View())
	for _, want := range []string{"interrupt", "Ctrl-C cancelled current turn", "Working"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stop frame missing %q: %q", want, got)
		}
	}
}

func TestTUIUpdateSequenceRendersTranscriptProgressAndOverlay(t *testing.T) {
	m := newTUIModel(context.Background(), &client{events: make(chan uiEvent)})
	m.width = 140
	m.height = 14
	m.reflow()
	events := []uiEvent{
		uiStateEvent{State: clientState{Busy: true, Model: "glm-5.1", Context: contextState{LeftPercent: 88}}, Opts: clientOptions{Permission: "prompt"}},
		uiUserEvent{Text: "inspect"},
		uiThinkingDeltaEvent{Text: "plan"},
		uiAssistantDeltaEvent{Text: "answer"},
		uiToolEvent{Status: "in_progress", Title: "Read file", Detail: "path: README.md"},
	}
	for _, ev := range events {
		next, _ := m.Update(tuiEventMsg{Event: ev})
		m = next.(tuiModel)
	}
	got := stripANSI(m.View())
	for _, want := range []string{"inspect", "reasoning", "plan", "assistant", "answer", "tool in_progress", "README.md", "Working"} {
		if !strings.Contains(got, want) {
			t.Fatalf("update sequence frame missing %q: %q", want, got)
		}
	}
	next, _ := m.Update(tuiEventMsg{Event: uiPermissionRequest{
		Title:   "tool permission",
		Detail:  "command: date",
		Options: []choiceOption{{Key: "1", Label: "yes"}, {Key: "3", Label: "no"}},
		Reply:   make(chan string, 1),
	}})
	m = next.(tuiModel)
	got = stripANSI(m.View())
	for _, want := range []string{"tool permission", "command: date", "1. yes", "approval requested"} {
		if !strings.Contains(got, want) {
			t.Fatalf("overlay sequence frame missing %q: %q", want, got)
		}
	}
}

func TestTUIBusySubmitQueueEventsReachFrame(t *testing.T) {
	events := make(chan uiEvent, 4)
	c := &client{
		events: events,
		stderr: ioDiscard{},
		state:  clientState{Busy: true, Model: "glm-5.1", Context: contextState{LeftPercent: 80}},
		opts:   clientOptions{Permission: "prompt"},
	}
	m := newTUIModel(context.Background(), c)
	m.width = 180
	m.height = 12
	m.reflow()

	c.SubmitPrompt(context.Background(), "queued prompt")
	for i := 0; i < 2; i++ {
		select {
		case ev := <-events:
			next, _ := m.Update(tuiEventMsg{Event: ev})
			m = next.(tuiModel)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for queue event %d", i+1)
		}
	}
	got := stripANSI(m.View())
	for _, want := range []string{"queued", "1 prompt(s) waiting", "Queue 1", "Working"} {
		if !strings.Contains(got, want) {
			t.Fatalf("queue frame missing %q: %q", want, got)
		}
	}
}

func TestTUITurnElapsedFormatting(t *testing.T) {
	if got := compactDuration(75 * time.Second); got != "1m15s" {
		t.Fatalf("duration=%q", got)
	}
	if got := compactDuration(3*time.Hour + 2*time.Minute); got != "3h02m" {
		t.Fatalf("duration=%q", got)
	}
}

func TestTUISubmitKeysIncludeLineFeedAndCarriageReturn(t *testing.T) {
	for _, keyName := range []string{"enter", "ctrl+j", "ctrl+m"} {
		if !isSubmitKey(keyName) {
			t.Fatalf("%s should submit input", keyName)
		}
	}
	if isSubmitKey("space") {
		t.Fatalf("space should not submit input")
	}
}

func TestTUIKeyLayersKeepComposerNavigationOutOfViewport(t *testing.T) {
	for _, keyName := range []string{"home", "end", "left", "right", "ctrl+d"} {
		if isViewportKey(keyName) {
			t.Fatalf("%s should not be captured by transcript viewport", keyName)
		}
	}
	for _, keyName := range []string{"up", "down", "pgup", "pgdown"} {
		if !isViewportKey(keyName) {
			t.Fatalf("%s should scroll transcript viewport", keyName)
		}
	}
	if !isGlobalExitKey("ctrl+d") {
		t.Fatalf("ctrl+d should remain a global exit key")
	}
}

func TestTUIRouteKeySeparatesGlobalOverlayViewportAndComposer(t *testing.T) {
	reply := make(chan string, 1)
	m := tuiModel{
		ctx: context.Background(),
		overlay: &uiPermissionRequest{
			Options: []choiceOption{{Key: "1", Label: "yes"}, {Key: "3", Label: "no"}},
			Reply:   reply,
		},
		input: newTUIComposer(),
	}
	next, cmd, handled := m.routeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")}, nil)
	if !handled || cmd != nil {
		t.Fatalf("overlay numeric key should be handled without command")
	}
	m = next.(tuiModel)
	if m.overlay != nil {
		t.Fatalf("overlay should be closed")
	}
	if got := <-reply; got != "3" {
		t.Fatalf("overlay reply=%q", got)
	}

	m = tuiModel{input: newTUIComposer()}
	next, cmd, handled = m.routeKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")}, nil)
	if handled || cmd != nil || next.(tuiModel).input.Value() != "" {
		t.Fatalf("composer text key should fall through to composer update")
	}

	m = tuiModel{input: newTUIComposer(), viewport: newTUIModel(context.Background(), &client{events: make(chan uiEvent)}).viewport}
	next, _, handled = m.routeKey(tea.KeyMsg{Type: tea.KeyPgUp}, nil)
	if !handled {
		t.Fatalf("viewport key should be handled by viewport layer")
	}
	if next.(tuiModel).input.Value() != "" {
		t.Fatalf("viewport key should not mutate composer")
	}
}

func TestTUICommandDoneAppendsErrorCell(t *testing.T) {
	m := tuiModel{}
	m.handleCommandDone(commandDoneMsg{Err: errors.New("boom")})
	if len(m.cells) != 1 || m.cells[0].Kind != "error" || !strings.Contains(m.cells[0].Body, "boom") {
		t.Fatalf("error cell=%#v", m.cells)
	}
}

func TestTUICommandDoneSuccessDoesNotAddEmptyCell(t *testing.T) {
	m := tuiModel{}
	m.handleCommandDone(commandDoneMsg{})
	if len(m.cells) != 0 {
		t.Fatalf("success command should not add synthetic cell: %#v", m.cells)
	}
}

func TestTUICommandDoneClearsRunState(t *testing.T) {
	m := tuiModel{commandRuns: 1, activity: "running command"}
	m.handleCommandDone(commandDoneMsg{Line: "/status"})
	if m.commandRuns != 0 || m.activity != "" {
		t.Fatalf("command state not cleared: runs=%d activity=%q", m.commandRuns, m.activity)
	}
}

func TestTUISubmitSlashCommandTracksRunState(t *testing.T) {
	c := &client{events: make(chan uiEvent, 8), stderr: ioDiscard{}}
	m := newTUIModel(context.Background(), c)
	m.input.SetValue("/status")
	next, cmd := m.submitInput(nil)
	if cmd == nil {
		t.Fatalf("slash command should schedule command")
	}
	m = next.(tuiModel)
	if m.commandRuns != 1 || m.activity != "running command" {
		t.Fatalf("command state = runs %d activity %q", m.commandRuns, m.activity)
	}
	if msg := cmd(); msg == nil {
		t.Fatalf("command should return completion message")
	}
}

func TestTUISubmitPromptDoesNotTrackLocalCommand(t *testing.T) {
	c := &client{events: make(chan uiEvent, 8), stderr: ioDiscard{}, state: clientState{SessionID: "s1"}}
	m := newTUIModel(context.Background(), c)
	m.input.SetValue("hello")
	next, cmd := m.submitInput(nil)
	if cmd == nil {
		t.Fatalf("prompt should schedule command wrapper")
	}
	m = next.(tuiModel)
	if m.commandRuns != 0 {
		t.Fatalf("plain prompt should not be a local command: %d", m.commandRuns)
	}
}

func TestTUIReflowReservesNoticeComposerStatusRows(t *testing.T) {
	m := tuiModel{width: 100, height: 30, autoFollow: true}
	m.reflow()
	if m.viewport.Height != 27 {
		t.Fatalf("viewport height=%d", m.viewport.Height)
	}
}

func TestTUIWindowSizeUpdateReflowsFrameAndComposer(t *testing.T) {
	m := newTUIModel(context.Background(), &client{events: make(chan uiEvent)})
	m.appendCell(tuiCell{Kind: "assistant", Title: "assistant", Body: strings.Repeat("wide ", 20)})
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 72, Height: 9})
	if cmd != nil {
		t.Fatalf("window resize should not launch command")
	}
	m = next.(tuiModel)
	if m.width != 72 || m.height != 9 {
		t.Fatalf("size=%dx%d", m.width, m.height)
	}
	if m.viewport.Width != 72 || m.viewport.Height != 6 {
		t.Fatalf("viewport=%dx%d", m.viewport.Width, m.viewport.Height)
	}
	if m.input.Width != 69 {
		t.Fatalf("input width=%d", m.input.Width)
	}
	got := stripANSI(m.View())
	for _, want := range []string{"assistant", "Ctrl-D: exit", "Type a message or /help", "Ready"} {
		if !strings.Contains(got, want) {
			t.Fatalf("resized frame missing %q: %q", want, got)
		}
	}
}

func TestTUIReflowClampsTinyComposerAndViewport(t *testing.T) {
	m := tuiModel{width: 2, height: 2, autoFollow: true, input: newTUIComposer()}
	m.reflow()
	if m.input.Width != 1 {
		t.Fatalf("tiny input width=%d", m.input.Width)
	}
	if m.viewport.Width != 2 || m.viewport.Height != 1 {
		t.Fatalf("tiny viewport=%dx%d", m.viewport.Width, m.viewport.Height)
	}
}

func TestTUIScrollPositionIsPreservedWhenNotFollowing(t *testing.T) {
	m := tuiModel{width: 80, height: 8, autoFollow: true}
	for i := 0; i < 20; i++ {
		m.appendCell(tuiCell{Kind: "info", Title: "line", Body: strings.Repeat("x", i+1)})
	}
	m.reflow()
	if !m.viewport.AtBottom() {
		t.Fatalf("expected initial viewport to follow bottom")
	}
	next, _ := m.updateViewport(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(tuiModel)
	if m.autoFollow {
		t.Fatalf("page up should disable auto-follow")
	}
	offset := m.viewport.YOffset
	m.applyEvent(uiAssistantDeltaEvent{Text: "new content"})
	if m.viewport.YOffset != offset {
		t.Fatalf("viewport offset changed from %d to %d", offset, m.viewport.YOffset)
	}
	m.width = 220
	if got := stripANSI(m.statusLine()); !strings.Contains(got, "Scroll") {
		t.Fatalf("status should expose non-following scroll state: %q", got)
	}
}

func TestTUIRefreshViewportUsesWrappedTranscriptAndFollowsBottom(t *testing.T) {
	m := newTUIModel(context.Background(), &client{events: make(chan uiEvent)})
	m.width = 32
	m.height = 8
	m.autoFollow = true
	m.reflow()
	m.applyEvent(uiAssistantDeltaEvent{Text: strings.Repeat("segment/", 20)})
	if !m.autoFollow || !m.viewport.AtBottom() {
		t.Fatalf("viewport should follow bottom after wrapped long content")
	}
	content := stripANSI(m.viewport.View())
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if width := lipgloss.Width(line); width > m.viewport.Width {
			t.Fatalf("viewport line width=%d > %d: %q\ncontent=%q", width, m.viewport.Width, line, content)
		}
	}
	if !strings.Contains(content, "segment/") {
		t.Fatalf("wrapped viewport missing assistant content: %q", content)
	}
}

func TestTUIViewIncludesNoticeComposerAndStatus(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	m := tuiModel{
		width:    140,
		height:   10,
		activity: "thinking",
		turnAt:   start,
		now:      start.Add(2 * time.Second),
		state: clientState{
			Busy:    true,
			Model:   "glm-5.1",
			Mode:    "default",
			Context: contextState{Tokens: 1, Window: 10, UsedPercent: 10, LeftPercent: 90},
		},
		opts: clientOptions{Permission: "prompt"},
	}
	m.reflow()
	got := stripANSI(m.View())
	for _, want := range []string{"running 2s", "thinking", "Context 90% left", "Ask"} {
		if !strings.Contains(got, want) {
			t.Fatalf("view missing %q: %q", want, got)
		}
	}
}

func TestTUIOverlayNotice(t *testing.T) {
	m := tuiModel{width: 120, overlay: &uiPermissionRequest{Title: "permission requested", Options: []choiceOption{{Key: "1", Label: "yes"}}}}
	got := stripANSI(m.noticeLine())
	if !strings.Contains(got, "approval requested") {
		t.Fatalf("overlay notice=%q", got)
	}
}
