package main

import (
	"context"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
)

type tuiEventMsg struct{ Event uiEvent }

type commandDoneMsg struct {
	Line string
	Err  error
}

type interruptDoneMsg struct{ Stopped bool }

type tuiCell struct {
	Kind  string
	Title string
	Body  string
}

type tuiModel struct {
	ctx           context.Context
	client        *client
	events        <-chan uiEvent
	state         clientState
	opts          clientOptions
	width         int
	height        int
	viewport      viewport.Model
	autoFollow    bool
	input         textinput.Model
	overlayInput  textinput.Model
	spinner       spinner.Model
	cells         []tuiCell
	overlay       *uiPermissionRequest
	choice        int
	overlayTyping bool
	activity      string
	commandRuns   int
	commandCancel context.CancelFunc
	answerRunes   int
	thinkingRunes int
	toolEvents    int
	lastEventAt   time.Time
	lastEventKind string
	turnAt        time.Time
	now           time.Time
	escArmed      bool
	ctrlCArmed    bool
	err           string
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
