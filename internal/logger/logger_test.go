package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestMaskSecret(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "****"},
		{"abcd", "****"},
		{"abcde", "****bcde"},
		{"my-secret-key-1234", "****1234"},
	}
	for _, c := range cases {
		if got := MaskSecret(c.in); got != c.want {
			t.Errorf("MaskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDebugRespectsFlag(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() { SetOutput(nil); ResetDebug() })
	if buf.Len() != 0 {
		buf.Reset()
	}

	SetDebug(false)
	Debug("hidden")
	if buf.Len() != 0 {
		t.Errorf("expected no output when debug disabled, got %q", buf.String())
	}

	SetDebug(true)
	Debug("visible")
	if !strings.Contains(buf.String(), "visible") || !strings.Contains(buf.String(), "[DEBUG]") {
		t.Errorf("expected DEBUG visible message, got %q", buf.String())
	}
}

func TestWarnAndErrorAlwaysWrite(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() { SetOutput(nil); ResetDebug() })
	SetDebug(false)

	Warn("careful")
	Error("bad")
	out := buf.String()
	if !strings.Contains(out, "[WARN] careful") {
		t.Errorf("missing warn line in %q", out)
	}
	if !strings.Contains(out, "[ERROR] bad") {
		t.Errorf("missing error line in %q", out)
	}
}

func TestPrintfHelpers(t *testing.T) {
	var buf bytes.Buffer
	SetOutput(&buf)
	t.Cleanup(func() { SetOutput(nil); ResetDebug() })
	SetDebug(true)
	Debugf("n=%d", 7)
	Warnf("w=%s", "x")
	Errorf("e=%v", true)
	out := buf.String()
	for _, want := range []string{"n=7", "w=x", "e=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in %q", want, out)
		}
	}
}
