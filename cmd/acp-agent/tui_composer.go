package main

type tuiComposerSurface struct {
	input string
	width int
}

func (s tuiComposerSurface) View() string {
	return tuiComposerStyle.Width(s.width).Render(s.input)
}

func (m tuiModel) composerSurface() tuiComposerSurface {
	return tuiComposerSurface{
		input: m.input.View(),
		width: m.width,
	}
}
