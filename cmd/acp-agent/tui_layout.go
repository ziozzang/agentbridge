package main

const tuiFixedShellRows = 3

func tuiTranscriptRows(totalHeight int) int {
	return maxInt(1, totalHeight-tuiFixedShellRows)
}

func tuiComposerInputWidth(frameWidth int) int {
	return maxInt(1, frameWidth-3)
}

func tuiOverlayInputWidth(frameWidth int) int {
	return maxInt(1, frameWidth-8)
}
