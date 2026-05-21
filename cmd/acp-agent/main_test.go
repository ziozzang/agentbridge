package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/ziozzang/agentbridge/internal/acp"
)

func TestPermissionPromptAcceptsYes(t *testing.T) {
	var stderr bytes.Buffer
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("yes\n")),
		stderr: &stderr,
		opts:   clientOptions{Permission: "prompt"},
	}
	resp, err := c.permission(acp.RequestPermissionParams{ToolCall: map[string]any{"title": "Run command: date"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Outcome.Outcome != "selected" || resp.Outcome.OptionID != "allow" {
		t.Fatalf("permission response = %#v", resp)
	}
	if !strings.Contains(stderr.String(), "Run command: date") {
		t.Fatalf("prompt missing tool title: %q", stderr.String())
	}
}

func TestPermissionReadOnlyRejects(t *testing.T) {
	c := &client{
		stdin:  bufio.NewReader(strings.NewReader("")),
		stderr: ioDiscard{},
		opts:   clientOptions{Permission: "reject"},
	}
	resp, err := c.permission(acp.RequestPermissionParams{ToolCall: map[string]any{"title": "Write file"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Outcome.Outcome != "selected" || resp.Outcome.OptionID != "reject" {
		t.Fatalf("permission response = %#v", resp)
	}
}

func TestUpdateText(t *testing.T) {
	got := updateText(map[string]any{
		"content": map[string]any{"type": "text", "text": "hello"},
	})
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
	got = updateText(map[string]any{
		"content": map[string]any{"content": map[string]any{"text": "nested"}},
	})
	if got != "nested" {
		t.Fatalf("nested got %q", got)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
