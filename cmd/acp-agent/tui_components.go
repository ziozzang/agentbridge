package main

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

func newTUIModel(ctx context.Context, c *client) tuiModel {
	input := newTUIComposer()
	sp := newTUISpinner()
	return tuiModel{
		ctx:        ctx,
		client:     c,
		events:     c.events,
		state:      c.state,
		opts:       c.opts,
		viewport:   viewport.New(80, 20),
		autoFollow: true,
		input:      input,
		spinner:    sp,
		now:        time.Now(),
	}
}

func newTUIComposer() textinput.Model {
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
	return input
}

func newTUISpinner() spinner.Model {
	sp := spinner.New()
	sp.Spinner = spinner.Line
	return sp
}
