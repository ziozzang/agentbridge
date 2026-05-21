package main

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
)

type tuiEventMsg struct{ Event uiEvent }

type commandDoneMsg struct{ Err error }

type tuiCell struct {
	Kind  string
	Title string
	Body  string
}

type tuiModel struct {
	ctx        context.Context
	client     *client
	events     <-chan uiEvent
	state      clientState
	opts       clientOptions
	width      int
	height     int
	viewport   viewport.Model
	input      textinput.Model
	spinner    spinner.Model
	cells      []tuiCell
	overlay    *uiPermissionRequest
	choice     int
	activity   string
	turnAt     time.Time
	now        time.Time
	escArmed   bool
	ctrlCArmed bool
	err        string
}

var slashCommandSuggestions = []string{
	"/help",
	"/status",
	"/sessions",
	"/resume ",
	"/session-load ",
	"/save ",
	"/list",
	"/load ",
	"/compact ",
	"/context",
	"/attach ",
	"/files",
	"/clear-files",
	"/structure",
	"/lua ",
	"/goal",
	"/goal status",
	"/goal set ",
	"/goal run",
	"/goal clear",
	"/new",
	"/stop",
	"/queue",
	"/skill list",
	"/skill status",
	"/skill clear",
	"/skill ",
	"/model",
	"/model ",
	"/mode",
	"/mode ",
	"/permission",
	"/permission prompt",
	"/permission allow",
	"/permission deny",
	"/permission reject",
	"/permission cancel",
	"/thinking on",
	"/thinking off",
	"/thinking toggle",
	"/tools on",
	"/tools off",
	"/tools toggle",
	"/raw on",
	"/raw off",
	"/raw toggle",
	"/quit",
	"/exit",
}

var approvalChoiceKeys = map[string]bool{
	"0": true,
	"1": true,
	"2": true,
	"3": true,
	"4": true,
	"5": true,
	"6": true,
	"7": true,
	"8": true,
	"9": true,
}
