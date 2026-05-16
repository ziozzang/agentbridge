// Package logger provides leveled stderr logging for the GLM ACP agent.
//
// Set the environment variable ACP_GLM_DEBUG=true (or 1) to enable verbose
// debug output. Warnings and errors are always written.
package logger

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	mu          sync.Mutex
	out         io.Writer = os.Stderr
	debugOn     bool
	debugLoaded bool
)

// SetOutput overrides the destination writer (mainly for tests). Passing nil
// resets the destination back to os.Stderr.
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	if w == nil {
		out = os.Stderr
	} else {
		out = w
	}
}

// SetDebug forces the debug flag, bypassing environment lookup. Tests use
// this to deterministically enable or disable verbose output.
func SetDebug(enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	debugOn = enabled
	debugLoaded = true
}

// ResetDebug clears any forced debug state so the next call re-reads the
// environment variable.
func ResetDebug() {
	mu.Lock()
	defer mu.Unlock()
	debugOn = false
	debugLoaded = false
}

// IsDebugEnabled reports whether debug output is currently active.
func IsDebugEnabled() bool {
	mu.Lock()
	defer mu.Unlock()
	if !debugLoaded {
		v := strings.ToLower(strings.TrimSpace(os.Getenv("ACP_GLM_DEBUG")))
		debugOn = v == "true" || v == "1"
		debugLoaded = true
	}
	return debugOn
}

// IsDebug reports whether debug-level logging is enabled. Alias of
// IsDebugEnabled, provided so call sites can read naturally
// (`if logger.IsDebug() { ... }`).
func IsDebug() bool { return IsDebugEnabled() }

func write(level, msg string) {
	mu.Lock()
	defer mu.Unlock()
	ts := time.Now().UTC().Format("15:04:05.000")
	fmt.Fprintf(out, "[glm-acp-agent] %s [%s] %s\n", ts, level, msg)
}

// Debug logs only when debug mode is enabled.
func Debug(args ...any) {
	if !IsDebugEnabled() {
		return
	}
	write("DEBUG", joinArgs(args))
}

// Debugf is the printf-style variant of Debug.
func Debugf(format string, args ...any) {
	if !IsDebugEnabled() {
		return
	}
	write("DEBUG", fmt.Sprintf(format, args...))
}

// Warn always writes a warning to stderr.
func Warn(args ...any) { write("WARN", joinArgs(args)) }

// Warnf is the printf-style variant of Warn.
func Warnf(format string, args ...any) { write("WARN", fmt.Sprintf(format, args...)) }

// Error always writes an error message to stderr.
func Error(args ...any) { write("ERROR", joinArgs(args)) }

// Errorf is the printf-style variant of Error.
func Errorf(format string, args ...any) { write("ERROR", fmt.Sprintf(format, args...)) }

// MaskSecret returns "****" plus the last 4 characters of s, mirroring the
// behavior of the TypeScript reference implementation.
func MaskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}

func joinArgs(args []any) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = fmt.Sprint(a)
	}
	return strings.Join(parts, " ")
}
