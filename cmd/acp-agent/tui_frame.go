package main

import "github.com/charmbracelet/lipgloss"

type tuiFrameSurface struct {
	width      int
	height     int
	transcript string
	overlay    string
	notice     string
	composer   string
	status     string
}

func (s tuiFrameSurface) View() string {
	if s.width <= 0 {
		return ""
	}
	transcript := s.transcript
	if s.overlay != "" {
		transcript = overlayBlock(s.width, s.height, transcript, s.overlay)
	}
	notice := tuiNoticeStyle.Width(s.width).Render(s.notice)
	status := tuiStatusStyle.Width(s.width).Render(s.status)
	return lipgloss.JoinVertical(lipgloss.Left, transcript, notice, s.composer, status)
}

func (m tuiModel) frameSurface() tuiFrameSurface {
	statusSurface := m.statusSurface()
	overlay := ""
	if m.overlay != nil {
		overlay = m.overlaySurface().View()
	}
	return tuiFrameSurface{
		width:      m.width,
		height:     m.height,
		transcript: m.viewport.View(),
		overlay:    overlay,
		notice:     statusSurface.Notice(),
		composer:   m.composerSurface().View(),
		status:     statusSurface.Status(),
	}
}
