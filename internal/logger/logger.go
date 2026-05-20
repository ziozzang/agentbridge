// Package logger provides leveled logging for the ACP harness with
// optional file output, size-based rotation, and a back-compatible API.
//
// Configuration knobs (all optional):
//
//	AGENTBRIDGE_LOG_LEVEL=trace|debug|info|warn|error   (default: warn)
//	AGENTBRIDGE_LOG_FILE=/path/to/agent.log             (default: stderr only)
//	AGENTBRIDGE_LOG_MAX_BYTES=10485760                  (rotation threshold)
//	AGENTBRIDGE_LOG_MAX_FILES=5                         (retention; >=1)
//	AGENTBRIDGE_LOG_BOTH=1                              (also write to stderr)
//
// Back-compat:
//
//	ACP_HARNESS_LOG_* and ACP_GLM_DEBUG remain supported aliases.
package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Level is the logger severity.
type Level int

const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelOff
)

// String returns the uppercase name of l.
func (l Level) String() string {
	switch l {
	case LevelTrace:
		return "TRACE"
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "OFF"
	}
}

// ParseLevel converts a textual level into Level. Unknown values yield
// LevelWarn, the conservative default.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error", "err":
		return LevelError
	case "off", "silent", "none":
		return LevelOff
	}
	return LevelWarn
}

var (
	mu          sync.Mutex
	out         io.Writer = os.Stderr
	rotator     *rotatingWriter
	level       = LevelWarn
	debugForced bool
	levelLoaded bool
)

// SetOutput overrides the destination writer (mainly for tests). Passing
// nil resets the destination back to os.Stderr.
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	if w == nil {
		out = os.Stderr
	} else {
		out = w
	}
}

// SetDebug forces the debug flag, bypassing env lookup.
func SetDebug(enabled bool) {
	mu.Lock()
	defer mu.Unlock()
	debugForced = enabled
	if enabled && level > LevelDebug {
		level = LevelDebug
	}
	levelLoaded = true
}

// ResetDebug clears any forced debug state so the next call re-reads env.
func ResetDebug() {
	mu.Lock()
	defer mu.Unlock()
	debugForced = false
	levelLoaded = false
	level = LevelWarn
	if rotator != nil {
		_ = rotator.Close()
		rotator = nil
	}
	out = os.Stderr
}

// SetLevel forces the active level. Useful for tests.
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
	levelLoaded = true
}

// Configure (re-)reads env vars and sets up file sink + rotation. Safe to
// call multiple times — only the first call wires up a rotator unless
// ResetDebug() has been called.
func Configure() error {
	mu.Lock()
	defer mu.Unlock()
	return loadFromEnvLocked()
}

func loadFromEnvLocked() error {
	if levelLoaded {
		return nil
	}
	levelLoaded = true
	level = ParseLevel(envFirst("AGENTBRIDGE_LOG_LEVEL", "ACP_HARNESS_LOG_LEVEL"))
	// Back-compat: ACP_GLM_DEBUG=true forces DEBUG.
	if isTrue(os.Getenv("ACP_GLM_DEBUG")) || debugForced {
		if level > LevelDebug {
			level = LevelDebug
		}
	}
	path := envFirst("AGENTBRIDGE_LOG_FILE", "ACP_HARNESS_LOG_FILE")
	if path == "" {
		return nil
	}
	maxBytes := parseIntDefault(envFirst("AGENTBRIDGE_LOG_MAX_BYTES", "ACP_HARNESS_LOG_MAX_BYTES"), 10*1024*1024)
	maxFiles := parseIntDefault(envFirst("AGENTBRIDGE_LOG_MAX_FILES", "ACP_HARNESS_LOG_MAX_FILES"), 5)
	if maxFiles < 1 {
		maxFiles = 1
	}
	r, err := newRotatingWriter(path, int64(maxBytes), maxFiles)
	if err != nil {
		return err
	}
	rotator = r
	if isTrue(envFirst("AGENTBRIDGE_LOG_BOTH", "ACP_HARNESS_LOG_BOTH")) {
		out = io.MultiWriter(os.Stderr, r)
	} else {
		out = r
	}
	return nil
}

// IsDebugEnabled reports whether debug output is currently active.
func IsDebugEnabled() bool {
	mu.Lock()
	defer mu.Unlock()
	if !levelLoaded {
		_ = loadFromEnvLocked()
	}
	return level <= LevelDebug
}

// IsDebug is an alias of IsDebugEnabled retained for readability at call sites.
func IsDebug() bool { return IsDebugEnabled() }

// IsTraceEnabled reports whether trace output is currently active.
func IsTraceEnabled() bool {
	mu.Lock()
	defer mu.Unlock()
	if !levelLoaded {
		_ = loadFromEnvLocked()
	}
	return level <= LevelTrace
}

func active(l Level) bool {
	mu.Lock()
	defer mu.Unlock()
	if !levelLoaded {
		_ = loadFromEnvLocked()
	}
	return l >= level
}

func write(lvl Level, msg string) {
	mu.Lock()
	defer mu.Unlock()
	if !levelLoaded {
		_ = loadFromEnvLocked()
	}
	if lvl < level {
		return
	}
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	fmt.Fprintf(out, "[agentbridge] %s [%s] %s\n", ts, lvl.String(), msg)
}

// Trace logs a trace-level event when enabled.
func Trace(args ...any)                 { write(LevelTrace, joinArgs(args)) }
func Tracef(format string, args ...any) { write(LevelTrace, fmt.Sprintf(format, args...)) }

// Debug logs a debug-level event when enabled.
func Debug(args ...any) { write(LevelDebug, joinArgs(args)) }
func Debugf(format string, args ...any) {
	write(LevelDebug, fmt.Sprintf(format, args...))
}

// Info logs an informational event when enabled.
func Info(args ...any)                 { write(LevelInfo, joinArgs(args)) }
func Infof(format string, args ...any) { write(LevelInfo, fmt.Sprintf(format, args...)) }

// Warn always writes a warning.
func Warn(args ...any)                 { write(LevelWarn, joinArgs(args)) }
func Warnf(format string, args ...any) { write(LevelWarn, fmt.Sprintf(format, args...)) }

// Error always writes an error message.
func Error(args ...any)                 { write(LevelError, joinArgs(args)) }
func Errorf(format string, args ...any) { write(LevelError, fmt.Sprintf(format, args...)) }

// MaskSecret returns "****" plus the last 4 characters of s.
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

func isTrue(s string) bool {
	v := strings.ToLower(strings.TrimSpace(s))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// ----- rotatingWriter ----------------------------------------------------

// rotatingWriter is a minimal size-based log rotator. It implements
// io.Writer; on each Write that would push the underlying file past
// maxBytes, the file is closed, renamed to "<path>.1", existing rotated
// files are bumped, and a fresh file is opened.
type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	file     *os.File
	size     int64
}

func newRotatingWriter(path string, maxBytes int64, maxFiles int) (*rotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &rotatingWriter{
		path:     path,
		maxBytes: maxBytes,
		maxFiles: maxFiles,
		file:     f,
		size:     info.Size(),
	}, nil
}

func (r *rotatingWriter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingWriter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *rotatingWriter) rotateLocked() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	// Shift existing rotations: .N -> .N+1, dropping the oldest.
	for i := r.maxFiles - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", r.path, i)
		to := fmt.Sprintf("%s.%d", r.path, i+1)
		if i+1 > r.maxFiles {
			_ = os.Remove(from)
			continue
		}
		_ = os.Rename(from, to)
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	r.file = f
	r.size = 0
	return nil
}
