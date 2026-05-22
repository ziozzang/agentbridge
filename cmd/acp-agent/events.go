package main

type uiEvent interface{}

type uiStateEvent struct {
	State clientState
	Opts  clientOptions
}

type uiUserEvent struct {
	Text string
}

type uiCommandEvent struct {
	Text string
}

type uiAssistantDeltaEvent struct {
	Text string
}

type uiThinkingDeltaEvent struct {
	Text string
}

type uiToolEvent struct {
	Status string
	Title  string
	Detail string
}

type uiInfoEvent struct {
	Title string
	Body  string
}

type uiErrorEvent struct {
	Message string
}

type uiPermissionRequest struct {
	Title   string
	Detail  string
	Options []choiceOption
	Reply   chan string
}

func (c *client) emit(ev uiEvent) {
	if c == nil || c.events == nil {
		return
	}
	select {
	case c.events <- ev:
	default:
	}
}

func (c *client) emitState() {
	if c == nil || c.events == nil {
		return
	}
	c.mu.Lock()
	state := c.state
	opts := c.opts
	c.mu.Unlock()
	c.emit(uiStateEvent{State: state, Opts: opts})
}

func (c *client) emitInfo(title, body string) bool {
	if c == nil || c.events == nil {
		return false
	}
	c.emit(uiInfoEvent{Title: title, Body: body})
	return true
}

func (c *client) emitError(message string) bool {
	if c == nil || c.events == nil {
		return false
	}
	c.emit(uiErrorEvent{Message: message})
	return true
}
