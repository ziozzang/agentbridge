package main

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIStatusLinePrioritizesState(t *testing.T) {
	m := tuiModel{width: 160, state: clientState{
		Cwd:       "/tmp/project",
		SessionID: "abcdef123456",
		Model:     "glm-5.1",
		Mode:      "default",
		Context:   contextState{Tokens: 64000, Window: 128000, UsedPercent: 50, LeftPercent: 50},
		Limits:    limitState{FiveHourPercent: 94, WeeklyPercent: 84},
	}, opts: clientOptions{Permission: "allow"}}
	got := m.statusLine()
	for _, want := range []string{"Ready", "Context 50% left", "5h 94%", "weekly 84%", "glm-5.1", "Full Access"} {
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
		{Kind: "assistant", Title: "assistant", Body: "world"},
		{Kind: "thinking", Title: "reasoning", Body: "step"},
		{Kind: "tool", Title: "tool in_progress", Body: "path: README.md"},
		{Kind: "error", Title: "error", Body: "boom"},
	}}
	got := stripANSI(tr.View())
	for _, want := range []string{"user", "  > hello", "assistant", "world", "reasoning", "  step", "tool in_progress", "path: README.md", "error", "boom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("transcript missing %q: %q", want, got)
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

func TestTUIInterruptKeysBypassOverlay(t *testing.T) {
	m := tuiModel{ctx: context.Background(), state: clientState{Busy: true}, overlay: &uiPermissionRequest{
		Options: []choiceOption{{Key: "1", Label: "yes"}},
		Reply:   make(chan string, 1),
	}}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(tuiModel)
	if cmd != nil {
		t.Fatalf("first ctrl-c should stop, not quit")
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
	if cmd != nil {
		t.Fatalf("second esc should not quit")
	}
	if m.escArmed {
		t.Fatalf("second esc should clear stop confirmation")
	}
}

func TestTUICtrlCStopsThenExits(t *testing.T) {
	m := tuiModel{state: clientState{Busy: true}}
	next, cmd := m.handleCtrlC()
	m = next.(tuiModel)
	if cmd != nil {
		t.Fatalf("first ctrl-c should not quit")
	}
	if !m.ctrlCArmed {
		t.Fatalf("first ctrl-c did not arm exit")
	}
	_, cmd = m.handleCtrlC()
	if cmd == nil {
		t.Fatalf("second ctrl-c should quit")
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

func TestTUIReflowReservesNoticeComposerStatusRows(t *testing.T) {
	m := tuiModel{width: 100, height: 30, autoFollow: true}
	m.reflow()
	if m.viewport.Height != 27 {
		t.Fatalf("viewport height=%d", m.viewport.Height)
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
