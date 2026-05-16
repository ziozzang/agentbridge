package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunSetupWritesKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	in := strings.NewReader("super-key-9999\n")
	var out bytes.Buffer
	if err := runSetup(in, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Saved.") {
		t.Errorf("expected 'Saved.' in: %q", out.String())
	}
}

func TestRunSetupRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	in := strings.NewReader("\n")
	var out bytes.Buffer
	if err := runSetup(in, &out); err == nil {
		t.Error("expected error for empty key")
	}
}
