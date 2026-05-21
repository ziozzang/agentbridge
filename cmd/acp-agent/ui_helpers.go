package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ziozzang/agentbridge/internal/agent"
)

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
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

func permissionLabel(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "allow", "y", "yes":
		return "Full Access"
	case "reject", "deny":
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

func intEnv(name string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
