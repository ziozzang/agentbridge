package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ziozzang/agentbridge/internal/agent"
	"golang.org/x/sys/unix"
)

type cliUI struct {
	mu        sync.Mutex
	w         io.Writer
	enabled   bool
	color     bool
	spinning  bool
	stop      chan struct{}
	frame     int
	status    string
	activity  string
	answer    bool
	streaming bool
	rows      int
	fixed     bool
}

func newCLIUI(w io.Writer) *cliUI {
	enabled := isTerminalWriter(w)
	color := enabled && os.Getenv("NO_COLOR") == ""
	rows := terminalRows(w)
	ui := &cliUI{w: w, enabled: enabled, color: color, status: "Ready", rows: rows}
	if enabled && rows > 2 {
		ui.fixed = true
		ui.setupFixedStatus()
	}
	return ui
}

func (u *cliUI) active() bool {
	return u != nil && u.enabled
}

func (u *cliUI) setupFixedStatus() {
	if u == nil || !u.fixed {
		return
	}
	bodyBottom := u.bodyBottomRow()
	fmt.Fprintf(u.w, "\033[1;%dr", bodyBottom)
	u.clearFixedLine(u.composerRow())
	u.clearFixedLine(u.statusRow())
	fmt.Fprintf(u.w, "\033[%d;1H", bodyBottom)
}

func (u *cliUI) restoreTerminal() {
	if u == nil || !u.fixed {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	fmt.Fprint(u.w, "\033[0m\033[r")
	u.clearFixedLine(u.composerRow())
	u.clearFixedLine(u.statusRow())
	fmt.Fprintf(u.w, "\033[%d;1H\n", u.rows)
	u.fixed = false
	u.mu.Unlock()
}

func (u *cliUI) bodyBottomRow() int {
	if u == nil || !u.fixed || u.rows <= 3 {
		return u.rows
	}
	return u.rows - 2
}

func (u *cliUI) composerRow() int {
	if u == nil || !u.fixed || u.rows <= 2 {
		return 0
	}
	return u.rows - 1
}

func (u *cliUI) statusRow() int {
	if u == nil || !u.fixed {
		return u.rows
	}
	return u.rows
}

func (u *cliUI) clearFixedLine(row int) {
	if row <= 0 {
		return
	}
	fmt.Fprintf(u.w, "\033[%d;1H\033[0m\033[2K", row)
}

func (u *cliUI) moveBodyCursor() {
	if u == nil || !u.fixed {
		return
	}
	fmt.Fprintf(u.w, "\033[%d;1H", u.bodyBottomRow())
}

func (u *cliUI) clearComposer() {
	if u == nil || !u.fixed {
		return
	}
	u.clearFixedLine(u.composerRow())
	u.moveBodyCursor()
}

func (u *cliUI) acceptComposer() {
	if u == nil || !u.enabled {
		return
	}
	u.mu.Lock()
	fmt.Fprint(u.w, "\033[0m")
	u.clearComposer()
	u.mu.Unlock()
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
	u.streaming = false
	u.renderLocked(c)
	u.renderComposerLocked()
	u.mu.Unlock()
}

func (u *cliUI) refresh(c *client) {
	if u == nil || !u.enabled {
		return
	}
	u.mu.Lock()
	if !u.answer {
		u.renderLocked(c)
	}
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
	u.streaming = false
	if !u.spinning {
		u.spinning = true
		u.stop = make(chan struct{})
		stop := u.stop
		go u.spin(c, stop)
	}
	u.renderLocked(c)
	u.renderComposerLocked()
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
		u.clearComposer()
		fmt.Fprint(u.w, "\r\033[2K")
		u.answer = true
		u.streaming = true
		fmt.Fprint(u.w, colorize(u.color, "36", "\nassistant"), "\n")
	}
	u.mu.Unlock()
}

func (u *cliUI) finishAnswer(c *client) {
	if c != nil && c.stream != nil {
		c.stream.finish()
	}
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	if u.streaming {
		fmt.Fprint(u.w, "\n")
		u.streaming = false
	}
	u.status = "Ready"
	u.activity = ""
	u.answer = false
	u.renderLocked(c)
	u.renderComposerLocked()
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

func (u *cliUI) prompt(c *client) {
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	u.status = "Ready"
	u.activity = ""
	u.answer = false
	u.streaming = false
	u.renderLocked(c)
	u.renderComposerLocked()
	u.mu.Unlock()
}

func (u *cliUI) userMessage(text string) {
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	u.clearComposer()
	fmt.Fprint(u.w, "\r\033[2K")
	lines := splitDisplayLines(text)
	if len(lines) == 1 {
		fmt.Fprintln(u.w, colorize(u.color, "35", "\nuser"))
		fmt.Fprintln(u.w, colorize(u.color, "2", "  > ")+lines[0])
	} else {
		fmt.Fprintln(u.w, colorize(u.color, "35", "\nuser"))
		for _, line := range lines {
			fmt.Fprintln(u.w, colorize(u.color, "2", "  > ")+line)
		}
	}
	u.mu.Unlock()
}

func (u *cliUI) infoCell(title, body string) {
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	u.clearComposer()
	fmt.Fprint(u.w, "\r\033[2K")
	fmt.Fprintln(u.w, colorize(u.color, "36", "\n"+title))
	for _, line := range splitDisplayLines(body) {
		fmt.Fprintln(u.w, "  "+line)
	}
	u.mu.Unlock()
}

func (u *cliUI) toolCell(status, title, detail string) {
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	u.clearComposer()
	fmt.Fprint(u.w, "\r\033[2K")
	if status == "completed" && strings.TrimSpace(detail) == "" {
		fmt.Fprintln(u.w, colorize(u.color, "2", "  done ")+title)
		u.mu.Unlock()
		return
	}
	label := "tool"
	if status != "" {
		label += ":" + status
	}
	code := "33"
	if strings.Contains(status, "fail") || strings.Contains(status, "reject") {
		code = "31"
	}
	fmt.Fprintln(u.w, colorize(u.color, code, "\n"+label), title)
	if strings.TrimSpace(detail) != "" {
		for _, line := range splitDisplayLines(detail) {
			fmt.Fprintln(u.w, colorize(u.color, "2", "  "+truncateLine(line, terminalColumns()-4)))
		}
	}
	u.mu.Unlock()
}

func (u *cliUI) statusCard(c *client, body string) {
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	u.clearComposer()
	fmt.Fprint(u.w, "\r\033[2K")
	fmt.Fprintln(u.w, colorize(u.color, "36", "\nstatus"))
	for _, line := range splitDisplayLines(body) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			fmt.Fprintf(u.w, "  %-14s %s\n", colorize(u.color, "2", parts[0]+":"), parts[1])
		} else {
			fmt.Fprintln(u.w, "  "+line)
		}
	}
	u.renderLocked(c)
	u.mu.Unlock()
}

func (u *cliUI) overlay(title, detail string, options []choiceOption) {
	if u == nil || !u.enabled {
		return
	}
	u.stopSpinner()
	u.mu.Lock()
	u.clearComposer()
	fmt.Fprint(u.w, "\r\033[2K")
	width := minInt(72, maxInt(40, terminalColumns()-2))
	fmt.Fprintln(u.w, colorize(u.color, "33", "\npermission"))
	fmt.Fprintln(u.w, colorize(u.color, "2", "╭"+strings.Repeat("─", width-2)+"╮"))
	fmt.Fprintf(u.w, "%s %-*s %s\n", colorize(u.color, "2", "│"), width-4, title, colorize(u.color, "2", "│"))
	if strings.TrimSpace(detail) != "" {
		fmt.Fprintf(u.w, "%s %-*s %s\n", colorize(u.color, "2", "│"), width-4, truncateLine(detail, width-4), colorize(u.color, "2", "│"))
	}
	fmt.Fprintln(u.w, colorize(u.color, "2", "├"+strings.Repeat("─", width-2)+"┤"))
	for i, opt := range options {
		key := opt.Key
		if key == "" {
			key = strconv.Itoa(i + 1)
		}
		row := fmt.Sprintf("%s. %s", key, opt.Label)
		prefix := "  "
		if i == 0 {
			prefix = "› "
		}
		fmt.Fprintf(u.w, "%s %-*s %s\n", colorize(u.color, "2", "│"), width-4, truncateLine(prefix+row, width-4), colorize(u.color, "2", "│"))
	}
	fmt.Fprintln(u.w, colorize(u.color, "2", "╰"+strings.Repeat("─", width-2)+"╯"))
	u.mu.Unlock()
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
	if state.Busy && status == "Ready" {
		status = "Working"
	}
	if u.activity != "" {
		status = status + ": " + u.activity
	}
	if u.spinning {
		status = spinnerFrame(u.frame) + " " + progressBar(u.frame, u.color) + " " + status
	}
	parts := []string{
		colorizeStatus(u.color, status),
		contextLabel(state.Context),
	}
	parts = append(parts, limitLabels(state.Limits)...)
	parts = append(parts,
		colorize(u.color, "36", model),
		colorize(u.color, "2", mode),
		colorize(u.color, "2", shortPath(cwd)),
		colorize(u.color, "33", perm),
	)
	if state.QueueLen > 0 {
		parts = append(parts, colorize(u.color, "35", fmt.Sprintf("Queue %d", state.QueueLen)))
	}
	if state.Subagents > 0 {
		parts = append(parts, colorize(u.color, "35", fmt.Sprintf("Subagents %d", state.Subagents)))
	}
	if state.Tools > 0 {
		parts = append(parts, colorize(u.color, "33", fmt.Sprintf("Tools %d", state.Tools)))
	}
	parts = append(parts, colorize(u.color, "2", agentVersionShort()), shortSession(state.SessionID))
	u.renderStatusLine(strings.Join(nonEmpty(parts), " · "))
}

func (u *cliUI) renderComposerLocked() {
	if u == nil || !u.enabled {
		return
	}
	if !u.fixed || u.composerRow() <= 0 {
		fmt.Fprint(u.w, "\n", colorize(u.color, "36", "› "))
		return
	}
	row := u.composerRow()
	width := maxInt(20, terminalColumns())
	prompt := " › "
	if u.color {
		fmt.Fprintf(u.w, "\033[%d;1H\033[48;5;236m\033[37m%-*s\033[%d;4H", row, width, prompt, row)
		return
	}
	fmt.Fprintf(u.w, "\033[%d;1H\033[2K%s", row, prompt)
}

func (u *cliUI) renderStatusLine(text string) {
	text = truncateLine(text, maxInt(20, terminalColumns()))
	row := u.statusRow()
	if row <= 0 {
		row = terminalRows(u.w)
	}
	if row <= 0 {
		fmt.Fprintf(u.w, "\r\033[2K%s", text)
		return
	}
	fmt.Fprintf(u.w, "\0337\033[%d;1H\r\033[2K%s\0338", row, text)
}

func contextLabel(ctx contextState) string {
	if ctx.Window <= 0 {
		return "Context ?"
	}
	return fmt.Sprintf("Context %.0f%% left · %.0f%% used · %s/%s", ctx.LeftPercent, ctx.UsedPercent, compactNumber(ctx.Tokens), compactNumber(ctx.Window))
}

func limitLabels(l limitState) []string {
	var out []string
	if l.FiveHourPercent > 0 {
		out = append(out, fmt.Sprintf("5h %.0f%%", l.FiveHourPercent))
	} else {
		out = append(out, "5h ?")
	}
	if l.WeeklyPercent > 0 {
		out = append(out, fmt.Sprintf("weekly %.0f%%", l.WeeklyPercent))
	} else {
		out = append(out, "weekly ?")
	}
	if l.MonthlyPercent > 0 {
		out = append(out, fmt.Sprintf("monthly %.0f%%", l.MonthlyPercent))
	}
	if l.Refreshing {
		out = append(out, "limits refreshing")
	}
	return out
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

func splitDisplayLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

func truncateLine(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func terminalColumns() int {
	if v := strings.TrimSpace(os.Getenv("COLUMNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 100
}

func terminalRows(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ); err == nil && ws.Row > 0 {
			return int(ws.Row)
		}
	}
	if v := strings.TrimSpace(os.Getenv("LINES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
