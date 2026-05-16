package imagepre

import (
	"strings"
	"testing"

	"github.com/ziozzang/glm-acp/internal/acp"
)

func TestDiagnostic_ImageWithDataIsRedacted(t *testing.T) {
	blocks := []acp.ContentBlock{
		{Type: "image", MimeType: "image/png", Data: strings.Repeat("a", 100)},
	}
	lines := BuildPromptBlockDiagnosticLines(blocks)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, strings.Repeat("a", 50)) {
		t.Fatalf("data payload leaked into diagnostic: %s", joined)
	}
	if !strings.Contains(joined, "image/png") {
		t.Fatalf("expected mime in output, got: %s", joined)
	}
	if !strings.Contains(joined, "data_bytes≈") {
		t.Fatalf("expected approx byte size, got: %s", joined)
	}
}

func TestDiagnostic_ImageWithURIRecordsURIPresence(t *testing.T) {
	blocks := []acp.ContentBlock{
		{Type: "image", MimeType: "image/jpeg", URI: "https://example.com/secret/path?key=secret"},
	}
	lines := BuildPromptBlockDiagnosticLines(blocks)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "secret") {
		t.Fatalf("URI leaked: %s", joined)
	}
	if !strings.Contains(joined, "uri=true") {
		t.Fatalf("expected uri=true, got: %s", joined)
	}
}

func TestDiagnostic_ResourceLinkScrubsPath(t *testing.T) {
	blocks := []acp.ContentBlock{
		{Type: "resource_link", Name: "doc", URI: "file:///home/user/private/notes.md"},
	}
	lines := BuildPromptBlockDiagnosticLines(blocks)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "private") {
		t.Fatalf("private path leaked: %s", joined)
	}
	if !strings.Contains(joined, "notes.md") {
		t.Fatalf("expected basename, got: %s", joined)
	}
}

func TestDiagnostic_DataURIRedacted(t *testing.T) {
	blocks := []acp.ContentBlock{
		{Type: "resource_link", Name: "inline", URI: "data:text/plain;base64,SECRET"},
	}
	lines := BuildPromptBlockDiagnosticLines(blocks)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "SECRET") {
		t.Fatalf("data: payload leaked: %s", joined)
	}
	if !strings.Contains(joined, "data:<redacted>") {
		t.Fatalf("expected redacted marker, got: %s", joined)
	}
}

func TestDiagnostic_BlockCountsListed(t *testing.T) {
	blocks := []acp.ContentBlock{
		{Type: "text", Text: "hi"},
		{Type: "image", MimeType: "image/png", Data: "AAAA"},
		{Type: "text", Text: "bye"},
	}
	lines := BuildPromptBlockDiagnosticLines(blocks)
	if !strings.Contains(lines[0], "text×2") {
		t.Fatalf("expected text×2, got: %s", lines[0])
	}
	if !strings.Contains(lines[0], "image×1") {
		t.Fatalf("expected image×1, got: %s", lines[0])
	}
}
