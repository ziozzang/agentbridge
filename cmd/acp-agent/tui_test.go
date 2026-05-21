package main

import (
	"context"
	"strings"
	"testing"

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
	m := tuiModel{width: 180, activity: "tool: Read file", state: clientState{
		Busy:    true,
		Model:   "glm-5.1",
		Mode:    "default",
		Context: contextState{Tokens: 64000, Window: 128000, UsedPercent: 50, LeftPercent: 50},
	}, opts: clientOptions{Permission: "reject"}}
	got := stripANSI(m.statusLine())
	for _, want := range []string{"Working: tool: Read file", "Context 50% left", "Read Only", "glm-5.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status missing %q: %q", want, got)
		}
	}
}
