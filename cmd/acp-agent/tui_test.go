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

func TestTUIThinkingDeltasCoalesce(t *testing.T) {
	m := tuiModel{}
	m.applyEvent(uiThinkingDeltaEvent{Text: "useful"})
	m.applyEvent(uiThinkingDeltaEvent{Text: " things"})
	if len(m.cells) != 1 {
		t.Fatalf("cells=%d", len(m.cells))
	}
	if m.cells[0].Kind != "thinking" || m.cells[0].Body != "useful things" {
		t.Fatalf("thinking cell=%#v", m.cells[0])
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
