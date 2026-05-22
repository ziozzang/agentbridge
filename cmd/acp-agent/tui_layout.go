package main

const tuiFixedShellRows = 3

func tuiTranscriptRows(totalHeight int) int {
	return maxInt(1, totalHeight-tuiFixedShellRows)
}
