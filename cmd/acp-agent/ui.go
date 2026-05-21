package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ziozzang/agentbridge/internal/agent"
)

type cliUI struct {
	mu       sync.Mutex
	w        io.Writer
	enabled  bool
	color    bool
	spinning bool
	stop     chan struct{}
	frame    int
	status   string
	activity string
	answer   bool
}

func newCLIUI(w io.Writer) *cliUI {
	enabled := isTerminalWriter(w)
	color := enabled && os.Getenv("NO_COLOR") == ""
	ui := &cliUI{w: w, enabled: enabled, color: color, status: "Ready"}
	return ui
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func (u *cliUI) ready(c *client) {
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	u.status = "Ready"
	u.activity = ""
	u.answer = false
	u.renderLocked(c)
	u.mu.Unlock()
}

func (u *cliUI) start(c *client, activity string) {
	if u == nil || !u.enabled {
		return
	}
	u.mu.Lock()
	u.status = "Working"
	u.activity = activity
	u.answer = false
	if !u.spinning {
		u.spinning = true
		u.stop = make(chan struct{})
		stop := u.stop
		go u.spin(c, stop)
	}
	u.renderLocked(c)
	u.mu.Unlock()
}

func (u *cliUI) setActivity(c *client, activity string) {
	if u == nil || !u.enabled {
		return
	}
	u.mu.Lock()
	u.activity = activity
	u.renderLocked(c)
	u.mu.Unlock()
}

func (u *cliUI) beginAnswer() {
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	if !u.answer {
		fmt.Fprint(u.w, "\r\033[2K")
		u.answer = true
	}
	u.mu.Unlock()
}

func (u *cliUI) stopSpinner() {
	u.mu.Lock()
	if u.spinning {
		close(u.stop)
		u.spinning = false
		u.stop = nil
	}
	u.mu.Unlock()
}

func (u *cliUI) spin(c *client, stop <-chan struct{}) {
	ticker := time.NewTicker(120 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			u.mu.Lock()
			u.frame++
			u.renderLocked(c)
			u.mu.Unlock()
		}
	}
}

func (u *cliUI) clear() {
	if u == nil || !u.enabled {
		return
	}
	u.mu.Lock()
	fmt.Fprint(u.w, "\r\033[2K")
	u.mu.Unlock()
}

func (u *cliUI) println(format string, args ...any) {
	if u == nil || !u.enabled {
		return
	}
	u.clear()
	fmt.Fprintf(u.w, format, args...)
}

func (u *cliUI) renderLocked(c *client) {
	if !u.enabled {
		return
	}
	c.mu.Lock()
	state := c.state
	opts := c.opts
	c.mu.Unlock()
	mode := firstNonEmpty(state.Mode, "default")
	perm := permissionLabel(firstNonEmpty(opts.Permission, "prompt"))
	cwd := state.Cwd
	if cwd == "" {
		cwd = "."
	}
	model := firstNonEmpty(state.Model, "(model)")
	status := u.status
	if u.activity != "" {
		status = status + ": " + u.activity
	}
	if u.spinning {
		status = spinnerFrame(u.frame) + " " + progressBar(u.frame, u.color) + " " + status
	}
	parts := []string{
		colorize(u.color, "36", model),
		colorize(u.color, "2", mode),
		colorize(u.color, "2", shortPath(cwd)),
		colorizeStatus(u.color, status),
		colorize(u.color, "33", perm),
		contextLabel(state.Context),
		colorize(u.color, "2", agentVersionShort()),
		shortSession(state.SessionID),
	}
	fmt.Fprintf(u.w, "\r\033[2K%s", strings.Join(nonEmpty(parts), " · "))
}

func contextLabel(ctx contextState) string {
	if ctx.Window <= 0 {
		return "Context ?"
	}
	return fmt.Sprintf("Context %.0f%% left · %.0f%% used · %s/%s", ctx.LeftPercent, ctx.UsedPercent, compactNumber(ctx.Tokens), compactNumber(ctx.Window))
}

func compactNumber(n int) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.2fM", float64(n)/1000000)
	case n >= 1000:
		return fmt.Sprintf("%.0fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func spinnerFrame(n int) string {
	frames := []string{"|", "/", "-", "\\"}
	return frames[n%len(frames)]
}

func progressBar(n int, color bool) string {
	const width = 12
	pos := n % width
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < width; i++ {
		switch i {
		case pos:
			b.WriteByte('>')
		case (pos + width - 1) % width:
			b.WriteByte('=')
		default:
			b.WriteByte('-')
		}
	}
	b.WriteByte(']')
	return colorize(color, "36", b.String())
}

func colorize(enabled bool, code, s string) string {
	if !enabled || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func colorizeStatus(enabled bool, s string) string {
	if strings.Contains(s, "Ready") {
		return colorize(enabled, "32", s)
	}
	if strings.Contains(s, "failed") || strings.Contains(s, "Error") {
		return colorize(enabled, "31", s)
	}
	return colorize(enabled, "35", s)
}

func permissionLabel(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "allow", "y", "yes":
		return "Full Access"
	case "reject":
		return "Read Only"
	case "cancel":
		return "Cancel"
	default:
		return "Ask"
	}
}

func shortPath(path string) string {
	if path == "" {
		return "."
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	if len(path) <= 48 {
		return path
	}
	return "..." + string(os.PathSeparator) + filepath.Base(filepath.Dir(path)) + string(os.PathSeparator) + filepath.Base(path)
}

func agentVersionShort() string {
	if agent.Version == "" {
		return ""
	}
	return agent.Version
}

func shortSession(id string) string {
	if id == "" {
		return ""
	}
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(stripANSI(s)) != "" {
			out = append(out, s)
		}
	}
	return out
}

func stripANSI(s string) string {
	repl := strings.NewReplacer("\033[0m", "", "\033[2m", "", "\033[31m", "", "\033[32m", "", "\033[33m", "", "\033[35m", "", "\033[36m", "")
	return repl.Replace(s)
}
