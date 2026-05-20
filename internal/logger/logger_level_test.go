package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want Level
	}{
		{"trace", LevelTrace},
		{"DEBUG", LevelDebug},
		{"info", LevelInfo},
		{"warning", LevelWarn},
		{"err", LevelError},
		{"off", LevelOff},
		{"weird", LevelWarn},
		{"", LevelWarn},
	}
	for _, c := range cases {
		if got := ParseLevel(c.in); got != c.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLevelFilters(t *testing.T) {
	t.Cleanup(ResetDebug)
	for _, tc := range []struct {
		name  string
		level Level
		debug bool
		info  bool
		warn  bool
	}{
		{"trace", LevelTrace, true, true, true},
		{"debug", LevelDebug, true, true, true},
		{"info", LevelInfo, false, true, true},
		{"warn", LevelWarn, false, false, true},
		{"error", LevelError, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ResetDebug()
			SetLevel(tc.level)
			if IsDebugEnabled() != tc.debug {
				t.Errorf("debug active=%v want=%v", IsDebugEnabled(), tc.debug)
			}
		})
	}
}

func TestFileSinkAndRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.log")
	t.Setenv("ACP_HARNESS_LOG_LEVEL", "info")
	t.Setenv("ACP_HARNESS_LOG_FILE", logPath)
	t.Setenv("ACP_HARNESS_LOG_MAX_BYTES", "256")
	t.Setenv("ACP_HARNESS_LOG_MAX_FILES", "3")
	t.Setenv("ACP_HARNESS_LOG_BOTH", "")
	ResetDebug()
	t.Cleanup(ResetDebug)
	if err := Configure(); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	for i := 0; i < 30; i++ {
		Info(strings.Repeat("x", 32))
	}

	// At least one rotated file should exist.
	for _, suffix := range []string{".1", ".2"} {
		if _, err := os.Stat(logPath + suffix); err != nil {
			t.Errorf("expected rotated log file %s; err=%v", logPath+suffix, err)
		}
	}
	// The most-recent file should also exist.
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("missing current log file: %v", err)
	}
}

func TestACPGLMDebugStillForcesDebug(t *testing.T) {
	t.Setenv("ACP_HARNESS_LOG_LEVEL", "warn")
	t.Setenv("ACP_GLM_DEBUG", "true")
	ResetDebug()
	t.Cleanup(ResetDebug)
	if !IsDebugEnabled() {
		t.Errorf("ACP_GLM_DEBUG=true should still enable debug for back-compat")
	}
}
