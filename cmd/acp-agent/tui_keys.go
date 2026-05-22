package main

import tea "github.com/charmbracelet/bubbletea"

func tuiKeyName(msg tea.KeyMsg) string {
	return msg.String()
}

func isSubmitKey(keyName string) bool {
	switch keyName {
	case "enter", "ctrl+j", "ctrl+m":
		return true
	default:
		return false
	}
}

func isGlobalExitKey(keyName string) bool {
	return keyName == "ctrl+d"
}

func isGlobalInterruptKey(keyName string) bool {
	return keyName == "ctrl+c"
}

func isViewportKey(keyName string) bool {
	switch keyName {
	case "up", "down", "pgup", "pgdown":
		return true
	default:
		return false
	}
}

func isCompletionAcceptKey(keyName string) bool {
	return keyName == "tab"
}

func isOverlayCancelKey(keyName string) bool {
	return keyName == "esc"
}

func isOverlaySubmitKey(keyName string) bool {
	return isSubmitKey(keyName)
}
